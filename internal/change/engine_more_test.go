package change

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// The checkpoint hook runs before every phase that has work, the first
// included, and before each step of a phase after its first. It runs
// after the plan and its consent, and before the first change.
func TestEngine_CheckpointOrder(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	var seen []string
	sentAtFirst := -1
	hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
		if sentAtFirst < 0 {
			sentAtFirst = len(e.sent())
		}
		seen = append(seen, next.String())
		return Checkpoint{}, nil
	}}
	mustRun(t, e, upgradeTo("1.33"), hooks)
	want := []string{
		"prod/1.33/ControlPlane",
		"prod/1.33/AddonsBefore", "prod/1.33/AddonsBefore/coredns",
		"prod/1.33/Nodegroups", "prod/1.33/Nodegroups/ng-b",
		"prod/1.33/AddonsAfter",
	}
	if diff := cmp.Diff(want, seen); diff != "" {
		t.Errorf("checkpoints (-want +got):\n%s", diff)
	}
	// Only the insights refresh of the plan came before the first one.
	if sentAtFirst != 1 {
		t.Errorf("%d request(s) that change something before the first checkpoint, want the refresh alone", sentAtFirst)
	}
}

// A phase with nothing to do ends as skipped, and a phase that ran does
// not.
func TestEngine_PhaseEndedSaysSkipped(t *testing.T) {
	// The control plane and the add-ons are done: only the rolls are left.
	e := newEnv(t, engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
	}))
	skipped := map[PhaseKind]bool{}
	mustRun(t, e, upgradeTo("1.33"), Hooks{Event: func(ev Event) {
		if ev.Kind == PhaseEnded {
			skipped[ev.Phase.Kind] = ev.Skipped
		}
	}})
	want := map[PhaseKind]bool{PhaseControlPlane: true, PhaseNodegroups: false, PhaseAddonsAfter: true}
	if diff := cmp.Diff(want, skipped); diff != "" {
		t.Errorf("skipped phases (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{sentA33, sentB33}, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
}

// An upgrade with ConfirmPhases asks once for each phase that has work.
// The first question is the start question, and it names the warnings of
// the gate. The insights are refreshed once: the plan after the answer
// reuses the refresh.
func TestEngine_ConfirmPhases(t *testing.T) {
	ask := func(answers map[PhaseKind]bool, asked *[]Question) func(context.Context, Question) (bool, error) {
		return func(_ context.Context, q Question) (bool, error) {
			*asked = append(*asked, q)
			no, ok := answers[q.Step.Phase.Kind]
			return !ok || !no, nil
		}
	}
	req := upgradeTo("1.33")
	req.Policy.Yes, req.ConfirmPhases = false, true
	t.Run("yes to all", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		var asked []Question
		res := mustRun(t, e, req, Hooks{Ask: ask(nil, &asked)})
		if res.ExitCode() != exitcode.OK {
			t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
		}
		var kinds []string
		for _, q := range asked {
			kinds = append(kinds, string(q.Kind)+" "+string(q.Step.Phase.Kind))
		}
		want := []string{"ConfirmPhase ControlPlane", "ConfirmPhase AddonsBefore", "ConfirmPhase Nodegroups", "ConfirmPhase AddonsAfter"}
		if diff := cmp.Diff(want, kinds); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
		// Without Kubernetes access the gate warns, and the first question
		// says so.
		if !strings.Contains(asked[0].Text, "despite the health warnings") || len(asked[0].Reasons) == 0 {
			t.Errorf("the first question = %q, reasons %q; want the warnings of the gate", asked[0].Text, asked[0].Reasons)
		}
		if n := e.count(fleettest.EKSStartInsightsRefresh); n != 1 {
			t.Errorf("%d insights refreshes, want 1", n)
		}
		if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("no before the nodegroups", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		var asked []Question
		res := mustRun(t, e, req, Hooks{Ask: ask(map[PhaseKind]bool{PhaseNodegroups: true}, &asked)})
		if res.Status != Declined || res.ExitCode() != exitcode.Failure {
			t.Errorf("status = %s, exit %d; want Declined, exit 1", res.Status, res.ExitCode())
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33, sentCNI33, sentDNS33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
}

// A rollback asks once, with the whole order in the question.
func TestEngine_RollbackAsksOnce(t *testing.T) {
	e := newEnv(t, upgradedWorld(nil))
	req := rollbackOf()
	req.Policy.Yes = false
	var asked []Question
	res := mustRun(t, e, req, Hooks{Ask: func(_ context.Context, q Question) (bool, error) {
		asked = append(asked, q)
		return true, nil
	}})
	if res.ExitCode() != exitcode.OK || len(asked) != 1 {
		t.Fatalf("exit %d (%v), %d question(s); want exit 0 and one question", res.ExitCode(), res.Err, len(asked))
	}
	for _, part := range []string{"Roll back prod from 1.33 to 1.32", "2 nodegroup roll(s) (ng-a, ng-b)", "3 add-on update(s)", "then the control plane to 1.32"} {
		if !strings.Contains(asked[0].Text, part) {
			t.Errorf("question = %q, want it to have %q", asked[0].Text, part)
		}
	}
}

// Two hops: the insights are refreshed again before the second control
// plane moves, because EKS judges them for the next minor version only.
func TestEngine_TwoHops(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	res := mustRun(t, e, upgradeTo("1.34"), Hooks{})
	if res.ExitCode() != exitcode.OK {
		t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
	}
	want := []string{
		sentRefresh, sentCP33, sentCNI33, sentDNS33, sentA33, sentB33, sentProxy33,
		sentRefresh, "eks:UpdateClusterVersion 1.34", "eks:UpdateAddon vpc-cni " + cni34, "eks:UpdateAddon coredns " + dns34,
		"eks:UpdateNodegroupVersion ng-a 1.34", "eks:UpdateNodegroupVersion ng-b 1.34", "eks:UpdateAddon kube-proxy " + proxy34,
	}
	if diff := cmp.Diff(want, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
}

// A nodegroup that is at the target and DEGRADED is not skipped and not
// rolled: it is verified, and the issue ends the run with exit code 5.
func TestEngine_VerifyOnlyStep(t *testing.T) {
	e := newEnv(t, engineWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
		c.Nodegroup("ng-a").Version = "1.33"
		degradeNodegroup("ng-a")(s, c)
	}))
	req := upgradeTo("1.33")
	// ng-b is left alone, so the gate has no target that it would fail.
	req.SkipNodegroups = []string{"ng-b"}
	res := mustRun(t, e, req, Hooks{})
	if res.Status != VerifyFailed || res.ExitCode() != exitcode.VerifyFailed || len(e.sent()) != 0 {
		t.Errorf("status = %s, exit %d, requests %v, err %v; want VerifyFailed, exit 5, and no request", res.Status, res.ExitCode(), e.sent(), res.Err)
	}
}

// --only without the add-ons leaves an add-on that the new control plane
// cannot run to the operator, with a notice, and moves the control plane.
func TestEngine_OnlyControlPlane(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	req := upgradeTo("1.33")
	req.Only = []Part{PartControlPlane}
	res := mustRun(t, e, req, Hooks{})
	if res.ExitCode() != exitcode.OK {
		t.Fatalf("exit %d: %v", res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff([]string{sentRefresh, sentCP33}, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	notices := strings.Join(res.Plan.Clusters[0].Notices, "\n")
	if !strings.Contains(notices, "may not run on Kubernetes 1.33") {
		t.Errorf("notices = %q, want one about the add-ons that 1.33 cannot run", notices)
	}
	for _, s := range res.Plan.Clusters[0].Steps() {
		if s.Ref.Name == "vpc-cni" && (s.Action != Manual || s.Reason != OutOfScope) {
			t.Errorf("vpc-cni = %s (%s), want Manual, OutOfScope", s.Action, s.Reason)
		}
	}
}

// An add-on that someone else is updating when its step comes is waited
// for, and no update is sent for it. When that update fails, the run
// stops and sends none of its own.
func TestEngine_AddonAttach(t *testing.T) {
	run := func(t *testing.T, script fleettest.UpdateScript) (*env, Result) {
		t.Helper()
		world := engineWorld(nil)
		e := newEnv(t, world)
		v := cni33
		hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			if next.Phase.Kind == PhaseAddonsBefore && next.Kind == "" {
				world.Edit(func(s *fleettest.State) {
					s.Cluster(east, "prod").Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{script}
				})
				if _, err := world.UpdateAddon(east, "prod", "vpc-cni", fleettest.AddonRequest{Version: &v}); err != nil {
					t.Errorf("starting the update: %v", err)
				}
			}
			return Checkpoint{}, nil
		}}
		return e, mustRun(t, e, upgradeTo("1.33"), hooks)
	}
	t.Run("the update succeeds", func(t *testing.T) {
		e, res := run(t, fleettest.UpdateScript{After: time.Minute})
		if res.ExitCode() != exitcode.OK {
			t.Errorf("exit %d: %v", res.ExitCode(), res.Err)
		}
		want := slices.DeleteFunc(wholeUpgrade(), func(s string) bool { return s == sentCNI33 })
		if diff := cmp.Diff(want, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("the update fails", func(t *testing.T) {
		e, res := run(t, fleettest.UpdateScript{After: time.Minute, Result: fleettest.UpdateFailed, ResourceStatus: fleettest.StatusUpdateFailed})
		if res.Status != Failed || res.ExitCode() != exitcode.Failure {
			t.Errorf("status = %s, exit %d; want Failed, exit 1", res.Status, res.ExitCode())
		}
		if diff := cmp.Diff([]string{sentRefresh, sentCP33}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
}

// The observer of a roll that changes the Kubernetes version tells the
// new nodes by that version. A roll onto a new AMI has no target version.
func TestRollIdentity(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		step Step
		want noderoll.Identity
	}{
		{name: "an upgrade roll", step: Step{From: "1.32", To: "1.33"}, want: noderoll.Identity{TargetVersion: "1.33", StartedAt: at}},
		{name: "a rollback roll", step: Step{From: "1.33", To: "1.32"}, want: noderoll.Identity{TargetVersion: "1.32", StartedAt: at}},
		{name: "an AMI roll", step: Step{From: "1.32", To: "1.32", ToImage: "ami-1"}, want: noderoll.Identity{TargetAMI: "ami-1", StartedAt: at}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, rollIdentity(tt.step, at)); diff != "" {
				t.Errorf("identity (-want +got):\n%s", diff)
			}
		})
	}
}

// The update in flight of a resource comes with its ID, its type, and the
// time of its creation.
func TestInFlightUpdate(t *testing.T) {
	world := engineWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} })
	v := "1.33"
	started, err := world.UpdateClusterVersion(east, "prod", fleettest.ClusterVersionRequest{Version: &v})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, world)
	got, err := inFlightUpdate(testContext(t), e.deps.AWS.Clients(east), Resource{Target: Target{Region: east, Cluster: "prod"}, Kind: StepControlPlane})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != started.ID || got.Type != "VersionUpdate" || got.CreatedAt.IsZero() {
		t.Errorf("update = %+v, want the ID %s, the type VersionUpdate, and a time", got, started.ID)
	}
}

// afterAnswer is a transport that calls then once the answer to a request
// for path has arrived in full, and before the caller has it.
type afterAnswer struct {
	base http.RoundTripper
	path string
	then func()
}

func (a *afterAnswer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := a.base.RoundTrip(req)
	if err != nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, a.path) {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	a.then()
	return resp, nil
}

// A roll that EKS accepted is never reported as only Started when the run
// ends right after the answer: the run knows that the update is in
// progress. An interrupt at that moment exits 1. A read budget that runs
// out at that moment does not stop the wait, which has its own budget.
func TestRoll_AcceptedRollIsFollowedWhenTheReadContextEnds(t *testing.T) {
	depsWith := func(e *env, then func()) Deps {
		cfg := e.srv.Config(east)
		cfg.HTTPClient = &http.Client{Transport: &afterAnswer{base: http.DefaultTransport, path: "/node-groups/ng-a/update-version", then: then}}
		return depsFor(e.world, cfg)
	}
	t.Run("interrupt", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		res, err := Run(ctx, depsWith(e, cancel), rollOf("ng-a"), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonInterrupted || s.UpdateID == "" {
			t.Errorf("ng-a = %s, update %q, failure %+v; want InProgress, with its update ID and the reason Interrupted", s.Status, s.UpdateID, s.Failure)
		}
		if res.Status != Interrupted || res.ExitCode() != exitcode.Failure {
			t.Errorf("status = %s, exit %d; want Interrupted, exit 1", res.Status, res.ExitCode())
		}
	})
	t.Run("the read budget runs out", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		// The timer of the read budget fires when the answer has arrived.
		var expire func()
		d := depsWith(e, func() { expire() })
		d.AfterFunc = func(_ time.Duration, f func()) (stop func() bool) {
			if expire == nil {
				expire = f
			}
			return func() bool { return true }
		}
		req := rollOf("ng-a")
		req.Limits.Read = time.Minute
		res, err := Run(testContext(t), d, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepSucceeded || e.count(fleettest.EKSDescribeUpdate) == 0 {
			t.Errorf("ng-a = %s after %d poll(s), failure %+v; want the roll waited for to its end", s.Status, e.count(fleettest.EKSDescribeUpdate), s.Failure)
		}
	})
}

// After a wait that timed out, the result says that the roll is still in
// progress, and does not also say that the rolls ended unverified.
func TestRoll_TimedOutRollIsNotReportedAsEnded(t *testing.T) {
	e := newEnv(t, rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} }))
	req := rollOf("ng-a")
	req.Limits.Wait = 2 * time.Minute
	res := mustRun(t, e, req, Hooks{})
	if res.ExitCode() != exitcode.Incomplete {
		t.Errorf("exit %d, want 4", res.ExitCode())
	}
	for _, f := range res.Failures {
		if strings.Contains(f.Error, "ended and were not verified") {
			t.Errorf("failure %q: the roll did not end", f.Error)
		}
	}
	if res.Err == nil || strings.Contains(res.Err.Error(), "ended and were not verified") {
		t.Errorf("message = %v", res.Err)
	}
}
