package runner

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/ui"
)

func TestPausableDeadlineExpires(t *testing.T) {
	ctx, cancel := withPausableTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not expire")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("Err() = %v, want DeadlineExceeded", ctx.Err())
	}
	<-child.Done()
	if !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Errorf("child Err() = %v, want DeadlineExceeded", child.Err())
	}
}

func TestPausableDeadlinePauseStopsTheClock(t *testing.T) {
	ctx, cancel := withPausableTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	resume := ctx.pause()
	time.Sleep(150 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("expired while paused: %v", err)
	}
	resume()
	resume() // idempotent
	if err := ctx.Err(); err != nil {
		t.Fatalf("expired right after resume: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not expire after resume")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestPausableDeadlineFollowsParentAndCancel(t *testing.T) {
	parent, stop := context.WithCancel(t.Context())
	ctx, cancel := withPausableTimeout(parent, time.Hour)
	defer cancel()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not propagate")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want Canceled", ctx.Err())
	}

	ctx2, cancel2 := withPausableTimeout(t.Context(), time.Hour)
	cancel2()
	if !errors.Is(ctx2.Err(), context.Canceled) {
		t.Errorf("Err() after cancel = %v, want Canceled", ctx2.Err())
	}
}

// An unanswered prompt must outlive the --timeout API deadline, and the time
// spent waiting must not be taken from that deadline.
func TestAPIContextPromptOutlivesDeadline(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := ui.NewPromptReader(r)

	ctx, cancel := apiContext(t.Context(), 50*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("y\n"))
	}()
	got, err := p.ReadLine(ctx)
	if err != nil || got != "y" {
		t.Fatalf("ReadLine() = %q, %v; want %q, nil", got, err, "y")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("API context expired during the prompt: %v", err)
	}
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) <= 0 {
		t.Fatalf("Deadline() = %v, %v; want a deadline in the future", dl, ok)
	}
}

// Ctrl+C (the signal context) still cancels a prompt under the API context.
func TestAPIContextSignalCancelsPrompt(t *testing.T) {
	for _, timeout := range []time.Duration{time.Hour, 0} {
		r, w := io.Pipe()
		t.Cleanup(func() { _ = w.Close() })
		p := ui.NewPromptReader(r)

		signalCtx, stop := context.WithCancel(t.Context())
		ctx, cancel := apiContext(signalCtx, timeout)
		done := make(chan error, 1)
		go func() {
			_, err := p.ReadLine(ctx)
			done <- err
		}()
		stop()
		select {
		case err := <-done:
			if !errors.Is(err, ui.ErrPromptCancelled) {
				t.Errorf("timeout %v: ReadLine() err = %v, want ErrPromptCancelled", timeout, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout %v: ReadLine did not return after Ctrl+C", timeout)
		}
		cancel()
	}
}
