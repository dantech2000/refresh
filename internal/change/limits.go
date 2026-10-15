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
	// Wait is --wait-timeout: the budget of each step alone, from its
	// submit or its attachment to the end of its confirmation. Zero means
	// no limit.
	Wait time.Duration
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

// finalPollGrace is how long after the deadline of a wait budget its
// timer ends a call that hangs. The last poll of a wait is due at the
// deadline, and it needs the time of one request to get its answer.
const finalPollGrace = 10 * time.Second

// newTimer starts a timer of the real clock. It is a variable so that the
// tests of this package can fail a test that starts one.
var newTimer = func(d time.Duration, f func()) (stop func() bool) {
	return time.AfterFunc(d, f).Stop
}

// budget is a time limit on the clock of the run that can be stopped
// while a person answers a question. It ends its context with a cause
// when it runs out.
//
// Two things end it. check compares the clock of the run with the
// deadline: Run calls it between two calls, and a wait calls it after
// every answer of a poll, which is what a test with a virtual clock
// relies on. A timer ends a call that hangs: Deps.AfterFunc, or the real
// clock without one. A test clock that does not move in real time never
// fires it.
//
// A wait under a budget never pauses past the deadline: its last pause is
// cut to the time that is left, so the last poll is due at the deadline.
// The answer of that poll counts. A change that the poll shows as
// finished is reported as finished, as a success or as a failure, because
// the result is then known and nothing is left running. Only an answer
// that shows the change still running ends the wait as timed out.
type budget struct {
	now    func() time.Time
	cause  error
	cancel context.CancelCauseFunc

	limited   bool
	running   bool
	deadline  time.Time     // while running
	remaining time.Duration // while paused
	// grace is added to the timer, and not to the deadline: see
	// finalPollGrace.
	grace time.Duration
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
		b.stopTimer = b.afterFunc(b.remaining+b.grace, expire)
		return
	}
	b.stopTimer = newTimer(b.remaining+b.grace, expire)
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

// left returns the time until the deadline on the clock of the run, which
// is zero or less at and after it. It returns false for a budget that has
// no limit or does not run. A nil budget has no limit.
func (b *budget) left() (time.Duration, bool) {
	if b == nil || !b.running {
		return 0, false
	}
	return b.deadline.Sub(b.now()), true
}

// check ends the context when the budget has run out on the clock of the
// run, and reports whether it has. A nil budget never runs out.
func (b *budget) check() bool {
	if b != nil && b.running && !b.now().Before(b.deadline) {
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
