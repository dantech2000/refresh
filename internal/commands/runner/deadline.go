package runner

import (
	"context"
	"sync"
	"time"
)

// pausableDeadline is a context with a timeout that stops counting while it
// is paused. setupAWS pauses it for interactive prompts, so time the user
// spends answering is not taken from the --timeout budget of the AWS calls.
// It is cancelled when its parent is cancelled, when cancel is called, or
// when the unpaused time reaches the timeout (Err is then
// context.DeadlineExceeded).
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

// withPausableTimeout returns a context that expires after timeout of
// unpaused time, and its cancel func.
func withPausableTimeout(parent context.Context, timeout time.Duration) (*pausableDeadline, context.CancelFunc) {
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

// finish cancels c with err. Only the first call has an effect.
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

// pause stops the timeout until the returned resume func is called. Pauses
// nest; the timeout runs again when every pause has been resumed.
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

// Deadline reports when c expires if it is not paused again. While paused,
// the deadline moves forward with the clock.
func (c *pausableDeadline) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused > 0 {
		return time.Now().Add(c.remaining), true
	}
	return c.deadline, true
}

func (c *pausableDeadline) Done() <-chan struct{} { return c.done }

func (c *pausableDeadline) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Value delegates to the parent. It must not expose an internal cancelCtx:
// the context package then watches Done and reads Err on c itself, so
// children see context.DeadlineExceeded when c expires.
func (c *pausableDeadline) Value(key any) any { return c.parent.Value(key) }
