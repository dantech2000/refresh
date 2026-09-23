package common

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go"
)

// RetryConfig controls retry behavior for AWS API calls.
//
// WithRetry runs on top of the SDK's own standard retryer (3 attempts per
// call), so each WithRetry attempt can already be up to 3 requests. Keep
// MaxAttempts small; values above maxRetryAttempts are clamped.
type RetryConfig struct {
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
}

var DefaultRetryConfig = RetryConfig{
	MaxAttempts:       5,
	InitialBackoff:    200 * time.Millisecond,
	MaxBackoff:        5 * time.Second,
	BackoffMultiplier: 2.0,
}

// maxRetryAttempts caps WithRetry attempts whatever the config asks for.
// Layered on the SDK retryer, 10 attempts is up to 30 requests for one call.
const maxRetryAttempts = 10

// withDefaults fills each zero or invalid field from DefaultRetryConfig
// independently, so a partial config (say, only MaxAttempts) keeps its other
// fields, and clamps the attempt count and the initial backoff.
func (c RetryConfig) withDefaults() RetryConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultRetryConfig.MaxAttempts
	}
	if c.MaxAttempts > maxRetryAttempts {
		c.MaxAttempts = maxRetryAttempts
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = DefaultRetryConfig.InitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultRetryConfig.MaxBackoff
	}
	if c.InitialBackoff > c.MaxBackoff {
		c.InitialBackoff = c.MaxBackoff
	}
	switch {
	case c.BackoffMultiplier <= 0:
		c.BackoffMultiplier = DefaultRetryConfig.BackoffMultiplier
	case c.BackoffMultiplier < 1:
		c.BackoffMultiplier = 1
	}
	return c
}

// retryableErrorCodes are typed AWS API error codes that indicate a transient
// condition worth retrying (throttling and server-side hiccups). The SDK's
// default throttle and retryable codes are checked as well, through
// sdkRetryables.
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

// sdkRetryables is the SDK standard retryer's own classification: canceled
// requests, errors that declare RetryableError(), connection errors (dial,
// refused, reset, temporary, timeout), 5xx status codes, and the default
// retryable and throttle error codes.
var sdkRetryables = retry.IsErrorRetryables(retry.DefaultRetryables)

// shouldRetry classifies transient errors with typed checks only: API error
// codes and fault, the SDK's retryable classification, and the syscall and io
// errors a dropped connection surfaces as.
func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	// Do not retry context cancellations/timeouts
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && (retryableErrorCodes[ae.ErrorCode()] || ae.ErrorFault() == smithy.FaultServer) {
		return true
	}
	switch sdkRetryables.IsErrorRetryable(err) {
	case aws.TrueTernary:
		return true
	case aws.FalseTernary:
		return false
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// IsRetryable reports whether err is a transient condition (throttling,
// server-side fault, network glitch) that is worth retrying or polling
// through. Permanent errors such as AccessDenied or validation failures
// return false, as do context cancellations.
func IsRetryable(err error) bool { return shouldRetry(err) }

// IdempotencyToken returns a random token for AWS APIs that accept a
// ClientRequestToken. Setting it explicitly ONCE per logical operation lets
// retry wrappers re-issue the request without risking a double-apply (the SDK
// only auto-fills a fresh token per call, which defeats idempotency across
// caller-level retries).
func IdempotencyToken() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		// Fall back to a time-based token; uniqueness is what matters here.
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}

// jitter returns a uniformly random duration in [0, d] ("full jitter"), so
// concurrent callers that fail together do not retry in lockstep. Tests
// replace it to make waits deterministic.
var jitter = func(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// sleep waits for d or until ctx ends. Tests replace it with a fake clock.
var sleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WithRetry runs fn with capped exponential backoff and full jitter,
// respecting context cancellation. Zero fields in cfg take their
// DefaultRetryConfig value; MaxAttempts is capped at maxRetryAttempts.
func WithRetry[T any](ctx context.Context, cfg RetryConfig, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	cfg = cfg.withDefaults()
	backoff := cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		// Respect ctx cancellation between attempts
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		if !shouldRetry(err) || attempt >= cfg.MaxAttempts {
			return zero, err
		}

		if err := sleep(ctx, jitter(backoff)); err != nil {
			return zero, err
		}
		// Grow the ceiling, clamped so it never passes MaxBackoff (and never
		// overflows however many attempts run).
		next := time.Duration(float64(backoff) * cfg.BackoffMultiplier)
		if next > cfg.MaxBackoff || next < backoff {
			next = cfg.MaxBackoff
		}
		backoff = next
	}
}
