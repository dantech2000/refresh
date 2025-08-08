package common

import (
	"context"
	"errors"
	"time"
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

// shouldRetry classifies transient errors. Extend as needed.
func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	// Do not retry context cancellations/timeouts
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	// Best-effort string checks for throttling/network glitches.
    s := err.Error()
    return containsAnyFold(s, []string{"throttl", "rate exceeded", "timeout", "temporarily unavailable", "connection reset"})
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
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
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
	return zero, context.DeadlineExceeded
}
