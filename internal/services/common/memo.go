package common

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
// error, and the next Get starts a fresh lookup. If a lookup failed only
// because its caller's ctx ended, waiters whose own ctx is still live retry
// it, so one caller's cancellation can't fail the others.
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
		// The lookup failed because its owner's ctx ended, but ours is still
		// live: look it up again instead of inheriting that cancellation.
		if isContextErr(e.err) && ctx.Err() == nil {
			continue
		}
		var zero V
		return zero, e.err
	}
}

// run performs the lookup for an entry the caller just registered. A failed
// entry is removed before done is closed, so the next Get starts afresh.
func (m *Memo[K, V]) run(ctx context.Context, key K, e *memoEntry[V], lookup func(context.Context) (V, error)) (V, error) {
	defer close(e.done)
	e.value, e.err = lookup(ctx)
	if e.err != nil {
		m.mu.Lock()
		if m.entries[key] == e {
			delete(m.entries, key)
		}
		m.mu.Unlock()
	}
	return e.value, e.err
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
