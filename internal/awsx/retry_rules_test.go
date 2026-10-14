package awsx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// dialTimeout returns the error of a real connect timeout: a dial whose
// deadline has passed. It makes no connection and waits for nothing.
func dialTimeout(t *testing.T) error {
	t.Helper()
	d := net.Dialer{Deadline: time.Now().Add(-time.Second)}
	conn, err := d.DialContext(t.Context(), "tcp", "127.0.0.1:9")
	if err == nil {
		_ = conn.Close()
		t.Fatal("a dial with a deadline in the past succeeded")
	}
	return err
}

// sent wraps err as the SDK wraps the error of a request that it could
// not send.
func sent(err error) error {
	return fmt.Errorf("operation error EKS: DescribeCluster, %w", &smithyhttp.RequestSendError{Err: err})
}

// A connect timeout answers errors.Is(err, context.DeadlineExceeded), so
// it was taken for the deadline of the caller, not retried, and it ended
// the poll of a long wait. It is a dial failure, as a proxy that cannot
// be reached is. A name that does not exist (an invalid region) is
// permanent, so the region help is not replaced by a deadline error
// after the retries. A DNS timeout stays retryable.
func TestIsRetryable_DialFailures(t *testing.T) {
	timeout := dialTimeout(t)
	if !errors.Is(timeout, context.DeadlineExceeded) {
		t.Fatalf("the connect timeout %v is not context.DeadlineExceeded; the test needs the error that looks like a deadline", timeout)
	}
	dial := func(err error) error { return &net.OpError{Op: "dial", Net: "tcp", Err: err} }
	tests := []struct {
		name      string
		err       error
		dial      bool
		retryable bool
	}{
		{"a connect timeout", sent(timeout), true, true},
		{"a refused connection", sent(dial(syscall.ECONNREFUSED)), true, true},
		{"a proxy that cannot be reached", sent(&net.OpError{Op: "proxyconnect", Net: "tcp", Err: syscall.ECONNREFUSED}), true, true},
		{"a proxy that times out", sent(&net.OpError{Op: "proxyconnect", Net: "tcp", Err: context.DeadlineExceeded}), true, true},
		{"a name that does not exist", sent(dial(&net.DNSError{Err: "no such host", Name: "eks.xx-nowhere-1.amazonaws.com", IsNotFound: true})), true, false},
		{"a DNS timeout", sent(dial(&net.DNSError{Err: "i/o timeout", Name: "eks.us-east-1.amazonaws.com", IsTimeout: true})), true, true},
		{"a dial that the caller cancelled", sent(dial(context.Canceled)), true, false},
		{"the deadline of the caller", sent(context.DeadlineExceeded), false, false},
		{"a read that failed", sent(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDialFailure(tt.err); got != tt.dial {
				t.Errorf("IsDialFailure = %t, want %t", got, tt.dial)
			}
			if got := IsRetryable(tt.err); got != tt.retryable {
				t.Errorf("IsRetryable = %t, want %t", got, tt.retryable)
			}
			// A wait polls through every error that is not a permanent
			// answer of the API, and none of these is an answer.
			if IsPermanentAPIError(tt.err) {
				t.Error("IsPermanentAPIError = true for an error that is no answer of the API")
			}
			// The retryer of a call uses the same classification. A
			// sweep does not dial a region twice.
			if got := newRetryer(Standard, nil).IsErrorRetryable(tt.err); got != tt.retryable {
				t.Errorf("the standard retryer retries = %t, want %t", got, tt.retryable)
			}
			if got := newRetryer(Sweep, nil).IsErrorRetryable(tt.err); got != (tt.retryable && !tt.dial) {
				t.Errorf("the sweep retryer retries = %t, want %t", got, tt.retryable && !tt.dial)
			}
		})
	}
}
