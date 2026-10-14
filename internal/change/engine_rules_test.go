package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// degradeNodegroup makes a nodegroup DEGRADED, which the Nodes check of the gate
// fails for a target.
func degradeNodegroup(name string) func(*fleettest.State, *fleettest.Cluster) {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup(name).Status = fleettest.StatusDegraded
		c.Nodegroup(name).HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "could not launch instances"}}
	}
}

// errorInsight gives the cluster an ERROR insight for 1.33.
func errorInsight(_ *fleettest.State, c *fleettest.Cluster) {
	c.Insights = []fleettest.Insight{{ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: fleettest.InsightError, KubernetesVersion: "1.33"}}
}

// Safety rule 1. Nothing is sent without consent. A dry run sends no
// request that is not a read, not even the insights refresh. A real run
// that ends before its first change has sent the refresh and nothing
// else: after a no, after a gate that blocks, after a plan that is
// refused, and with nobody to ask and no --yes.
func TestEngine_NothingSentWithoutConsent(t *testing.T) {
	noYes := func(r UpgradeRequest) UpgradeRequest {
		r.Policy.Yes = false
		return r
	}
	t.Run("dry run", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		p, err := NewPlan(testContext(t), e.deps, upgradeTo("1.33"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Decision() != Ask || len(e.sent()) != 0 {
			t.Errorf("decision %s, requests %v; want a plan that asks, and no request that changes anything", p.Decision(), e.sent())
		}
	})
	tests := []struct {
		name   string
		seed   func(*fleettest.State, *fleettest.Cluster)
		req    UpgradeRequest
		hooks  Hooks
		status RunStatus
		exit   int
	}{
		{
			name: "answered no", req: noYes(upgradeTo("1.33")), status: Declined, exit: exitcode.Failure,
			hooks: Hooks{Ask: func(context.Context, Question) (bool, error) { return false, nil }},
		},
		{name: "the gate blocks", seed: degradeNodegroup("ng-b"), req: upgradeTo("1.33"), status: Refused, exit: exitcode.Blocked},
		{name: "the plan is refused", seed: errorInsight, req: upgradeTo("1.33"), status: Refused, exit: exitcode.Blocked},
		{name: "nobody can be asked", req: noYes(upgradeTo("1.33")), status: Refused, exit: exitcode.Failure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, engineWorld(tt.seed))
			res := mustRun(t, e, tt.req, tt.hooks)
			if res.Status != tt.status || res.ExitCode() != tt.exit {
				t.Errorf("status = %s, exit %d, err %v; want %s, exit %d", res.Status, res.ExitCode(), res.Err, tt.status, tt.exit)
			}
			if diff := cmp.Diff([]string{sentRefresh}, e.sent()); diff != "" {
				t.Errorf("requests (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("a rollback that is declined sends nothing", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(nil))
		req := rollbackOf()
		req.Policy.Yes = false
		res := mustRun(t, e, req, Hooks{Ask: func(context.Context, Question) (bool, error) { return false, nil }})
		if res.Status != Declined || len(e.sent()) != 0 {
			t.Errorf("status = %s, requests %v; want Declined and no request", res.Status, e.sent())
		}
	})
}

// gateReadsBefore reports, for each request that changes the cluster,
// whether the gate read the vCPU quota since the request before it.
func gateReadsBefore(e *env) map[string]bool {
	out := map[string]bool{}
	gated := false
	for _, r := range e.srv.Requests() {
		switch {
		case r.Operation == fleettest.ServiceQuotasGetServiceQuota:
			gated = true
		case r.Mutates:
			line := string(r.Operation)
			if name, ok := r.Input["nodegroupName"]; ok {
				line += " " + fmt.Sprint(name)
			}
			out[line] = gated
			gated = false
		}
	}
	return out
}

// Safety rule 2. Every gate runs in the plan, before any change, and
// again right before its own phase.
func TestEngine_GatesRunInThePlanAndBeforeEachPhase(t *testing.T) {
	t.Run("the order of the gate reads", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.ExitCode() != exitcode.OK {
			t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
		}
		got := gateReadsBefore(e)
		// The gate of the plan ran before the first change, and the gate
		// of each nodegroup ran after the change before its roll.
		for _, req := range []string{"eks:UpdateClusterVersion", "eks:UpdateNodegroupVersion ng-a", "eks:UpdateNodegroupVersion ng-b"} {
			if !got[req] {
				t.Errorf("no gate read before %s: %v", req, got)
			}
		}
	})
	t.Run("a blocker of the last nodegroup stops the first change", func(t *testing.T) {
		e := newEnv(t, engineWorld(degradeNodegroup("ng-b")))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || !slices.Equal(e.sent(), []string{sentRefresh}) {
			t.Errorf("status = %s, exit %d, requests %v; want Refused, exit 3, and only the refresh", res.Status, res.ExitCode(), e.sent())
		}
	})
	t.Run("a nodegroup that degrades during the run is refused by its own gate", func(t *testing.T) {
		world := engineWorld(nil)
		e := newEnv(t, world)
		// ng-b degrades once the roll of ng-a is accepted.
		hooks := Hooks{Event: func(ev Event) {
			if ev.Kind == StepChanged && ev.Step.Name == "ng-a" && ev.Result.Status == StepStarted {
				world.Edit(func(s *fleettest.State) { degradeNodegroup("ng-b")(s, s.Cluster(east, "prod")) })
			}
		}}
		res := mustRun(t, e, upgradeTo("1.33"), hooks)
		if res.Status != RunBlocked || res.ExitCode() != exitcode.Blocked {
			t.Errorf("status = %s, exit %d, err %v; want Blocked, exit 3", res.Status, res.ExitCode(), res.Err)
		}
		if slices.Contains(e.sent(), sentB33) || slices.Contains(e.sent(), sentProxy33) {
			t.Errorf("requests %v: ng-b was rolled, or a later phase ran", e.sent())
		}
		b := res.Clusters[0].Steps[slices.IndexFunc(res.Clusters[0].Steps, func(s StepResult) bool { return s.Ref.Name == "ng-b" })]
		if b.Status != StepBlocked || b.Failure == nil || !strings.Contains(b.Failure.Error, "blocked by") {
			t.Errorf("ng-b = %s, %+v; want Blocked by the gate", b.Status, b.Failure)
		}
	})
}

// payloads returns the payload of each request that changes the cluster
// and is not the insights refresh, keyed as the step that sent it.
func payloads(e *env) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range e.srv.Requests() {
		if !r.Mutates || r.Operation == fleettest.EKSStartInsightsRefresh {
			continue
		}
		key := "control-plane"
		for _, name := range []string{"nodegroupName", "addonName"} {
			if v, ok := r.Input[name]; ok {
				key = fmt.Sprint(v)
			}
		}
		fields := map[string]any{}
		for _, f := range []string{"version", "addonVersion", "force", "rollbackConfig", "releaseVersion", "launchTemplate", "resolveConflicts", "configurationValues"} {
			if v, ok := r.Input[f]; ok {
				fields[f] = v
			}
		}
		out[key] = fields
	}
	return out
}

