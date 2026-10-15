package change

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// watched runs a roll of ng-a whose update never ends, and cancels the
// run when stop accepts an event: the event is the handshake, so the test
// waits for nothing. It returns the events that stop accepted.
func watched(t *testing.T, e *env, req RollRequest, stop func(Event) bool) (Result, []Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var (
		mu   sync.Mutex
		seen []Event
	)
	res, err := Run(ctx, e.deps, req, Hooks{Event: func(ev Event) {
		if stop(ev) {
			mu.Lock()
			seen = append(seen, ev)
			mu.Unlock()
			cancel()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res, seen
}

func neverEnds(s *fleettest.State, _ *fleettest.Cluster) {
	s.DefaultUpdate = fleettest.UpdateScript{Never: true}
}

// The Auto Scaling watch beside the wait reports a failed launch as a
// notice, and changes nothing in the result.
func TestRoll_ScalingFailureIsANotice(t *testing.T) {
	world := rollWorld(neverEnds)
	now := world.Now()
	world.Edit(func(s *fleettest.State) {
		s.ScalingActivities = append(s.ScalingActivities,
			fleettest.ScalingActivity{ID: "act-old", Group: "eks-prod-ng-a", Status: "Failed", Message: "an old failure", Start: now.Add(-time.Hour)},
			fleettest.ScalingActivity{ID: "act-1", Group: "eks-prod-ng-a", Status: "Failed", Message: "You have requested more vCPU capacity\n than your current limit", Start: now},
			fleettest.ScalingActivity{ID: "act-2", Group: "eks-prod-ng-a", Status: "Successful", Message: "launched", Start: now},
		)
	})
	e := newEnv(t, world)
	req := rollOf("ng-a")
	req.Policy.Skip = true
	res, seen := watched(t, e, req, func(ev Event) bool { return ev.Kind == Notice && strings.Contains(ev.Text, "Auto Scaling group") })
	want := "Auto Scaling group eks-prod-ng-a: You have requested more vCPU capacity than your current limit"
	if len(seen) == 0 || seen[0].Text != want {
		t.Errorf("notices = %+v, want %q", seen, want)
	}
	// The watch is beside the wait: the roll is what the wait says.
	if got := statuses(res)["ng-a"]; got != StepInProgress {
		t.Errorf("ng-a = %s, want InProgress", got)
	}
}

// A watch that may not read the activities ends with no word, and the
// roll goes on to its end.
func TestRoll_ScalingWatchDeniedChangesNothing(t *testing.T) {
	world := rollWorld(nil)
	world.SetFault(fleettest.AutoScalingDescribeScalingActivities, fleettest.Fault{Deny: true})
	e := newEnv(t, world)
	s := &script{t: t}
	res, err := Run(testContext(t), e.deps, rollOf("ng-a"), s.hooks())
	if err != nil || res.ExitCode() != 0 {
		t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
	}
	if said := strings.Join(s.events, "\n"); strings.Contains(said, "Auto Scaling") || strings.Contains(said, "AccessDenied") {
		t.Errorf("the denied watch said something:\n%s", said)
	}
}

// With Common.Nodes, an observer beside the wait reports the nodes of the
// one roll. Its clock is Deps.Tick, which the test holds.
func TestRoll_NodeObserverReports(t *testing.T) {
	e := newEnv(t, rollWorld(neverEnds))
	e.withKube(t)
	e.deps.Tick = func(time.Duration) <-chan time.Time { return nil }
	req := rollOf("ng-a")
	req.Policy.Skip, req.Nodes = true, true
	res, seen := watched(t, e, req, func(ev Event) bool { return ev.Kind == NodeProgress })
	if len(seen) == 0 {
		t.Fatal("no NodeProgress event")
	}
	nodes := seen[0].Nodes
	if nodes.Nodegroup != "ng-a" || nodes.Snapshot.Total < 2 || nodes.StartedAt.IsZero() || seen[0].Step.Name != "ng-a" {
		t.Errorf("event = %+v with the nodes %+v", seen[0], nodes)
	}
	if got := statuses(res)["ng-a"]; got != StepInProgress {
		t.Errorf("ng-a = %s, want InProgress", got)
	}
}

// Without Kubernetes access the observer cannot run: one notice says so,
// and the roll goes on. Several rolls at once have no node view.
func TestRoll_NodeObserverNeedsAccessAndOneRoll(t *testing.T) {
	t.Run("no access", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		req := rollOf("ng-a")
		req.Nodes = true
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
		}
		if n := strings.Count(strings.Join(s.events, "\n"), "the nodes of the roll are not shown"); n != 1 {
			t.Errorf("%d notices about the node view, want 1:\n%s", n, strings.Join(s.events, "\n"))
		}
	})
	t.Run("two rolls", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		e.withKube(t)
		e.deps.Tick = func(time.Duration) <-chan time.Time { return nil }
		req := rollOf("ng-a", "ng-b")
		req.Policy.Skip, req.Nodes = true, true
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
		}
		if said := strings.Join(s.events, "\n"); strings.Contains(said, string(NodeProgress)) {
			t.Errorf("a batch of two rolls has a node view:\n%s", said)
		}
	})
}
