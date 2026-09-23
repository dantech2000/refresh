package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// fastRetry keeps test wall-time near zero while exercising the full retry path.
var fastRetry = RetryConfig{
	MaxAttempts:       3,
	InitialBackoff:    1 * time.Millisecond,
	MaxBackoff:        5 * time.Millisecond,
	BackoffMultiplier: 2.0,
}

// throttled is a typed throttling error, as the SDK returns it.
var throttled = &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}

func TestShouldRetry_Classification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline", fmt.Errorf("op: %w", context.DeadlineExceeded), false},
		{"throttling code", throttled, true},
		{"sdk throttle code", &smithy.GenericAPIError{Code: "EC2ThrottledException"}, true},
		{"server fault", &smithy.GenericAPIError{Code: "Whatever", Fault: smithy.FaultServer}, true},
		{"service unavailable", &smithy.GenericAPIError{Code: "ServiceUnavailableException"}, true},
		{"access denied", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "request timeout budget"}, false},
		{"not found", &smithy.GenericAPIError{Code: "ResourceNotFoundException"}, false},
		{"http 503", &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 503}}, Err: errors.New("x")}}, true},
		{"dial refused", &url.Error{Op: "Post", URL: "u", Err: &net.OpError{Op: "dial", Net: "tcp",
			Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}, true},
		{"connection reset", os.NewSyscallError("read", syscall.ECONNRESET), true},
		{"nxdomain", &net.DNSError{Err: "no such host", Name: "eks.bogus.amazonaws.com", IsNotFound: true}, false},
		{"unexpected eof", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		{"sdk canceled request", &aws.RequestCanceledError{Err: errors.New("x")}, false},
		// Plain strings no longer count: they caught unrelated errors before.
		{"string throttled", errors.New("throttled"), false},
		{"string timeout", errors.New("request timeout"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetry(tc.err); got != tc.want {
				t.Errorf("shouldRetry(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if got := IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
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
			return 0, throttled
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
		return 0, throttled
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
		return 0, throttled
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
		return 0, throttled
	})
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("MaxBackoff not respected: elapsed %v, expected <100ms", elapsed)
	}
}

// fakeClock records every backoff wait instead of sleeping, and makes jitter
// return its full ceiling so the schedule is deterministic.
func fakeClock(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	origSleep, origJitter := sleep, jitter
	sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	jitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { sleep, jitter = origSleep, origJitter })
	return &waits
}

func alwaysThrottled(calls *int) func(context.Context) (int, error) {
	return func(context.Context) (int, error) {
		*calls++
		return 0, throttled
	}
}

func TestWithRetry_BackoffGrowsAndClampsAtMax(t *testing.T) {
	waits := fakeClock(t)
	calls := 0
	cfg := RetryConfig{MaxAttempts: 6, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 500 * time.Millisecond, BackoffMultiplier: 2}
	_, _ = WithRetry(context.Background(), cfg, alwaysThrottled(&calls))
	want := []time.Duration{100, 200, 400, 500, 500}
	if len(*waits) != len(want) {
		t.Fatalf("waits = %v, want %d waits", *waits, len(want))
	}
	for i, w := range want {
		if (*waits)[i] != w*time.Millisecond {
			t.Errorf("wait[%d] = %v, want %v", i, (*waits)[i], w*time.Millisecond)
		}
	}
}

// A huge multiplier must not overflow the duration into a negative wait.
func TestWithRetry_BackoffNeverOverflows(t *testing.T) {
	waits := fakeClock(t)
	calls := 0
	cfg := RetryConfig{MaxAttempts: maxRetryAttempts, InitialBackoff: time.Second, MaxBackoff: time.Minute, BackoffMultiplier: 1e300}
	_, _ = WithRetry(context.Background(), cfg, alwaysThrottled(&calls))
	for i, w := range *waits {
		if w <= 0 || w > time.Minute {
			t.Errorf("wait[%d] = %v, want in (0, 1m]", i, w)
		}
	}
}

func TestWithRetry_MaxAttemptsCapped(t *testing.T) {
	fakeClock(t)
	calls := 0
	_, _ = WithRetry(context.Background(), RetryConfig{MaxAttempts: 1000}, alwaysThrottled(&calls))
	if calls != maxRetryAttempts {
		t.Errorf("calls = %d, want the %d-attempt cap", calls, maxRetryAttempts)
	}
}

// Zero fields default one by one: a config that sets only MaxAttempts keeps
// it, and the backoff fields come from DefaultRetryConfig.
func TestWithRetry_DefaultsFieldsIndividually(t *testing.T) {
	waits := fakeClock(t)
	calls := 0
	_, _ = WithRetry(context.Background(), RetryConfig{MaxAttempts: 2}, alwaysThrottled(&calls))
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (MaxAttempts kept)", calls)
	}
	if len(*waits) != 1 || (*waits)[0] != DefaultRetryConfig.InitialBackoff {
		t.Errorf("waits = %v, want [%v]", *waits, DefaultRetryConfig.InitialBackoff)
	}

	*waits = nil
	calls = 0
	_, _ = WithRetry(context.Background(), RetryConfig{}, alwaysThrottled(&calls))
	if calls != DefaultRetryConfig.MaxAttempts {
		t.Errorf("zero config: calls = %d, want %d", calls, DefaultRetryConfig.MaxAttempts)
	}
}

func TestJitter_StaysWithinCeiling(t *testing.T) {
	for range 1000 {
		if j := jitter(10 * time.Millisecond); j < 0 || j > 10*time.Millisecond {
			t.Fatalf("jitter = %v, want in [0, 10ms]", j)
		}
	}
	if jitter(0) != 0 {
		t.Error("jitter(0) should be 0")
	}
}
