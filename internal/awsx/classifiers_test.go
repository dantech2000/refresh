package awsx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
)

// headerTimeoutError returns the error of a real request whose response
// headers do not come in time. net/http reports it as
// context.DeadlineExceeded, although the context of the caller is live.
func headerTimeoutError(t *testing.T) error {
	t.Helper()
	headers := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: time.Nanosecond}}
	defer headers.CloseIdleConnections()
	url, _ := stalledServer(t, false)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := headers.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the request to a server that sends no headers succeeded")
	}
	return err
}

// The classifiers of an error are Classify, IsRetryable, IsNetworkError,
// and FormatAWSError. Each of them answers for one error set here, so a
// change to one of them that the others do not follow fails this test.
func TestClassifiersAgree(t *testing.T) {
	// The start of the first line of FormatAWSError for each kind.
	const (
		network  = "network connectivity issue"
		timedOut = "timed out"
		denied   = "insufficient AWS permissions"
		region   = "AWS region configuration issue"
	)
	tests := []struct {
		name      string
		err       error
		class     ErrorClass
		retryable bool
		network   bool
		message   string // "" when the message is not one of the kinds above
	}{
		{name: "the deadline of the run", err: sdkOpErr(context.DeadlineExceeded), class: ClassTimeout, message: timedOut},
		{name: "the deadline, wrapped", err: fmt.Errorf("listing: %w", sdkOpErr(context.DeadlineExceeded)), class: ClassTimeout, message: timedOut},
		{name: "interrupt", err: sdkOpErr(context.Canceled), class: ClassInterrupted},
		{name: "a refused connection", err: dialErr(syscall.ECONNREFUSED), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a connect timeout", err: fmt.Errorf("%w: %w", dialErr(syscall.ETIMEDOUT), context.DeadlineExceeded), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a response header timeout", err: sdkOpErr(headerTimeoutError(t)), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a body that stalls", err: sdkOpErr(&awshttp.ResponseTimeoutError{TimeoutDur: time.Second}), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a client timeout", err: sdkOpErr(timeoutError{}), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a reset connection", err: sdkOpErr(syscall.ECONNRESET), class: ClassNetworkError, retryable: true, network: true, message: network},
		{name: "a DNS lookup that the deadline cut", err: fmt.Errorf("%w: %w", dnsErr("eks.us-east-1.amazonaws.com"), context.DeadlineExceeded), class: ClassTimeout, message: timedOut},
		{name: "an AWS host that does not resolve", err: dnsErr("eks.xx-nowhere-1.amazonaws.com"), class: ClassRegionUnavailable, network: true, message: region},
		{name: "throttled", err: apiErr("ThrottlingException", "slow down"), class: ClassThrottled, retryable: true},
		{name: "a failure of AWS", err: apiErr("InternalFailure", "oops"), class: ClassServiceError, retryable: true},
		{name: "access denied", err: apiErr("AccessDeniedException", "no"), class: ClassAccessDenied, message: denied},
		{name: "not found", err: apiErr("ResourceNotFoundException", "gone"), class: ClassNotFound},
		{name: "anything else", err: sdkOpErr(errors.New("boom")), class: ClassUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.class {
				t.Errorf("Classify = %s, want %s", got, tt.class)
			}
			if got := IsRetryable(tt.err); got != tt.retryable {
				t.Errorf("IsRetryable = %t, want %t", got, tt.retryable)
			}
			if got := IsNetworkError(tt.err); got != tt.network {
				t.Errorf("IsNetworkError = %t, want %t", got, tt.network)
			}
			msg := FormatAWSError(tt.err, "listing clusters").Error()
			for _, kind := range []string{network, timedOut, denied, region} {
				if got := strings.HasPrefix(msg, kind); got != (kind == tt.message) {
					t.Errorf("FormatAWSError starts with %q: %t, want %t; message:\n%s", kind, got, !got, msg)
				}
			}
		})
	}
}
