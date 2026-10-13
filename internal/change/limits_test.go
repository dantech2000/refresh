package change

import (
	"context"
	"errors"
	"testing"
	"time"
)

// clock is a time that a test moves by hand.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *clock {
	return &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// A budget runs out on the clock of the run, and its context then has the
// cause of the budget.
func TestBudget_RunsOut(t *testing.T) {
	c := newClock()
	ctx, b := newBudget(t.Context(), c.Now, time.Hour, ErrWaitTimeout)
	defer b.stop()
	b.start()
	c.advance(59 * time.Minute)
	if b.check() || ctx.Err() != nil {
		t.Fatal("the budget ran out a minute early")
	}
	c.advance(time.Minute)
	if !b.check() {
		t.Fatal("the budget did not run out at its deadline")
	}
	if !errors.Is(context.Cause(ctx), ErrWaitTimeout) || !timedOut(ctx) {
		t.Errorf("cause = %v, want the wait timeout", context.Cause(ctx))
	}
}

// The clock of a budget stops while it is paused: the time of a question
// does not count.
func TestBudget_Pause(t *testing.T) {
	c := newClock()
	ctx, b := newBudget(t.Context(), c.Now, time.Hour, ErrReadTimeout)
	defer b.stop()
	b.start()
	c.advance(40 * time.Minute)
	b.pause()
	c.advance(10 * time.Hour)
	if b.check() {
		t.Fatal("a paused budget ran out")
	}
	b.start()
	c.advance(19 * time.Minute)
	if b.check() || ctx.Err() != nil {
		t.Fatal("the budget ran out with a minute left")
	}
	c.advance(time.Minute)
	if !b.check() || !errors.Is(context.Cause(ctx), ErrReadTimeout) {
		t.Fatalf("the budget did not run out; cause = %v", context.Cause(ctx))
	}
	// A budget that has run out ends its context when it is started again.
	ctx2, b2 := newBudget(t.Context(), c.Now, time.Second, ErrReadTimeout)
	defer b2.stop()
	b2.start()
	c.advance(time.Minute)
	b2.pause()
	b2.start()
	if !errors.Is(context.Cause(ctx2), ErrReadTimeout) {
		t.Errorf("cause = %v, want the read timeout", context.Cause(ctx2))
	}
}

// A limit of zero never runs out, and an interrupt is not a timeout.
func TestBudget_NoLimitAndInterrupt(t *testing.T) {
	c := newClock()
	parent, cancel := context.WithCancel(t.Context())
	ctx, b := newBudget(parent, c.Now, 0, ErrWaitTimeout)
	defer b.stop()
	b.start()
	c.advance(1000 * time.Hour)
	if b.check() || ctx.Err() != nil {
		t.Fatal("a budget with no limit ran out")
	}
	cancel()
	if ctx.Err() == nil || timedOut(ctx) {
		t.Errorf("after an interrupt: err = %v, timedOut = %v; want cancelled and not timed out", ctx.Err(), timedOut(ctx))
	}
}

// A deadline of the caller counts as a timeout.
func TestTimedOut_CallerDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	if !timedOut(ctx) {
		t.Error("a context past its deadline is not timed out")
	}
}