// Safety rule 3. The payload of every request equals the plan that was
// shown: the version, force, the rollback timeout, and the add-on version.
func TestEngine_PayloadEqualsThePlan(t *testing.T) {
	planOf := func(t *testing.T, res Result) map[string]Step {
		t.Helper()
		out := map[string]Step{}
		for _, s := range res.Plan.Clusters[0].Steps() {
			if s.work() {
				name := s.Ref.Name
				if s.Ref.Kind == StepControlPlane {
					name = "control-plane"
				}
				out[name] = s
			}
		}
		return out
	}
	t.Run("upgrade with --force", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		req := upgradeTo("1.33")
		req.Policy.Force = true
		res := mustRun(t, e, req, Hooks{})
		steps := planOf(t, res)
		want := map[string]map[string]any{}
		for name, s := range steps {
			switch s.Ref.Kind {
			case StepAddon:
				want[name] = map[string]any{"addonVersion": s.To}
			case StepNodegroup:
				want[name] = map[string]any{"version": s.To, "force": true}
			default:
				want[name] = map[string]any{"version": s.To}
			}
			if s.Ref.Kind == StepNodegroup && !s.Force {
				t.Errorf("the plan does not show force on %s", name)
			}
		}
		if len(want) != 6 {
			t.Fatalf("the plan has %d steps with work, want 6", len(want))
		}
		if diff := cmp.Diff(want, payloads(e)); diff != "" {
			t.Errorf("payloads (-plan +sent):\n%s", diff)
		}
	})
	t.Run("rollback with force and a timeout", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(nil))
		req := rollbackOf()
		req.SkipInsights, req.RollbackTimeout = true, 3*time.Hour
		res := mustRun(t, e, req, Hooks{})
		if res.ExitCode() != exitcode.OK {
			t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
		}
		steps := planOf(t, res)
		cp := steps["control-plane"]
		if !cp.Force || cp.To != "1.32" {
			t.Fatalf("the control-plane step of the plan = %+v, want force and 1.32", cp)
		}
		if !slices.ContainsFunc(res.Plan.Clusters[0].Notices, func(n string) bool { return strings.Contains(n, "rollback timeout of 180 minutes") }) {
			t.Errorf("the plan does not show the rollback timeout: %q", res.Plan.Clusters[0].Notices)
		}
		got := payloads(e)
		want := map[string]any{"version": "1.32", "force": true, "rollbackConfig": map[string]any{"timeoutMinutes": float64(180)}}
		if diff := cmp.Diff(want, got["control-plane"]); diff != "" {
			t.Errorf("the control-plane payload (-want +got):\n%s", diff)
		}
		for _, name := range []string{"ng-a", "ng-b"} {
			if diff := cmp.Diff(map[string]any{"version": steps[name].To}, got[name]); diff != "" {
				t.Errorf("the payload of %s (-plan +sent):\n%s", name, diff)
			}
		}
		for _, name := range []string{"vpc-cni", "coredns", "kube-proxy"} {
			if diff := cmp.Diff(map[string]any{"addonVersion": steps[name].To}, got[name]); diff != "" {
				t.Errorf("the payload of %s (-plan +sent):\n%s", name, diff)
			}
		}
	})
}

