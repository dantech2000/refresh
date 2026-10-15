package change

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// The requests of a rollback of the upgraded engine world.
const (
	sentCP32 = "eks:UpdateClusterVersion 1.32"
	sentA32  = "eks:UpdateNodegroupVersion ng-a 1.32"
)

// atCheckpoint returns a checkpoint hook that calls change once, before
// the phase of a kind, as someone else would change the cluster while the
// run is held there.
func atCheckpoint(before PhaseKind, change func()) func(context.Context, StepRef) (Checkpoint, error) {
	done := false
	return func(_ context.Context, next StepRef) (Checkpoint, error) {
		if next.Phase.Kind == before && next.Kind == "" && !done {
			done = true
			change()
			return Checkpoint{Waited: true}, nil
		}
		return Checkpoint{}, nil
	}
}

// stepResultOf returns the result of the step with a name. The control plane
// has the name "".
func stepResultOf(t *testing.T, res Result, kind StepKind, name string) StepResult {
	t.Helper()
	for _, s := range res.Clusters[0].Steps {
		if s.Ref.Kind == kind && s.Ref.Name == name {
			return s
		}
	}
	t.Fatalf("no %s step %q in %v", kind, name, stepStatuses(res))
	return StepResult{}
}

// wantBlocked checks that a step is Blocked with a failure that has every
// one of the words.
func wantBlocked(t *testing.T, s StepResult, words ...string) {
	t.Helper()
	if s.Status != StepBlocked || s.Failure == nil {
		t.Fatalf("%s = %s with the failure %+v; want Blocked", s.Ref, s.Status, s.Failure)
	}
	for _, w := range words {
		if !strings.Contains(s.Failure.Error, w) {
			t.Errorf("the failure of %s lacks %q: %s", s.Ref, w, s.Failure.Error)
		}
	}
}

// startControlPlaneElsewhere starts an update of the control plane, as
// another process would, with the script.
func startControlPlaneElsewhere(t *testing.T, w *fleettest.World, version string, script fleettest.UpdateScript) {
	t.Helper()
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.UpdateScripts = append([]fleettest.UpdateScript{script}, c.UpdateScripts...)
	})
	if _, err := w.UpdateClusterVersion(east, "prod", fleettest.ClusterVersionRequest{Version: &version}); err != nil {
		t.Fatalf("starting the update of the control plane: %v", err)
	}
}

