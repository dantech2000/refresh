package change

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go/middleware"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// script is a front end for a test: it records the events and the
// questions of a run, and answers from a list.
type script struct {
	t      *testing.T
	events []string
	asked  []Question
	// answers are the answers to the questions, in order. A question
	// beyond the list fails the test.
	answers []bool
	// onAsk runs while the question with the index is open.
	onAsk func(i int)
	// askErr is the error of every question, when it is set.
	askErr error
}

func (s *script) hooks() Hooks {
	return Hooks{Event: s.event, Ask: s.ask}
}

func (s *script) event(e Event) {
	line := string(e.Kind)
	switch e.Kind {
	case PlanReady:
		line += " " + string(e.Plan.Decision())
	case GateReport:
		line += " " + string(e.Verdict.Decision)
	case PhaseStarted, PhaseEnded:
		line += " " + e.Phase.String()
	case StepChanged:
		line += " " + e.Step.Name + " " + string(e.Result.Status)
	case UpdatePolled:
		line += " " + e.Step.Name + " " + e.Update.Status
	case VerifyReport:
		line += " " + e.Step.Name + " " + string(e.Checks[0].Outcome)
	case Notice:
		line += " " + e.Text
	}
	s.events = append(s.events, line)
}

func (s *script) ask(ctx context.Context, q Question) (bool, error) {
	i := len(s.asked)
	s.asked = append(s.asked, q)
	if s.onAsk != nil {
		s.onAsk(i)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.askErr != nil {
		return false, s.askErr
	}
	if i >= len(s.answers) {
		s.t.Errorf("question %d has no answer in the script: %q", i+1, q.Text)
		return false, nil
	}
	return s.answers[i], nil
}

// kinds returns the kinds of the questions that were asked.
func (s *script) kinds() []QuestionKind {
	var out []QuestionKind
	for _, q := range s.asked {
		out = append(out, q.Kind)
	}
	return out
}

// The limits of the runs of these tests.
var limits = Limits{Read: time.Minute, Wait: 5 * time.Minute, Poll: 15 * time.Second}

func request(names ...string) AddonUpdateRequest {
	return AddonUpdateRequest{Common: Common{Limits: limits}, Target: Target{Cluster: "prod"}, Addons: names}
}

func yes(req AddonUpdateRequest) AddonUpdateRequest {
	req.Policy.Yes = true
	return req
}

// statuses returns the status of each step of the result, by name.
func statuses(res Result) map[string]StepStatus {
	out := map[string]StepStatus{}
	for _, c := range res.Clusters {
		for _, s := range c.Steps {
			out[s.Ref.Name] = s.Status
		}
	}
	return out
}

// The order of the events of an add-on update of two add-ons, one of
// them slow.
func TestRun_EventOrder(t *testing.T) {
	e := newEnv(t, newWorld(func(c *fleettest.Cluster) {
		c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{{After: 20 * time.Second}}
	}))
	s := &script{t: t}
	req := yes(request())
	req.Addons, req.All = nil, true
	res, err := Run(testContext(t), e.deps, req, s.hooks())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PlanReady Proceed",
		"GateReport Go",
		"PhaseStarted prod/Addons",
		"StepChanged vpc-cni Started",
		"UpdateProgress vpc-cni InProgress",
		"UpdateProgress vpc-cni Successful",
		// The outcome of a step comes before its verification.
		"StepChanged vpc-cni Succeeded",
		"VerifyReport vpc-cni Pass",
		"StepChanged coredns Started",
		"UpdateProgress coredns Successful",
		"StepChanged coredns Succeeded",
		"VerifyReport coredns Pass",
		"StepChanged kube-proxy Skipped",
		"PhaseEnded prod/Addons",
	}
	if diff := cmp.Diff(want, s.events); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
	if res.Status != Succeeded || res.ExitCode() != 0 || res.Err != nil {
		t.Errorf("result = %s, exit %d, %v; want Succeeded, 0", res.Status, res.ExitCode(), res.Err)
	}
	if len(s.asked) != 0 {
		t.Errorf("a run with --yes asked %v", s.kinds())
	}
	// With --yes the plan is computed once. The cluster is read once more
	// before coredns, because the wait for vpc-cni took time.
	if n := e.count(fleettest.EKSDescribeCluster); n != 2 {
		t.Errorf("%d reads of the cluster, want 2", n)
	}
	doc, ok := res.Document().(apidoc.Run)
	if !ok || len(doc.Clusters[0].Results) != 3 || len(doc.Clusters[0].Verification) != 2 {
		t.Errorf("document = %T %+v", res.Document(), doc)
	}
}

// --no-wait submits every named add-on, in the order given, and waits for
// nothing: no poll, and no read after a submit. coredns is named before
// vpc-cni, its dependency, so the run has no order to keep: see
// TestRun_NoWaitDependentPair.
func TestRun_NoWait(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	s := &script{t: t}
	req := yes(request("coredns", "vpc-cni", "kube-proxy"))
	req.NoWait = true
	res, err := Run(testContext(t), e.deps, req, s.hooks())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StepStatus{"vpc-cni": StepStarted, "coredns": StepStarted, "kube-proxy": StepSkipped}
	if diff := cmp.Diff(want, statuses(res)); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"coredns", "vpc-cni"}, updated(e)); diff != "" {
		t.Errorf("updates (-want +got), in the order given:\n%s", diff)
	}
	if res.Status != Started || res.ExitCode() != 0 {
		t.Errorf("result = %s, exit %d; want Started, 0", res.Status, res.ExitCode())
	}
	if n := e.count(fleettest.EKSDescribeUpdate); n != 0 {
		t.Errorf("%d DescribeUpdate requests, want none", n)
	}
	ops := e.operations(0)
	if diff := cmp.Diff([]string{"eks:DescribeAddon", "eks:UpdateAddon", "eks:DescribeAddon", "eks:UpdateAddon"}, ops[len(ops)-4:]); diff != "" {
		t.Errorf("the last requests (-want +got):\n%s", diff)
	}
}