// describeOf is the read of one resource that a request changes.
func describeOf(r fleettest.Operation) fleettest.Operation {
	switch r {
	case fleettest.EKSUpdateAddon:
		return fleettest.EKSDescribeAddon
	case fleettest.EKSUpdateNodegroupVersion:
		return fleettest.EKSDescribeNodegroup
	}
	return fleettest.EKSDescribeCluster
}

// Safety rule 4. A fresh read of the target comes before every submit,
// and a target that changed since the plan is derived again, and not
// sent blindly.
func TestEngine_FreshReadBeforeEverySubmit(t *testing.T) {
	t.Run("each request follows a read of its own target", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		mustRun(t, e, upgradeTo("1.33"), Hooks{})
		reqs := e.srv.Requests()
		last := 0
		for i, r := range reqs {
			if !r.Mutates || r.Operation == fleettest.EKSStartInsightsRefresh {
				continue
			}
			name := fmt.Sprint(r.Input["nodegroupName"]) + fmt.Sprint(r.Input["addonName"])
			read := false
			for _, before := range reqs[last:i] {
				same := fmt.Sprint(before.Input["nodegroupName"])+fmt.Sprint(before.Input["addonName"]) == name
				read = read || (before.Operation == describeOf(r.Operation) && same)
			}
			if !read {
				t.Errorf("%s %s was sent with no read of its target since the request before it", r.Operation, name)
			}
			last = i
		}
	})
	// Each case changes the world at the checkpoint before a phase, as
	// someone else would while the run is held, and names the request
	// that must then not be sent.
	v33 := "1.33"
	tests := []struct {
		name    string
		before  PhaseKind
		change  func(t *testing.T, w *fleettest.World)
		notSent string
	}{
		{
			name: "the control plane was upgraded elsewhere", before: PhaseControlPlane, notSent: sentCP33,
			change: func(_ *testing.T, w *fleettest.World) {
				w.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Version = "1.33" })
			},
		},
		{
			name: "an add-on was updated elsewhere", before: PhaseAddonsBefore, notSent: sentCNI33,
			change: func(_ *testing.T, w *fleettest.World) {
				w.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = cni33 })
			},
		},
		{
			name: "a nodegroup was rolled elsewhere", before: PhaseNodegroups, notSent: sentA33,
			change: func(_ *testing.T, w *fleettest.World) {
				w.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = v33 })
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := engineWorld(nil)
			e := newEnv(t, world)
			hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
				if next.Phase.Kind == tt.before && next.Kind == "" {
					tt.change(t, world)
					return Checkpoint{Waited: true}, nil
				}
				return Checkpoint{}, nil
			}}
			res := mustRun(t, e, upgradeTo("1.33"), hooks)
			if res.ExitCode() != exitcode.OK {
				t.Errorf("exit %d: %v", res.ExitCode(), res.Err)
			}
			want := slices.DeleteFunc(wholeUpgrade(), func(s string) bool { return s == tt.notSent })
			if diff := cmp.Diff(want, e.sent()); diff != "" {
				t.Errorf("requests (-want +got):\n%s", diff)
			}
		})
	}
}