// A rollback is judged again before every request that it sends, against
// the target of its plan: the plan is old by then.
func TestEngineGuard_RollbackEligibility(t *testing.T) {
	t.Run("the rollback window ends while the nodegroups roll back", func(t *testing.T) {
		world := upgradedWorld(nil)
		e := newEnv(t, world)
		// The hook holds the run before the control plane for eight days.
		hooks := Hooks{Checkpoint: atCheckpoint(PhaseControlPlane, func() { world.Advance(8 * 24 * time.Hour) })}
		res := mustRun(t, e, rollbackOf(), hooks)
		if slices.Contains(e.sent(), sentCP32) || !slices.Contains(e.sent(), sentA32) {
			t.Errorf("requests %v; want the nodegroup rolls and no control-plane rollback", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepControlPlane, ""), "can no longer roll back to 1.32", "rollback window closed")
		if res.ExitCode() != exitcode.Blocked {
			t.Errorf("exit = %d, want 3 (%v)", res.ExitCode(), res.Err)
		}
	})
	t.Run("the window ends before an add-on downgrade", func(t *testing.T) {
		world := upgradedWorld(nil)
		e := newEnv(t, world)
		hooks := Hooks{Checkpoint: atCheckpoint(PhaseAddons, func() { world.Advance(8 * 24 * time.Hour) })}
		res := mustRun(t, e, rollbackOf(), hooks)
		for _, r := range e.sent() {
			if strings.HasPrefix(r, "eks:UpdateAddon") || r == sentCP32 {
				t.Errorf("%s was sent after the window ended (%v)", r, e.sent())
			}
		}
		if res.ExitCode() != exitcode.Blocked {
			t.Errorf("exit = %d, want 3 (%v)", res.ExitCode(), res.Err)
		}
	})
	t.Run("with --yes, an insight turns ERROR before the first change", func(t *testing.T) {
		world := upgradedWorld(nil)
		e := newEnv(t, world)
		hooks := Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() {
			world.Edit(func(s *fleettest.State) {
				c := s.Cluster(east, "prod")
				c.Insights = append(c.Insights, fleettest.Insight{ID: "rb-1", Name: "Kubelet version skew", Category: "ROLLBACK_READINESS", Status: fleettest.InsightError})
			})
		})}
		res := mustRun(t, e, rollbackOf(), hooks)
		if len(e.sent()) != 0 {
			t.Errorf("requests %v, want none", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "can no longer roll back to 1.32", "blocking insight")
		if res.ExitCode() != exitcode.Blocked {
			t.Errorf("exit = %d, want 3 (%v)", res.ExitCode(), res.Err)
		}
	})
	t.Run("the target changes, and an answer does not accept it", func(t *testing.T) {
		world := upgradedWorld(nil)
		e := newEnv(t, world)
		// The cluster is upgraded once more while the start question is
		// answered, so a rollback now returns to 1.33.
		req := rollbackOf()
		req.Policy.Yes = false
		hooks := Hooks{
			Ask: func(context.Context, Question) (bool, error) { return true, nil },
			Checkpoint: atCheckpoint(PhaseNodegroups, func() {
				at := world.Now().Add(-time.Hour)
				world.Edit(func(s *fleettest.State) {
					c := s.Cluster(east, "prod")
					c.Version = "1.34"
					c.Updates = append(c.Updates, &fleettest.Update{
						ID: "22222222-2222-4222-8222-222222222222", Type: fleettest.TypeVersionUpdate,
						Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.34"}}, CreatedAt: at,
					})
				})
			}),
		}
		res := mustRun(t, e, req, hooks)
		if len(e.sent()) != 0 {
			t.Errorf("requests %v, want none", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "the rollback target changed from 1.32 to 1.33")
	})
}

