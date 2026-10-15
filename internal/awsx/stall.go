package awsx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

// stallLimits bound a call that gets no answer, so that it does not wait for
// the deadline of the command. A long wait and the TUI have none.
type stallLimits struct {
	// headers is how long the response headers may take.
	headers time.Duration
	// bodyIdle is how long the response body may deliver nothing. It is
	// not a limit on the whole body: a large answer that keeps coming
	// passes, whatever its size.
	bodyIdle time.Duration
	// An HTTP/2 connection that is silent for pingAfter gets a ping, and
	// is closed when the ping has no answer after pingTimeout.
	pingAfter   time.Duration
	pingTimeout time.Duration
}

// defaultStallLimits are the limits of a real run. Each failure they cause
// is a timeout, which is transient: the retry policy of the call applies.
func defaultStallLimits() stallLimits {
	return stallLimits{
		headers:     time.Minute,
		bodyIdle:    time.Minute,
		pingAfter:   30 * time.Second,
		pingTimeout: 15 * time.Second,
	}
}

// freeze returns the HTTP client of buildable as one that no service client
// can change, with the body limit added.
//
// The SDK has a read timeout of its own (BuildableClient.WithReadTimeout),
// which sets a deadline on each read of a connection. It does not see a
// stalled response on HTTP/2: the pings, and the other responses on the
// same connection, keep the connection reading. The limit here is on the
// body of one response, so it holds for both protocols.
func freeze(buildable *awshttp.BuildableClient, bodyIdle time.Duration) aws.HTTPClient {
	frozen := buildable.Freeze()
	client, ok := frozen.(*http.Client)
	if !ok || bodyIdle <= 0 {
		return frozen
	}
	client.Transport = &bodyIdleTransport{next: client.Transport, idle: bodyIdle}
	return client
}

// errBodyStalled is the cause that cancels the request of a stalled body.
var errBodyStalled = errors.New("the response body stalled")

// bodyIdleTransport fails a response whose body delivers nothing for idle.
// Response headers have their own limit in the transport below; after the
// headers nothing else watches the body.
type bodyIdleTransport struct {
	next http.RoundTripper
	idle time.Duration
}

func (t *bodyIdleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	body := &idleBody{body: resp.Body, ctx: ctx, cancel: cancel, idle: t.idle}
	// The timer runs only inside a Read: the time that the caller takes
	// between two reads is not the server's.
	body.timer = time.AfterFunc(t.idle, func() { cancel(errBodyStalled) })
	body.timer.Stop()
	resp.Body = body
	return resp, nil
}

// idleBody is a response body that cancels its request when one Read waits
// longer than idle.
type idleBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
	timer  *time.Timer
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.timer.Reset(b.idle)
	n, err := b.body.Read(p)
	b.timer.Stop()
	if err != nil && errors.Is(context.Cause(b.ctx), errBodyStalled) {
		// The SDK's own error for a read that took too long. It is a
		// timeout, so the retryer treats it as transient; the error of
		// the cancelled request would read as an interrupt.
		return n, &awshttp.ResponseTimeoutError{TimeoutDur: b.idle}
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.body.Close()
	b.cancel(nil)
	return err
}
