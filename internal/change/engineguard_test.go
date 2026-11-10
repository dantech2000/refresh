package change

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// afterRead calls change once, right after EKS answered the nth describe
// of a target since the last list of its kind: the read of a phase is the
// first describe, and the fresh read of the step is the second. Each read
// of the cluster lists before it describes, and the fresh read of a step
// describes its target alone, so the count needs no knowledge of the
// order of the other requests. op is EKSDescribeNodegroup or
// EKSDescribeAddon. armed, when not nil, says when the count starts.
func afterRead(e *env, op fleettest.Operation, name string, nth int, armed func() bool, change func()) {
	list, key := fleettest.EKSListNodegroups, "nodegroupName"
	if op == fleettest.EKSDescribeAddon {
		list, key = fleettest.EKSListAddons, "addonName"
	}
	var mu sync.Mutex
	since, done := 0, false
	e.srv.OnAnswer = func(r awsxtest.Request, _ awsxtest.Answer) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case armed != nil && !armed():
		case r.Operation == list:
			since = 0
		case r.Operation == op && r.Input[key] == name:
			if since++; since == nth && !done {
				done = true
				change()
			}
		}
	}
}

// onGateRefresh calls change once, when the insights refresh of the gate
// of the second hop starts, before EKS sees it: the plan sends the first
// refresh.
func onGateRefresh(e *env, change func()) {
	var mu sync.Mutex
	starts := 0
	e.srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Operation == fleettest.EKSStartInsightsRefresh {
			if starts++; starts == 2 {
				change()
			}
		}
	}
}

// editProd returns a change that edits the cluster prod.
func editProd(e *env, edit func(c *fleettest.Cluster)) func() {
	return func() { e.world.Edit(func(s *fleettest.State) { edit(s.Cluster(east, "prod")) }) }
}

// startNever starts an update that never ends, as another process would.
// It runs in a hook of the fake, so it reports with t.Error.
func startNever(t *testing.T, e *env, start func(*fleettest.World) error) func() {
	t.Helper()
	return func() {
		if err := start(e.world); err != nil {
			t.Errorf("starting an update elsewhere: %v", err)
		}
	}
}

// at133 is the engine world after its control plane and its add-ons moved
// to 1.33: what is left of an upgrade to 1.33 is the nodegroup rolls.
func at133() *fleettest.World {
	return engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
	})
}

// cpAt133 is the engine world whose control plane runs 1.33 and whose
// add-ons run 1.32.
func cpAt133() *fleettest.World {
	return engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) { c.Version = "1.33" })
}

// rolls is the upgrade to 1.33 with --only nodegroups, and with
// --skip-health-check when skip is set.
func rolls(skip bool) func() Request {
	return func() Request {
		req := upgradeTo("1.33")
		req.Only = []Part{PartNodegroups}
		req.Policy.Skip = skip
		return req
	}
}

// addonUpdates is the upgrade to 1.33 with --only addons.
func addonUpdates() Request {
	req := upgradeTo("1.33")
	req.Only = []Part{PartAddons}
	return req
}

// upgradeTo1 and upgradeTo2 are the upgrades of one and of two hops.
func upgradeTo1() Request { return upgradeTo("1.33") }
func upgradeTo2() Request { return upgradeTo("1.34") }

