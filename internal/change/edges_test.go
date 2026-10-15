package change

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// stepOf returns the result of the step of the named resource.
func stepOf(t *testing.T, res Result, name string) StepResult {
	t.Helper()
	for _, c := range res.Clusters {
		for _, s := range c.Steps {
			if s.Ref.Name == name {
				return s
			}
		}
	}
	t.Fatalf("no step for %s in %+v", name, res.Clusters)
	return StepResult{}
}

// A laptop that sleeps during a wait: the clock jumps hours past
// --wait-timeout between two polls. A final answer after the jump still
// counts. An update still in progress ends the wait as a timeout at the
// next poll, exit 4, and the run polls no more. Credentials that expired
// during the sleep stop the wait at once: the update may still run, exit 4.
func TestRoll_ClockJumpsDuringAWait(t *testing.T) {
	const sleep = 10 * time.Hour
	tests := []struct {
		name string
		seed func(s *fleettest.State, c *fleettest.Cluster)
		// expire makes AWS refuse the token after the sleep.
		expire bool
		exit   int
		status StepStatus
		reason apidoc.Reason
	}{
		{name: "the update ended while asleep", exit: exitcode.OK, status: StepSucceeded},
		{
			name: "the update still runs", seed: func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} },
			exit: exitcode.Incomplete, status: StepInProgress, reason: apidoc.ReasonTimeout,
		},
		{
			name: "the token expired while asleep", seed: func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} },
			expire: true, exit: exitcode.Incomplete, status: StepInProgress, reason: apidoc.ReasonNotMonitored,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(tt.seed))
			e.withKube(t)
			// The second poll is the handshake: the wait is under way.
			e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
				if n == 2 {
					e.world.Advance(sleep)
					if tt.expire {
						e.world.SetScopedFault(fleettest.Scope{}, fleettest.Fault{Code: "ExpiredTokenException"})
					}
				}
			})
			req := rollOf("ng-a")
			req.Limits.Wait = 40 * time.Minute
			start := e.world.Now()
			res := mustRun(t, e, req, Hooks{})
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), tt.exit, res.Err)
			}
			s := stepOf(t, res, "ng-a")
			if s.Status != tt.status || (tt.reason != "" && (s.Failure == nil || s.Failure.Reason != tt.reason)) {
				t.Errorf("ng-a = %s, failure %+v; want %s with the reason %q", s.Status, s.Failure, tt.status, tt.reason)
			}
			if polls := e.count(fleettest.EKSDescribeUpdate); polls > 3 {
				t.Errorf("%d polls, want at most one after the jump", polls)
			}
			if waited := e.world.Now().Sub(start); waited > sleep+5*time.Minute {
				t.Errorf("the run went on %s after the jump", waited-sleep)
			}
			if tt.exit != exitcode.OK && (res.Err == nil || !strings.Contains(res.Err.Error(), "may still be running")) {
				t.Errorf("message = %v, want that the update may still be running", res.Err)
			}
		})
	}
}

// A nodegroup deleted during its roll: its update is gone, so the wait
// cannot follow it, and the run says so and exits 4. The other roll of
// the batch goes on, and nothing about the deleted nodegroup is verified.
func TestRoll_NodegroupDeletedDuringTheWait(t *testing.T) {
	e := newEnv(t, rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 5 * time.Minute}
	}))
	e.withKube(t)
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 2 {
			e.world.Edit(func(s *fleettest.State) {
				c := s.Cluster(east, "prod")
				c.Nodegroups = slices.DeleteFunc(c.Nodegroups, func(n *fleettest.Nodegroup) bool { return n.Name == "ng-a" })
				c.Nodes = slices.DeleteFunc(c.Nodes, func(n *fleettest.Node) bool { return n.Nodegroup == "ng-a" })
			})
		}
	})
	res := mustRun(t, e, rollOf("ng-a", "ng-b"), Hooks{})
	if res.ExitCode() != exitcode.Incomplete {
		t.Errorf("exit = %d, want 4 (%v)", res.ExitCode(), res.Err)
	}
	a, b := stepOf(t, res, "ng-a"), stepOf(t, res, "ng-b")
	if a.Status != StepInProgress || a.Failure == nil || a.Failure.Reason != apidoc.ReasonNotMonitored {
		t.Errorf("ng-a = %s, failure %+v; want InProgress, NotMonitored", a.Status, a.Failure)
	}
	if b.Status != StepSucceeded {
		t.Errorf("ng-b = %s, want Succeeded", b.Status)
	}
	for _, v := range res.Clusters[0].Verification {
		if strings.Contains(v.Name, "ng-a") || slices.Contains(v.Findings, "ng-a") {
			t.Errorf("the deleted nodegroup was verified: %+v", v)
		}
	}
}

// The cluster deleted while the control plane updates: the run cannot
// follow the update, sends nothing more, and names the command that goes
// on.
func TestUpgrade_ClusterDeletedDuringTheWait(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 2 {
			e.world.Edit(func(s *fleettest.State) { s.Clusters = nil })
		}
	})
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	if code := res.ExitCode(); code != exitcode.Incomplete && code != exitcode.Failure {
		t.Errorf("exit = %d, want 1 or 4 (%v)", code, res.Err)
	}
	if diff := slices.Compare(e.sent(), []string{sentRefresh, sentCP33}); diff != 0 {
		t.Errorf("sent %v, want the refresh and the control plane only", e.sent())
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "refresh cluster upgrade prod --to 1.33") {
		t.Errorf("message = %v, want the command that goes on", res.Err)
	}
}

