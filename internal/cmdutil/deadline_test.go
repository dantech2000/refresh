package cmdutil

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

// The tests that need time to pass run in a synctest bubble: the clock there
// is fake and moves only when every goroutine of the test is blocked, so a
// time.Sleep in these tests waits for nothing and always ends the same way.

func TestPausableDeadlineExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := newPausableDeadline(t.Context(), 20*time.Minute)
		defer cancel()
		child, childCancel := context.WithCancel(ctx)
		defer childCancel()

		time.Sleep(20*time.Minute - time.Nanosecond)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("Err() just before the deadline = %v, want nil", err)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()

		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("Err() = %v, want DeadlineExceeded", ctx.Err())
		}
		<-child.Done()
		if !errors.Is(child.Err(), context.DeadlineExceeded) {
			t.Errorf("child Err() = %v, want DeadlineExceeded", child.Err())
		}
	})
}

func TestPausableDeadlinePauseStopsTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := newPausableDeadline(t.Context(), time.Hour)
		defer cancel()
		time.Sleep(40 * time.Minute) // 20 minutes are left

		resume := ctx.pause()
		inner := ctx.pause() // pauses nest
		time.Sleep(3 * time.Hour)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired while paused: %v", err)
		}
		inner()
		inner() // a second call does nothing
		time.Sleep(3 * time.Hour)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired while the outer pause was still open: %v", err)
		}

		resume()
		time.Sleep(20*time.Minute - time.Nanosecond)
		synctest.Wait()
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired before the time that was left had passed: %v", err)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("Err() after the time that was left = %v, want DeadlineExceeded", ctx.Err())
		}
	})
}

func TestPausableDeadlineFollowsParentAndCancel(t *testing.T) {
	parent, stop := context.WithCancel(t.Context())
	ctx, cancel := newPausableDeadline(parent, time.Hour)
	defer cancel()
	stop()
	// The cancellation of the parent closes Done.
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want Canceled", ctx.Err())
	}

	ctx2, cancel2 := newPausableDeadline(t.Context(), time.Hour)
	cancel2()
	if !errors.Is(ctx2.Err(), context.Canceled) {
		t.Errorf("Err() after cancel = %v, want Canceled", ctx2.Err())
	}

	// A parent that is already done.
	ctx3, cancel3 := newPausableDeadline(parent, time.Hour)
	defer cancel3()
	<-ctx3.Done()
	if !errors.Is(ctx3.Err(), context.Canceled) {
		t.Errorf("Err() under a done parent = %v, want Canceled", ctx3.Err())
	}
}

// A timer that fired while a pause was taking the lock, or just before a
// resume replaced it, calls expire late. That call must not end the context.
func TestPausableDeadlineIgnoresAStaleTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := newPausableDeadline(t.Context(), time.Hour)
		defer cancel()

		resume := ctx.pause()
		ctx.expire() // the timer of before the pause
		if err := ctx.Err(); err != nil {
			t.Fatalf("a timer that fired during the pause ended the context: %v", err)
		}
		resume()
		ctx.expire() // the timer of before the resume; the deadline is an hour away
		if err := ctx.Err(); err != nil {
			t.Fatalf("a timer from before the resume ended the context: %v", err)
		}

		// The real expiry still works.
		time.Sleep(time.Hour)
		synctest.Wait()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("Err() after the deadline = %v, want DeadlineExceeded", ctx.Err())
		}
	})
}

// Err sees the cancellation of the parent at once, not after the goroutine
// that carries it has run: a check right after Ctrl+C must report an
// interrupt, and Done is closed by then.
func TestPausableDeadlineErrSeesParentCancelAtOnce(t *testing.T) {
	for range 1000 {
		parent, cancelParent := context.WithCancel(t.Context())
		c, cancel := newPausableDeadline(parent, time.Hour)
		cancelParent()
		if err := c.Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Err() right after the cancel of the parent = %v, want context.Canceled", err)
		}
		select {
		case <-c.Done():
		default:
			t.Fatal("Done is open although Err reported the cancellation")
		}
		cancel()
	}
}

// promptStarted returns ctx with one more pause hook, which closes the
// returned channel when a prompt under ctx starts to wait. Hooks run outer
// first, so the deadline is already paused when the channel closes.
func promptStarted(ctx context.Context) (context.Context, <-chan struct{}) {
	started := make(chan struct{})
	return withPromptScope(ctx, ctx, func() func() {
		close(started)
		return func() {}
	}), started
}