// The questions of a run: each answer, and what the run sent after it.
func TestRun_Questions(t *testing.T) {
	degraded := func(c *fleettest.Cluster) { c.Addon("vpc-cni").Status = fleettest.StatusDegraded }
	hookErr := errors.New("stdin: input/output error")
	tests := []struct {
		name    string
		seed    func(c *fleettest.Cluster)
		policy  health.Policy
		answers []bool
		askErr  error
		cancel  bool
		noAsk   bool
		asked   []QuestionKind
		status  RunStatus
		exit    int
		updates int
		wantErr error
		errText string
	}{
		{name: "confirm: yes", answers: []bool{true}, asked: []QuestionKind{ConfirmStart}, status: Succeeded, updates: 1},
		{name: "confirm: no", answers: []bool{false}, asked: []QuestionKind{ConfirmStart}, status: Declined, exit: 1, errText: "declined; nothing was changed"},
		{name: "confirm: --yes", policy: health.Policy{Yes: true}, status: Succeeded, updates: 1},
		{name: "warnings: yes", seed: degraded, answers: []bool{true}, asked: []QuestionKind{GateWarnings}, status: Succeeded, updates: 1},
		{name: "warnings: no", seed: degraded, answers: []bool{false}, asked: []QuestionKind{GateWarnings}, status: Declined, exit: 1},
		{name: "warnings: --yes", seed: degraded, policy: health.Policy{Yes: true}, status: Succeeded, updates: 1},
		{
			name: "warnings: --require-healthy", seed: degraded, policy: health.Policy{RequireHealthy: true},
			status: Refused, exit: 3, errText: "--require-healthy is set",
		},
		{name: "cancel during the question", cancel: true, asked: []QuestionKind{ConfirmStart}, status: Interrupted, exit: 1, errText: "interrupted before anything was changed"},
		{
			name: "the hook fails", askErr: hookErr, asked: []QuestionKind{ConfirmStart}, status: Failed, exit: 1,
			wantErr: hookErr, errText: "nothing was changed",
		},
		{
			name: "nobody to ask: no answer is not a yes", noAsk: true, status: Refused, exit: 1,
			errText: "nobody confirmed the change and nobody can be asked; pass --yes; nothing was changed",
		},
		{
			name: "nobody to ask: warnings refuse", seed: degraded, noAsk: true, status: Refused, exit: 1,
			errText: "health warnings need an answer and nobody can be asked; pass --yes or --require-healthy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(tt.seed))
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			s := &script{t: t, answers: tt.answers, askErr: tt.askErr}
			if tt.cancel {
				s.onAsk = func(int) { cancel() }
			}
			req := request("vpc-cni")
			req.Policy = tt.policy
			hooks := s.hooks()
			if tt.noAsk {
				hooks.Ask = nil
			}
			res, err := Run(ctx, e.deps, req, hooks)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.asked, s.kinds()); diff != "" {
				t.Errorf("questions (-want +got):\n%s", diff)
			}
			if res.Status != tt.status || res.ExitCode() != tt.exit {
				t.Errorf("result = %s, exit %d; want %s, %d", res.Status, res.ExitCode(), tt.status, tt.exit)
			}
			if tt.errText != "" && (res.Err == nil || !strings.Contains(res.Err.Error(), tt.errText)) {
				t.Errorf("message = %v, want %q", res.Err, tt.errText)
			}
			if n := e.count(fleettest.EKSUpdateAddon); n != tt.updates {
				t.Errorf("%d UpdateAddon requests, want %d", n, tt.updates)
			}
			if tt.updates == 0 {
				if got := statuses(res)["vpc-cni"]; got != StepNotAttempted {
					t.Errorf("the step is %s, want NotAttempted", got)
				}
			}
			// After a yes of a person the plan is computed again.
			plans := 1
			if len(tt.answers) > 0 && tt.answers[0] {
				plans = 2
			}
			if n := e.count(fleettest.EKSDescribeCluster); n != plans {
				t.Errorf("%d plans, want %d", n, plans)
			}
		})
	}
}

func TestRun_QuestionText(t *testing.T) {
	e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Addon("coredns").Status = fleettest.StatusDegraded }))
	for _, tt := range []struct {
		req  AddonUpdateRequest
		want string
	}{
		{request("vpc-cni"), "Update add-on vpc-cni in prod from " + cniOld + " to " + cniNew + "?"},
		{request("vpc-cni", "kube-proxy"), "Update add-on vpc-cni in prod from " + cniOld + " to " + cniNew + "?"},
		{request("coredns"), "Proceed despite 1 warning (Addons: coredns is DEGRADED; the update will try to repair it)?"},
	} {
		s := &script{t: t, answers: []bool{false}}
		if _, err := Run(testContext(t), e.deps, tt.req, s.hooks()); err != nil {
			t.Fatal(err)
		}
		if len(s.asked) != 1 || s.asked[0].Text != tt.want {
			t.Errorf("questions = %+v, want %q", s.asked, tt.want)
		}
	}
	p := planOf(AddonUpdate, addonStep("a", Submit, VersionBehind, "v1", "v2"), addonStep("b", Attach, InFlight, "v2", "v2"))
	if got, want := startQuestion(p), "Update 2 add-ons in prod (a, b)?"; got != want {
		t.Errorf("startQuestion = %q, want %q", got, want)
	}
	if got, want := startQuestion(planOf(AddonUpdate, addonStep("a", Attach, InFlight, "v2", "v2"))), "Wait for the update of add-on a in prod to v2?"; got != want {
		t.Errorf("startQuestion = %q, want %q", got, want)
	}
	if got, want := startQuestion(planOf(AddonUpdate, addonStep("a", Submit, Repair, "v2", "v2"))), "Apply add-on a in prod again at v2?"; got != want {
		t.Errorf("startQuestion = %q, want %q", got, want)
	}
}

