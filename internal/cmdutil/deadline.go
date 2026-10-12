package cmdutil

import (
	"context"
	"sync"
	"time"
)

// WithTimeout returns a context for the AWS calls of a command. It expires
// after timeout, and a timeout of zero or less means no deadline: only
// parent, the signal context, then ends it.
//
// A prompt under the returned context (ReadLine) does not count against the
// timeout. The clock stops while the user answers, and the prompt waits on
// parent, so the deadline cannot cut a prompt short and Ctrl+C still ends
// it. With nested calls, a prompt stops every clock and waits on the
// outermost parent.
//
// A command uses it for the budget of its API calls (--timeout) and for the
// deadline of a wait (--wait-timeout). The credential setup inside runs
// under context.WithTimeout of the result, which the earlier of the two
// deadlines ends.
func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		ctx, cancel := context.WithCancel(parent)
		return withPromptScope(ctx, parent, nil), cancel
	}
	dl, cancel := newPausableDeadline(parent, timeout)
	return withPromptScope(dl, parent, dl.pause), cancel
}

type promptScopeKey struct{}

// promptScope tells ReadLine which context to wait on and how to stop the
// deadlines of the caller while the user answers.
type promptScope struct {
	wait  context.Context
	pause func() (resume func())
}

// withPromptScope returns ctx marked so that a prompt under it waits on wait
// and calls pause. When ctx already has a scope, the outer wait context is
// kept and both pause hooks run.
func withPromptScope(ctx, wait context.Context, pause func() func()) context.Context {
	if outer, ok := ctx.Value(promptScopeKey{}).(promptScope); ok {
		wait = outer.wait
		pause = chainPause(outer.pause, pause)
	}
	return context.WithValue(ctx, promptScopeKey{}, promptScope{wait: wait, pause: pause})
}

// chainPause returns a pause hook that runs a and b. Either may be nil.
func chainPause(a, b func() func()) func() func() {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func() func() {
		ra, rb := a(), b()
		return func() { rb(); ra() }
	}
}

// promptContext returns the context that a prompt under ctx waits on, and
// the function to call when the prompt ends.
func promptContext(ctx context.Context) (wait context.Context, done func()) {
	s, ok := ctx.Value(promptScopeKey{}).(promptScope)
	if !ok {
		return ctx, func() {}
	}
	if s.pause == nil {
		return s.wait, func() {}
	}
	return s.wait, s.pause()
}

// pausableDeadline is a context with a timeout that stops counting while it
// is paused. It ends when its parent ends, when its cancel function is
// called, or when the unpaused time reaches the timeout; Err is then
// context.DeadlineExceeded.
type pausableDeadline struct {
	parent context.Context
	done   chan struct{}

	mu         sync.Mutex
	err        error
	timer      *time.Timer
	deadline   time.Time     // meaningful while not paused
	remaining  time.Duration // meaningful while paused
	paused     int
	stopParent func() bool
}

func newPausableDeadline(parent context.Context, timeout time.Duration) (*pausableDeadline, context.CancelFunc) {
	c := &pausableDeadline{parent: parent, done: make(chan struct{})}
	c.mu.Lock()
	c.deadline = time.Now().Add(timeout)
	c.timer = time.AfterFunc(timeout, c.expire)
	c.mu.Unlock()

	stop := context.AfterFunc(parent, func() { c.finish(parent.Err()) })
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		stop()
	} else {
		c.stopParent = stop
		c.mu.Unlock()
	}
	return c, func() { c.finish(context.Canceled) }
}

func (c *pausableDeadline) expire() { c.finish(context.DeadlineExceeded) }

// finish ends c with err. Only the first call has an effect.
func (c *pausableDeadline) finish(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	c.timer.Stop()
	stop := c.stopParent
	close(c.done)
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// pause stops the clock until the returned function is called. Pauses nest:
// the clock runs again when every pause has been resumed.
func (c *pausableDeadline) pause() (resume func()) {
	c.mu.Lock()
	if c.paused == 0 && c.err == nil {
		c.timer.Stop()
		c.remaining = time.Until(c.deadline)
	}
	c.paused++
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(c.resume) }
}

func (c *pausableDeadline) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused--
	if c.paused == 0 && c.err == nil {
		c.deadline = time.Now().Add(c.remaining)
		c.timer = time.AfterFunc(c.remaining, c.expire)
	}
}

// Deadline reports when c expires if it is not paused again. While c is
// paused, the deadline moves forward with the clock.
func (c *pausableDeadline) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused > 0 {
		return time.Now().Add(c.remaining), true
	}
	return c.deadline, true
}

func (c *pausableDeadline) Done() <-chan struct{} { return c.done }

// Err reports why c is done. The cancellation of the parent reaches c
// through a goroutine, which may not have run yet right after the parent was
// cancelled. Err checks the parent itself, so a caller that asks "was this an
// interrupt?" after Ctrl+C always sees it: an interrupt exits 1, not 4.
func (c *pausableDeadline) Err() error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if perr := c.parent.Err(); perr != nil {
		c.finish(perr)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	}
	return nil
}

// Value returns the value of the parent. It must not expose an inner
// context of the standard library: the context package then watches Done and
// reads Err on c itself, so a child sees context.DeadlineExceeded when c
// expires.
func (c *pausableDeadline) Value(key any) any { return c.parent.Value(key) }