// readAsync runs ReadLine in a goroutine and returns its result.
func readAsync(ctx context.Context, in io.Reader) <-chan string {
	res := make(chan string, 1)
	go func() {
		line, err := ReadLine(ctx, &IOStreams{In: in})
		if err != nil {
			line = "error: " + err.Error()
		}
		res <- line
	}()
	return res
}

// A prompt still waits for its answer when the --timeout deadline has
// already passed.
func TestPromptOutlivesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		defer func() { _ = w.Close() }()

		apiCtx, cancel := WithTimeout(t.Context(), time.Minute)
		defer cancel()
		<-apiCtx.Done() // the deadline passes before the prompt starts
		if !errors.Is(apiCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("Err() = %v, want DeadlineExceeded", apiCtx.Err())
		}

		ctx, started := promptStarted(apiCtx)
		res := readAsync(ctx, r)
		<-started
		time.Sleep(time.Hour) // the user takes a long time
		if _, err := w.Write([]byte("y\n")); err != nil {
			t.Fatal(err)
		}
		if got := <-res; got != "y" {
			t.Fatalf("ReadLine() = %q, want %q", got, "y")
		}
	})
}

// The deadline is paused while a prompt waits, so the time to answer is not
// taken from the --timeout budget. A nested wait deadline pauses too.
func TestPromptPausesDeadline(t *testing.T) {
	tests := []struct {
		name   string
		nested bool // a wait deadline inside the API budget
	}{
		{name: "one deadline"},
		{name: "nested deadlines", nested: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, w := io.Pipe()
				defer func() { _ = w.Close() }()

				outer, cancel := WithTimeout(t.Context(), time.Hour)
				defer cancel()
				deadlines := []context.Context{outer}
				inner := outer
				if tt.nested {
					var cancelInner context.CancelFunc
					inner, cancelInner = WithTimeout(outer, 2*time.Hour)
					defer cancelInner()
					deadlines = append(deadlines, inner)
				}
				time.Sleep(10 * time.Minute) // 50 minutes of the budget are left
				ctx, started := promptStarted(inner)
				res := readAsync(ctx, r)
				<-started

				// The user answers after a day. No deadline counts it.
				time.Sleep(24 * time.Hour)
				synctest.Wait()
				for i, d := range deadlines {
					if err := d.Err(); err != nil {
						t.Errorf("deadline %d expired while the prompt waited: %v", i, err)
					}
					if until, _ := d.Deadline(); time.Until(until) < 50*time.Minute {
						t.Errorf("deadline %d is %v away during the prompt, want the time that was left", i, time.Until(until))
					}
				}
				if _, err := w.Write([]byte("y\n")); err != nil {
					t.Fatal(err)
				}
				if got := <-res; got != "y" {
					t.Fatalf("ReadLine() = %q, want %q", got, "y")
				}

				// The clock runs again: the budget ends 50 minutes later.
				time.Sleep(50*time.Minute - time.Nanosecond)
				synctest.Wait()
				if err := outer.Err(); err != nil {
					t.Errorf("the budget ended before the time that was left had passed: %v", err)
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if !errors.Is(outer.Err(), context.DeadlineExceeded) {
					t.Errorf("Err() of the budget = %v, want DeadlineExceeded", outer.Err())
				}
			})
		})
	}
}

// Ctrl+C (the signal context) still ends a prompt under a deadline, with or
// without a timeout.
func TestSignalCancelsPromptUnderDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{time.Hour, 0, -time.Second} {
		r, w := io.Pipe()
		signalCtx, stop := context.WithCancel(t.Context())
		apiCtx, cancel := WithTimeout(signalCtx, timeout)
		ctx, started := promptStarted(apiCtx)

		res := readAsync(ctx, r)
		<-started
		stop()

		if got := <-res; got != "error: context canceled" {
			t.Errorf("timeout %v: ReadLine() = %q, want the cancellation", timeout, got)
		}
		cancel()
		// The end of the input stops the read that ReadLine left behind.
		_ = w.Close()
	}
}

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

// The credential setup runs under context.WithTimeout of the command's
// context: the earlier deadline ends it, and its end is a deadline error.
func TestSetupDeadlineNestsInside(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wait, cancel := WithTimeout(t.Context(), time.Hour) // --wait-timeout
		defer cancel()
		setup, cancelSetup := context.WithTimeout(wait, time.Minute) // --timeout
		defer cancelSetup()

		<-setup.Done()

		if !errors.Is(setup.Err(), context.DeadlineExceeded) {
			t.Errorf("setup Err() = %v, want DeadlineExceeded", setup.Err())
		}
		if err := wait.Err(); err != nil {
			t.Errorf("the wait deadline ended with the setup: %v", err)
		}
	})
}