// The world changes while the question is open. The run computes the plan
// again after the answer and compares before it sends anything.
func TestRun_WorldChangesWhileAsking(t *testing.T) {
	edit := func(e *env, f func(c *fleettest.Cluster, s *fleettest.State)) {
		e.world.Edit(func(s *fleettest.State) { f(s.Cluster(east, "prod"), s) })
	}
	newer := func(version string) func(e *env) {
		return func(e *env) {
			edit(e, func(_ *fleettest.Cluster, s *fleettest.State) {
				s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: version, ClusterVersions: []string{"1.33"}})
			})
		}
	}
	tests := []struct {
		name string
		// changes[i] changes the world while question i is open.
		changes []func(e *env)
		answers []bool
		asked   []QuestionKind
		status  RunStatus
		exit    int
		// sent is the version of the UpdateAddon request, or "" for none.
		sent    string
		notice  string
		errText string
	}{
		{
			name: "the add-on is updated elsewhere: done, nothing is sent", answers: []bool{true}, asked: []QuestionKind{ConfirmStart},
			changes: []func(e *env){func(e *env) {
				edit(e, func(c *fleettest.Cluster, _ *fleettest.State) { c.Addon("vpc-cni").Version = cniNew })
			}},
			status: NothingToDo, notice: "Notice add-on vpc-cni needs no change any more",
		},
		{
			name: "the same update starts elsewhere: the run attaches", answers: []bool{true}, asked: []QuestionKind{ConfirmStart},
			changes: []func(e *env){func(e *env) {
				startElsewhere(e.t, e.world, "vpc-cni", cniNew, fleettest.UpdateScript{After: time.Minute})
			}},
			status: Succeeded, notice: "Notice add-on vpc-cni is already changing to " + cniNew + "; waiting for it",
		},
		{
			name: "another update starts elsewhere: the target is busy", answers: []bool{true}, asked: []QuestionKind{ConfirmStart},
			changes: []func(e *env){func(e *env) {
				startElsewhere(e.t, e.world, "vpc-cni", cniOther, fleettest.UpdateScript{Never: true})
			}},
			status: RunBlocked, exit: 3, notice: "Notice add-on vpc-cni is now changing elsewhere",
		},
		{
			name: "a newer version is published: the run asks again", answers: []bool{true, true},
			asked:   []QuestionKind{ConfirmStart, PlanChanged},
			changes: []func(e *env){newer("v1.20.1-eksbuild.1")},
			status:  Succeeded, sent: "v1.20.1-eksbuild.1",
		},
		{
			name: "a newer version is published: the second answer is no", answers: []bool{true, false},
			asked:   []QuestionKind{ConfirmStart, PlanChanged},
			changes: []func(e *env){newer("v1.20.1-eksbuild.1")},
			status:  Declined, exit: 1,
		},
		{
			name: "the plan changes at every question: refused", answers: []bool{true, true},
			asked:   []QuestionKind{ConfirmStart, PlanChanged},
			changes: []func(e *env){newer("v1.20.1-eksbuild.1"), newer("v1.20.2-eksbuild.1")},
			status:  Refused, exit: 3, errText: "the cluster keeps changing",
		},
		{
			name: "the cluster starts to update: refused", answers: []bool{true}, asked: []QuestionKind{ConfirmStart},
			changes: []func(e *env){func(e *env) {
				edit(e, func(c *fleettest.Cluster, _ *fleettest.State) { c.Status = fleettest.StatusUpdating })
			}},
			status: Refused, exit: 3, errText: "prod is busy (cluster UPDATING); nothing was started",
		},
		{
			name: "the add-on degrades: the run asks about the warning", answers: []bool{true, true},
			asked: []QuestionKind{ConfirmStart, GateWarnings},
			changes: []func(e *env){func(e *env) {
				edit(e, func(c *fleettest.Cluster, _ *fleettest.State) { c.Addon("vpc-cni").Status = fleettest.StatusDegraded })
			}},
			status: Succeeded, sent: cniNew,
		},
		{
			name: "nothing changes: one question", answers: []bool{true}, asked: []QuestionKind{ConfirmStart},
			status: Succeeded, sent: cniNew,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(nil))
			s := &script{t: t, answers: tt.answers}
			s.onAsk = func(i int) {
				// No request is in flight while a question is open.
				if i < len(tt.changes) {
					tt.changes[i](e)
				}
			}
			res, err := Run(testContext(t), e.deps, request("vpc-cni"), s.hooks())
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.asked, s.kinds()); diff != "" {
				t.Errorf("questions (-want +got):\n%s", diff)
			}
			if res.Status != tt.status || res.ExitCode() != tt.exit {
				t.Errorf("result = %s, exit %d (%v); want %s, %d", res.Status, res.ExitCode(), res.Err, tt.status, tt.exit)
			}
			sent := ""
			for _, r := range e.srv.Requests() {
				if r.Operation == fleettest.EKSUpdateAddon {
					sent = fmt.Sprint(r.Input["addonVersion"])
				}
			}
			if sent != tt.sent {
				t.Errorf("UpdateAddon sent version %q, want %q", sent, tt.sent)
			}
			if tt.notice != "" && !contains(s.events, tt.notice) {
				t.Errorf("no event %q in %v", tt.notice, s.events)
			}
			if tt.errText != "" && (res.Err == nil || !strings.Contains(res.Err.Error(), tt.errText)) {
				t.Errorf("message = %v, want %q", res.Err, tt.errText)
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// A plan that a person reviewed before the call is compared with the
// first plan of the run, and the run asks nothing to confirm its start.
func TestRun_Expect(t *testing.T) {
	t.Run("the same plan", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		req := request("vpc-cni")
		reviewed, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		req.Expect = reviewed
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil || res.Status != Succeeded || len(s.asked) != 0 {
			t.Fatalf("result = %s, %v, questions %v; want Succeeded and no question", res.Status, err, s.kinds())
		}
		if contains(s.events, "PlanReady Proceed") {
			t.Error("a plan that equals the reviewed one was announced again")
		}
	})
	// --yes is how the consent is given, and no part of the plan: a plan
	// that was reviewed without it and is run with it matches, and so does
	// the reverse. A warning that the reviewed plan showed is covered too.
	degraded := func(c *fleettest.Cluster) { c.Addon("vpc-cni").Status = fleettest.StatusDegraded }
	for _, tt := range []struct {
		name                string
		seed                func(c *fleettest.Cluster)
		reviewedYes, runYes bool
	}{
		{name: "reviewed without --yes, run with --yes", runYes: true},
		{name: "reviewed with --yes, run without --yes", reviewedYes: true},
		{name: "a warning, reviewed without --yes, run with --yes", seed: degraded, runYes: true},
		{name: "a warning, reviewed with --yes, run without --yes", seed: degraded, reviewedYes: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(tt.seed))
			before, req := request("vpc-cni"), request("vpc-cni")
			before.Policy.Yes, req.Policy.Yes = tt.reviewedYes, tt.runYes
			reviewed, err := NewPlan(testContext(t), e.deps, before)
			if err != nil {
				t.Fatal(err)
			}
			req.Expect = reviewed
			// No answer is in the script: a question fails the test.
			s := &script{t: t}
			res, err := Run(testContext(t), e.deps, req, s.hooks())
			if err != nil || res.Status != Succeeded || res.ExitCode() != 0 || len(s.asked) != 0 {
				t.Fatalf("result = %s, exit %d, %v, questions %v; want Succeeded, 0, and no question", res.Status, res.ExitCode(), err, s.kinds())
			}
			if n := e.count(fleettest.EKSUpdateAddon); n != 1 {
				t.Errorf("%d UpdateAddon requests, want 1", n)
			}
		})
	}
	t.Run("the version changed since", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		req := request("vpc-cni")
		reviewed, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		e.world.Edit(func(s *fleettest.State) {
			s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.20.1-eksbuild.1", ClusterVersions: []string{"1.33"}})
		})
		req.Expect = reviewed
		s := &script{t: t, answers: []bool{true}}
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v", res.Status, err)
		}
		if diff := cmp.Diff([]QuestionKind{PlanChanged}, s.kinds()); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
		if !strings.Contains(s.asked[0].Text, "add-on vpc-cni now goes to v1.20.1-eksbuild.1") {
			t.Errorf("question = %q", s.asked[0].Text)
		}
		// --yes accepts the change and says so.
		e2 := newEnv(t, newWorld(nil))
		// The plan was reviewed without --yes, and that is no difference.
		reviewed2, err := NewPlan(testContext(t), e2.deps, request("vpc-cni"))
		if err != nil {
			t.Fatal(err)
		}
		e2.world.Edit(func(s *fleettest.State) {
			s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.20.1-eksbuild.1", ClusterVersions: []string{"1.33"}})
		})
		req2 := yes(request("vpc-cni"))
		req2.Expect = reviewed2
		s2 := &script{t: t}
		res2, err := Run(testContext(t), e2.deps, req2, s2.hooks())
		if err != nil || res2.Status != Succeeded || len(s2.asked) != 0 {
			t.Fatalf("with --yes: result = %s, %v, questions %v", res2.Status, err, s2.kinds())
		}
		if !contains(s2.events, "Notice add-on vpc-cni now goes to v1.20.1-eksbuild.1, not "+cniNew) {
			t.Errorf("no notice about the accepted change in %v", s2.events)
		}
	})
}

