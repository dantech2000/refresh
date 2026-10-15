package awsx

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go"
)

// Policy says how the clients of a Clients value retry a failed call. The
// SDK retryer is the only retry layer: nothing above it calls an operation
// again.
type Policy int

// The retry policies.
const (
	// Standard is for describe, list, and update calls: up to 5 attempts.
	// For an update call the SDK fills a client request token once, so
	// every attempt carries the same token and a retry after a lost answer
	// starts nothing new.
	Standard Policy = iota
	// Sweep is for every call inside the region callback of a default
	// sweep. It is Standard with a short dial timeout, and a connection
	// that cannot be opened is not tried again: a region whose endpoint is
	// unreachable fails in seconds.
	Sweep
	// Poll is for calls inside a wait loop: 1 attempt. The loop itself goes
	// on through a transient error and stops on a permanent one
	// (IsPermanentAPIError).
	Poll
)

func (p Policy) String() string {
	switch p {
	case Standard:
		return "Standard"
	case Sweep:
		return "Sweep"
	case Poll:
		return "Poll"
	}
	return "Policy(?)"
}

// The numbers of the Standard and Sweep policies.
const (
	maxAttempts    = 5
	initialBackoff = 200 * time.Millisecond
	maxBackoff     = 5 * time.Second
	// sweepDialTimeout bounds opening a connection (DNS and TCP connect)
	// in a sweep.
	sweepDialTimeout = 10 * time.Second
)

// newRetryer returns the SDK retryer for policy. backoff, when not nil,
// replaces the delay between attempts.
func newRetryer(policy Policy, backoff retry.BackoffDelayer) aws.Retryer {
	if policy == Poll {
		return aws.NopRetryer{}
	}
	// A failure to get credentials is not retried here, although it can be
	// transient: the credential client has made its own attempts. A wait
	// loop stops on it too (IsIdentityFailure).
	retryable := func(err error) bool { return !IsIdentityFailure(err) && IsRetryable(err) }
	if policy == Sweep {
		retryable = func(err error) bool {
			return !IsIdentityFailure(err) && !IsDialFailure(err) && IsRetryable(err)
		}
	}
	if backoff == nil {
		backoff = fullJitterBackoff{}
	}
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = maxAttempts
		o.MaxBackoff = maxBackoff
		o.Backoff = backoff
		// No retry quota. The SDK's token bucket ends a run of throttled
		// calls with "retry quota exceeded" in place of the throttling
		// error; the attempt limit and the backoff are the bound here.
		o.RateLimiter = ratelimit.None
		// One classification decides, here and in IsPermanentAPIError.
		o.Retryables = []retry.IsErrorRetryable{
			retry.IsErrorRetryableFunc(func(err error) aws.Ternary {
				return aws.BoolTernary(retryable(err))
			}),
		}
	})
}

// fullJitterBackoff is the delay before a retry: a random time up to a
// ceiling that starts at 200 ms, doubles with each attempt, and stops at
// 5 s. The random part keeps callers that fail together from retrying
// together. Five attempts wait 3 s in total at most.
type fullJitterBackoff struct{}

// BackoffDelay returns the delay after the failed attempt number attempt,
// which starts at 1.
func (fullJitterBackoff) BackoffDelay(attempt int, _ error) (time.Duration, error) {
	return time.Duration(rand.Int64N(int64(backoffCeiling(attempt)) + 1)), nil //nolint:gosec // G404: jitter needs spread, not secrecy
}

// backoffCeiling returns the longest delay after the failed attempt number
// attempt.
func backoffCeiling(attempt int) time.Duration {
	ceiling := initialBackoff
	for i := 1; i < attempt && ceiling < maxBackoff; i++ {
		ceiling *= 2
	}
	return min(ceiling, maxBackoff)
}

// IsRetryable reports whether err is a transient condition that is worth
// another attempt, or another poll: throttling, a failure on the AWS side,
// or a network failure. A permanent error such as an access denial or a
// validation failure is not, and neither is a cancelled or expired context.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// A connection that could not be opened is transient, also when it is
	// a connect timeout (which matches context.DeadlineExceeded below). A
	// name that does not exist (an invalid region) is permanent.
	if IsDialFailure(err) && !errors.Is(err, context.Canceled) && !nameNotFound(err) {
		return true
	}
	if isContextDeadline(err) || errors.Is(err, context.Canceled) {
		return false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && (retryableErrorCodes[ae.ErrorCode()] || ae.ErrorFault() == smithy.FaultServer) {
		return true
	}
	// The SDK's own classification: errors that declare themselves
	// retryable, connection errors, 5xx statuses, and its default
	// retryable and throttle codes.
	switch retry.IsErrorRetryables(retry.DefaultRetryables).IsErrorRetryable(err) {
	case aws.TrueTernary:
		return true
	case aws.FalseTernary:
		return false
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// retryableErrorCodes are the API error codes of a transient condition,
// next to the SDK's default retryable and throttle codes.
var retryableErrorCodes = map[string]bool{
	"ThrottlingException":         true,
	"Throttling":                  true,
	"TooManyRequestsException":    true,
	"RequestLimitExceeded":        true,
	"RequestThrottled":            true,
	"RequestThrottledException":   true,
	"SlowDown":                    true,
	"PriorRequestNotComplete":     true,
	"ServiceUnavailableException": true,
	"InternalServerException":     true,
	"InternalFailure":             true,
	"ServerException":             true,
}

// isContextDeadline reports whether the deadline of a context ended the
// call: context.DeadlineExceeded itself is in the chain of err.
//
// errors.Is is not the test for that. A timeout of the HTTP transport, such
// as the wait for response headers, also answers errors.Is(err,
// context.DeadlineExceeded), although the context of the caller is live. It
// is a network failure, and another attempt can succeed.
func isContextDeadline(err error) bool {
	for err != nil {
		if err == context.DeadlineExceeded { //nolint:errorlint // the identity of the sentinel is the point; see above
			return true
		}
		switch e := err.(type) { //nolint:errorlint // walking the chain link by link
		case interface{ Unwrap() error }:
			err = e.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				if isContextDeadline(inner) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// IsDialFailure reports whether err is a connection that could not be
// opened: a DNS failure, a refused connection, a connect timeout, or a proxy
// that could not be reached. No answer came from the server, AWS or a
// Kubernetes API server. Go's connect timeout
// also reports itself as context.DeadlineExceeded, so a caller checks this
// first to tell it from its own deadline.
func IsDialFailure(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && (op.Op == "dial" || op.Op == "proxyconnect")
}

// nameNotFound reports a DNS answer that the name does not exist.
func nameNotFound(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

// IsPermanentAPIError reports whether err is an AWS API error that another
// attempt or another poll will not fix: an access denial, a resource that is
// not found, a validation failure. An error that never got an answer from
// the API (DNS, a refused or reset connection, a timeout) and a retryable
// API error are not permanent: a long wait keeps polling through them.
func IsPermanentAPIError(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && !IsRetryable(err)
}

// IdempotencyToken returns a random client request token. The SDK fills a
// token by itself for one call. A caller sets this one when several calls
// must count as the same request, such as a submit that it repeats after a
// restart.
//
// The SDK writes the token that it generates into the input struct of the
// call. A second call with the same input struct therefore sends the same
// token, and AWS answers with the first update. Build a new input for each
// request that must start something new.
//
// The token is 40 hex characters: EKS takes 33 to 126 characters for the
// token of UpdateClusterVersion.
func IdempotencyToken() string {
	b := make([]byte, 20)
	if _, err := crand.Read(b); err != nil {
		// What matters is that the token is unique.
		return "refresh-token-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}
