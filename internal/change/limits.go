package change

import (
	"context"
	"errors"
	"time"
)

// Limits are the time budgets of a run.
type Limits struct {
	// Read is --timeout: the budget for the reads and the submits outside
	// a wait. Zero means no limit.
	Read time.Duration
	// Wait is --wait-timeout. Zero means no limit.
	Wait time.Duration
	// StepWait makes Wait the budget of each step alone, from its submit
	// or its attachment to the end of its confirmation (addon update).
	StepWait bool
	// Poll is --poll-interval. Zero means DefaultPollInterval.
	Poll time.Duration
}

// The causes of a context that a budget ended. context.Cause returns one
// of them, so a caller can tell a deadline from an interrupt.
var (
	// ErrReadTimeout: the Read budget ran out.
	ErrReadTimeout = errors.New("the --timeout budget ran out")
	// ErrWaitTimeout: the Wait budget ran out.
	ErrWaitTimeout = errors.New("--wait-timeout passed")
)

// budget is a time limit on the clock of the run that can be stopped
// while a person answers a question. It ends its context with a cause
// when it runs out.
//
// Two things end it. check compares the clock of the run with the
// deadline: Run calls it between two calls and after every poll, which is
// what a test with a virtual clock relies on. A timer of the real clock
// ends a call that hangs. A test clock that does not move in real time
// never fires it.
type budget struct {
	now    func() time.Time
	cause  error
	cancel context.CancelCauseFunc

	limited   bool
	running   bool
	deadline  time.Time     // while running
	remaining time.Duration // while paused
	// afterFunc starts the timer that ends a call that hangs. Nil means
	// the real clock. stopTimer stops the timer that runs.
	afterFunc func(time.Duration, func()) (stop func() bool)
	stopTimer func() bool
}

// newBudget returns a context under parent and its budget, which is not
// running yet. A limit of zero or less never runs out.
func newBudget(parent context.Context, now func() time.Time, limit time.Duration, cause error) (context.Context, *budget) {
	ctx, cancel := context.WithCancelCause(parent)
	return ctx, &budget{now: now, cause: cause, cancel: cancel, limited: limit > 0, remaining: limit}
}

// start runs the clock of the budget. It does nothing while it runs.
func (b *budget) start() {
	if !b.limited || b.running {
		return
	}
	if b.remaining <= 0 {
		b.cancel(b.cause)
		return
	}
	b.running = true
	b.deadline = b.now().Add(b.remaining)
	expire := func() { b.cancel(b.cause) }
	if b.afterFunc != nil {
		b.stopTimer = b.afterFunc(b.remaining, expire)
		return
	}
	b.stopTimer = time.AfterFunc(b.remaining, expire).Stop
}

// pause stops the clock until the next start.
func (b *budget) pause() {
	if !b.running {
		return
	}
	b.running = false
	b.stopTimer()
	b.remaining = max(b.deadline.Sub(b.now()), 0)
}

// check ends the context when the budget has run out on the clock of the
// run, and reports whether it has.
func (b *budget) check() bool {
	if b.running && !b.now().Before(b.deadline) {
		b.cancel(b.cause)
		return true
	}
	return false
}

// stop ends the budget and releases its context.
func (b *budget) stop() {
	b.pause()
	b.cancel(context.Canceled)
}

// timedOut reports whether ctx ended because a deadline passed: a budget
// of the run, or a deadline of the caller. Anything else that ends a
// context is an interrupt.
func timedOut(ctx context.Context) bool {
	cause := context.Cause(ctx)
	return errors.Is(cause, ErrReadTimeout) || errors.Is(cause, ErrWaitTimeout) || errors.Is(cause, context.DeadlineExceeded)
}
