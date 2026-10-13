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
	results := make([]R, len(items))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i, item := range items {
		// Acquire BEFORE spawning so the cap limits live goroutines. Observe
		// cancellation at the dispatch point so a cancelled context stops
		// queueing new work promptly; unstarted items keep their zero value
		// (e.g. nil pointer), matching best-effort list semantics.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results
		}
		// select picks at random when a slot frees up just as ctx is
		// cancelled, so re-check: never start new work after cancellation.
		if ctx.Err() != nil {
			<-sem
			wg.Wait()
			return results
		}
		wg.Add(1)
		go func(i int, it T) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fn(ctx, it)
		}(i, item)
	}
	wg.Wait()
	return results
}
