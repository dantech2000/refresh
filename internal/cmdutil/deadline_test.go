package cmdutil

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// Without a timeout the context has no deadline, and only its parent or its
// cancel function ends it.
func TestWithTimeoutZeroMeansNoDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Minute} {
		ctx, cancel := WithTimeout(t.Context(), timeout)
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("timeout %v: the context has a deadline", timeout)
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("timeout %v: Err() = %v, want nil", timeout, err)
		}
		cancel()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("timeout %v: Err() after cancel = %v, want Canceled", timeout, ctx.Err())
		}
	}
}

// A timeout ends the context with a deadline error.
func TestWithTimeoutExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := WithTimeout(t.Context(), time.Minute)
		defer cancel()
		<-ctx.Done()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("Err() = %v, want DeadlineExceeded", ctx.Err())
		}
	})
}

// Ctrl+C (the signal context) reaches the context in the same call, so a
// caller that asks "was this an interrupt?" sees it: an interrupt exits 1,
// not 4.
func TestWithTimeoutSeesParentCancelAtOnce(t *testing.T) {
	for _, timeout := range []time.Duration{time.Hour, 0} {
		parent, interrupt := context.WithCancel(t.Context())
		ctx, cancel := WithTimeout(parent, timeout)
		interrupt()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Errorf("timeout %v: Err() right after the interrupt = %v, want Canceled", timeout, ctx.Err())
		}
		cancel()
	}
}
