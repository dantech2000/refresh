package change

import (
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// An upgrade of one minor version sends the control plane, the add-ons
// that the new control plane cannot run, each nodegroup, and then the
// other add-ons, and nothing else that changes the cluster.
func TestUpgrade_WholeRun(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
		t.Errorf("status = %s, exit %d, err %v; want Succeeded, exit 0", res.Status, res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	t.Log(stepStatuses(res))
}

// The planner asks for the add-on catalogue one pair at a time. Its first
// ask reads every pair of the plan side by side, a pair is read once in
// a plan, and the two plans of a real run (before and after the insights
// refresh) share the reads.
func TestUpgrade_CatalogueReads(t *testing.T) {
	// vpc-cni runs a build that the live catalogue does not list, so the
	// plan has a catch-up hop that asks for the live catalogue again.
	lagging := func(_ *fleettest.State, c *fleettest.Cluster) { c.Addon("vpc-cni").Version = "v1.18.0-eksbuild.1" }
	tests := []struct {
		name  string
		world *fleettest.World
		req   Request
		plan  bool // NewPlan only, as a dry run does
		want  int
	}{
		{name: "dry run, one hop", world: engineWorld(nil), req: upgradeTo("1.33"), plan: true, want: 6},
		{name: "dry run, two hops", world: engineWorld(nil), req: upgradeTo("1.34"), plan: true, want: 9},
		{name: "dry run, catch-up hop", world: engineWorld(lagging), req: upgradeTo("1.33"), plan: true, want: 6},
		{name: "real run, one hop", world: engineWorld(nil), req: upgradeTo("1.33"), want: 6},
		{name: "rollback", world: upgradedWorld(nil), req: rollbackOf(), plan: true, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.world)
			// The first read waits until a second one arrives: the reads
			// of a plan run side by side.
			second := make(chan struct{})
			var once sync.Once
			e.onRequest(fleettest.EKSDescribeAddonVersions, func(n int) {
				switch n {
				case 1:
					select {
					case <-second:
					case <-time.After(10 * time.Second):
						t.Error("the first catalogue read was alone for 10 s: the reads are serial")
					}
				case 2:
					once.Do(func() { close(second) })
				}
			})
			if tt.plan {
				if _, err := NewPlan(testContext(t), e.deps, tt.req); err != nil {
					t.Fatal(err)
				}
			} else {
				mustRun(t, e, tt.req, Hooks{})
			}
			if got := e.count(fleettest.EKSDescribeAddonVersions); got != tt.want {
				t.Errorf("%d DescribeAddonVersions requests, want %d", got, tt.want)
			}
		})
	}
}