// A request of the run that follows an attachment has its own read of the
// cluster, the busy rule, and the gate: the wait for the update of
// someone else took time.
func TestEngineGuard_SubmitAfterAttachment(t *testing.T) {
	t.Run("the control plane: the hop is not ready any more", func(t *testing.T) {
		world := engineWorld(nil)
		startControlPlaneElsewhere(t, world, "1.33", fleettest.UpdateScript{After: 2 * time.Minute, Result: fleettest.UpdateFailed})
		e := newEnv(t, world)
		// An insight turns ERROR while the run waits for that update.
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 1 {
				world.Edit(func(s *fleettest.State) { errorInsight(s, s.Cluster(east, "prod")) })
			}
		})
		req := upgradeTo("1.33")
		res := mustRun(t, e, req, Hooks{})
		if slices.Contains(e.sent(), sentCP33) {
			t.Errorf("requests %v: the control plane was upgraded after an attachment with no gate", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepControlPlane, ""), "insight", "the control plane of prod was not changed")
	})
	t.Run("the control plane: the allowed retry is still sent", func(t *testing.T) {
		world := engineWorld(nil)
		startControlPlaneElsewhere(t, world, "1.33", fleettest.UpdateScript{After: 2 * time.Minute, Result: fleettest.UpdateFailed})
		e := newEnv(t, world)
		var notices []string
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{Event: func(ev Event) {
			if ev.Kind == Notice {
				notices = append(notices, ev.Text)
			}
		}})
		if !slices.Contains(e.sent(), sentCP33) || res.ExitCode() != exitcode.OK {
			t.Errorf("requests %v, exit %d (%v); want the run's own control-plane update", e.sent(), res.ExitCode(), res.Err)
		}
		if text := strings.Join(notices, "\n"); !strings.Contains(text, "that was already running ended Failed") || !strings.Contains(text, "did not reach 1.33") {
			t.Errorf("the notices do not say how the attached update ended:\n%s", text)
		}
	})
	t.Run("an add-on: the control plane starts to change during the wait", func(t *testing.T) {
		// The control plane is at 1.33 already, and someone updates
		// vpc-cni to a build that 1.33 cannot run.
		world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) { c.Version = "1.33" })
		startElsewhere(t, world, "vpc-cni", "v1.19.5-eksbuild.1", fleettest.UpdateScript{After: 30 * time.Second})
		e := newEnv(t, world)
		started := false
		hooks := Hooks{Event: func(ev Event) {
			if ev.Kind == Notice && strings.Contains(ev.Text, "add-on vpc-cni is UPDATING already") && !started {
				started = true
				startControlPlaneElsewhere(t, world, "1.34", fleettest.UpdateScript{Never: true})
			}
		}}
		res := mustRun(t, e, upgradeTo("1.33"), hooks)
		if !started {
			t.Fatal("the run did not attach to the update of vpc-cni")
		}
		if slices.Contains(e.sent(), sentCNI33) {
			t.Errorf("requests %v: vpc-cni was updated while the control plane changes", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepAddon, "vpc-cni"), "prod is busy (cluster UPDATING)")
	})
	t.Run("a nodegroup: the control plane starts to change during the wait", func(t *testing.T) {
		// Someone rolls ng-a, and the roll fails, so ng-a stays at 1.32.
		world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
			c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Result: fleettest.UpdateFailed}}
		})
		v := "1.33"
		if _, err := world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v}); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, world)
		started := false
		hooks := Hooks{Event: func(ev Event) {
			if ev.Kind == Notice && strings.Contains(ev.Text, "nodegroup ng-a is UPDATING already") && !started {
				started = true
				startControlPlaneElsewhere(t, world, "1.34", fleettest.UpdateScript{Never: true})
			}
		}}
		res := mustRun(t, e, upgradeTo("1.33"), hooks)
		if !started {
			t.Fatal("the run did not attach to the roll of ng-a")
		}
		if slices.Contains(e.sent(), sentA33) {
			t.Errorf("requests %v: ng-a was rolled while the control plane changes", e.sent())
		}
		wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "prod is busy (cluster UPDATING)")
	})
}

// A person accepts a warning of the gate of a nodegroup, and the control
// plane starts to change while the question is open. The read after the
// answer applies the busy rule, and the roll is not sent.
func TestEngineGuard_BusyRuleAfterARenewedConsent(t *testing.T) {
	world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
	})
	e := newEnv(t, world)
	req := upgradeTo("1.33")
	req.Policy.Yes = false
	renewed := false
	hooks := Hooks{
		// The health gets worse after the plan was accepted.
		Checkpoint: atCheckpoint(PhaseNodegroups, func() {
			world.Edit(func(s *fleettest.State) {
				for i := range s.Metrics {
					if s.Metrics[i].Name == "CPUUtilization" {
						s.Metrics[i].Values = []float64{85, 85}
					}
				}
			})
		}),
		Ask: func(_ context.Context, q Question) (bool, error) {
			if q.Kind == GateWarnings && q.Step.Name == "ng-a" && !renewed {
				renewed = true
				startControlPlaneElsewhere(t, world, "1.34", fleettest.UpdateScript{Never: true})
			}
			return true, nil
		},
	}
	res := mustRun(t, e, req, hooks)
	if !renewed {
		t.Fatal("the gate of ng-a did not ask about the health that got worse")
	}
	if slices.Contains(e.sent(), sentA33) {
		t.Errorf("requests %v: ng-a was rolled while the control plane changes", e.sent())
	}
	wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "prod is busy (cluster UPDATING)")
}

