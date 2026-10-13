package awsxtest

import (
	"context"
	"sync"
)

// Deadline returns a context that ends as a deadline does, with
// context.DeadlineExceeded, at the moment the test calls expire. A test of
// what the product does when its time runs out uses it with a handshake, in
// place of a real timeout that the test would have to wait for.
func Deadline(parent context.Context) (ctx context.Context, expire func()) {
	d := &deadline{Context: parent, done: make(chan struct{})}
	var once sync.Once
	return d, func() { once.Do(func() { close(d.done) }) }
}

// deadline is a context that its owner expires by hand.
type deadline struct {
	context.Context
	done chan struct{}
}

func (d *deadline) Done() <-chan struct{} { return d.done }

func (d *deadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
