package change

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// rollElsewhere starts a roll of a nodegroup to a version, as another
// process does, and returns the ID of the update. The roll takes ten
// minutes unless the nodegroup has a script of its own.
func rollElsewhere(t *testing.T, world *fleettest.World, nodegroup, version string) string {
	t.Helper()
	u, err := world.UpdateNodegroupVersion(east, "prod", nodegroup, fleettest.NodegroupVersionRequest{Version: &version})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// rolledBackWorld is the world of the rollback after the upgrade to
// 1.33: the nodegroups and the add-ons are back on 1.32, and the control
// plane is not.
func rolledBackWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	return upgradedWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		for _, ng := range c.Nodegroups {
			ng.Version = "1.32"
		}
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni32, dns32, proxy33
		if edit != nil {
			edit(s, c)
		}
	})
}

// longRolls makes every nodegroup update take ten minutes.
func longRolls(s *fleettest.State, _ *fleettest.Cluster) {
	s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 10 * time.Minute}
}

// A nodegroup at the goal of a step and UPDATING is attached to only when
// the update in flight moves it to that goal. The real case: a rollback
// rolls ng-a back to 1.32, so EKS reports it UPDATING at 1.33 until the
// roll ends, and an upgrade to 1.33 starts meanwhile.
func TestEngine_UnsettledNodegroupRollingElsewhereIsBusy(t *testing.T) {
	t.Run("an upgrade refuses a nodegroup that rolls back", func(t *testing.T) {
		world := upgradedWorld(longRolls)
		id := rollElsewhere(t, world, "ng-a", "1.32")
		e := newEnv(t, world)
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want Refused, exit 3, and no request", res.Status, res.ExitCode(), e.sent())
		}
		want := "nodegroup ng-a is rolling to 1.32 (update " + id + "), not to 1.33"
		if res.Err == nil || !strings.Contains(res.Err.Error(), want) {
			t.Errorf("message = %v, want %q", res.Err, want)
		}
	})
	t.Run("a rollback refuses a nodegroup that rolls forward", func(t *testing.T) {
		world := rolledBackWorld(longRolls)
		id := rollElsewhere(t, world, "ng-a", "1.33")
		e := newEnv(t, world)
		res := mustRun(t, e, rollbackOf(), Hooks{})
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want Refused, exit 3, and no request", res.Status, res.ExitCode(), e.sent())
		}
		want := "nodegroup ng-a is rolling to 1.33 (update " + id + "), not to 1.32"
		if res.Err == nil || !strings.Contains(res.Err.Error(), want) {
			t.Errorf("message = %v, want %q", res.Err, want)
		}
	})
	t.Run("a dry run shows the refusal", func(t *testing.T) {
		world := upgradedWorld(longRolls)
		id := rollElsewhere(t, world, "ng-a", "1.32")
		e := newEnv(t, world)
		p, err := NewPlan(testContext(t), e.deps, upgradeTo("1.33"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Decision() != DecisionBlocked || p.ExitCode() != exitcode.Blocked {
			t.Errorf("decision = %s, exit %d; want Blocked, exit 3", p.Decision(), p.ExitCode())
		}
		want := "nodegroup ng-a is rolling to 1.32 (update " + id + "), not to 1.33"
		if bs := p.Clusters[0].Blockers; len(bs) != 1 || bs[0].Kind != BlockerBusy || bs[0].Step == nil || !strings.Contains(bs[0].Message, want) {
			t.Errorf("blockers = %+v, want one Busy blocker of ng-a with %q", bs, want)
		}
	})
	t.Run("an update of another type is not attached to", func(t *testing.T) {
		world := upgradedWorld(longRolls)
		desired := int32(3)
		u, err := world.UpdateNodegroupConfig(east, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &desired}})
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, world)
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want exit 3 and no request", res.Status, res.ExitCode(), e.sent())
		}
		want := "nodegroup ng-a is UPDATING with a ConfigUpdate (update " + u.ID + "), not rolling to 1.33"
		if res.Err == nil || !strings.Contains(res.Err.Error(), want) {
			t.Errorf("message = %v, want %q", res.Err, want)
		}
	})
	t.Run("an update that turns away after the plan blocks the step", func(t *testing.T) {
		// The plan sees the roll of an earlier run to 1.33. Before the
		// nodegroup phase that update is a roll to 1.32.
		world := upgradedWorld(longRolls)
		id := rollElsewhere(t, world, "ng-a", "1.33")
		e := newEnv(t, world)
		hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			if next.Phase.Kind == PhaseNodegroups && next.Kind == "" {
				world.Edit(func(s *fleettest.State) {
					for _, u := range s.Cluster(east, "prod").Updates {
						if u.ID == id {
							u.Params[0].Value = "1.32"
						}
					}
				})
			}
			return Checkpoint{}, nil
		}}
		res := mustRun(t, e, upgradeTo("1.33"), hooks)
		if res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want exit 3 and no request", res.Status, res.ExitCode(), e.sent())
		}
		a := stepResultOf(t, res, StepNodegroup, "ng-a")
		wantBlocked(t, a, "nodegroup ng-a is rolling to 1.32 (update "+id+"), not to 1.33")
		if a.Failure.Reason != apidoc.ReasonBusy {
			t.Errorf("reason = %s, want Busy", a.Failure.Reason)
		}
	})
}

// The resume case stays: the roll in flight is the one of an earlier run
// to the same goal, so the run attaches to it and verifies it.
func TestEngine_UnsettledNodegroupOwnRollIsAttached(t *testing.T) {
	world := upgradedWorld(longRolls)
	rollElsewhere(t, world, "ng-a", "1.33")
	e := newEnv(t, world)
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	if res.ExitCode() != exitcode.OK || len(e.sent()) != 0 {
		t.Errorf("exit %d (%v), requests %v; want exit 0 and no request", res.ExitCode(), res.Err, e.sent())
	}
	if a := stepResultOf(t, res, StepNodegroup, "ng-a"); a.Status != StepSucceeded || !a.Attached {
		t.Errorf("ng-a = %s, attached %v; want Succeeded and attached", a.Status, a.Attached)
	}
}

// A nodegroup that settles away from the goal of its step is never done.
// The step plans again: the roll that it then sends is its own, and here
// that roll fails, so the step fails.
func TestEngine_UnsettledNodegroupThatMissesTheGoalIsNotDone(t *testing.T) {
	failing := fleettest.UpdateScript{After: 2 * time.Minute, Result: fleettest.UpdateFailed}
	world := upgradedWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{failing, failing}
	})
	rollElsewhere(t, world, "ng-a", "1.33")
	e := newEnv(t, world)
	var notices []string
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{Event: func(ev Event) {
		if ev.Kind != Notice {
			return
		}
		notices = append(notices, ev.Text)
		if strings.Contains(ev.Text, "waiting for it to settle") {
			// Something else moves the nodegroup back while the run waits.
			world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = "1.32" })
		}
	}})
	a := stepResultOf(t, res, StepNodegroup, "ng-a")
	if a.Status == StepSucceeded || res.ExitCode() == exitcode.OK {
		t.Errorf("ng-a = %s, exit %d; want a step that did not succeed", a.Status, res.ExitCode())
	}
	if diff := cmp.Diff([]string{sentA33}, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	if text := strings.Join(notices, "\n"); !strings.Contains(text, "did not reach 1.33 (it is ACTIVE at 1.32)") {
		t.Errorf("notices = %q, want one that says the update did not reach 1.33", notices)
	}
}
