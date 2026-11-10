package backend_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// The session ends while a change of the UI asks a question. Before the
// end the change is unfinished, as refresh ui names it on the way out.
// The end stops the change at its question: Run returns, the change ends
// with nothing sent, and nothing is unfinished any more.
func TestSessionEndsWhileAChangeAsks(t *testing.T) {
	r := changeRig(t, changeWorld(nil), false)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: change.AddonUpdate, Cluster: prod})
	// A newer vpc-cni appears after the dry run, so the change asks.
	r.world.Edit(func(s *fleettest.State) {
		s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.21.0-eksbuild.1", ClusterVersions: []string{"1.33"}})
	})
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.wait(t, 0, func(st state.State) bool {
		c, ok := changeOf(st, id)
		return ok && c.State == change.Asking
	})
	if left := r.b.Unfinished(); len(left) != 1 || left[0].ID != id || left[0].State != change.Asking {
		t.Fatalf("unfinished = %+v, want the change that asks", left)
	}

	r.cancel()
	<-r.done
	if left := r.b.Unfinished(); len(left) != 0 {
		t.Errorf("unfinished after Run = %+v, want nothing: the change stopped at its question", left)
	}
	for _, req := range r.server.Requests() {
		if req.Mutates {
			t.Errorf("%s was sent by a change whose session ended at its question", req.Operation)
		}
	}
}
