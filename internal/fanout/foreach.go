// Package fanout runs work side by side: a bounded loop over items, an
// observer next to an authoritative wait, and a memo that shares one lookup
// between the callers that ask for the same key.
package fanout

import (
	"context"
	"sync"
)

// DefaultConcurrency is the limit that ForEach uses when it is given none.
const DefaultConcurrency = 8

// ForEach runs fn over items with bounded concurrency and returns the
// results in input order. fn must be safe to call concurrently; per-item
// failures should be encoded in R (e.g. a nil pointer) rather than aborting
// the whole batch, matching the best-effort semantics of list operations.
func ForEach[T, R any](ctx context.Context, items []T, maxConcurrency int, fn func(ctx context.Context, item T) R) []R {
	if maxConcurrency <= 0 {
		maxConcurrency = DefaultConcurrency
	}
	results, _ := ForEachIn(ctx, make(chan struct{}, maxConcurrency), items, fn)
	return results
}

// ForEachIn runs fn over items and returns the results in input order, and
// the number of items that it started. An item starts when it gets a slot
// of sem, so the caller decides the limit and can share it between calls.
// A nil sem means no limit.
//
// The items start in input order, and when ctx ends nothing more starts.
// So the items that did not start are always results[started:], and they
// keep the zero value of R. ForEachIn returns when every started item has
// returned.
func ForEachIn[T, R any](ctx context.Context, sem chan struct{}, items []T, fn func(ctx context.Context, item T) R) (results []R, started int) {
	if sem == nil {
		sem = make(chan struct{}, max(len(items), 1))
	}
	results = make([]R, len(items))
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, item := range items {
		// Acquire BEFORE spawning so the cap limits live goroutines.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return results, i
		}
		// select picks at random when a slot frees up just as ctx is
		// cancelled, so re-check: never start new work after cancellation.
		if ctx.Err() != nil {
			<-sem
			return results, i
		}
		wg.Go(func() {
			defer func() { <-sem }()
			results[i] = fn(ctx, item)
		})
	}
	return results, len(items)
}