// Safety rule 5. The phases are a sequence: after a phase that failed no
// later request is sent, and the run exits 1, whatever the phases before
// it did. The message names what completed and the command to go on.
func TestEngine_FailFast(t *testing.T) {
	failing := fleettest.UpdateScript{After: 30 * time.Second, Result: fleettest.UpdateFailed}
	tests := []struct {
		name string
		seed func(*fleettest.State, *fleettest.Cluster)
		sent []string
	}{
		{
			name: "the control plane fails", sent: []string{sentRefresh, sentCP33},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) { c.UpdateScripts = []fleettest.UpdateScript{failing} },
		},
		{
			name: "an add-on fails", sent: []string{sentRefresh, sentCP33, sentCNI33},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{failing}
			},
		},
		{
			name: "a nodegroup fails", sent: []string{sentRefresh, sentCP33, sentCNI33, sentDNS33, sentA33},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{failing}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, engineWorld(tt.seed))
			res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
			if res.Status != Failed || res.ExitCode() != exitcode.Failure {
				t.Errorf("status = %s, exit %d; want Failed, exit 1", res.Status, res.ExitCode())
			}
			if diff := cmp.Diff(tt.sent, e.sent()); diff != "" {
				t.Errorf("requests (-want +got):\n%s", diff)
			}
			if res.Err == nil || !strings.Contains(res.Err.Error(), "refresh cluster upgrade prod --to 1.33") {
				t.Errorf("message = %v, want the command that goes on", res.Err)
			}
			notAttempted := 0
			for _, s := range res.Clusters[0].Steps {
				if s.Status == StepNotAttempted {
					notAttempted++
				}
			}
			if notAttempted == 0 {
				t.Error("no step is NotAttempted after the phase that failed")
			}
		})
	}
	t.Run("the message names the phases that completed", func(t *testing.T) {
		e := newEnv(t, engineWorld(tests[1].seed))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if !strings.Contains(res.Err.Error(), "completed: the control plane to 1.33") {
			t.Errorf("message = %v", res.Err)
		}
	})
}

// Safety rule 6, the pure part. The goal of a step has a direction.
func TestDirection(t *testing.T) {
	tests := []struct {
		kind             Kind
		live, goal       string
		done, sent, roll bool
	}{
		{kind: ClusterUpgrade, live: "1.32", goal: "1.33", sent: true, roll: true},
		{kind: ClusterUpgrade, live: "1.33", goal: "1.33", done: true},
		{kind: ClusterUpgrade, live: "1.34", goal: "1.33", done: true},
		{kind: ClusterUpgrade, live: "1.31", goal: "1.33", roll: true},
		{kind: ClusterUpgrade, live: "", goal: "1.33"},
		{kind: ClusterRollback, live: "1.33", goal: "1.32", sent: true, roll: true},
		{kind: ClusterRollback, live: "1.32", goal: "1.32", done: true},
		{kind: ClusterRollback, live: "1.31", goal: "1.32", done: true},
		// A rollback never takes a newer version as done.
		{kind: ClusterRollback, live: "1.34", goal: "1.32", roll: true},
		{kind: ClusterRollback, live: "", goal: "1.32"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %s to %s", tt.kind, tt.live, tt.goal), func(t *testing.T) {
			if got := versionDone(tt.kind, tt.live, tt.goal); got != tt.done {
				t.Errorf("versionDone = %v, want %v", got, tt.done)
			}
			if got := adjacent(tt.kind, tt.live, tt.goal); got != tt.sent {
				t.Errorf("adjacent = %v, want %v", got, tt.sent)
			}
			if got := rollDirection(tt.kind, tt.live, tt.goal); got != tt.roll {
				t.Errorf("rollDirection = %v, want %v", got, tt.roll)
			}
		})
	}
}

