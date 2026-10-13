package fleet

import (
	"context"
	"sync"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
)

// tickets returns the tickets of the region of c: as many as the request
// limit of the region, or nil when there is no limit.
//
// A ticket lets a fan-out start one goroutine for one item. Without
// tickets, a sweep starts a goroutine for every nodegroup and every add-on
// of every cluster in work, and almost all of them only wait for one of
// the few request slots. A goroutine that waits costs its stack, and those
// stacks were a third of the memory of a large sweep. With tickets, the
// goroutines of the items follow the requests in flight.
func (f *Fetcher) tickets(c *awsx.Clients) chan struct{} {
	if c.MaxInFlight <= 0 {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regionTickets == nil {
		f.regionTickets = map[string]chan struct{}{}
	}
	t, ok := f.regionTickets[c.Region]
	if !ok {
		t = make(chan struct{}, c.MaxInFlight)
		f.regionTickets[c.Region] = t
	}
	return t
}

// each runs fn over items and returns the results in the order of items.
// It starts the goroutine of an item when the region has a ticket for it.
// fn makes the calls of one item and starts no fan-out of its own, so a
// goroutine that holds a ticket never waits for another ticket.
//
// When ctx ends, each starts nothing more. The items that it did not start
// keep the zero value of R, as with fanout.ForEach, so R says whether its
// item ran.
func each[T, R any](ctx context.Context, tickets chan struct{}, items []T, fn func(context.Context, T) R) []R {
	if tickets == nil {
		return fanout.ForEach(ctx, items, max(len(items), 1), fn)
	}
	results := make([]R, len(items))
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, item := range items {
		select {
		case tickets <- struct{}{}:
		case <-ctx.Done():
			return results
		}
		// Both cases can be ready at once. Nothing starts after ctx ended.
		if ctx.Err() != nil {
			<-tickets
			return results
		}
		wg.Go(func() {
			defer func() { <-tickets }()
			results[i] = fn(ctx, item)
		})
	}
	return results
}