// A nodegroup is never rolled to a version above the live control plane.
// The plan of a resumed run has no control-plane step, and its read of
// the control plane is old when the roll is due.
func TestEngineGuard_NodegroupNeedsTheControlPlane(t *testing.T) {
	for _, skip := range []bool{false, true} {
		name := "with the health gate"
		if skip {
			name = "with --skip-health-check"
		}
		t.Run(name, func(t *testing.T) {
			// The plan reads the control plane at 1.33. At the roll it
			// runs 1.32: the plan was made against another answer.
			world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Version = "1.33"
				c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
			})
			e := newEnv(t, world)
			req := upgradeTo("1.33")
			req.Only = []Part{PartNodegroups}
			req.Policy.Skip = skip
			hooks := Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() {
				world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Version = "1.32" })
			})}
			res := mustRun(t, e, req, hooks)
			if slices.Contains(e.sent(), sentA33) || slices.Contains(e.sent(), sentB33) {
				t.Errorf("requests %v: a nodegroup was rolled to 1.33 under a control plane at 1.32", e.sent())
			}
			wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "the control plane of prod runs 1.32", "must be at 1.33 first")
			if res.ExitCode() != exitcode.Blocked {
				t.Errorf("exit = %d, want 3 (%v)", res.ExitCode(), res.Err)
			}
		})
	}
}

// A nodegroup that is ACTIVE with a health issue is not rolled, with the
// health gate and without it: the old engine refused it in a gate that no
// flag skipped.
func TestEngineGuard_NodegroupWithIssuesIsNotRolled(t *testing.T) {
	for _, skip := range []bool{false, true} {
		name := "with the health gate"
		if skip {
			name = "with --skip-health-check"
		}
		t.Run(name, func(t *testing.T) {
			world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodegroup("ng-a").HealthIssues = []fleettest.Issue{{Code: "AccessDenied", Message: "the node role cannot join the cluster"}}
			})
			e := newEnv(t, world)
			req := upgradeTo("1.33")
			req.Policy = health.Policy{Yes: true, Skip: skip}
			res := mustRun(t, e, req, Hooks{})
			if slices.Contains(e.sent(), sentA33) {
				t.Errorf("requests %v: ng-a was rolled with a health issue", e.sent())
			}
			wantBlocked(t, stepResultOf(t, res, StepNodegroup, "ng-a"), "nodegroup ng-a has 1 health issue(s), first: AccessDenied: the node role cannot join the cluster")
		})
	}
}

// A request that follows a failed attachment has the force of the
// request of the run, and the plan shows it on the step that attaches.
func TestEngineGuard_ForceAfterAnAttachment(t *testing.T) {
	t.Run("a nodegroup roll with --force", func(t *testing.T) {
		world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
			c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Result: fleettest.UpdateFailed}}
		})
		v := "1.33"
		if _, err := world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v}); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, world)
		req := upgradeTo("1.33")
		req.Policy.Force = true
		plan, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		if s := findStep(t, &plan.Clusters[0], "1.33", StepNodegroup, "ng-a"); s.Action != Attach || !s.Force {
			t.Errorf("the step of ng-a = %s, force %t; want Attach with the force of the roll that may follow", s.Action, s.Force)
		}
		var notices []string
		mustRun(t, e, req, Hooks{Event: func(ev Event) {
			if ev.Kind == Notice {
				notices = append(notices, ev.Text)
			}
		}})
		if !slices.Contains(e.sent(), sentA33+" force") {
			t.Errorf("requests %v; want the roll of ng-a with force", e.sent())
		}
		// The plan showed an attachment, and the run sends a roll: it
		// says so.
		if want := "nodegroup ng-a is not at 1.33 and no update of it is running, so the run rolls it to 1.33 itself; the plan would wait for an update that was running"; !slices.Contains(notices, want) {
			t.Errorf("no notice says %q:\n%s", want, strings.Join(notices, "\n"))
		}
	})
	t.Run("a control-plane rollback with --skip-insights-check", func(t *testing.T) {
		world := upgradedWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			for _, ng := range c.Nodegroups {
				ng.Version = "1.32"
			}
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni32, dns32, proxy32
		})
		startControlPlaneElsewhere(t, world, "1.32", fleettest.UpdateScript{After: 2 * time.Minute, Result: fleettest.UpdateFailed})
		e := newEnv(t, world)
		req := rollbackOf()
		req.SkipInsights = true
		mustRun(t, e, req, Hooks{})
		if !slices.Contains(e.sent(), sentCP32+" force") {
			t.Errorf("requests %v; want the run's own rollback with force", e.sent())
		}
	})
}

