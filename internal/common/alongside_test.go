package common

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// The observer must be cancelled and joined as soon as wait returns, and
// wait's error must come back unchanged — even when the observer would
// otherwise block forever (a failed roll that never converges).
func TestRunAlongside_WaitEndsObserver(t *testing.T) {
	defer goleak.VerifyNone(t)
	wantErr := errors.New("update failed: PodEvictionFailure")
	var observerDone atomic.Bool

	start := time.Now()
	err := RunAlongside(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // simulate a final repaint
		observerDone.Store(true)
	}, func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if !observerDone.Load() {
		t.Fatal("RunAlongside returned before the observer finished")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RunAlongside took %v; observer was not cancelled promptly", elapsed)
	}
}

// An observer that finishes on its own must not cut the wait short.
func TestRunAlongside_ObserverEarlyReturnKeepsWaiting(t *testing.T) {
	waited := false
	err := RunAlongside(context.Background(), func(context.Context) {}, func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		waited = true
		return nil
	})
	if err != nil || !waited {
		t.Fatalf("err = %v, waited = %v; want nil, true", err, waited)
	}
}

func TestRunAlongside_NilObserver(t *testing.T) {
	calls := 0
	if err := RunAlongside(context.Background(), nil, func(context.Context) error {
		calls++
		return nil
	}); err != nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d; want nil, 1", err, calls)
	}
}
