package fanout

import (
	"context"
	"errors"
	"sync"
)

// Memo memoizes successful lookups per key. It is safe for concurrent use,
// and concurrent callers for the same key share one in-flight lookup, so a
// parallel fan-out costs one call per distinct key. The zero value is ready
// to use.
//
// Failures are never memoized: callers that waited on a failed lookup get its
// error, and the next Get starts a fresh lookup. If the ctx of the caller that
// ran a lookup ended, waiters whose own ctx is still live retry it, so one
// caller's cancellation can't fail the others. Any other failure goes to
// every waiter as it is, also a timeout inside the lookup: N waiters must
// not repeat a lookup that times out N times.
type Memo[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*memoEntry[V]
}

// memoEntry is one lookup, in flight until done is closed. value and err are
// written before done is closed and read only after it.
type memoEntry[V any] struct {
	done  chan struct{}
	value V
	err   error
	// ownerDone reports that the ctx of the caller that ran the lookup had
	// ended when the lookup returned.
	ownerDone bool
}

// Get returns the result of lookup for key. A successful result is shared by
// every later call; a failure is returned to the callers that waited on it
// and then forgotten.
func (m *Memo[K, V]) Get(ctx context.Context, key K, lookup func(context.Context) (V, error)) (V, error) {
	for {
		m.mu.Lock()
		if m.entries == nil {
			m.entries = make(map[K]*memoEntry[V])
		}
		e, ok := m.entries[key]
		if !ok {
			e = &memoEntry[V]{done: make(chan struct{})}
			m.entries[key] = e
			m.mu.Unlock()
			return m.run(ctx, key, e, lookup)
		}
		m.mu.Unlock()

		select {
		case <-e.done:
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
		if e.err == nil {
			return e.value, nil
		}
		// The owner's ctx ended, but ours is still live: look it up again
		// instead of inheriting that cancellation.
		if e.ownerDone && ctx.Err() == nil {
			continue
		}
		var zero V
		return zero, e.err
	}
}

// run performs the lookup for an entry the caller just registered. A failed
// entry is removed before done is closed, so the next Get starts afresh. A
// lookup that panics counts as failed: its waiters get errLookupPanicked and
// the panic continues up the owner's stack, never leaving a cached zero value.
func (m *Memo[K, V]) run(ctx context.Context, key K, e *memoEntry[V], lookup func(context.Context) (V, error)) (V, error) {
	finished := false
	defer func() {
		if !finished {
			var zero V
			e.value, e.err = zero, errLookupPanicked
		}
		if e.err != nil {
			m.mu.Lock()
			if m.entries[key] == e {
				delete(m.entries, key)
			}
			m.mu.Unlock()
		}
		close(e.done)
	}()
	e.value, e.err = lookup(ctx)
	e.ownerDone = e.err != nil && ctx.Err() != nil
	finished = true
	return e.value, e.err
}

var errLookupPanicked = errors.New("memoized lookup panicked")
