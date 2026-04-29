package common

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fastRetry keeps test wall-time near zero while exercising the full retry path.
var fastRetry = RetryConfig{
	MaxAttempts:       3,
	InitialBackoff:    1 * time.Millisecond,
	MaxBackoff:        5 * time.Millisecond,
	BackoffMultiplier: 2.0,
}

// ──────────────────────────────────────────────────────────────────────────────
// indexFold
// ──────────────────────────────────────────────────────────────────────────────

func TestIndexFold_EmptyNeedleReturnsMinus1(t *testing.T) {
	if indexFold("haystack", "") != -1 {
		t.Error("empty needle should return -1")
	}
}

func TestIndexFold_NeedleLongerThanHaystackReturnsMinus1(t *testing.T) {
	if indexFold("hi", "hello") != -1 {
		t.Error("needle longer than haystack should return -1")
	}
}

func TestIndexFold_ExactMatchAtStart(t *testing.T) {
	if idx := indexFold("throttled request", "throttl"); idx != 0 {
		t.Errorf("expected index 0, got %d", idx)
	}
}

func TestIndexFold_MatchAtNonZeroIndex(t *testing.T) {
	if idx := indexFold("request throttled", "throttl"); idx <= 0 {
		t.Errorf("expected positive index, got %d", idx)
	}
}

func TestIndexFold_CaseInsensitive(t *testing.T) {
	if idx := indexFold("THROTTLED", "throttl"); idx != 0 {
		t.Errorf("case-insensitive match should return 0, got %d", idx)
	}
}

func TestIndexFold_UppercaseNeedle(t *testing.T) {
	if idx := indexFold("throttled", "THROTTL"); idx != 0 {
		t.Errorf("uppercase needle match should return 0, got %d", idx)
	}
}

