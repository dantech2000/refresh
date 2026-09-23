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
	// Start with a long timeout so the timer cannot fire before pause, then
	// shrink the remaining budget while paused. Scheduling delays can only
	// make the "still alive while paused" check easier to pass.
	ctx, cancel := withPausableTimeout(t.Context(), time.Hour)
	defer cancel()

	resume := ctx.pause()
	ctx.mu.Lock()
	ctx.remaining = 10 * time.Millisecond
	ctx.mu.Unlock()
	start := time.Now()
	for time.Since(start) < 50*time.Millisecond {
		time.Sleep(5 * time.Millisecond) // well past the remaining budget
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("expired while paused: %v", err)
	}
	resume()
	resume() // idempotent
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

// promptStarted returns ctx with an extra pause hook that closes the returned
// channel when a prompt under ctx starts waiting. Hooks run outer first, so
// the API deadline is already paused when the channel closes.
func promptStarted(ctx context.Context) (context.Context, <-chan struct{}) {
	started := make(chan struct{})
	return ui.WithPromptScope(ctx, ctx, func() func() {
		close(started)
		return func() {}
	}), started
}

// readAsync runs p.ReadLine(ctx) in a goroutine and returns its result.
func readAsync(ctx context.Context, p *ui.PromptReader) <-chan string {
	res := make(chan string, 1)
	go func() {
		line, err := p.ReadLine(ctx)
		if err != nil {
			line = "error: " + err.Error()
		}
		res <- line
	}()
	return res
}

// A prompt must still wait for input when the --timeout API deadline has
// already passed.
func TestAPIContextPromptOutlivesDeadline(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := ui.NewPromptReader(r)

	apiCtx, cancel := apiContext(t.Context(), time.Nanosecond)
	defer cancel()
	<-apiCtx.Done() // the API deadline has expired before the prompt starts
	if !errors.Is(apiCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("API ctx Err() = %v, want DeadlineExceeded", apiCtx.Err())
	}

	ctx, started := promptStarted(apiCtx)
	res := readAsync(ctx, p)
	<-started
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	if got := <-res; got != "y" {
		t.Fatalf("ReadLine() = %q, want %q", got, "y")
	}
}

// The API deadline is paused while a prompt waits, so answer time is not
// taken from the --timeout budget. While paused, Deadline() moves with the
// clock; after the prompt it is fixed again.
func TestAPIContextPausesDeadlineDuringPrompt(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := ui.NewPromptReader(r)

	apiCtx, cancel := apiContext(t.Context(), time.Hour)
	defer cancel()
	ctx, started := promptStarted(apiCtx)
	res := readAsync(ctx, p)
	<-started

	if !deadlineMoves(apiCtx) {
		t.Error("deadline kept counting while the prompt waited")
	}
	if _, err := w.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	if got := <-res; got != "y" {
		t.Fatalf("ReadLine() = %q, want %q", got, "y")
	}
	if deadlineMoves(apiCtx) {
		t.Error("deadline still paused after the prompt returned")
	}
	if err := apiCtx.Err(); err != nil {
		t.Errorf("API ctx Err() = %v, want nil", err)
	}
}

// deadlineMoves reports whether ctx's deadline changes as the clock advances,
// which is true only while a pausableDeadline is paused.
func deadlineMoves(ctx context.Context) bool {
	d1, _ := ctx.Deadline()
	start := time.Now()
	for time.Since(start) < time.Millisecond {
		time.Sleep(100 * time.Microsecond)
	}
	d2, _ := ctx.Deadline()
	return !d2.Equal(d1)
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