// The fresh read before a submit sees what changed since the plan.
func TestRun_FreshReadBeforeSubmit(t *testing.T) {
	tests := []struct {
		name string
		// version is --version, or "" for the latest.
		version string
		// change runs when the fresh read of vpc-cni arrives.
		change  func(t *testing.T, e *env)
		status  StepStatus
		exit    int
		updates int
		check   func(t *testing.T, res Result)
	}{
		{
			name: "updated elsewhere: skipped", status: StepSkipped,
			change: func(_ *testing.T, e *env) {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = cniNew })
			},
		},
		{
			name: "the same update started elsewhere: attached", status: StepSucceeded,
			change: func(t *testing.T, e *env) {
				t.Helper()
				startElsewhere(t, e.world, "vpc-cni", cniNew, fleettest.UpdateScript{After: 30 * time.Second})
			},
			check: func(t *testing.T, res Result) {
				t.Helper()
				if s := res.Clusters[0].Steps[0]; !s.Attached || s.UpdateID != "" {
					t.Errorf("step = %+v, want attached and no update ID", s)
				}
			},
		},
		{
			name: "another update started elsewhere: busy", status: StepBusy, exit: 3,
			change: func(t *testing.T, e *env) {
				t.Helper()
				startElsewhere(t, e.world, "vpc-cni", cniOther, fleettest.UpdateScript{Never: true})
			},
			check: func(t *testing.T, res Result) {
				t.Helper()
				s := res.Clusters[0].Steps[0]
				if s.InFlight != "AddonUpdate" || s.Failure == nil || s.Failure.Reason != apidoc.ReasonBusy {
					t.Errorf("step = %+v, failure %+v; want AddonUpdate in flight and a Busy failure", s, s.Failure)
				}
				if len(res.Failures) != 0 {
					t.Errorf("the failure of the step is also a failure of the run: %v", res.Failures)
				}
			},
		},
		{
			// The same request from the newer version is a downgrade that the
			// plan did not show.
			name: "--version, and the add-on moved past it: failed, and nothing is sent", version: cniNew, status: StepFailed, exit: 1,
			change: func(_ *testing.T, e *env) {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = "v1.20.5-eksbuild.1" })
			},
			check: func(t *testing.T, res Result) {
				t.Helper()
				want := "add-on vpc-cni changed to v1.20.5-eksbuild.1 since the plan, which read " + cniOld + "; nothing was sent. Run the command again"
				if f := res.Clusters[0].Steps[0].Failure; f == nil || f.Error != want {
					t.Errorf("failure = %+v, want %q", f, want)
				}
			},
		},
		{
			name: "moved to another version below the target: failed, and nothing is sent", status: StepFailed, exit: 1,
			change: func(_ *testing.T, e *env) {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = cniOther })
			},
		},
		{
			name: "the read is denied: failed, and nothing is sent", status: StepFailed, exit: 1,
			change: func(_ *testing.T, e *env) {
				e.world.SetFault(fleettest.EKSDescribeAddon, fleettest.Fault{Deny: true})
			},
			check: func(t *testing.T, res Result) {
				t.Helper()
				if f := res.Clusters[0].Steps[0].Failure; f == nil || f.Reason != apidoc.ReasonAccessDenied || f.Operation != "eks:DescribeAddon" {
					t.Errorf("failure = %+v", f)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(nil))
			// The plan describes each add-on once. The next read of an
			// add-on is the fresh one.
			e.onRequest(fleettest.EKSDescribeAddon, func(n int) {
				if n == 4 {
					tt.change(t, e)
				}
			})
			req := yes(request("vpc-cni"))
			req.Version = tt.version
			res, err := Run(testContext(t), e.deps, req, (&script{t: t}).hooks())
			if err != nil {
				t.Fatal(err)
			}
			if got := statuses(res)["vpc-cni"]; got != tt.status || res.ExitCode() != tt.exit {
				t.Errorf("step = %s, exit %d; want %s, %d", got, res.ExitCode(), tt.status, tt.exit)
			}
			if n := e.count(fleettest.EKSUpdateAddon); n != tt.updates {
				t.Errorf("%d UpdateAddon requests, want %d", n, tt.updates)
			}
			if tt.check != nil {
				tt.check(t, res)
			}
		})
	}
}

// The lookup of the update in flight happens only for a busy target.
func TestRun_BusyLookupOnlyWhenBusy(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	if _, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(fleettest.EKSListUpdates); n != 0 {
		t.Errorf("%d ListUpdates requests in a run with no busy target, want none", n)
	}

	busy := newEnv(t, newWorld(nil))
	startElsewhere(t, busy.world, "vpc-cni", cniOther, fleettest.UpdateScript{Never: true})
	req := yes(request())
	req.Addons, req.All = nil, true
	res, err := Run(testContext(t), busy.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	// coredns depends on vpc-cni, so --all does not update it.
	want := map[string]StepStatus{"vpc-cni": StepBusy, "coredns": StepNotAttempted, "kube-proxy": StepSkipped}
	if diff := cmp.Diff(want, statuses(res)); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	if n := busy.count(fleettest.EKSListUpdates); n != 1 {
		t.Errorf("%d ListUpdates requests, want 1", n)
	}
	if res.ExitCode() != 3 || res.Status != RunBlocked {
		t.Errorf("result = %s, exit %d; want Blocked, 3: nothing changed", res.Status, res.ExitCode())
	}
}

// An answer of UpdateAddon with no update in it cannot be followed: the
// step is in progress, and the run is incomplete.
func TestRun_NoUpdateID(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	deps := e.intercept(t, func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/addons/vpc-cni/update")
	})
	res, err := Run(testContext(t), deps, yes(request("vpc-cni")), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Clusters[0].Steps[0]
	if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonNotMonitored {
		t.Errorf("step = %s, failure %+v; want InProgress with NotMonitored", s.Status, s.Failure)
	}
	if res.ExitCode() != 4 || res.Status != Incomplete {
		t.Errorf("result = %s, exit %d; want Incomplete, 4", res.Status, res.ExitCode())
	}
	if n := e.count(fleettest.EKSDescribeUpdate); n != 0 {
		t.Errorf("%d DescribeUpdate requests, want none", n)
	}
}

// A response that is lost after EKS accepted the update is retried with
// the same token: one update starts, and the run follows it.
func TestRun_LostResponse(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	e.world.SetFault(fleettest.EKSUpdateAddon, fleettest.Fault{LoseResponse: 1})
	res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{})
	if err != nil || res.Status != Succeeded {
		t.Fatalf("result = %s, %v", res.Status, err)
	}
	var tokens []string
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSUpdateAddon {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
	e.world.View(func(s *fleettest.State) {
		if n := len(s.Cluster(east, "prod").Updates); n != 1 {
			t.Errorf("%d updates in the world, want 1", n)
		}
	})
}

// A request that EKS refuses fails its step, and the next add-on is
// still attempted when it does not depend on the first: see
// TestRun_StopsDependents.
func TestRun_SubmitRefused(t *testing.T) {
	e := newEnv(t, withAgent(newWorld(nil)))
	e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSUpdateAddon, Addon: "vpc-cni"}, fleettest.Fault{Code: "InvalidParameterException"})
	res, err := Run(testContext(t), e.deps, yes(request("vpc-cni", agent)), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StepStatus{"vpc-cni": StepFailed, agent: StepSucceeded}
	if diff := cmp.Diff(want, statuses(res)); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	if f := res.Clusters[0].Steps[0].Failure; f == nil || f.Operation != "eks:UpdateAddon" || f.Reason != apidoc.ReasonInvalidRequest {
		t.Errorf("failure = %+v", f)
	}
	if res.ExitCode() != 4 {
		t.Errorf("exit = %d, want 4: one failed and one succeeded", res.ExitCode())
	}
}

// agent is an add-on that depends on no other add-on: beside vpc-cni it
// is an independent target.
const agent, agentOld, agentNew = "eks-pod-identity-agent", "v1.3.0-eksbuild.1", "v1.3.4-eksbuild.1"