// A later phase that a person declines keeps the guidance of a run that
// stopped part way: what completed, and the command that goes on.
func TestEngine_DeclinedPhaseKeepsTheResumeHint(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	req := upgradeTo("1.33")
	req.Policy.Yes, req.ConfirmPhases = false, true
	res := mustRun(t, e, req, Hooks{Ask: func(_ context.Context, q Question) (bool, error) {
		return q.Step.Phase.Kind != PhaseNodegroups, nil
	}})
	if res.Status != Declined || res.ExitCode() != exitcode.Failure || res.Err == nil {
		t.Fatalf("result = %s, exit %d, %v; want Declined, 1", res.Status, res.ExitCode(), res.Err)
	}
	for _, want := range []string{"declined before the nodegroup rolls to 1.33", "completed: the control plane to 1.33", "to go on from the live state of the cluster, run: refresh cluster upgrade prod --to 1.33"} {
		if !strings.Contains(res.Err.Error(), want) {
			t.Errorf("the message lacks %q: %v", want, res.Err)
		}
	}
}

// A later hop of an upgrade starts from the version that the hop before
// it reaches, and the plan, the question, and the rows say so: EKS moves
// one minor version at a time. The run still compares the live version.
func TestEngine_LaterHopStartsWhereTheHopBeforeEnds(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	req := upgradeTo("1.34")
	req.Policy.Yes, req.ConfirmPhases = false, true
	plan, err := NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	froms := map[string]string{}
	for _, s := range plan.Clusters[0].Steps() {
		if s.Ref.Kind != StepAddon {
			froms[s.Ref.String()] = s.From + " -> " + s.To
		}
	}
	want := map[string]string{
		"prod/1.33/ControlPlane": "1.32 -> 1.33", "prod/1.33/Nodegroups/ng-a": "1.32 -> 1.33", "prod/1.33/Nodegroups/ng-b": "1.32 -> 1.33",
		"prod/1.34/ControlPlane": "1.33 -> 1.34", "prod/1.34/Nodegroups/ng-a": "1.33 -> 1.34", "prod/1.34/Nodegroups/ng-b": "1.33 -> 1.34",
	}
	if diff := cmp.Diff(want, froms); diff != "" {
		t.Errorf("the versions of the plan steps (-want +got):\n%s", diff)
	}
	// The rows of the plan document are the steps.
	doc := plan.Document()
	rows := map[string]string{}
	for _, s := range doc.Clusters[0].Steps {
		if s.Kind != apidoc.StepAddon && s.Hop == "1.34" {
			rows[string(s.Kind)+" "+s.Name] = s.From + " -> " + s.To
		}
	}
	wantRows := map[string]string{"ControlPlane ": "1.33 -> 1.34", "Nodegroup ng-a": "1.33 -> 1.34", "Nodegroup ng-b": "1.33 -> 1.34"}
	if diff := cmp.Diff(wantRows, rows); diff != "" {
		t.Errorf("the rows of hop 1.34 in the document (-want +got):\n%s", diff)
	}
	var questions []string
	res := mustRun(t, e, req, Hooks{Ask: func(_ context.Context, q Question) (bool, error) {
		questions = append(questions, q.Text)
		return true, nil
	}})
	if !slices.Contains(questions, "Upgrade the control plane of prod from 1.33 to 1.34?") {
		t.Errorf("no question about the hop from 1.33 to 1.34:\n%s", strings.Join(questions, "\n"))
	}
	for _, q := range questions {
		if strings.Contains(q, "from 1.32 to 1.34") {
			t.Errorf("a question names a jump of two minor versions: %s", q)
		}
	}
	// Both hops ran: the guards compare the live version, not From.
	if res.ExitCode() != exitcode.OK || !slices.Contains(e.sent(), "eks:UpdateClusterVersion 1.34") || !slices.Contains(e.sent(), "eks:UpdateNodegroupVersion ng-b 1.34") {
		t.Errorf("exit %d (%v), requests %v; want both hops", res.ExitCode(), res.Err, e.sent())
	}
}

