package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// rollDuration is how long a scripted roll takes on the world's clock.
const rollDuration = 4 * time.Minute

// roll scripts the next roll of ng-a and starts it. The harness then holds
// the world mid-roll until Advance moves it on.
func (h *harness) roll(script fleettest.RollScript) {
	h.t.Helper()
	h.world.Edit(func(s *fleettest.State) {
		s.Cluster(east, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: rollDuration, Roll: script}}
	})
	if _, err := h.world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{}); err != nil {
		h.t.Fatal(err)
	}
}

// clock is a fake clock for the wait of NodesReady: After moves it on by
// the duration at once, so a test never sleeps.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }
func (c *clock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// verify takes the baseline, runs the roll with the script to its end, and
// runs the verification set.
func (h *harness) verify(ctx context.Context, script fleettest.RollScript) []health.Result {
	h.t.Helper()
	base := health.TakeBaseline(ctx, h.client, "prod")
	h.roll(script)
	h.world.Advance(rollDuration)
	c := &clock{now: now}
	in := h.input(ctx, "ng-a")
	in.Now = c.now
	return health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{Interval: 15 * time.Second, After: c.After, Now: c.Now}), in)
}

func TestVerify(t *testing.T) {
	ctx := testContext(t)
	t.Run("a clean roll", func(t *testing.T) {
		h := newHarness(t)
		results := h.verify(ctx, fleettest.RollScript{})
		for _, r := range results {
			if r.Outcome != health.Pass && r.Name != health.AccessCheck {
				t.Errorf("%s: %s (%s), want Pass", r.Name, r.Outcome, r.Message)
			}
		}
		check(t, results[1], want{outcome: health.Pass, message: "2 new nodes ready"})
		if code := health.VerifyExit(results); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
	t.Run("new nodes that never become ready", func(t *testing.T) {
		h := newHarness(t)
		results := h.verify(ctx, fleettest.RollScript{NodesNeverReady: true})
		check(t, results[1], want{outcome: health.Fail, message: "2 of 2 new nodes not ready after the roll: ng-a-u1-1, ng-a-u1-2", findings: []string{"ng-a-u1-1", "ng-a-u1-2"}})
		if code := health.VerifyExit(results); code != 5 {
			t.Errorf("exit = %d, want 5", code)
		}
		// The wait polled until its bound: eight lists of 15 seconds
		// within two minutes, after the first.
		if n := count(h.kube.Requests(), fleettest.KubeListNodes); n < 8 {
			t.Errorf("node lists = %d, want the polls of the wait", n)
		}
	})
	t.Run("a node that becomes ready during the wait", func(t *testing.T) {
		h := newHarness(t)
		base := health.TakeBaseline(ctx, h.client, "prod")
		h.roll(fleettest.RollScript{NodesNeverReady: true})
		h.world.Advance(rollDuration)
		c := &clock{now: now}
		polls := 0
		h.kube.OnRequest = func(_ context.Context, r kubetestRequest) {
			if r.Operation == fleettest.KubeListNodes {
				polls++
				if polls == 3 {
					h.world.Edit(func(s *fleettest.State) {
						for _, n := range s.Cluster(east, "prod").Nodes {
							n.NotReady = false
						}
					})
				}
			}
		}
		in := h.input(ctx, "ng-a")
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{Interval: 15 * time.Second, After: c.After, Now: c.Now}), in)
		check(t, results[1], want{outcome: health.Pass, message: "2 new nodes ready"})
	})
	t.Run("pods left pending", func(t *testing.T) {
		h := newHarness(t)
		results := h.verify(ctx, fleettest.RollScript{PendingPods: true})
		check(t, results[2], want{outcome: health.Fail, message: "newly Pending after the roll", findings: []string{"default/web-1-u1", "default/web-2-u1", "kube-system/coredns-1-u1", "kube-system/kube-proxy-1-u1"}})
		if code := health.VerifyExit(results); code != 5 {
			t.Errorf("exit = %d, want 5", code)
		}
	})
	t.Run("a pod pending before the roll is not new", func(t *testing.T) {
		h := newHarness(t, func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: "stuck", Phase: "Pending"})
		})
		results := h.verify(ctx, fleettest.RollScript{})
		check(t, results[2], want{outcome: health.Pass, message: "no new Pending pods"})
	})
	t.Run("a pod pinned to a node that is gone is not stuck", func(t *testing.T) {
		h := newHarness(t)
		base := health.TakeBaseline(ctx, h.client, "prod")
		h.roll(fleettest.RollScript{})
		h.world.Advance(rollDuration)
		h.world.Edit(func(s *fleettest.State) {
			c := s.Cluster(east, "prod")
			c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "kube-system", Name: "aws-node-old", Node: "a-1", Phase: "Pending"})
		})
		in := h.input(ctx, "ng-a")
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), in)
		check(t, results[2], want{outcome: health.Pass, message: "no new Pending pods"})
	})
	t.Run("a degraded nodegroup", func(t *testing.T) {
		h := newHarness(t)
		h.world.Edit(func(s *fleettest.State) {
			s.Cluster(east, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: rollDuration, ResourceStatus: fleettest.StatusDegraded}}
		})
		base := health.TakeBaseline(ctx, h.client, "prod")
		if _, err := h.world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{}); err != nil {
			t.Fatal(err)
		}
		h.world.Advance(rollDuration)
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), h.input(ctx, "ng-a"))
		check(t, results[0], want{outcome: health.Fail, message: "ng-a is DEGRADED (expected ACTIVE)", findings: []string{"ng-a"}})
	})
	t.Run("the pod list fails after the roll", func(t *testing.T) {
		h := newHarness(t)
		base := health.TakeBaseline(ctx, h.client, "prod")
		h.roll(fleettest.RollScript{})
		h.world.Advance(rollDuration)
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), h.input(ctx, "ng-a"))
		check(t, results[2], want{outcome: health.Unreadable, message: "could not list the Pending pods after the roll", failures: 1})
		if code := health.VerifyExit(results); code != 4 {
			t.Errorf("exit = %d, want 4", code)
		}
	})
	t.Run("the baseline could not be taken", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		base := health.TakeBaseline(ctx, h.client, "prod")
		if base.Taken != true || len(base.Failures) != 1 {
			t.Fatalf("baseline = %+v", base)
		}
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{})
		h.roll(fleettest.RollScript{})
		h.world.Advance(rollDuration)
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), h.input(ctx, "ng-a"))
		check(t, results[1], want{outcome: health.Unreadable, message: "could not be listed before the roll", failures: 1})
		check(t, results[2], want{outcome: health.Unreadable, failures: 1})
	})
	t.Run("the nodegroup cannot be read after the roll", func(t *testing.T) {
		h := newHarness(t)
		base := health.TakeBaseline(ctx, h.client, "prod")
		h.roll(fleettest.RollScript{PendingPods: true})
		h.world.Advance(rollDuration)
		h.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-a"}, fleettest.Fault{Deny: true})
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), h.input(ctx, "ng-a"))
		check(t, results[0], want{outcome: health.Unreadable, message: "not read after the roll: ng-a"})
		// A failure beside an unreadable check: the highest code wins.
		if code := health.VerifyExit(results); code != 5 {
			t.Errorf("exit = %d, want 5", code)
		}
	})
	t.Run("no Kubernetes", func(t *testing.T) {
		h := newHarness(t)
		base := health.TakeBaseline(ctx, nil, "prod")
		h.roll(fleettest.RollScript{})
		h.world.Advance(rollDuration)
		in := h.input(ctx, "ng-a")
		in.Kube = nil
		results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), in)
		check(t, results[0], want{outcome: health.Pass})
		check(t, results[1], want{outcome: health.Skipped, message: "no kubeconfig context points at prod"})
		check(t, results[2], want{outcome: health.Skipped})
		if code := health.VerifyExit(results); code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
	})
}

// A snapshot read through the fetcher after the roll sees the nodegroup
// ACTIVE at its new release.
func TestVerify_SnapshotAfterTheRoll(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	h.roll(fleettest.RollScript{})
	h.world.Advance(rollDuration)
	in := h.input(ctx, "ng-a")
	if n := in.Snapshot.Nodegroups[0]; n.Name != "ng-a" || n.Status != "ACTIVE" || n.ReleaseVersion != fleettest.LatestRelease("1.33") {
		t.Errorf("nodegroup after the roll = %+v", n)
	}
	if in.Snapshot.Have.Nodegroups != fleet.Collected {
		t.Errorf("state = %v", in.Snapshot.Have.Nodegroups)
	}
}
