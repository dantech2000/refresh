package common

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/aws/smithy-go"
)

// RetryConfig controls retry behavior for AWS API calls.
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

// retryableErrorCodes are typed AWS API error codes that indicate a transient
// condition worth retrying (throttling and server-side hiccups).
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

// shouldRetry classifies transient errors. Typed AWS API errors are judged by
// their error code (and server fault classification); substring matching is
// only the fallback for transport-level errors that never reached the API.
func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	// Do not retry context cancellations/timeouts
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return retryableErrorCodes[ae.ErrorCode()] || ae.ErrorFault() == smithy.FaultServer
	}
	// Best-effort string checks for throttling/network glitches.
	s := err.Error()
	return containsAnyFold(s, []string{"throttl", "rate exceeded", "timeout", "temporarily unavailable", "connection reset"})
}

// IdempotencyToken returns a random token for AWS APIs that accept a
// ClientRequestToken. Setting it explicitly ONCE per logical operation lets
// retry wrappers re-issue the request without risking a double-apply (the SDK
// only auto-fills a fresh token per call, which defeats idempotency across
// caller-level retries).
func IdempotencyToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a time-based token; uniqueness is what matters here.
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}

func containsAnyFold(haystack string, needles []string) bool {
	for _, n := range needles {
		if len(n) == 0 {
			continue
		}
		if indexFold(haystack, n) >= 0 {
			return true
		}
	}
	return false
}

// indexFold returns index of needle in haystack, case-insensitive, or -1.
func indexFold(haystack, needle string) int {
	hl := len(haystack)
	nl := len(needle)
	if nl == 0 || nl > hl {
		return -1
	}
	for i := 0; i <= hl-nl; i++ {
		match := true
		for j := 0; j < nl; j++ {
			hc := haystack[i+j]
			nc := needle[j]
			if hc >= 'A' && hc <= 'Z' {
				hc += 'a' - 'A'
			}
			if nc >= 'A' && nc <= 'Z' {
				nc += 'a' - 'A'
			}
			if hc != nc {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// WithRetry runs fn with exponential backoff respecting context cancellation.
func WithRetry[T any](ctx context.Context, cfg RetryConfig, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if cfg.MaxAttempts <= 0 {
		cfg = DefaultRetryConfig
	}
	backoff := cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		// Respect ctx cancellation between attempts
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}

		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		if !shouldRetry(err) || attempt == cfg.MaxAttempts {
			return zero, err
		}

		// Wait with context awareness
		wait := backoff
		if wait > cfg.MaxBackoff && cfg.MaxBackoff > 0 {
			wait = cfg.MaxBackoff
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
		// Exponential increase
		if cfg.BackoffMultiplier > 1 {
			backoff = time.Duration(float64(backoff) * cfg.BackoffMultiplier)
		}
	}
}
