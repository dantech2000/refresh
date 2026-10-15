package change

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A cluster of a fleet that its plan blocked has Blocked rows in the run.
// Each row says why: the blocker of the plan, and never Blocked alone.
func TestFleet_BlockedClusterSaysWhy(t *testing.T) {
	tests := []struct {
		name string
		seed func(s *fleettest.State, prod, staging *fleettest.Cluster)
		want string
	}{
		{name: "the target is not ACTIVE", want: "nodegroup ng-a is DEGRADED, not ACTIVE",
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
				prod.Nodegroup("ng-a").Status = fleettest.StatusDegraded
			}},
		{name: "the cluster is busy", want: "prod is busy (cluster UPDATING)",
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) { prod.Status = fleettest.StatusUpdating }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fleetWorld(tt.seed))
			res, err := Run(testContext(t), e.deps, fleetRoll(), Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			row := res.Clusters[0].Steps[0]
			if row.Status != StepBlocked || row.Failure == nil || !strings.Contains(row.Failure.Error, tt.want) {
				t.Fatalf("the row of prod/ng-a = %s with the failure %+v; want Blocked with %q", row.Status, row.Failure, tt.want)
			}
			if row.Failure.Cluster != "prod" || row.Failure.Region != east {
				t.Errorf("the failure names %s in %s, want prod in %s", row.Failure.Cluster, row.Failure.Region, east)
			}
			// The reason is on the row, and the code is still the one of a block.
			if res.ExitCode() != exitcode.Blocked {
				t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), exitcode.Blocked, res.Err)
			}
		})
	}
}

// With --yes, the plan of a cluster at its turn replaces the plan that was
// confirmed, and a difference that would ask a person is a notice. Another
// AMI release is the exception: a nodegroup that --reroll targets and that
// someone else rolled while the cluster before it ran is not rolled again.
// Its step keeps the release that was confirmed, and the fresh read before
// the request skips it, as it does in one cluster
// (TestRoll_FreshReadDecidesAgain).
func TestFleet_YesSkipsARerollOfAChangedRelease(t *testing.T) {
	e := newEnv(t, fleetWorld(nil))
	req := fleetRoll()
	req.Reroll = true
	req.Select = &Selector{Text: "-f name=ng-c", Match: func(n fleet.Nodegroup) bool { return n.Name == "ng-c" }}
	// While prod rolls, someone rolls ng-c of staging onto a newer release.
	e.onRequest(fleettest.EKSUpdateNodegroupVersion, func(n int) {
		if n == 1 {
			e.world.Edit(func(s *fleettest.State) {
				s.Cluster(east, "staging").Nodegroup("ng-c").ReleaseVersion = "1.32.0-20270101"
			})
		}
	})
	s := &script{t: t}
	res, err := Run(testContext(t), e.deps, req, Hooks{Event: s.event})
	if err != nil {
		t.Fatal(err)
	}
	want := []sentRoll{{"prod", "ng-c", "1.32", false}}
	if diff := cmp.Diff(want, e.rolls()); diff != "" {
		t.Errorf("rolls (-want +got):\n%s", diff)
	}
	wantStatuses := map[string]StepStatus{"prod/ng-c": StepSucceeded, "staging/ng-c": StepSkipped}
	if diff := cmp.Diff(wantStatuses, clusterStatuses(res)); diff != "" {
		t.Fatalf("statuses (-want +got):\n%s", diff)
	}
	note := "nodegroup ng-c of staging runs the AMI release 1.32.0-20270101, and the plan read 1.32.0-20260101: it was rolled elsewhere since the plan, and is not rolled again"
	if got := res.Clusters[1].Steps[0].Note; got != note {
		t.Errorf("the note of staging/ng-c = %q, want %q", got, note)
	}
	if !strings.Contains(strings.Join(s.events, "\n"), "Notice "+note) {
		t.Errorf("nothing says %q:\n%s", "Notice "+note, strings.Join(s.events, "\n"))
	}
	if res.ExitCode() != exitcode.OK {
		t.Errorf("exit = %d, want 0 (%v)", res.ExitCode(), res.Err)
	}
	// The step of staging keeps the release that was confirmed.
	if got := res.Plan.Clusters[1].Steps()[0].FromRelease; got != "1.32.0-20260101" {
		t.Errorf("the plan of staging has the release %q, want the one that was confirmed", got)
	}
}

// A fleet of two regions, one of which cannot list its clusters. The
// region that answers is rolled, the other is a failure of the plan and
// of the run, and the run is incomplete: exit 4.
func TestFleet_OneRegionFails(t *testing.T) {
	const west = "us-west-2"
	world := fleetWorld(nil)
	world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSListClusters, Region: west}, fleettest.Fault{Deny: true})
	e := newEnv(t, world)
	req := fleetRoll()
	req.Fleet.Regions.Names = []string{east, west}
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StepStatus{"prod/ng-a": StepSucceeded, "staging/ng-a": StepSucceeded}
	if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
		t.Errorf("statuses (-want +got):\n%s", diff)
	}
	if len(res.Failures) != 1 || res.Failures[0].Kind != apidoc.FailureRegion || res.Failures[0].Name != west {
		t.Errorf("failures = %+v, want one for the region %s", res.Failures, west)
	}
	if res.Status != Incomplete || res.ExitCode() != exitcode.Incomplete {
		t.Errorf("status = %s, exit %d; want Incomplete, exit 4", res.Status, res.ExitCode())
	}
	for _, r := range e.rolls() {
		if r.Cluster != "prod" && r.Cluster != "staging" {
			t.Errorf("a roll in a region that did not answer: %+v", r)
		}
	}
}