// Safety rule 6, in a run. An upgrade never sends a version below the
// live one, and a rollback never takes a newer version as done.
func TestEngine_Direction(t *testing.T) {
	t.Run("an upgrade sends nothing below the live version", func(t *testing.T) {
		world := engineWorld(nil)
		e := newEnv(t, world)
		// While the run is held, the control plane and a nodegroup move
		// past the goal of the plan.
		hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			if next.Phase.Kind == PhaseControlPlane {
				world.Edit(func(s *fleettest.State) {
					c := s.Cluster(east, "prod")
					c.Version, c.Nodegroup("ng-a").Version = "1.34", "1.34"
				})
			}
			return Checkpoint{}, nil
		}}
		mustRun(t, e, upgradeTo("1.33"), hooks)
		for _, s := range e.sent() {
			if s == sentCP33 || s == sentA33 {
				t.Errorf("%s was sent to a resource that runs 1.34", s)
			}
		}
	})
	t.Run("a rollback rolls what is above its target", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(nil))
		res := mustRun(t, e, rollbackOf(), Hooks{})
		want := []string{
			"eks:UpdateNodegroupVersion ng-a 1.32", "eks:UpdateNodegroupVersion ng-b 1.32",
			"eks:UpdateAddon vpc-cni " + cni32, "eks:UpdateAddon coredns " + dns32, "eks:UpdateAddon kube-proxy " + proxy32,
			"eks:UpdateClusterVersion 1.32",
		}
		if diff := cmp.Diff(want, e.sent()); diff != "" || res.ExitCode() != exitcode.OK {
			t.Errorf("exit %d (%v), requests (-want +got):\n%s", res.ExitCode(), res.Err, diff)
		}
	})
	t.Run("a control plane that a rollback left above its target is not done", func(t *testing.T) {
		// The rollback that was in flight fails, so the cluster settles
		// ACTIVE at 1.33, above the target. The run says so and sends
		// its own.
		world := upgradedWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			for _, ng := range c.Nodegroups {
				ng.Version = "1.32"
			}
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni32, dns32, proxy32
			c.UpdateScripts = []fleettest.UpdateScript{{After: 2 * time.Minute, Result: fleettest.UpdateFailed}}
		})
		v := "1.32"
		if _, err := world.UpdateClusterVersion(east, "prod", fleettest.ClusterVersionRequest{Version: &v}); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, world)
		var notices []string
		res := mustRun(t, e, rollbackOf(), Hooks{Event: func(ev Event) {
			if ev.Kind == Notice {
				notices = append(notices, ev.Text)
			}
		}})
		if diff := cmp.Diff([]string{"eks:UpdateClusterVersion 1.32"}, e.sent()); diff != "" || res.ExitCode() != exitcode.OK {
			t.Errorf("exit %d (%v), requests (-want +got):\n%s", res.ExitCode(), res.Err, diff)
		}
		text := strings.Join(notices, "\n")
		if !strings.Contains(text, "that was already running ended Failed") || !strings.Contains(text, "did not reach 1.32 (the cluster is ACTIVE at 1.33); starting a new one") {
			t.Errorf("notices = %q, want one about the update that failed and one before the new update", notices)
		}
	})
}

// Safety rule 7. A rollback that would now return to another version
// than the one that was confirmed is refused, whatever the answer and
// whatever --yes says.
func TestEngine_ChangedRollbackTargetRefuses(t *testing.T) {
	// The nodegroups and the add-ons are back on 1.32 already, so only
	// the control-plane step carries the target.
	back := func(_ *fleettest.State, c *fleettest.Cluster) {
		for _, ng := range c.Nodegroups {
			ng.Version = "1.32"
		}
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni32, dns32, proxy33
	}
	// moveOn upgrades the cluster once more, so a rollback now returns to
	// 1.33 and not to 1.32.
	moveOn := func(w *fleettest.World) {
		at := w.Now().Add(-time.Hour)
		w.Edit(func(s *fleettest.State) {
			c := s.Cluster(east, "prod")
			c.Version = "1.34"
			c.Updates = append(c.Updates, &fleettest.Update{
				ID: "22222222-2222-4222-8222-222222222222", Type: fleettest.TypeVersionUpdate,
				Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.34"}}, CreatedAt: at,
			})
		})
	}
	check := func(t *testing.T, e *env, res Result) {
		t.Helper()
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want Refused, exit 3, and no request", res.Status, res.ExitCode(), e.sent())
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "the rollback target changed from 1.32 to 1.33") {
			t.Errorf("message = %v", res.Err)
		}
	}
	t.Run("a reviewed plan and --yes", func(t *testing.T) {
		world := upgradedWorld(back)
		e := newEnv(t, world)
		reviewed, err := NewPlan(testContext(t), e.deps, rollbackOf())
		if err != nil || reviewed.Clusters[0].To.String() != "1.32" || reviewed.Decision() == DecisionBlocked {
			t.Fatalf("the reviewed plan: %v, to %s, %s", err, reviewed.Clusters[0].To, reviewed.Decision())
		}
		moveOn(world)
		req := rollbackOf()
		req.Expect = reviewed
		check(t, e, mustRun(t, e, req, Hooks{}))
	})
	t.Run("a yes at the prompt", func(t *testing.T) {
		world := upgradedWorld(back)
		e := newEnv(t, world)
		req := rollbackOf()
		req.Policy.Yes = false
		asked := 0
		res := mustRun(t, e, req, Hooks{Ask: func(context.Context, Question) (bool, error) {
			asked++
			moveOn(world)
			return true, nil
		}})
		check(t, e, res)
		if asked != 1 {
			t.Errorf("asked %d times, want once: no second answer can accept the new target", asked)
		}
	})
}