// withAgent adds agent to the world, one version behind.
func withAgent(w *fleettest.World) *fleettest.World {
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Addons = append(c.Addons, &fleettest.Addon{Name: agent, Version: agentOld})
		s.AddonVersions = append(s.AddonVersions,
			fleettest.AddonVersion{Addon: agent, Version: agentNew, ClusterVersions: []string{"1.33"}},
			fleettest.AddonVersion{Addon: agent, Version: agentOld, ClusterVersions: []string{"1.33"}})
	})
	return w
}

// An add-on is not updated after a dependency that ran before it in the
// same run did not end clean: it failed, timed out, was busy, or was
// updated and is not healthy. It is NotAttempted, with a failure that
// says why, and nothing is sent for it. A dependency that was Skipped is
// clean. An add-on that depends on nothing goes on.
//
// --all orders the add-ons by dependency. Named add-ons run in the order
// given: a dependency named after its dependent has not run when the
// dependent does, so it does not stop it.
func TestRun_StopsDependents(t *testing.T) {
	refused := func(_ *testing.T, e *env) {
		e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSUpdateAddon, Addon: "vpc-cni"}, fleettest.Fault{Code: "InvalidParameterException"})
	}
	cniScript := func(script fleettest.UpdateScript) func(c *fleettest.Cluster) {
		return func(c *fleettest.Cluster) { c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{script} }
	}
	fails := cniScript(fleettest.UpdateScript{After: 30 * time.Second, Result: fleettest.UpdateFailed})
	degraded := cniScript(fleettest.UpdateScript{ResourceStatus: fleettest.StatusDegraded})
	// unreadOnce makes the read of vpc-cni after its update fail once: the
	// first DescribeAddon of it after a poll of its update is denied, and
	// the next one, the read of the cluster before the next step, works.
	unreadOnce := func(_ *testing.T, e *env) {
		var mu sync.Mutex
		polled, reads := false, 0
		cni := fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "vpc-cni"}
		e.srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.Operation == fleettest.EKSDescribeUpdate && r.Input["addonName"] == "vpc-cni":
				polled = true
			case r.Operation == fleettest.EKSDescribeAddon && r.Input["addonName"] == "vpc-cni" && polled:
				if reads++; reads == 1 {
					e.world.SetScopedFault(cni, fleettest.Fault{Deny: true})
				} else {
					e.world.SetScopedFault(cni, fleettest.Fault{})
				}
			}
		}
	}
	didNotFinish := func(status string) string {
		return "add-on coredns was not updated: its dependency vpc-cni did not finish (" + status + "); nothing was sent"
	}
	const (
		notHealthy = "add-on coredns was not updated: its dependency vpc-cni was updated and is not healthy (add-on vpc-cni ended in status DEGRADED (expected ACTIVE)); nothing was sent"
		notRead    = "add-on coredns was not updated: its dependency vpc-cni was updated and could not be read after it, so its health is not known; nothing was sent"
	)
	tests := []struct {
		name string
		// named are the add-ons of the request; nil is --all.
		named   []string
		seed    func(c *fleettest.Cluster)
		prepare func(t *testing.T, e *env)
		want    map[string]StepStatus
		updates []any
		exit    int
		// coredns is what the failure of coredns starts with, or "" for none.
		coredns string
	}{
		{
			name: "--all: the request of the dependency is refused", prepare: refused,
			want:    map[string]StepStatus{"vpc-cni": StepFailed, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "vpc-cni"}, exit: 4, coredns: didNotFinish("Failed"),
		},
		{
			name: "--all: the update of the dependency fails", seed: fails,
			want:    map[string]StepStatus{"vpc-cni": StepFailed, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "vpc-cni"}, exit: 4, coredns: didNotFinish("Failed"),
		},
		{
			name: "--all: the wait of the dependency times out", seed: cniScript(fleettest.UpdateScript{Never: true}),
			want:    map[string]StepStatus{"vpc-cni": StepInProgress, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "vpc-cni"}, exit: 4, coredns: didNotFinish("InProgress"),
		},
		{
			name: "--all: the dependency is busy",
			prepare: func(t *testing.T, e *env) {
				t.Helper()
				startElsewhere(t, e.world, "vpc-cni", cniOther, fleettest.UpdateScript{Never: true})
			},
			want:    map[string]StepStatus{"vpc-cni": StepBusy, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent}, exit: 4, coredns: didNotFinish("Busy"),
		},
		{
			name: "--all: the dependency is updated and DEGRADED", seed: degraded,
			want:    map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "vpc-cni"}, exit: 5, coredns: notHealthy,
		},
		{
			name: "--all: the dependency cannot be read after its update", prepare: unreadOnce,
			want:    map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepNotAttempted, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "vpc-cni"}, exit: 4, coredns: notRead,
		},
		{
			name: "--all: the dependency is at its target", seed: func(c *fleettest.Cluster) { c.Addon("vpc-cni").Version = cniNew },
			want:    map[string]StepStatus{"vpc-cni": StepSkipped, "coredns": StepSucceeded, "kube-proxy": StepSkipped, agent: StepSucceeded},
			updates: []any{agent, "coredns"},
		},
		{
			name: "named, the dependency first: it fails", named: []string{"vpc-cni", "coredns"}, seed: fails,
			want:    map[string]StepStatus{"vpc-cni": StepFailed, "coredns": StepNotAttempted},
			updates: []any{"vpc-cni"}, exit: 1, coredns: didNotFinish("Failed"),
		},
		{
			name: "named, the dependency first: it is DEGRADED", named: []string{"vpc-cni", "coredns"}, seed: degraded,
			want:    map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepNotAttempted},
			updates: []any{"vpc-cni"}, exit: 5, coredns: notHealthy,
		},
		{
			name: "named, the dependency first: it cannot be read after its update", named: []string{"vpc-cni", "coredns"}, prepare: unreadOnce,
			want:    map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepNotAttempted},
			updates: []any{"vpc-cni"}, exit: 4, coredns: notRead,
		},
		{
			name: "named, the dependent first: the order is kept, and it goes on", named: []string{"coredns", "vpc-cni"}, seed: fails,
			want:    map[string]StepStatus{"vpc-cni": StepFailed, "coredns": StepSucceeded},
			updates: []any{"coredns", "vpc-cni"}, exit: 4,
		},
		{
			name: "named, independent add-ons: a failure stops nothing", named: []string{"vpc-cni", agent}, prepare: refused,
			want:    map[string]StepStatus{"vpc-cni": StepFailed, agent: StepSucceeded},
			updates: []any{"vpc-cni", agent}, exit: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, withAgent(newWorld(tt.seed)))
			if tt.prepare != nil {
				tt.prepare(t, e)
			}
			req := all(health.Policy{Yes: true})
			if tt.named != nil {
				req = yes(request(tt.named...))
			}
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, statuses(res)); diff != "" {
				t.Errorf("steps (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.updates, updated(e)); diff != "" {
				t.Errorf("updates (-want +got):\n%s", diff)
			}
			// The exit-code rule is the same.
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d", res.ExitCode(), tt.exit)
			}
			if _, ok := tt.want["coredns"]; !ok {
				return
			}
			s := stepResultOf(t, res, StepAddon, "coredns")
			switch {
			case tt.coredns == "" && s.Failure != nil:
				t.Errorf("coredns failure = %+v, want none", s.Failure)
			case tt.coredns != "" && (s.Failure == nil || s.Failure.Reason != apidoc.ReasonNotAttempted || s.Failure.Error != tt.coredns):
				t.Errorf("coredns failure = %+v, want NotAttempted: %q", s.Failure, tt.coredns)
			}
		})
	}
}