// The plan would wait for a roll that someone else started. The roll ends
// before its phase, and the nodegroup is ACTIVE at the goal. No run has
// verified that roll, so this one does: the step is not skipped.
func TestEngine_VerifiesAnAttachedRollThatEndedBeforeItsPhase(t *testing.T) {
	world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
	})
	v := "1.33"
	if _, err := world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: &v}); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, world)
	req := upgradeTo("1.33")
	plan, err := NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	if s := findStep(t, &plan.Clusters[0], "1.33", StepNodegroup, "ng-a"); s.Action != Attach {
		t.Fatalf("the step of ng-a = %s, want Attach", s.Action)
	}
	// The roll of ng-a ends while the run is held before the phase.
	hooks := Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() { world.Advance(time.Minute) })}
	res := mustRun(t, e, req, hooks)
	if a := stepResultOf(t, res, StepNodegroup, "ng-a"); a.Status != StepSucceeded {
		t.Errorf("ng-a = %s (%+v), want Succeeded: verified", a.Status, a.Failure)
	}
	verified := false
	for _, row := range res.Clusters[0].Verification {
		verified = verified || (strings.HasPrefix(row.Name, nodegroupActiveCheck) && row.Outcome == health.Pass && strings.Contains(row.Name+row.Message, "ng-a"))
	}
	if !verified {
		t.Errorf("no verification row for ng-a: %+v", res.Clusters[0].Verification)
	}
	if slices.Contains(e.sent(), sentA33) {
		t.Errorf("requests %v: ng-a was rolled a second time", e.sent())
	}
}

// A gate that stops a later phase exits 3, and the message names the
// phases that completed and the command that goes on.
func TestEngine_GateStopsALaterPhase(t *testing.T) {
	// kube-proxy runs a build that 1.33 keeps: its step has no work.
	world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) { c.Addon("kube-proxy").Version = proxy33 })
	e := newEnv(t, world)
	hooks := Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() {
		world.Edit(func(s *fleettest.State) { degradeNodegroup("ng-a")(s, s.Cluster(east, "prod")) })
	})}
	reported := map[string]StepStatus{}
	hooks.Event = func(ev Event) {
		if ev.Kind == StepChanged {
			reported[ev.Step.String()] = ev.Result.Status
		}
	}
	res := mustRun(t, e, upgradeTo("1.33"), hooks)
	if res.ExitCode() != exitcode.Blocked || res.Status != RunBlocked || res.Err == nil {
		t.Fatalf("result = %s, exit %d, %v; want Blocked, 3", res.Status, res.ExitCode(), res.Err)
	}
	for _, want := range []string{"completed: the control plane to 1.33, the add-ons that 1.33 needs before the nodegroup rolls", "to go on from the live state of the cluster, run: refresh cluster upgrade prod --to 1.33"} {
		if !strings.Contains(res.Err.Error(), want) {
			t.Errorf("the message lacks %q: %v", want, res.Err)
		}
	}
	// A step with no work is Skipped, also in a phase that the run did
	// not reach, and its row is reported as every other row is. A step
	// with work that was not reached is NotAttempted.
	const proxy, b = "prod/1.33/AddonsAfter/kube-proxy", "prod/1.33/Nodegroups/ng-b"
	if got := stepStatuses(res); got[proxy] != StepSkipped || got[b] != StepNotAttempted {
		t.Errorf("kube-proxy = %s and ng-b = %s; want Skipped and NotAttempted", got[proxy], got[b])
	}
	if reported[proxy] != StepSkipped {
		t.Errorf("the events report kube-proxy as %q, want Skipped (all: %v)", reported[proxy], reported)
	}
}