// Safety rule 8. A cluster that someone else is changing refuses the
// run, and the run attaches to its own work that is still in flight.
func TestEngine_BusyRule(t *testing.T) {
	t.Run("a cluster that someone else changes refuses", func(t *testing.T) {
		// The control plane is at the target and UPDATING: no step of
		// this plan is that update.
		e := newEnv(t, engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version, c.Status = "1.33", fleettest.StatusUpdating
		}))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want Refused, exit 3, and no request", res.Status, res.ExitCode(), e.sent())
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "prod is busy (cluster UPDATING)") {
			t.Errorf("message = %v", res.Err)
		}
	})
	t.Run("a cluster that starts changing before a later phase blocks it", func(t *testing.T) {
		world := engineWorld(nil)
		e := newEnv(t, world)
		hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			if next.Phase.Kind == PhaseNodegroups && next.Kind == "" {
				world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Status = fleettest.StatusUpdating })
			}
			return Checkpoint{}, nil
		}}
		res := mustRun(t, e, upgradeTo("1.33"), hooks)
		if res.Status != RunBlocked || res.ExitCode() != exitcode.Blocked {
			t.Errorf("status = %s, exit %d, err %v; want Blocked, exit 3", res.Status, res.ExitCode(), res.Err)
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33, sentCNI33, sentDNS33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("the run attaches to its own roll that is in flight", func(t *testing.T) {
		// A run before this one was stopped during the roll of ng-a.
		world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
		})
		v := "1.33"
		if _, err := world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v}); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, world)
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.ExitCode() != exitcode.OK {
			t.Errorf("exit %d: %v", res.ExitCode(), res.Err)
		}
		if diff := cmp.Diff([]string{sentB33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
		a := res.Clusters[0].Steps[slices.IndexFunc(res.Clusters[0].Steps, func(s StepResult) bool { return s.Ref.Name == "ng-a" })]
		if a.Status != StepSucceeded || !a.Attached {
			t.Errorf("ng-a = %s, attached %v; want Succeeded and attached", a.Status, a.Attached)
		}
	})
}

// Safety rule 9. A hop that the kubelet skew blocks sends no insights
// refresh.
func TestEngine_SkewBlockedHopSendsNoRefresh(t *testing.T) {
	e := newEnv(t, engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("ng-a").Version = "1.29" }))
	req := upgradeTo("1.33")
	// A nodegroup that the run leaves alone is not rolled first, and it
	// still counts for the skew.
	req.SkipNodegroups = []string{"ng-a"}
	res := mustRun(t, e, req, Hooks{})
	if res.Status != Refused || res.ExitCode() != exitcode.Blocked {
		t.Errorf("status = %s, exit %d, err %v; want Refused, exit 3", res.Status, res.ExitCode(), res.Err)
	}
	if len(e.sent()) != 0 || e.count(fleettest.EKSListInsights) != 0 {
		t.Errorf("requests %v, %d insight reads; want none: the skew blocks the hop first", e.sent(), e.count(fleettest.EKSListInsights))
	}
}

