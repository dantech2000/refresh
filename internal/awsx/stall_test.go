package awsx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

func TestDefaultStallLimits(t *testing.T) {
	want := stallLimits{headers: time.Minute, bodyIdle: time.Minute, pingAfter: 30 * time.Second, pingTimeout: 15 * time.Second}
	if got := defaultStallLimits(); got != want {
		t.Errorf("defaultStallLimits() = %+v, want %+v", got, want)
	}
	tr := NewHTTPClient(8).GetTransport()
	if tr.ResponseHeaderTimeout != want.headers || tr.HTTP2 == nil ||
		tr.HTTP2.SendPingTimeout != want.pingAfter || tr.HTTP2.PingTimeout != want.pingTimeout {
		t.Errorf("the client of a real run does not carry the default limits: header timeout %v, HTTP/2 %+v", tr.ResponseHeaderTimeout, tr.HTTP2)
	}
}

// pipeTransport answers every request with headers at once and a body that
// the test writes. As a real transport does, it fails a blocked read of the
// body when the context of the request ends.
type pipeTransport struct {
	body *io.PipeWriter
}

func (p *pipeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r, w := io.Pipe()
	p.body = w
	context.AfterFunc(req.Context(), func() { _ = w.CloseWithError(req.Context().Err()) })
	return &http.Response{StatusCode: http.StatusOK, Body: r, Request: req}, nil
}

// The tests of the body limit run in a synctest bubble: the clock is fake,
// so a minute of silence takes no time and always ends the same way.

func TestBodyIdleLimit(t *testing.T) {
	const idle = time.Minute
	get := func(t *testing.T) (*pipeTransport, *http.Response) {
		t.Helper()
		next := &pipeTransport{}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&bodyIdleTransport{next: next, idle: idle}).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		return next, resp
	}

	t.Run("a body that stops is a timeout after the limit", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			next, resp := get(t)
			go func() { _, _ = next.body.Write([]byte("{\"clusters\":[")) }()
			buf := make([]byte, 64)
			if n, err := resp.Body.Read(buf); err != nil || n == 0 {
				t.Fatalf("first Read = %d, %v; want the bytes that came", n, err)
			}

			start := time.Now()
			_, err := resp.Body.Read(buf)

			var timeout *awshttp.ResponseTimeoutError
			if !errors.As(err, &timeout) || timeout.TimeoutDur != idle {
				t.Errorf("Read of a stalled body: error = %v, want the SDK's response timeout of %v", err, idle)
			}
			if errors.Is(err, context.Canceled) {
				t.Error("the error reads as an interrupt")
			}
			if !IsRetryable(err) {
				t.Error("the timeout is not transient for the retry policy")
			}
			if waited := time.Since(start); waited != idle {
				t.Errorf("the read failed after %v, want %v", waited, idle)
			}
			_ = resp.Body.Close()
		})
	})

	t.Run("a slow body that keeps coming passes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			next, resp := get(t)
			const chunks = 20
			go func() {
				for range chunks {
					time.Sleep(idle - time.Second) // just inside the limit, every time
					_, _ = next.body.Write([]byte("x"))
				}
				_ = next.body.Close()
			}()

			got, err := io.ReadAll(resp.Body)

			if err != nil || len(got) != chunks {
				t.Errorf("ReadAll = %d bytes, %v; want %d bytes over %v", len(got), err, chunks, chunks*(idle-time.Second))
			}
			_ = resp.Body.Close()
		})
	})

	t.Run("a caller that reads slowly is not a stall", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			next, resp := get(t)
			go func() {
				_, _ = next.body.Write([]byte("ab"))
				_ = next.body.Close()
			}()
			buf := make([]byte, 1)
			for i := range 2 {
				if _, err := resp.Body.Read(buf); err != nil {
					t.Fatalf("Read %d: %v", i+1, err)
				}
				time.Sleep(10 * idle) // the caller is busy with what it read
			}
			if _, err := resp.Body.Read(buf); !errors.Is(err, io.EOF) {
				t.Errorf("last Read: error = %v, want io.EOF", err)
			}
			_ = resp.Body.Close()
		})
	})

	t.Run("the limit does not fire after Close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, resp := get(t)
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			time.Sleep(10 * idle)
			synctest.Wait()
			// The request was cancelled by Close, with no stall as cause.
			if cause := context.Cause(resp.Body.(*idleBody).ctx); errors.Is(cause, errBodyStalled) {
				t.Errorf("cause after Close = %v, want no stall", cause)
			}
		})
	})
}

