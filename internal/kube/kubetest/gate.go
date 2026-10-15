package kubetest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Gate holds every request of a handler until a test lets it through, one
// wave at a time, as the gate of awsxtest does for AWS. A test names the
// calls of each wave in order:
//
//	gate := kubetest.NewGate(t, srv.Handler)
//	go run()
//	gate.Wave(fleettest.KubeListNodes, fleettest.KubeListPDBs)
//	gate.Wave(fleettest.KubeListPods)
//	gate.Done()
//
// Wave returns when exactly the named calls are held, and releases them. A
// call that the product makes only after an answer of the same wave never
// arrives while the wave is held, so Wave fails at its deadline and names
// what it holds and what it still expects.
type Gate struct {
	// Timeout is how long Wave waits for its calls. Zero means 5 seconds.
	Timeout time.Duration

	t testing.TB

	mu      sync.Mutex
	arrived chan struct{}
	held    []heldRequest
	open    bool
	late    []fleettest.Operation
}

type heldRequest struct {
	op      fleettest.Operation
	release chan struct{}
}

// NewGate puts a gate on h. It takes the OnRequest hook of the handler, so
// the test must set no hook of its own, and it must do so before the first
// request. A request that arrives after Done fails the test when it ends.
func NewGate(tb testing.TB, h *Handler) *Gate {
	tb.Helper()
	g := &Gate{t: tb, arrived: make(chan struct{})}
	h.OnRequest = g.onRequest
	tb.Cleanup(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if len(g.late) > 0 {
			tb.Errorf("Kubernetes requests after the last wave: %v", g.late)
		}
	})
	return g
}

func (g *Gate) onRequest(ctx context.Context, r Request) {
	g.mu.Lock()
	if g.open {
		g.late = append(g.late, r.Operation)
		g.mu.Unlock()
		return
	}
	release := make(chan struct{})
	g.held = append(g.held, heldRequest{op: r.Operation, release: release})
	close(g.arrived)
	g.arrived = make(chan struct{})
	g.mu.Unlock()

	select {
	case <-release:
	case <-ctx.Done():
	}
}

// Wave waits until the held requests are exactly ops, in any order, and
// releases them. It fails the test at once when a request outside ops is
// held, and at the deadline when a request of ops does not arrive.
func (g *Gate) Wave(ops ...fleettest.Operation) {
	g.t.Helper()
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		g.mu.Lock()
		held := make([]fleettest.Operation, len(g.held))
		for i, h := range g.held {
			held[i] = h.op
		}
		missing, extra := difference(ops, held)
		if len(extra) > 0 {
			g.mu.Unlock()
			g.t.Fatalf("wave %v: a Kubernetes request outside the wave is held: %v (held: %v)", ops, extra, held)
		}
		if len(missing) == 0 {
			for _, h := range g.held {
				close(h.release)
			}
			g.held = nil
			g.mu.Unlock()
			return
		}
		arrived := g.arrived
		g.mu.Unlock()

		select {
		case <-arrived:
		case <-deadline.C:
			g.t.Fatalf("wave %v: after %s it holds %v and still expects %v; a call that is missing may run only after an answer of this wave",
				ops, timeout, held, missing)
		}
	}
}

// Done opens the gate: the requests that it holds, and every later one, go
// through. A request that arrives after Done fails the test when it ends.
func (g *Gate) Done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = true
	for _, h := range g.held {
		g.late = append(g.late, h.op)
		close(h.release)
	}
	g.held = nil
}

// difference returns the operations of want that are not in got, and those
// of got that are not in want. Both lists may repeat an operation.
func difference(want, got []fleettest.Operation) (missing, extra []fleettest.Operation) {
	rest := slices.Clone(got)
	for _, op := range want {
		if i := slices.Index(rest, op); i >= 0 {
			rest = slices.Delete(rest, i, i+1)
		} else {
			missing = append(missing, op)
		}
	}
	return missing, rest
}