// Safety rule 9, the boundary. The planner blocks a hop when a nodegroup
// would trail its target by more than 3 minor versions. health.VersionSkew
// fails a nodegroup that trails the live control plane by 3 or more. A
// control-plane step is sent only from the minor version before its
// target, and there the two are one condition. The table holds them to
// each other on every distance, and the gate of the step blocks when
// either one does.
func TestSkewRulesAgree(t *testing.T) {
	live := fleet.Version{Major: 1, Minor: 33}
	hop := nextMinor(live)
	for behind := range 6 {
		t.Run(fmt.Sprintf("%d behind", behind), func(t *testing.T) {
			snap := &fleet.Snapshot{
				Cluster: fleet.Cluster{Name: "prod", Region: east, Version: live, Status: "ACTIVE"},
				Have:    fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected},
				Nodegroups: []fleet.Nodegroup{
					{Name: "ng", State: fleet.Collected, Status: "ACTIVE", Version: fleet.Version{Major: 1, Minor: live.Minor - behind}},
				},
			}
			planner := HopReadiness(snap, hop, true, nil).Blocker != nil
			check := health.Run(t.Context(), []health.Check{health.VersionSkew()}, &health.Input{Snapshot: snap})
			gate := check[0].Outcome == health.Fail
			if planner != gate {
				t.Errorf("the planner blocks: %v; health.VersionSkew fails: %v (%s)", planner, gate, check[0].Message)
			}
			if want := behind >= 3; planner != want {
				t.Errorf("blocked = %v, want %v", planner, want)
			}
		})
	}
}

// Safety rule 10. A deadline exits 4 and leaves the step that it cut
// InProgress. An interrupt exits 1. A stop from the checkpoint exits 1.
func TestEngine_DeadlineInterruptStop(t *testing.T) {
	never := func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} }
	t.Run("deadline", func(t *testing.T) {
		e := newEnv(t, engineWorld(never))
		req := upgradeTo("1.33")
		req.Limits.Wait = 2 * time.Minute
		res := mustRun(t, e, req, Hooks{})
		if res.Status != Incomplete || res.ExitCode() != exitcode.Incomplete {
			t.Errorf("status = %s, exit %d, err %v; want Incomplete, exit 4", res.Status, res.ExitCode(), res.Err)
		}
		cp := res.Clusters[0].Steps[0]
		if cp.Status != StepInProgress || cp.Failure == nil || cp.Failure.Reason != apidoc.ReasonTimeout {
			t.Errorf("the control plane = %s, %+v; want InProgress with the reason Timeout", cp.Status, cp.Failure)
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("the deadline is for the whole run", func(t *testing.T) {
		// Each update takes 30 seconds. The budget covers the control
		// plane and the first add-on, and not the second.
		e := newEnv(t, engineWorld(nil))
		req := upgradeTo("1.33")
		req.Limits.Wait = 75 * time.Second
		res := mustRun(t, e, req, Hooks{})
		if res.ExitCode() != exitcode.Incomplete {
			t.Errorf("status = %s, exit %d, err %v; want exit 4", res.Status, res.ExitCode(), res.Err)
		}
		if slices.Contains(e.sent(), sentA33) {
			t.Errorf("requests %v: a nodegroup was rolled after the deadline of the run", e.sent())
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		e := newEnv(t, engineWorld(never))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		// The second poll is the handshake: the wait is under way.
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 2 {
				cancel()
			}
		})
		res, err := Run(ctx, e.deps, upgradeTo("1.33"), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Interrupted || res.ExitCode() != exitcode.Failure || !errors.Is(res.Err, context.Canceled) {
			t.Errorf("status = %s, exit %d, err %v; want Interrupted, exit 1", res.Status, res.ExitCode(), res.Err)
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("stop before the first phase", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{Checkpoint: func(context.Context, StepRef) (Checkpoint, error) {
			return Checkpoint{Stop: true}, nil
		}})
		if res.Status != Stopped || res.ExitCode() != exitcode.Failure {
			t.Errorf("status = %s, exit %d; want Stopped, exit 1", res.Status, res.ExitCode())
		}
		if diff := cmp.Diff([]string{sentRefresh}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("stop before the second nodegroup", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			return Checkpoint{Stop: next.Name == "ng-b"}, nil
		}})
		if res.Status != Stopped || res.ExitCode() != exitcode.Failure {
			t.Errorf("status = %s, exit %d; want Stopped, exit 1", res.Status, res.ExitCode())
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33, sentCNI33, sentDNS33, sentA33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
}