// stalledServer answers each request up to a point and then holds it until
// the client gives up. It counts the requests.
func stalledServer(t *testing.T, afterHeaders bool) (url string, requests *atomic.Int32) {
	t.Helper()
	requests = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if afterHeaders {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"clusters":["a"`)) // the body never ends
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

// TestStalledCallFails makes real calls to a server that stops answering,
// through the client that Load builds, with the limits made short. Without
// a limit each call would wait for the context, which has no deadline here.
func TestStalledCallFails(t *testing.T) {
	limits := stallLimits{headers: 50 * time.Millisecond, bodyIdle: 50 * time.Millisecond, pingAfter: time.Minute, pingTimeout: time.Minute}
	tests := []struct {
		name         string
		afterHeaders bool
		policy       Policy
		wantRequests int32
	}{
		{name: "no headers, one attempt", policy: Poll, wantRequests: 1},
		{name: "no headers, standard policy", policy: Standard, wantRequests: 5},
		{name: "body stops after the headers, one attempt", afterHeaders: true, policy: Poll, wantRequests: 1},
		{name: "body stops after the headers, standard policy", afterHeaders: true, policy: Standard, wantRequests: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, requests := stalledServer(t, tt.afterHeaders)
			client := freeze(newHTTPClient(8, limits), limits.bodyIdle)
			defer client.(*http.Client).CloseIdleConnections()
			provider := NewProvider(aws.Config{
				Region:       "us-east-1",
				BaseEndpoint: aws.String(url),
				HTTPClient:   client,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "AKIDFAKEFAKEFAKE", SecretAccessKey: "fake"}, nil
				}),
			}, func(o *ProviderOptions) { o.Backoff = zeroBackoff{} })

			_, err := provider.clientsFor("", tt.policy).EKS.ListClusters(context.Background(), &eks.ListClustersInput{})

			var timeout interface{ Timeout() bool }
			if err == nil || !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("error = %v, want a timeout", err)
			}
			// errors.Is(err, context.DeadlineExceeded) is true for the header
			// timeout of net/http; isContextDeadline tells it from the
			// deadline of the caller.
			if errors.Is(err, context.Canceled) || isContextDeadline(err) {
				t.Errorf("error = %v, which reads as the end of the caller's context", err)
			}
			if msg := FormatAWSError(err, "listing clusters").Error(); !strings.HasPrefix(msg, "network connectivity issue while listing clusters") {
				t.Errorf("formatted error = %q, want the network help, not the --timeout hint", msg)
			}
			if !IsRetryable(err) || !IsNetworkError(err) {
				t.Errorf("error = %v, want a transient network error", err)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Errorf("requests = %d, want %d", got, tt.wantRequests)
			}
		})
	}
}

func TestIsContextDeadline(t *testing.T) {
	headers := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: time.Nanosecond}}
	defer headers.CloseIdleConnections()
	url, _ := stalledServer(t, false)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, headerTimeout := headers.Do(req)
	if headerTimeout == nil {
		_ = resp.Body.Close()
		t.Fatal("the request to a server that sends no headers succeeded")
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "the sentinel", err: context.DeadlineExceeded, want: true},
		{name: "wrapped", err: fmt.Errorf("listing: %w", context.DeadlineExceeded), want: true},
		{name: "joined", err: errors.Join(errors.New("x"), fmt.Errorf("y: %w", context.DeadlineExceeded)), want: true},
		{name: "two wrapped errors", err: fmt.Errorf("%w: %w", ErrNotSent, context.DeadlineExceeded), want: true},
		{name: "cancelled", err: context.Canceled},
		{name: "other", err: errors.New("boom")},
		// net/http says errors.Is(err, context.DeadlineExceeded) for this.
		{name: "response header timeout of the transport", err: headerTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContextDeadline(tt.err); got != tt.want {
				t.Errorf("isContextDeadline(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
	if !errors.Is(headerTimeout, context.DeadlineExceeded) {
		t.Errorf("net/http no longer reports its header timeout as a deadline (%v); isContextDeadline may be simplified", headerTimeout)
	}
}