// --no-wait is refused when an add-on to update depends on one that comes
// before it in the run: without a wait, that order means nothing. --all
// puts every dependency first. Named add-ons keep the order of the user:
// a dependent named first is no pair, and the run goes on.
func TestRun_NoWaitDependentPair(t *testing.T) {
	tests := []struct {
		name string
		// named are the add-ons of the request; nil is --all.
		named []string
		seed  func(c *fleettest.Cluster)
		// refused is the refusal, or "" for a run that goes on.
		refused string
		updates []any
	}{
		{name: "--all with a dependent pair", refused: "--no-wait cannot keep the dependency order of --all: add-on coredns waits for vpc-cni to finish"},
		{
			name: "--all with no dependent pair", seed: func(c *fleettest.Cluster) { c.Addon("coredns").Version = dnsNew },
			updates: []any{"vpc-cni"},
		},
		{name: "named, the dependency first", named: []string{"vpc-cni", "coredns"}, refused: "--no-wait cannot keep the order of the add-ons: add-on coredns waits for vpc-cni to finish"},
		{name: "named, the dependent first", named: []string{"coredns", "vpc-cni"}, updates: []any{"coredns", "vpc-cni"}},
	}
	t.Run("the plan says that a dependent named first keeps its order", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		p, err := NewPlan(testContext(t), e.deps, yes(request("coredns", "vpc-cni")))
		if err != nil {
			t.Fatal(err)
		}
		const want = "add-on coredns depends on vpc-cni, which is named after it; the order given is kept"
		if !contains(p.Clusters[0].Notices, want) {
			t.Errorf("notices = %q, want %q", p.Clusters[0].Notices, want)
		}
		p, err = NewPlan(testContext(t), e.deps, yes(request("vpc-cni", "coredns")))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Clusters[0].Notices) != 0 {
			t.Errorf("notices = %q, want none: the dependency comes first", p.Clusters[0].Notices)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(tt.seed))
			req := all(health.Policy{Yes: true})
			if tt.named != nil {
				req = yes(request(tt.named...))
			}
			req.NoWait = true
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if tt.refused == "" {
				if err != nil || res.Status != Started {
					t.Fatalf("result = %s, %v; want Started", res.Status, err)
				}
				if diff := cmp.Diff(tt.updates, updated(e)); diff != "" {
					t.Errorf("updates (-want +got):\n%s", diff)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.refused) {
				t.Fatalf("error = %v, want %q", err, tt.refused)
			}
			if res.ExitCode() != 1 || len(e.mutations()) != 0 {
				t.Errorf("exit %d, %d changing requests; want 1 and none", res.ExitCode(), len(e.mutations()))
			}
			if _, err := NewPlan(testContext(t), e.deps, req); err == nil || !strings.Contains(err.Error(), tt.refused) {
				t.Errorf("NewPlan error = %v, want %q: a dry run refuses as the run does", err, tt.refused)
			}
		})
	}
}

// The wait deadline and an interrupt end a wait in different ways: the
// first is incomplete, exit 4, and the second exits 1.
func TestRun_DeadlineAndInterrupt(t *testing.T) {
	never := func(c *fleettest.Cluster) { c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }

	t.Run("the wait deadline", func(t *testing.T) {
		e := newEnv(t, withAgent(newWorld(never)))
		res, err := Run(testContext(t), e.deps, yes(request("vpc-cni", agent)), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		// Each add-on has its own wait budget: the next one is still
		// updated, as it does not depend on vpc-cni.
		want := map[string]StepStatus{"vpc-cni": StepInProgress, agent: StepSucceeded}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		s := res.Clusters[0].Steps[0]
		if s.Failure == nil || s.Failure.Reason != apidoc.ReasonTimeout || s.UpdateID == "" || !s.FinishedAt.IsZero() {
			t.Errorf("step = %+v, failure %+v; want a Timeout failure, the update ID, and no end time", s, s.Failure)
		}
		if res.ExitCode() != 4 || res.Status != Incomplete {
			t.Errorf("result = %s, exit %d; want Incomplete, 4", res.Status, res.ExitCode())
		}
		// 5 minutes at 15 seconds: 20 polls, and none after the deadline.
		polls := 0
		for _, r := range e.srv.Requests() {
			if r.Operation == fleettest.EKSDescribeUpdate && strings.HasSuffix(fmt.Sprint(r.Input["updateId"]), "1") {
				polls++
			}
		}
		if polls != 20 {
			t.Errorf("%d polls of the update that timed out, want 20", polls)
		}
	})

	t.Run("an interrupt", func(t *testing.T) {
		e := newEnv(t, newWorld(never))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 3 {
				cancel()
			}
		})
		req := yes(request())
		req.Addons, req.All = nil, true
		res, err := Run(ctx, e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepInProgress, "coredns": StepNotAttempted, "kube-proxy": StepNotAttempted}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if f := res.Clusters[0].Steps[0].Failure; f == nil || f.Reason != apidoc.ReasonInterrupted {
			t.Errorf("failure = %+v, want Interrupted", f)
		}
		if res.ExitCode() != 1 || res.Status != Interrupted || !errors.Is(res.Err, context.Canceled) {
			t.Errorf("result = %s, exit %d, %v; want Interrupted, 1", res.Status, res.ExitCode(), res.Err)
		}
		if n := e.count(fleettest.EKSUpdateAddon); n != 1 {
			t.Errorf("%d UpdateAddon requests, want 1: nothing is sent after an interrupt", n)
		}
	})

	t.Run("an interrupt during the plan", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		e.onRequest(fleettest.EKSDescribeCluster, func(int) { cancel() })
		res, err := Run(ctx, e.deps, yes(request("vpc-cni")), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode() != 1 || res.Status != Interrupted || res.Plan != nil || res.Document() != nil {
			t.Errorf("result = %s, exit %d, plan %v; want Interrupted, 1, and no plan", res.Status, res.ExitCode(), res.Plan)
		}
	})
}

// The cluster was read, and the interrupt ends the reads after it. The
// planner then has a snapshot with parts that were not read, which is
// the snapshot of a plan that is blocked. An interrupted run has no
// plan: it is not a blocked change.
func TestRun_InterruptDuringThePlanAfterTheClusterWasRead(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	// The interrupt comes when the client has the answer of
	// DescribeCluster, so that read always succeeds.
	cfg := e.srv.Config(east)
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("interruptAfterDescribeCluster",
			func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				out, md, err := next.HandleInitialize(ctx, in)
				if _, ok := in.Parameters.(*eks.DescribeClusterInput); ok && err == nil {
					cancel()
				}
				return out, md, err
			}), middleware.After)
	})
	res, err := Run(ctx, depsFor(e.world, cfg), yes(request("vpc-cni")), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode() != 1 || res.Status != Interrupted || res.Plan != nil || res.Document() != nil {
		t.Errorf("result = %s, exit %d, plan %v; want Interrupted, 1, and no plan", res.Status, res.ExitCode(), res.Plan)
	}
	if n := len(e.mutations()); n != 0 {
		t.Errorf("%d requests that change the cluster, want none", n)
	}
}

