package backend_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// An update of the control plane that is not a change of refresh, such as
// a change of the logging, is watched once: the watch ends with nothing to
// do, and the sweeps after it leave the cluster alone while it is
// UPDATING. Once a sweep shows the cluster ACTIVE, the next update is
// watched again.
func TestAForeignUpdateIsWatchedOnce(t *testing.T) {
	inFlight := func(id string) *fleettest.Update {
		return &fleettest.Update{ID: id, Type: fleettest.TypeConfigUpdate, Status: fleettest.UpdateInProgress, CreatedAt: cmdutiltest.Now()}
	}
	world := changeWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Status = fleettest.StatusUpdating
		c.Updates = []*fleettest.Update{inFlight("11111111-1111-4111-8111-111111111111")}
	})
	r := changeRig(t, world, false)
	// The watch of a sweep has ended, and nothing claims the cluster.
	quiet := func(n int) func(state.State) bool {
		return func(st state.State) bool {
			return st.Sweep.Count >= n && len(st.Changes) == 0 && st.Clusters[0].Change == 0
		}
	}
	watches := func() int {
		n := 0
		for _, req := range r.server.Requests() {
			if req.Operation == fleettest.EKSListUpdates {
				n++
			}
		}
		return n
	}
	st := r.wait(t, 0, quiet(1))
	if n := watches(); n != 1 {
		t.Fatalf("%d watches after the first sweep, want 1", n)
	}
	r.b.Refresh()
	st = r.wait(t, st.Version, quiet(2))
	if n := watches(); n != 1 {
		t.Fatalf("%d watches after a second sweep of the same update, want 1", n)
	}

	// The update ends, a sweep shows it, and the next update is watched.
	world.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Status, c.Updates[0].Status = fleettest.StatusActive, fleettest.UpdateSuccessful
	})
	r.b.Refresh()
	st = r.wait(t, st.Version, quiet(3))
	world.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Status = fleettest.StatusUpdating
		c.Updates = append(c.Updates, inFlight("22222222-2222-4222-8222-222222222222"))
	})
	r.b.Refresh()
	r.wait(t, st.Version, func(st state.State) bool { return quiet(4)(st) && watches() == 2 })
}