// Someone scales the nodegroup in the console while it rolls: two more
// nodes join, Ready. The roll still succeeds, and the verification counts
// the new nodes, all Ready.
func TestRoll_ScaledDuringTheRoll(t *testing.T) {
	e := newEnv(t, rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 5 * time.Minute}
	}))
	e.withKube(t)
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 2 {
			e.world.Edit(func(s *fleettest.State) {
				c := s.Cluster(east, "prod")
				ng := c.Nodegroup("ng-a")
				ng.Desired, ng.Max = 4, 4
				c.Nodes = append(c.Nodes, &fleettest.Node{Name: "ng-a-console-1", Nodegroup: "ng-a"}, &fleettest.Node{Name: "ng-a-console-2", Nodegroup: "ng-a"})
			})
		}
	})
	res := mustRun(t, e, rollOf("ng-a"), Hooks{})
	if res.ExitCode() != exitcode.OK {
		t.Errorf("exit = %d, want 0 (%v)", res.ExitCode(), res.Err)
	}
	for _, v := range res.Clusters[0].Verification {
		if v.Outcome != health.Pass {
			t.Errorf("%s = %s (%s), want Pass", v.Name, v.Outcome, v.Message)
		}
	}
}

// A nodegroup scaled to zero has no node and no pod: the gate has nothing
// to drain, the roll moves its version, and the verification finds no new
// node to wait for.
func TestRoll_NodegroupAtZero(t *testing.T) {
	e := newEnv(t, rollWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		ng := c.Nodegroup("ng-a")
		ng.Desired, ng.Min, ng.Max = 0, 0, 3
		c.Nodes = slices.DeleteFunc(c.Nodes, func(n *fleettest.Node) bool { return n.Nodegroup == "ng-a" })
		c.Pods = slices.DeleteFunc(c.Pods, func(p *fleettest.Pod) bool { return strings.HasPrefix(p.Node, "ng-a-") })
		for i := range c.PDBs {
			if c.PDBs[i].Name == "ng-a" {
				c.PDBs[i].DisruptionsAllowed, c.PDBs[i].CurrentHealthy, c.PDBs[i].ExpectedPods = 0, 0, 0
			}
		}
		for i := range s.AutoScalingGroups {
			if s.AutoScalingGroups[i].Name == "eks-prod-ng-a" {
				s.AutoScalingGroups[i].InstanceIDs = nil
			}
		}
	}))
	e.withKube(t)
	req := rollOf("ng-a")
	req.Policy.Yes = true
	res := mustRun(t, e, req, Hooks{})
	if res.ExitCode() != exitcode.OK && res.ExitCode() != exitcode.NeedsAttention {
		t.Errorf("exit = %d, want 0 (%v)", res.ExitCode(), res.Err)
	}
	if got := e.rolls(); len(got) != 1 || got[0].Nodegroup != "ng-a" {
		t.Errorf("rolls = %+v, want ng-a once", got)
	}
	for _, v := range res.Clusters[0].Verification {
		if v.Outcome != health.Pass {
			t.Errorf("%s = %s (%s), want Pass", v.Name, v.Outcome, v.Message)
		}
	}
}

// An upgrade and a rollback of one cluster at once, in either order. The
// second run finds the control plane UPDATING toward the other goal. It
// must not attach to that update and then send its own, which would undo
// the other run: it changes nothing. The refresh of the insights is the
// one request a blocked upgrade may send. The exit code of a busy target
// is not asserted here.
func TestEngine_UpgradeAndRollbackAtOnce(t *testing.T) {
	long := func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultUpdate = fleettest.UpdateScript{After: 10 * time.Minute}
	}
	tests := []struct {
		name string
		// world and other build the cluster and start the other run's
		// control-plane update.
		world func() *fleettest.World
		other fleettest.ClusterVersionRequest
		req   Request
	}{
		{
			name: "an upgrade while a rollback runs",
			world: func() *fleettest.World {
				return rolledBackWorld(long)
			},
			other: fleettest.ClusterVersionRequest{Version: new("1.32"), Force: new(true)},
			req:   upgradeTo("1.33"),
		},
		{
			name: "an upgrade past the rolled-back version while a rollback runs",
			world: func() *fleettest.World {
				return rolledBackWorld(long)
			},
			other: fleettest.ClusterVersionRequest{Version: new("1.32"), Force: new(true)},
			req:   upgradeTo("1.34"),
		},
		{
			name: "a rollback while an upgrade runs",
			world: func() *fleettest.World {
				return engineWorld(long)
			},
			other: fleettest.ClusterVersionRequest{Version: new("1.33")},
			req:   rollbackOf(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := tt.world()
			other, err := world.UpdateClusterVersion(east, "prod", tt.other)
			if err != nil {
				t.Fatal(err)
			}
			e := newEnv(t, world)
			res := mustRun(t, e, tt.req, Hooks{})
			for _, s := range e.sent() {
				if s != sentRefresh {
					t.Errorf("sent %s while another run changes the control plane", s)
				}
			}
			if res.Err == nil || !strings.Contains(res.Err.Error(), "UPDATING") {
				t.Errorf("message = %v, want one that names the control plane in flight", res.Err)
			}
			if res.ExitCode() == exitcode.OK {
				t.Errorf("the run succeeded beside another run's update")
			}
			world.View(func(s *fleettest.State) {
				if u := s.Cluster(east, "prod").Update(other.ID); u == nil || u.Status != fleettest.UpdateInProgress {
					t.Errorf("the other run's update is %+v, want it in progress, untouched", u)
				}
			})
		})
	}
}