// The force of a control-plane step is what --skip-insights-check sends,
// and Diff words a change of it with the flag that the user typed.
func TestDiff_ForceIsWordedAsTheFlag(t *testing.T) {
	target := Target{Region: east, Cluster: "prod"}
	plan := func(kind StepKind, name string, force bool) *Plan {
		step := Step{Ref: StepRef{Phase: PhaseRef{Target: target, Kind: PhaseControlPlane}, Kind: kind, Name: name}, Action: Submit, From: "1.33", To: "1.32", Force: force}
		return &Plan{Kind: ClusterRollback, Clusters: []ClusterPlan{{Target: target, Phases: []Phase{{Ref: step.Ref.Phase, Steps: []Step{step}}}}}}
	}
	tests := []struct {
		kind       StepKind
		name, want string
	}{
		{StepControlPlane, "", "with --skip-insights-check, not without it"},
		{StepNodegroup, "ng-a", "with --force, not without it"},
	}
	for _, tt := range tests {
		diff := Diff(plan(tt.kind, tt.name, false), plan(tt.kind, tt.name, true))
		if len(diff) != 1 || !strings.Contains(diff[0].Message, tt.want) {
			t.Errorf("%s: diff = %+v, want one change that says %q", tt.kind, diff, tt.want)
		}
	}
}

// The question of a phase names the steps that the phase has now: a
// nodegroup that was rolled elsewhere since the plan is not in it.
func TestEngine_PhaseQuestionNamesTheLiveSteps(t *testing.T) {
	world := engineWorld(nil)
	e := newEnv(t, world)
	req := upgradeTo("1.33")
	req.Policy.Yes, req.ConfirmPhases = false, true
	var questions []string
	hooks := Hooks{
		Checkpoint: atCheckpoint(PhaseNodegroups, func() {
			world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = "1.33" })
		}),
		Ask: func(_ context.Context, q Question) (bool, error) {
			// The question comes before any request of its phase.
			if q.Step.Phase.Kind == PhaseNodegroups && slices.Contains(e.sent(), sentB33) {
				t.Errorf("the roll of ng-b was sent before the question of its phase")
			}
			questions = append(questions, q.Text)
			return true, nil
		},
	}
	res := mustRun(t, e, req, hooks)
	if res.ExitCode() != exitcode.OK {
		t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
	}
	if want := "Roll 1 nodegroup(s) of prod to Kubernetes 1.33 (ng-b)?"; !slices.Contains(questions, want) {
		t.Errorf("no question %q:\n%s", want, strings.Join(questions, "\n"))
	}
	if slices.Contains(e.sent(), sentA33) || !slices.Contains(e.sent(), sentB33) {
		t.Errorf("requests %v; want the roll of ng-b alone", e.sent())
	}
}

// The health checks of the guard call AWS, which takes time. The control
// plane starts to change while the last of them is under way: the read
// after the checks applies the busy rule, and the roll is not sent.
func TestEngineGuard_ChangeDuringTheHealthChecks(t *testing.T) {
	world := func() *fleettest.World {
		return engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
		})
	}
	req := upgradeTo("1.33")
	req.Only = []Part{PartNodegroups}

	// A first run counts the metric reads before the first roll.
	e := newEnv(t, world())
	mustRun(t, e, req, Hooks{})
	before := 0
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupVersion {
			break
		}
		if r.Operation == fleettest.CloudWatchGetMetricData {
			before++
		}
	}
	if before == 0 {
		t.Fatal("the health checks read no metric before the first roll")
	}

	w := world()
	e = newEnv(t, w)
	e.onRequest(fleettest.CloudWatchGetMetricData, func(n int) {
		if n == before {
			startControlPlaneElsewhere(t, w, "1.34", fleettest.UpdateScript{Never: true})
		}
	})
	res := mustRun(t, e, req, Hooks{})
	if slices.Contains(e.sent(), sentA33) || slices.Contains(e.sent(), sentB33) {
		t.Errorf("requests %v: a nodegroup was rolled while the control plane changes", e.sent())
	}
	if res.ExitCode() != exitcode.Blocked {
		t.Errorf("exit = %d, want 3 (%v)", res.ExitCode(), res.Err)
	}
}

