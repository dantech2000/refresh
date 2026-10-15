package change

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A batch that has no time left to start a roll still reports what the
// plan decided: the target that is already changing has its Busy row, and
// the one on the latest AMI is Skipped. Only the roll that was not sent
// is NotAttempted.
func TestRoll_OutOfTimeReportsWhatWasDecided(t *testing.T) {
	e := newEnv(t, rollWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup("ng-b").Status = fleettest.StatusUpdating
	}))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	s := &script{t: t}
	hooks := s.hooks()
	hooks.Event = func(ev Event) {
		s.event(ev)
		// The plan is ready, and the user stops the run before its batch.
		if ev.Kind == PlanReady {
			cancel()
		}
	}
	res, err := Run(ctx, e.deps, rollOf(), hooks)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StepStatus{"ng-a": StepNotAttempted, "ng-b": StepBusy, "ng-c": StepSkipped}
	if diff := cmp.Diff(want, statuses(res)); diff != "" {
		t.Errorf("statuses (-want +got):\n%s", diff)
	}
	said := strings.Join(s.events, "\n")
	for _, line := range []string{"StepChanged ng-b Busy", "StepChanged ng-c Skipped"} {
		if !strings.Contains(said, line) {
			t.Errorf("no event %q:\n%s", line, said)
		}
	}
	if res.Status != Interrupted || res.ExitCode() != exitcode.Failure || len(e.rolls()) != 0 {
		t.Errorf("status = %s, exit %d, rolls %v; want Interrupted, exit 1, no roll", res.Status, res.ExitCode(), e.rolls())
	}
}

// A nodegroup that is deleted between the plan and its submit is not
// rolled: the fresh read fails, the step is Failed with the answer of
// EKS, and the other roll of the batch goes on. One failed beside one
// that succeeded is a partial failure, exit 4.
func TestRoll_NodegroupDeletedAfterThePlan(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	// The quota read is part of the gate, so the plan has read the cluster.
	e.onRequest(fleettest.ServiceQuotasGetServiceQuota, func(n int) {
		if n == 1 {
			e.world.Edit(func(s *fleettest.State) {
				c := s.Cluster(east, "prod")
				c.Nodegroups = slices.DeleteFunc(c.Nodegroups, func(ng *fleettest.Nodegroup) bool { return ng.Name == "ng-a" })
			})
		}
	})
	req := rollOf()
	req.SkipVerify = true
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]sentRoll{{"prod", "ng-b", "1.32", false}}, e.rolls()); diff != "" {
		t.Errorf("rolls (-want +got):\n%s", diff)
	}
	row := res.Clusters[0].Steps[0]
	if row.Ref.Name != "ng-a" || row.Status != StepFailed || row.Failure == nil || row.Failure.Reason != apidoc.ReasonNotFound {
		t.Errorf("the row of ng-a = %s with the failure %+v; want Failed with the reason NotFound", row.Status, row.Failure)
	}
	if got := statuses(res)["ng-b"]; got != StepSucceeded {
		t.Errorf("ng-b = %s, want Succeeded", got)
	}
	if res.ExitCode() != exitcode.Incomplete {
		t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), exitcode.Incomplete, res.Err)
	}
}

// --skip-health-check skips the gate, and not the rule that a target must
// be ACTIVE: a nodegroup that is DEGRADED is not rolled, in the plan and
// at the fresh read before the request alike.
func TestRoll_SkipHealthCheckStillNeedsAnActiveTarget(t *testing.T) {
	degrade := func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Status = fleettest.StatusDegraded }
	skipping := func() RollRequest {
		req := rollOf("ng-a")
		req.Policy.Skip, req.SkipVerify = true, true
		return req
	}
	t.Run("in the plan", func(t *testing.T) {
		e := newEnv(t, rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) { degrade(s) }))
		res, err := Run(testContext(t), e.deps, skipping(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		cp := &res.Plan.Clusters[0]
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerNotReady {
			t.Errorf("status = %s, exit %d, blockers %+v; want Refused, exit 3, one NotReady blocker", res.Status, res.ExitCode(), cp.Blockers)
		}
		if cp.Gate.Decision != "" && !cp.Gate.Skipped {
			t.Errorf("the gate ran with --skip-health-check: %+v", cp.Gate)
		}
		if len(e.rolls()) != 0 {
			t.Errorf("rolls = %v, want none", e.rolls())
		}
	})
	t.Run("at the fresh read", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		// The plan reads the instances of the nodegroups after the
		// nodegroups themselves: it has their status by then.
		e.onRequest(fleettest.EC2DescribeInstances, func(n int) {
			if n == 1 {
				e.world.Edit(degrade)
			}
		})
		res, err := Run(testContext(t), e.deps, skipping(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		row := res.Clusters[0].Steps[0]
		if row.Status != StepBlocked || row.Failure == nil || !strings.Contains(row.Failure.Error, "nodegroup ng-a is DEGRADED, not ACTIVE") {
			t.Errorf("the row of ng-a = %s with the failure %+v; want Blocked, not ACTIVE", row.Status, row.Failure)
		}
		if res.ExitCode() != exitcode.Blocked || len(e.rolls()) != 0 {
			t.Errorf("exit = %d, rolls %v; want 3 and no roll", res.ExitCode(), e.rolls())
		}
	})
}
