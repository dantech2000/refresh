package fleet

import "github.com/dantech2000/refresh/internal/awsx"

// tickets returns the tickets of the region of c: as many as the request
// limit of the region, or nil when there is no limit.
//
// A ticket lets a fan-out start one goroutine for one item. Without
// tickets, a sweep starts a goroutine for every nodegroup and every add-on
// of every cluster in work, and almost all of them only wait for one of
// the few request slots. A goroutine that waits costs its stack, and those
// stacks were a third of the memory of a large sweep. With tickets, the
// goroutines of the items follow the requests in flight.
//
// The tickets go to fanout.ForEachIn. A function that holds a ticket makes
// the calls of one item and starts no fan-out of its own, so it never waits
// for another ticket.
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