// TestGuardSubmit holds every row of the decision table of guardSubmit,
// and of the guards of its three kinds of step, to a run in which the
// cluster changes after the fresh read of the step: during the wait of an
// attachment, a question of the gate, or an insights refresh, which take
// minutes. A checkpoint before the phase is too early for these rows: the
// read of the phase and the fresh read of the step see the change first.
// Each row checks how the step ends, and that its request is not sent.
// The rows of the gates themselves (the health gate of a nodegroup, the
// readiness of a hop, the rollback window) and of the busy rule have
// tests of their own in engine_guard_test.go.
func TestGuardSubmit(t *testing.T) {
	tests := []struct {
		name string
		// world is the world of the row, req its request. arm sets the
		// hook of the fake that changes the world during the run, and
		// returns the hooks of the run.
		world func() *fleettest.World
		req   func() Request
		arm   func(t *testing.T, e *env) Hooks
		// The step that the row checks: its kind, its name, and the hop of
		// its phase ("" for any).
		kind StepKind
		step string
		hop  string
		// How the step ends: its status, the reason of its failure ("" for
		// no failure), and words of the failure.
		status StepStatus
		reason apidoc.Reason
		words  []string
		// never is a request that must not be sent, and counts the number
		// of requests of an operation.
		never  string
		counts map[fleettest.Operation]int
	}{
		// guardSubmit, step 3: the busy rule on the read of the guard.
		{
			name:  "busy rule: a list that cannot be read shows no change is running",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, func() {
					e.world.SetFault(fleettest.EKSListAddons, fleettest.Fault{Deny: true})
				})
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepBlocked, reason: apidoc.ReasonUnknown, words: []string{"could not read every add-on of prod"},
			never: sentA33,
		},

		// guardControlPlane, after the refresh of the gate of the second hop.
		{
			name:  "control plane, row 1: at the goal",
			world: func() *fleettest.World { return engineWorld(nil) }, req: upgradeTo2,
			arm: func(_ *testing.T, e *env) Hooks {
				onGateRefresh(e, editProd(e, func(c *fleettest.Cluster) { c.Version = "1.34" }))
				return Hooks{}
			},
			kind: StepControlPlane, hop: "1.34", status: StepSkipped,
			never: "eks:UpdateClusterVersion 1.34",
		},
		{
			name:  "control plane, row 2: not the version the request starts from",
			world: func() *fleettest.World { return engineWorld(nil) }, req: upgradeTo2,
			arm: func(_ *testing.T, e *env) Hooks {
				onGateRefresh(e, editProd(e, func(c *fleettest.Cluster) { c.Version = "1.32" }))
				return Hooks{}
			},
			kind: StepControlPlane, hop: "1.34",
			status: StepFailed, reason: apidoc.ReasonUnknown,
			words: []string{"the control plane of prod runs 1.32 now, and the plan moves it from 1.33 to 1.34"},
			never: "eks:UpdateClusterVersion 1.34",
		},
		{
			name:  "control plane, row 3: the refresh of the gate fails",
			world: func() *fleettest.World { return engineWorld(nil) }, req: upgradeTo2,
			arm: func(_ *testing.T, e *env) Hooks {
				onGateRefresh(e, func() { e.world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{Deny: true}) })
				return Hooks{}
			},
			kind: StepControlPlane, hop: "1.34",
			status: StepBlocked, reason: apidoc.ReasonUnknown,
			words: []string{"could not refresh cluster insights for 1.34", "the control plane of prod was not changed"},
			never: "eks:UpdateClusterVersion 1.34",
			// The failed refresh is the read of the gate: it is not tried
			// again.
			counts: map[fleettest.Operation]int{fleettest.EKSStartInsightsRefresh: 2},
		},

		// guardAddon, after the fresh read of the step.
		{
			name:  "add-on, row 1: gone",
			world: cpAt133, req: addonUpdates,
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeAddon, "vpc-cni", 2, nil, editProd(e, func(c *fleettest.Cluster) {
					c.Addons = slices.DeleteFunc(c.Addons, func(a *fleettest.Addon) bool { return a.Name == "vpc-cni" })
				}))
				return Hooks{}
			},
			kind: StepAddon, step: "vpc-cni",
			status: StepBlocked, reason: apidoc.ReasonUnknown, words: []string{"could not read add-on vpc-cni before its update"},
			never: sentCNI33,
		},
		{
			name:  "add-on, row 2: EKS is changing it",
			world: cpAt133, req: addonUpdates,
			arm: func(t *testing.T, e *env) Hooks {
				t.Helper()
				afterRead(e, fleettest.EKSDescribeAddon, "vpc-cni", 2, nil, startNever(t, e, func(w *fleettest.World) error {
					w.Edit(func(s *fleettest.State) {
						s.Cluster(east, "prod").Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{{Never: true}}
					})
					v := cni33
					_, err := w.UpdateAddon(east, "prod", "vpc-cni", fleettest.AddonRequest{Version: &v})
					return err
				}))
				return Hooks{}
			},
			kind: StepAddon, step: "vpc-cni",
			status: StepBlocked, reason: apidoc.ReasonBusy,
			words: []string{"add-on vpc-cni started changing before its update was sent (UPDATING)"},
			never: sentCNI33,
		},
		{
			name:  "add-on, row 4: at the goal",
			world: cpAt133, req: addonUpdates,
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeAddon, "vpc-cni", 2, nil, editProd(e, func(c *fleettest.Cluster) { c.Addon("vpc-cni").Version = cni33 }))
				return Hooks{}
			},
			kind: StepAddon, step: "vpc-cni", status: StepSkipped,
			never: sentCNI33,
		},

		// guardNodegroup, after the fresh read of the step, with
		// --skip-health-check: the read of the guard is the next read.
		{
			name:  "nodegroup, row 1: gone",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, editProd(e, func(c *fleettest.Cluster) {
					c.Nodegroups = slices.DeleteFunc(c.Nodegroups, func(n *fleettest.Nodegroup) bool { return n.Name == "ng-a" })
				}))
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepBlocked, reason: apidoc.ReasonUnknown, words: []string{"could not read nodegroup ng-a before its roll"},
			never: sentA33,
		},
		{
			name:  "nodegroup, row 4: EKS is changing it",
			world: at133, req: rolls(true),
			arm: func(t *testing.T, e *env) Hooks {
				t.Helper()
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, startNever(t, e, func(w *fleettest.World) error {
					w.Edit(func(s *fleettest.State) {
						s.Cluster(east, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{Never: true}}
					})
					v := "1.33"
					_, err := w.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v})
					return err
				}))
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepBlocked, reason: apidoc.ReasonBusy, words: []string{"nodegroup ng-a started changing", "(UPDATING)"},
			never: sentA33,
		},
		{
			name:  "nodegroup, row 5: not ACTIVE",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, editProd(e, func(c *fleettest.Cluster) { c.Nodegroup("ng-a").Status = "DEGRADED" }))
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepBlocked, reason: apidoc.ReasonUnknown, words: []string{"nodegroup ng-a is DEGRADED, not ACTIVE"},
			never: sentA33,
		},
		{
			name:  "nodegroup, row 6: at the goal",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, editProd(e, func(c *fleettest.Cluster) { c.Nodegroup("ng-a").Version = "1.33" }))
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a", status: StepSkipped,
			never: sentA33,
		},
		{
			name:  "nodegroup, row 7: a version that goes no way",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 2, nil, editProd(e, func(c *fleettest.Cluster) { c.Nodegroup("ng-a").Version = "" }))
				return Hooks{}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepFailed, reason: apidoc.ReasonUnknown,
			words: []string{"nodegroup ng-a runs Kubernetes unknown now, and the plan moves it to 1.33"},
			never: sentA33,
		},
		{
			// The health gate warns about the CPU, and while the question
			// is open someone rolls ng-a to the goal. The answer is no
			// consent to send a roll: everything is read again.
			name:  "nodegroup, row 6, after a question of the gate",
			world: at133, req: func() Request { r := rolls(false)().(UpgradeRequest); r.Policy.Yes = false; return r },
			arm: func(_ *testing.T, e *env) Hooks {
				asked := false
				return Hooks{
					Checkpoint: atCheckpoint(PhaseNodegroups, func() {
						e.world.Edit(func(s *fleettest.State) {
							for i := range s.Metrics {
								if s.Metrics[i].Name == "CPUUtilization" {
									s.Metrics[i].Values = []float64{85, 85}
								}
							}
						})
					}),
					Ask: func(_ context.Context, q Question) (bool, error) {
						if q.Kind == GateWarnings && q.Step.Name == "ng-a" && !asked {
							asked = true
							e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = "1.33" })
						}
						return true, nil
					},
				}
			},
			kind: StepNodegroup, step: "ng-a", status: StepSkipped,
			never: sentA33,
		},

		// The rows around guardSubmit that only a change after a read
		// reaches.
		{
			name:  "nodegroup: DELETING at the fresh read of the step",
			world: at133, req: rolls(true),
			arm: func(_ *testing.T, e *env) Hooks {
				// The count starts at the phase: the plan reads ng-a too.
				var phase atomic.Bool
				afterRead(e, fleettest.EKSDescribeNodegroup, "ng-a", 1, phase.Load, editProd(e, func(c *fleettest.Cluster) { c.Nodegroup("ng-a").Status = "DELETING" }))
				return Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() { phase.Store(true) })}
			},
			kind: StepNodegroup, step: "ng-a",
			status: StepBlocked, reason: apidoc.ReasonBusy, words: []string{"nodegroup ng-a is DELETING; it was not changed"},
			never: sentA33,
		},
		{
			name:  "control plane: the update reports Successful and the version did not move",
			world: func() *fleettest.World { return engineWorld(nil) }, req: upgradeTo1,
			arm: func(_ *testing.T, e *env) Hooks {
				var mu sync.Mutex
				done := false
				e.srv.OnAnswer = func(r awsxtest.Request, a awsxtest.Answer) {
					mu.Lock()
					defer mu.Unlock()
					if r.Operation == fleettest.EKSDescribeUpdate && strings.Contains(string(a.JSON), `"Successful"`) && !done {
						done = true
						e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Version = "1.32" })
					}
				}
				return Hooks{}
			},
			kind:   StepControlPlane,
			status: StepFailed, reason: apidoc.ReasonUpdateFailed,
			words: []string{"reported Successful, but the control plane of prod reports version 1.32, not 1.33"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.world())
			res := mustRun(t, e, tt.req(), tt.arm(t, e))
			if tt.never != "" && slices.Contains(e.sent(), tt.never) {
				t.Errorf("%s was sent: %v", tt.never, e.sent())
			}
			for op, want := range tt.counts {
				if got := e.count(op); got != want {
					t.Errorf("%d %s requests, want %d", got, op, want)
				}
			}
			wantStep(t, guardStep(t, res, tt.kind, tt.step, tt.hop), tt.status, tt.reason, tt.words...)
		})
	}
}