// The budgets of a run. They follow the clock of the run, which these
// tests move from a hook of the fake.
func TestRun_Budgets(t *testing.T) {
	t.Run("neither budget runs while a person answers", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		s := &script{t: t, answers: []bool{true}}
		s.onAsk = func(int) { e.world.Advance(3 * time.Hour) }
		res, err := Run(testContext(t), e.deps, request("vpc-cni"), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
	})

	t.Run("the read budget runs out after the plan was read", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		// The plan takes two minutes of the one that --timeout gives.
		e.onRequest(fleettest.EKSDescribeAddonVersions, func(int) { e.world.Advance(2 * time.Minute) })
		res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if got := statuses(res)["vpc-cni"]; got != StepNotAttempted {
			t.Errorf("step = %s, want NotAttempted", got)
		}
		if len(res.Failures) != 1 || res.Failures[0].Reason != apidoc.ReasonTimeout {
			t.Errorf("failures = %+v, want one Timeout", res.Failures)
		}
		if res.ExitCode() != 4 || e.count(fleettest.EKSUpdateAddon) != 0 {
			t.Errorf("exit = %d with %d updates; want 4 and none", res.ExitCode(), e.count(fleettest.EKSUpdateAddon))
		}
	})

	t.Run("the read budget does not run during a wait", func(t *testing.T) {
		e := newEnv(t, newWorld(func(c *fleettest.Cluster) {
			c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{{After: 4 * time.Minute}}
		}))
		req := yes(request())
		req.Addons, req.All = nil, true
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded: the wait of 4m is not read time", res.Status, err, res.Err)
		}
	})

	t.Run("the read budget runs out between two add-ons", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		// The submit of the first add-on takes two minutes of read time.
		e.onRequest(fleettest.EKSUpdateAddon, func(n int) {
			if n == 1 {
				e.world.Advance(2 * time.Minute)
			}
		})
		req := yes(request())
		req.Addons, req.All = nil, true
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepNotAttempted, "kube-proxy": StepNotAttempted}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if res.ExitCode() != 4 || len(res.Failures) != 1 || res.Failures[0].Reason != apidoc.ReasonTimeout {
			t.Errorf("exit = %d, failures %+v; want 4 and one Timeout", res.ExitCode(), res.Failures)
		}
	})

	t.Run("a read that hangs is ended by the timer of the budget, with nothing read", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		e.world.SetFault(fleettest.EKSDescribeCluster, fleettest.Fault{Hang: true})
		ctx := testContext(t)
		// The clock of the world does not move while the read hangs, so
		// only the timer of the budget can end it. The test holds that
		// timer, and fires it once the read hangs.
		timers := make(chan func(), 8)
		deps := e.deps
		deps.AfterFunc = func(_ time.Duration, expire func()) (stop func() bool) {
			timers <- expire
			return func() bool { return true }
		}
		fired := make(chan error, 1)
		go func() {
			err := e.world.AwaitBlocked(ctx, 1)
			(<-timers)()
			fired <- err
		}()
		res, err := Run(ctx, deps, yes(request("vpc-cni")), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if err := <-fired; err != nil {
			t.Fatalf("waiting for the read to hang: %v", err)
		}
		if res.ExitCode() != 1 || res.Plan != nil || res.Err == nil || !strings.Contains(res.Err.Error(), "timed out reading prod (increase --timeout); nothing was changed") {
			t.Errorf("result = exit %d, %v; want 1 and the timeout message", res.ExitCode(), res.Err)
		}
	})
}

// unbuiltRequest is a request of a kind that has no planner yet.
type unbuiltRequest struct {
	Common
	Target
}

func (unbuiltRequest) kind() Kind { return ClusterUpgrade }

func (unbuiltRequest) differs(Request) string { return "" }

func (r unbuiltRequest) withExpect(p *Plan) Request { r.Expect = p; return r }

// The errors of Run: a request that is invalid, a name that does not
// exist, a cluster that does not exist, and a kind that is not built.
func TestRun_Errors(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{"an invalid request", AddonUpdateRequest{Target: Target{Cluster: "prod"}}, "name at least one add-on"},
		{"an add-on that is not installed", request("vpc-cn"), `add-on "vpc-cn" not found in cluster prod`},
		{"a skipped add-on that is not installed", AddonUpdateRequest{Target: Target{Cluster: "prod"}, All: true, Skip: []string{"corends"}}, `add-on "corends" not found`},
		{"a version that the cluster cannot run", AddonUpdateRequest{Target: Target{Cluster: "prod"}, Addons: []string{"vpc-cni"}, Version: cniNext}, "is not compatible with Kubernetes 1.33"},
		{"a kind that is not built", unbuiltRequest{Target: Target{Cluster: "prod"}}, "this build cannot run a ClusterUpgrade change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Run(testContext(t), e.deps, tt.req, Hooks{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if res.Plan != nil || res.ExitCode() != 1 || res.Document() != nil {
				t.Errorf("result = %+v, want no plan and exit 1", res)
			}
			if n := e.count(fleettest.EKSUpdateAddon); n != 0 {
				t.Errorf("%d UpdateAddon requests, want none", n)
			}
			if _, perr := NewPlan(testContext(t), e.deps, tt.req); perr == nil || perr.Error() != err.Error() {
				t.Errorf("NewPlan error = %v, want the error of Run", perr)
			}
		})
	}
	t.Run("a cluster that does not exist", func(t *testing.T) {
		req := request("vpc-cni")
		req.Cluster = "prd"
		_, err := Run(testContext(t), e.deps, req, Hooks{})
		var read *ClusterReadError
		if !errors.As(err, &read) || read.Cluster != "prd" || read.Region != east {
			t.Fatalf("error = %v, want a *ClusterReadError for prd in %s", err, east)
		}
		var unsupported *UnsupportedError
		if errors.As(err, &unsupported) {
			t.Error("the error is also an *UnsupportedError")
		}
	})
}

// NewPlan and Run build the same plan, and NewPlan sends nothing.
func TestNewPlan(t *testing.T) {
	e := newEnv(t, newWorld(func(c *fleettest.Cluster) {
		c.Addon("coredns").Status = fleettest.StatusDegraded
		c.Addons = append(c.Addons, &fleettest.Addon{Name: "my-addon", Version: "v1.0.0"})
	}))
	req := request()
	req.Addons, req.All, req.Skip = nil, true, []string{"kube-proxy"}
	p, err := NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	want := []Step{
		{Ref: StepRef{Phase: addonPhase, Kind: StepAddon, Name: "my-addon"}, Action: Manual, From: "v1.0.0"},
		addonStep("vpc-cni", Submit, VersionBehind, cniOld, cniNew),
		addonStep("coredns", Submit, VersionBehind, dnsOld, dnsNew),
	}
	cp := &p.Clusters[0]
	if diff := cmp.Diff(want, cp.Steps()); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"EKS publishes no version of add-on my-addon for Kubernetes 1.33; not updated"}, cp.Notices); diff != "" {
		t.Errorf("notices (-want +got):\n%s", diff)
	}
	if p.Decision() != Ask || p.ExitCode() != 2 || cp.Gate.Decision != health.Ask {
		t.Errorf("decision = %s, exit %d, gate %s; want Ask, 2, Ask", p.Decision(), p.ExitCode(), cp.Gate.Decision)
	}
	doc := p.Document()
	if doc.Operation != apidoc.OperationAddonUpdate || doc.Clusters[0].Version != "1.33" || doc.Clusters[0].Gate == nil || len(doc.Clusters[0].Steps) != 3 {
		t.Errorf("document = %+v", doc)
	}
	for _, r := range e.srv.Requests() {
		if r.Mutates {
			t.Errorf("NewPlan sent %s", r.Operation)
		}
	}
	// One catalogue read for each add-on of --all, in the wave of the
	// describe calls.
	if n := e.count(fleettest.EKSDescribeAddonVersions); n != 4 {
		t.Errorf("%d DescribeAddonVersions requests, want 4", n)
	}

	// The checks are skipped with a notice, and the plan has no gate.
	req.Policy.Skip = true
	p, err = NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Clusters[0]; got.Decision() != Proceed || len(got.Checks) != 0 || !contains(got.Notices, "the health checks were skipped (--skip-health-check)") {
		t.Errorf("with the checks skipped: decision %s, checks %v, notices %v", got.Decision(), got.Checks, got.Notices)
	}
	if p.Document().Clusters[0].Gate != nil {
		t.Error("the document has a gate that did not run")
	}
}

