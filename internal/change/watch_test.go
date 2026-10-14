package change

import (
	"errors"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// startRollElsewhere starts a roll of the nodegroup web in the world, as
// another process would, with the script.
func startRollElsewhere(t *testing.T, w *fleettest.World, script fleettest.UpdateScript) string {
	t.Helper()
	w.Edit(func(s *fleettest.State) {
		ng := s.Cluster(east, "prod").Nodegroup("web")
		ng.UpdateScripts = append([]fleettest.UpdateScript{script}, ng.UpdateScripts...)
	})
	version := "1.33"
	u, err := w.UpdateNodegroupVersion(east, "prod", "web", fleettest.NodegroupVersionRequest{Version: &version})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// waitFinished waits for the handle through its own channel.
func waitFinished(t *testing.T, h *Handle) View {
	t.Helper()
	ctx := testContext(t)
	for {
		ch := h.Changed()
		if v := h.View(); v.State == Finished {
			return v
		}
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("the watch did not finish")
		}
	}
}

// A roll started elsewhere is watched to its end: one attached step, the
// polls of its update, the verification after it, and no control.
func TestWatch_RollStartedElsewhere(t *testing.T) {
	env := newEnv(t, newWorld(nil))
	id := startRollElsewhere(t, env.world, fleettest.UpdateScript{After: 2 * time.Minute})
	r := Resource{Target: handleTarget, Kind: StepNodegroup, Name: "web"}
	h := Watch(testContext(t), env.deps, r, WatchOptions{Nodes: true, Limits: Limits{Wait: time.Hour, Poll: 20 * time.Second}})
	v := waitFinished(t, h)
	if !v.Elsewhere || v.Controls != (Controls{}) || v.Kind != NodegroupUpdate {
		t.Fatalf("view = elsewhere %v, controls %+v, kind %s", v.Elsewhere, v.Controls, v.Kind)
	}
	if err := h.Stop(); !errors.Is(err, ErrNoControls) {
		t.Fatalf("Stop = %v, want ErrNoControls", err)
	}
	if v.Result.Status != Succeeded || len(v.Steps) != 1 || v.Steps[0].Status != StepSucceeded || !v.Steps[0].Attached || v.Steps[0].UpdateID != id {
		t.Fatalf("result %s (%v), steps %+v, events %d", v.Result.Status, v.Result.Err, v.Steps, len(h.Events(0)))
	}
	if v.Progress[v.Steps[0].Ref].Update == nil {
		t.Fatal("no poll was reported")
	}
	kinds := map[EventKind]int{}
	for _, e := range h.Events(0) {
		kinds[e.Kind]++
	}
	for _, k := range []EventKind{PlanReady, PhaseStarted, StepChanged, VerifyReport, PhaseEnded} {
		if kinds[k] == 0 {
			t.Errorf("no %s event", k)
		}
	}
	if kinds[UpdatePolled] != 0 {
		t.Error("polls were logged")
	}
	if len(v.Result.Clusters[0].Verification) == 0 {
		t.Error("no verification after the roll")
	}
}

func TestWatch_NothingInFlight(t *testing.T) {
	env := newEnv(t, newWorld(nil))
	h := Watch(testContext(t), env.deps, Resource{Target: handleTarget, Kind: StepNodegroup, Name: "web"}, WatchOptions{})
	if v := waitFinished(t, h); v.Result.Status != NothingToDo || len(v.Steps) != 0 {
		t.Fatalf("result = %s with %d steps, want NothingToDo with none", v.Result.Status, len(v.Steps))
	}
}

func TestWatch_FailedUpdate(t *testing.T) {
	env := newEnv(t, newWorld(nil))
	startRollElsewhere(t, env.world, fleettest.UpdateScript{After: time.Minute, Result: fleettest.UpdateFailed, Errors: []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "a PDB blocked the drain"}}})
	h := Watch(testContext(t), env.deps, Resource{Target: handleTarget, Kind: StepNodegroup, Name: "web"}, WatchOptions{Limits: Limits{Wait: time.Hour, Poll: 20 * time.Second}})
	v := waitFinished(t, h)
	if v.Result.Status != Failed || v.Steps[0].Status != StepFailed || v.Steps[0].Failure == nil || v.Result.ExitCode() != 1 {
		t.Fatalf("result %s, step %+v, exit %d", v.Result.Status, v.Steps[0], v.Result.ExitCode())
	}
}

// A control-plane update is an upgrade or a rollback by its type.
func TestWatch_ControlPlaneByDirection(t *testing.T) {
	for _, tc := range []struct {
		version string
		kind    Kind
	}{{"1.34", ClusterUpgrade}, {"1.32", ClusterRollback}} {
		env := newEnv(t, newWorld(nil))
		env.world.Edit(func(s *fleettest.State) {
			s.Cluster(east, "prod").UpdateScripts = []fleettest.UpdateScript{{After: time.Minute}}
		})
		if _, err := env.world.UpdateClusterVersion(east, "prod", fleettest.ClusterVersionRequest{Version: &tc.version}); err != nil {
			t.Fatal(err)
		}
		h := Watch(testContext(t), env.deps, Resource{Target: handleTarget, Kind: StepControlPlane}, WatchOptions{Limits: Limits{Wait: time.Hour, Poll: 20 * time.Second}})
		v := waitFinished(t, h)
		if v.Plan.Kind != tc.kind || v.Result.Status != Succeeded {
			t.Fatalf("to %s: kind %s, result %s; want %s, Succeeded", tc.version, v.Plan.Kind, v.Result.Status, tc.kind)
		}
	}
}