// wantStep checks how a step ended: its status, the reason of its failure
// ("" for no failure), and words of the failure.
func wantStep(t *testing.T, got StepResult, status StepStatus, reason apidoc.Reason, words ...string) {
	t.Helper()
	if got.Status != status {
		t.Errorf("%s = %s with %+v, want %s", got.Ref, got.Status, got.Failure, status)
	}
	switch {
	case reason == "" && got.Failure != nil:
		t.Errorf("%s has the failure %+v, want none", got.Ref, got.Failure)
	case reason != "" && got.Failure == nil:
		t.Fatalf("%s has no failure, want %s", got.Ref, reason)
	case reason != "" && got.Failure.Reason != reason:
		t.Errorf("the failure of %s is %s, want %s: %s", got.Ref, got.Failure.Reason, reason, got.Failure.Error)
	}
	for _, w := range words {
		if got.Failure == nil || !strings.Contains(got.Failure.Error, w) {
			t.Errorf("the failure of %s lacks %q: %+v", got.Ref, w, got.Failure)
		}
	}
}

// A nodegroup that someone else keeps rolling is waited for twice, and
// then the step fails: the run does not wait without end, and it sends no
// roll of its own. Each roll of the other process fails, so the
// nodegroup settles ACTIVE below the goal, and the next one starts at
// once.
func TestEngineNodegroup_KeepsChanging(t *testing.T) {
	world := at133()
	failing := fleettest.UpdateScript{After: 30 * time.Second, Result: fleettest.UpdateFailed}
	world.Edit(func(s *fleettest.State) {
		s.Cluster(east, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{failing, failing, failing}
	})
	roll := func() error {
		v := "1.33"
		_, err := world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v})
		return err
	}
	if err := roll(); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, world)
	var mu sync.Mutex
	restarts := 0
	e.srv.OnAnswer = func(r awsxtest.Request, a awsxtest.Answer) {
		mu.Lock()
		defer mu.Unlock()
		if r.Operation == fleettest.EKSDescribeNodegroup && r.Input["nodegroupName"] == "ng-a" &&
			strings.Contains(string(a.JSON), `"status":"ACTIVE"`) && restarts < 2 {
			restarts++
			if err := roll(); err != nil {
				t.Errorf("starting roll %d elsewhere: %v", restarts+1, err)
			}
		}
	}
	res := mustRun(t, e, rolls(true)(), Hooks{})
	mu.Lock()
	defer mu.Unlock()
	if restarts != 2 {
		t.Fatalf("the other process started %d more rolls, want 2", restarts)
	}
	if slices.Contains(e.sent(), sentA33) {
		t.Errorf("the run rolled ng-a while it keeps changing: %v", e.sent())
	}
	got := guardStep(t, res, StepNodegroup, "ng-a", "")
	if got.Status != StepFailed || got.Failure == nil || got.Failure.Reason != apidoc.ReasonBusy ||
		!strings.Contains(got.Failure.Error, "nodegroup ng-a keeps changing; it was not rolled") {
		t.Errorf("ng-a = %s with %+v; want Failed, Busy, keeps changing", got.Status, got.Failure)
	}
}

// guardStep returns the result of the first step of a kind and a name, in
// the phase of hop when hop is set.
func guardStep(t *testing.T, res Result, kind StepKind, name, hop string) StepResult {
	t.Helper()
	for _, s := range res.Clusters[0].Steps {
		if s.Ref.Kind == kind && s.Ref.Name == name && (hop == "" || s.Ref.Phase.Hop.String() == hop) {
			return s
		}
	}
	t.Fatalf("no %s step %q of %q in %v", kind, name, hop, stepStatuses(res))
	return StepResult{}
}