// A read of the plan that fails blocks the plan: nothing shows that no
// change is running, or which version to move to.
func TestNewPlan_Unread(t *testing.T) {
	t.Run("an add-on that is not a target", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "coredns"}, fleettest.Fault{Deny: true})
		p, err := NewPlan(testContext(t), e.deps, request("vpc-cni"))
		if err != nil {
			t.Fatal(err)
		}
		cp := p.Clusters[0]
		if p.Decision() != DecisionBlocked || p.ExitCode() != 4 || len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnread {
			t.Errorf("decision %s, exit %d, blockers %+v; want Blocked, 4, one Unread", p.Decision(), p.ExitCode(), cp.Blockers)
		}
		res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{})
		if err != nil || res.Status != Refused || res.ExitCode() != 4 {
			t.Errorf("run = %s, exit %d, %v; want Refused, 4", res.Status, res.ExitCode(), err)
		}
		if _, ok := res.Document().(apidoc.Plan); !ok {
			t.Errorf("document = %T, want a Plan", res.Document())
		}
	})
	t.Run("the target", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "vpc-cni"}, fleettest.Fault{Deny: true})
		p, err := NewPlan(testContext(t), e.deps, request("vpc-cni"))
		if err != nil {
			t.Fatal(err)
		}
		cp := p.Clusters[0]
		if got := cp.Steps()[0].Action; got != Blocked {
			t.Errorf("step action = %s, want Blocked", got)
		}
		if p.ExitCode() != 4 || len(cp.Blockers) != 2 || cp.Blockers[0].Step == nil {
			t.Errorf("exit %d, blockers %+v; want 4, and a blocker for the step", p.ExitCode(), cp.Blockers)
		}
		if doc := p.Document(); doc.Clusters[0].Blockers[0].Step == nil || doc.Clusters[0].Blockers[0].Step.Name != "vpc-cni" {
			t.Errorf("document blockers = %+v", doc.Clusters[0].Blockers)
		}
	})
	t.Run("the latest version of the target", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		e.world.SetFault(fleettest.EKSDescribeAddonVersions, fleettest.Fault{Deny: true})
		p, err := NewPlan(testContext(t), e.deps, request("vpc-cni"))
		if err != nil {
			t.Fatal(err)
		}
		cp := p.Clusters[0]
		if got := cp.Steps()[0].Action; got != Blocked || p.ExitCode() != 4 || cp.Blockers[0].Kind != BlockerUnread {
			t.Errorf("action %s, exit %d, blockers %+v", got, p.ExitCode(), cp.Blockers)
		}
		// The same for a version that the user named.
		req := request("vpc-cni")
		req.Version = cniNew
		p, err = NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Clusters[0].Steps()[0].Action; got != Blocked || p.ExitCode() != 4 {
			t.Errorf("pinned: action %s, exit %d", got, p.ExitCode())
		}
	})
	t.Run("the add-on list", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		e.world.SetFault(fleettest.EKSListAddons, fleettest.Fault{Deny: true})
		p, err := NewPlan(testContext(t), e.deps, request("vpc-cni"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Decision() != DecisionBlocked || p.ExitCode() != 4 {
			t.Errorf("decision %s, exit %d; want Blocked, 4", p.Decision(), p.ExitCode())
		}
	})
}

// An add-on that is updated and not healthy, a version that did not
// change, and a read after the update that fails.
func TestRun_Confirm(t *testing.T) {
	tests := []struct {
		name   string
		script fleettest.UpdateScript
		// fault is set when the update was accepted.
		fault  *fleettest.Fault
		status StepStatus
		exit   int
		run    RunStatus
		check  func(t *testing.T, res Result)
	}{
		{
			name:   "not ACTIVE after the update",
			script: fleettest.UpdateScript{ResourceStatus: fleettest.StatusDegraded},
			status: StepSucceeded, exit: 5, run: VerifyFailed,
			check: func(t *testing.T, res Result) {
				t.Helper()
				if got := res.Clusters[0].Steps[0].VerifyIssue; got != "add-on vpc-cni ended in status DEGRADED (expected ACTIVE)" {
					t.Errorf("issue = %q", got)
				}
			},
		},
		{
			name:   "ACTIVE with a health issue",
			script: fleettest.UpdateScript{ResourceIssues: []fleettest.Issue{{Code: "ConfigurationConflict", Message: "a field is managed twice"}}},
			status: StepSucceeded, exit: 5, run: VerifyFailed,
			check: func(t *testing.T, res Result) {
				t.Helper()
				want := "add-on vpc-cni is ACTIVE but has 1 health issue(s): ConfigurationConflict: a field is managed twice"
				if got := res.Clusters[0].Steps[0].VerifyIssue; got != want {
					t.Errorf("issue = %q", got)
				}
				if v := res.Clusters[0].Verification; len(v) != 1 || v[0].Outcome != health.Fail || v[0].Name != addonCheck+"/vpc-cni" {
					t.Errorf("verification = %+v", v)
				}
			},
		},
		{
			name:  "the read after the update fails",
			fault: &fleettest.Fault{Deny: true}, status: StepSucceeded, exit: 4, run: Incomplete,
			check: func(t *testing.T, res Result) {
				t.Helper()
				if len(res.Failures) != 1 || res.Failures[0].Reason != apidoc.ReasonAccessDenied || res.Clusters[0].Steps[0].Failure != nil {
					t.Errorf("failures = %+v, step failure %+v", res.Failures, res.Clusters[0].Steps[0].Failure)
				}
				if v := res.Clusters[0].Verification; len(v) != 1 || v[0].Outcome != health.Unreadable {
					t.Errorf("verification = %+v", v)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{tt.script} }))
			if tt.fault != nil {
				e.onRequest(fleettest.EKSDescribeUpdate, func(int) { e.world.SetFault(fleettest.EKSDescribeAddon, *tt.fault) })
			}
			res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if got := statuses(res)["vpc-cni"]; got != tt.status || res.ExitCode() != tt.exit || res.Status != tt.run {
				t.Errorf("step %s, exit %d, run %s; want %s, %d, %s", got, res.ExitCode(), res.Status, tt.status, tt.exit, tt.run)
			}
			tt.check(t, res)
		})
	}
}