// With --skip-insights-check, insights that the guard could not read block
// nothing, and the run still reports them: it is not a clean success.
func TestEngineGuard_UnreadInsightsAreReported(t *testing.T) {
	world := upgradedWorld(nil)
	e := newEnv(t, world)
	req := rollbackOf()
	req.SkipInsights = true
	hooks := Hooks{Checkpoint: atCheckpoint(PhaseNodegroups, func() {
		world.SetFault(fleettest.EKSListInsights, fleettest.Fault{Deny: true})
	})}
	res := mustRun(t, e, req, hooks)
	if !slices.Contains(e.sent(), sentCP32+" force") {
		t.Errorf("requests %v; want the rollback of the control plane", e.sent())
	}
	if res.ExitCode() != exitcode.Incomplete || len(res.Failures) == 0 {
		t.Errorf("exit = %d with the failures %v; want 4 and the insights failure", res.ExitCode(), res.Failures)
	}
}

// The guard holds the direction of an add-on step against the read right
// before the request: a rollback never sends a version above the live one.
// Someone moves vpc-cni below the build of the plan, to one that the
// rollback target does not list, while the rollback runs.
func TestEngineGuard_AddonRollbackNeverUpgrades(t *testing.T) {
	const older = "v1.18.0-eksbuild.1"
	world := upgradedWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: older, ClusterVersions: []string{"1.31"}})
	})
	e := newEnv(t, world)
	moved := false
	hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
		if next.Phase.Kind == PhaseAddons && next.Kind == "" && !moved {
			moved = true
			startElsewhere(t, world, "vpc-cni", older, fleettest.UpdateScript{After: 30 * time.Second})
		}
		return Checkpoint{}, nil
	}}
	res := mustRun(t, e, rollbackOf(), hooks)
	if !moved {
		t.Fatal("the run did not reach the add-on phase")
	}
	if want := "eks:UpdateAddon vpc-cni " + cni32; slices.Contains(e.sent(), want) {
		t.Errorf("requests %v: the rollback upgraded vpc-cni from %s", e.sent(), older)
	}
	wantBlocked(t, stepResultOf(t, res, StepAddon, "vpc-cni"), "vpc-cni", older, cni32, "a rollback does not upgrade an add-on")
}

// An add-on version that is empty or does not parse is neither at the
// goal nor behind it: the step is blocked and names the add-on and the
// value, and nothing is sent for it.
func TestEngineGuard_AddonVersionMustParse(t *testing.T) {
	for _, bad := range []string{"", "garbage"} {
		t.Run("upgrade, live "+strconv.Quote(bad), func(t *testing.T) {
			world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) { c.Version = "1.33" })
			e := newEnv(t, world)
			hooks := Hooks{Checkpoint: atCheckpoint(PhaseAddonsBefore, func() {
				world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = bad })
			})}
			res := mustRun(t, e, upgradeTo("1.33"), hooks)
			if slices.Contains(e.sent(), sentCNI33) {
				t.Errorf("requests %v: vpc-cni was updated from the version %q", e.sent(), bad)
			}
			wantBlocked(t, stepResultOf(t, res, StepAddon, "vpc-cni"), "add-on vpc-cni", strconv.Quote(bad), "not a version")
		})
		t.Run("rollback, live "+strconv.Quote(bad), func(t *testing.T) {
			world := upgradedWorld(nil)
			e := newEnv(t, world)
			hooks := Hooks{Checkpoint: atCheckpoint(PhaseAddons, func() {
				world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = bad })
			})}
			res := mustRun(t, e, rollbackOf(), hooks)
			if want := "eks:UpdateAddon vpc-cni " + cni32; slices.Contains(e.sent(), want) {
				t.Errorf("requests %v: vpc-cni was updated from the version %q", e.sent(), bad)
			}
			wantBlocked(t, stepResultOf(t, res, StepAddon, "vpc-cni"), "add-on vpc-cni", strconv.Quote(bad), "not a version")
		})
	}
}