func TestIndexFold_NoMatch(t *testing.T) {
	if idx := indexFold("not found", "throttl"); idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// containsAnyFold
// ──────────────────────────────────────────────────────────────────────────────

func TestContainsAnyFold_EmptyNeedlesReturnsFalse(t *testing.T) {
	if containsAnyFold("throttled", nil) {
		t.Error("empty needle list should return false")
	}
}

func TestContainsAnyFold_EmptyNeedleInListIsSkipped(t *testing.T) {
	if containsAnyFold("hello", []string{"", "nope"}) {
		t.Error("empty needle should be skipped, not match everything")
	}
}

func TestContainsAnyFold_FirstNeedleMatches(t *testing.T) {
	if !containsAnyFold("throttled", []string{"throttl", "timeout"}) {
		t.Error("expected match on first needle")
	}
}

func TestContainsAnyFold_SecondNeedleMatches(t *testing.T) {
	if !containsAnyFold("connection timeout", []string{"throttl", "timeout"}) {
		t.Error("expected match on second needle")
	}
}

func TestContainsAnyFold_NoneMatch(t *testing.T) {
	if containsAnyFold("resource not found", []string{"throttl", "timeout"}) {
		t.Error("expected no match")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// shouldRetry
// ──────────────────────────────────────────────────────────────────────────────

func TestShouldRetry_NilReturnsFalse(t *testing.T) {
	if shouldRetry(nil) {
		t.Error("nil error should not be retried")
	}
}

func TestShouldRetry_ContextCancelledReturnsFalse(t *testing.T) {
	if shouldRetry(context.Canceled) {
		t.Error("context.Canceled should not be retried")
	}
}

func TestShouldRetry_ContextDeadlineExceededReturnsFalse(t *testing.T) {
	if shouldRetry(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded should not be retried")
	}
}

func TestShouldRetry_ThrottledReturnsTrue(t *testing.T) {
	if !shouldRetry(errors.New("ThrottlingException: rate exceeded")) {
		t.Error("throttling error should be retried")
	}
}

func TestShouldRetry_RateExceededReturnsTrue(t *testing.T) {
	if !shouldRetry(errors.New("rate exceeded")) {
		t.Error("rate exceeded error should be retried")
	}
}

func TestShouldRetry_TimeoutReturnsTrue(t *testing.T) {
	if !shouldRetry(errors.New("request timeout")) {
		t.Error("timeout error should be retried")
	}
}

func TestShouldRetry_TemporarilyUnavailableReturnsTrue(t *testing.T) {
	if !shouldRetry(errors.New("service temporarily unavailable")) {
		t.Error("temporarily unavailable should be retried")
	}
}

func TestShouldRetry_ConnectionResetReturnsTrue(t *testing.T) {
	if !shouldRetry(errors.New("connection reset by peer")) {
		t.Error("connection reset should be retried")
	}
}

func TestShouldRetry_NotFoundReturnsFalse(t *testing.T) {
	if shouldRetry(errors.New("ResourceNotFoundException: cluster not found")) {
		t.Error("not-found error should not be retried")
	}
}

func TestShouldRetry_AccessDeniedReturnsFalse(t *testing.T) {
	if shouldRetry(errors.New("AccessDeniedException: not authorized")) {
		t.Error("access denied error should not be retried")
	}
}

func TestShouldRetry_CaseInsensitive(t *testing.T) {
	if !shouldRetry(errors.New("THROTTLED")) {
		t.Error("throttling check should be case-insensitive")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// WithRetry
// ──────────────────────────────────────────────────────────────────────────────

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), fastRetry, func(_ context.Context) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("got %q, want %q", result, "ok")
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
}

func TestWithRetry_NonRetryableErrorReturnsImmediately(t *testing.T) {
	calls := 0
	sentinel := errors.New("resource not found")
	_, err := WithRetry(context.Background(), fastRetry, func(_ context.Context) (int, error) {
		calls++
		return 0, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-retryable error: fn called %d times, want 1", calls)
	}
}

func TestWithRetry_RetryableErrorSucceedsOnSecondAttempt(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), fastRetry, func(_ context.Context) (int, error) {
		calls++
		if calls < 2 {
			return 0, errors.New("throttled")
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestWithRetry_ExhaustsAttemptsReturnsLastError(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        5 * time.Millisecond,
		BackoffMultiplier: 1.0,
	}
	calls := 0
	_, err := WithRetry(context.Background(), cfg, func(_ context.Context) (int, error) {
		calls++
		return 0, errors.New("throttled: keep failing")
	})
	if err == nil {
		t.Fatal("expected error after exhausted attempts")
	}
	if calls != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", calls)
	}
}

func TestWithRetry_ZeroMaxAttemptsFallsBackToDefault(t *testing.T) {
	// Zero MaxAttempts should use DefaultRetryConfig (5 attempts).
	// Use a non-retryable error so we exit immediately; we just want to confirm
	// the function doesn't panic or treat 0 attempts as "never try".
	calls := 0
	_, err := WithRetry(context.Background(), RetryConfig{MaxAttempts: 0}, func(_ context.Context) (int, error) {
		calls++
		return 0, errors.New("not found") // non-retryable, exits on first attempt
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call with zero config fallback, got %d", calls)
	}
}

func TestWithRetry_ContextCancelledBeforeFn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before we start

	calls := 0
	_, err := WithRetry(ctx, fastRetry, func(_ context.Context) (int, error) {
		calls++
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Errorf("fn should not be called when context is already cancelled, got %d calls", calls)
	}
}

func TestWithRetry_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := RetryConfig{
		MaxAttempts:       5,
		InitialBackoff:    50 * time.Millisecond, // long enough to cancel during
		MaxBackoff:        200 * time.Millisecond,
		BackoffMultiplier: 1.0,
	}

	calls := 0
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := WithRetry(ctx, cfg, func(_ context.Context) (int, error) {
		calls++
		return 0, errors.New("throttled")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled during backoff, got %v", err)
	}
	if calls > 2 {
		t.Errorf("expected at most 2 calls before cancellation, got %d", calls)
	}
}

func TestWithRetry_MaxBackoffCapsWait(t *testing.T) {
	// Verify MaxBackoff is respected: start with 1s backoff but cap at 2ms.
	// If MaxBackoff wasn't applied, the test would take ~1s.
	cfg := RetryConfig{
		MaxAttempts:       3,
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        2 * time.Millisecond,
		BackoffMultiplier: 2.0,
	}
	start := time.Now()
	calls := 0
	WithRetry(context.Background(), cfg, func(_ context.Context) (int, error) { //nolint:errcheck
		calls++
		return 0, errors.New("throttled")
	})
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("MaxBackoff not respected: elapsed %v, expected <100ms", elapsed)
	}
}
