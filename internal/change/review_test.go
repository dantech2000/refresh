package change

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// mutations returns the operations of the mutating requests that the
// fake got.
func (e *env) mutations() []string {
	var out []string
	for _, r := range e.srv.Requests() {
		if r.Mutates {
			out = append(out, string(r.Operation))
		}
	}
	return out
}

// Consent is positive: a run with no --yes, no reviewed plan, and nobody
// to ask sends nothing, for both kinds of change.
func TestRun_NoConsentSendsNothing(t *testing.T) {
	scale := scaleTo(3)
	scale.Policy.Yes = false
	tests := []struct {
		name  string
		req   Request
		hooks Hooks
	}{
		{"an add-on update with no hooks", request("vpc-cni"), Hooks{}},
		{"an add-on update with events alone", request("vpc-cni"), Hooks{Event: func(Event) {}}},
		{"a scale with no hooks", scale, Hooks{}},
		{"a scale with events alone", scale, Hooks{Event: func(Event) {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(nil))
			res, err := Run(testContext(t), e.deps, tt.req, tt.hooks)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.mutations(); len(got) != 0 {
				t.Errorf("a run that nobody confirmed sent %v", got)
			}
			if res.Status != Refused || res.ExitCode() != 1 {
				t.Errorf("result = %s, exit %d; want Refused, 1", res.Status, res.ExitCode())
			}
			if res.Err == nil || !strings.Contains(res.Err.Error(), "nobody confirmed the change and nobody can be asked; pass --yes; nothing was changed") {
				t.Errorf("message = %v", res.Err)
			}
			for name, status := range statuses(res) {
				if status != StepNotAttempted {
					t.Errorf("step %s is %s, want NotAttempted", name, status)
				}
			}
			if _, ok := res.Document().(apidoc.Plan); !ok {
				t.Errorf("document = %T, want the plan", res.Document())
			}
		})
	}
}

// A reviewed plan that the current plan matches is consent, also with
// nobody to ask.
func TestRun_ExpectIsConsent(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	req := request("vpc-cni")
	reviewed, err := NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Expect = reviewed
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil || res.Status != Succeeded {
		t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
	}
	if diff := cmp.Diff([]string{"eks:UpdateAddon"}, e.mutations()); diff != "" {
		t.Errorf("mutations (-want +got):\n%s", diff)
	}
}

// stepFieldRows has one row for each field of Step and of ScaleSizes: a
// change of that field alone, in a step with work. A field without a row
// fails TestDiff_EveryStepField, so a new field cannot go uncompared.
var stepFieldRows = map[string]func(s *Step){
	"Ref":       func(s *Step) { s.Ref.Name += "-2" },
	"Action":    func(s *Step) { s.Action = Verify },
	"Reason":    func(s *Step) { s.Reason = Repair },
	"From":      func(s *Step) { s.From += "-other" },
	"To":        func(s *Step) { s.To += "-other" },
	"ToRelease": func(s *Step) { s.ToRelease += "-other" },
	"Force":     func(s *Step) { s.Force = !s.Force },
	"Sizes": func(s *Step) {
		if s.Sizes == nil {
			s.Sizes = &ScaleSizes{}
			return
		}
		s.Sizes = nil
	},
	"Sizes.Before": func(s *Step) {
		sizes := ScaleSizes{}
		if s.Sizes != nil {
			sizes = *s.Sizes
		}
		sizes.Before.Max += 4
		s.Sizes = &sizes
	},
	"Sizes.After": func(s *Step) {
		sizes := ScaleSizes{}
		if s.Sizes != nil {
			sizes = *s.Sizes
		}
		sizes.After.Desired--
		s.Sizes = &sizes
	},
}

// Diff reports a change of any one field of a step, for every kind of
// step that a plan has, and none of them goes on without an answer.
func TestDiff_EveryStepField(t *testing.T) {
	var fields []string
	for f := range reflect.TypeFor[Step]().Fields() {
		fields = append(fields, f.Name)
	}
	for f := range reflect.TypeFor[ScaleSizes]().Fields() {
		fields = append(fields, "Sizes."+f.Name)
	}
	for _, name := range fields {
		if stepFieldRows[name] == nil {
			t.Errorf("the field %s has no row: add one, and compare the field in diffStep", name)
		}
	}
	if len(stepFieldRows) != len(fields) {
		t.Errorf("%d rows for %d fields: a row names a field that is gone", len(stepFieldRows), len(fields))
	}
	steps := map[string]Step{
		"add-on": addonStep("vpc-cni", Submit, VersionBehind, "v1", "v2"),
		"scale": {
			Ref: StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseScale}, Kind: StepScale, Name: "web"}, Action: Submit,
			Sizes: &ScaleSizes{Before: Sizes{Desired: 2, Min: 1, Max: 3}, After: Sizes{Desired: 3, Min: 1, Max: 3}},
		},
	}
	kinds := map[string]Kind{"add-on": AddonUpdate, "scale": NodegroupScale}
	for kind, base := range steps {
		if got := Diff(planOf(kinds[kind], base), planOf(kinds[kind], base)); len(got) != 0 {
			t.Errorf("%s: the same step differs: %v", kind, got)
		}
		for field, change := range stepFieldRows {
			t.Run(kind+"/"+field, func(t *testing.T) {
				cur := base
				change(&cur)
				got := Diff(planOf(kinds[kind], base), planOf(kinds[kind], cur))
				if len(got) == 0 {
					t.Fatalf("Diff reports nothing for a step whose %s changed", field)
				}
				if worstEffect(got) < AskAgain {
					t.Errorf("a changed %s goes on without an answer: %v", field, got)
				}
			})
		}
	}
}

// The rows of Diff that the field table does not name: the goal of a
// scale, and a step that sends its own change where it waited before.
func TestDiff_GoalAndWidening(t *testing.T) {
	scale := Step{
		Ref: StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseScale}, Kind: StepScale, Name: "web"}, Action: Submit,
		Sizes: &ScaleSizes{Before: Sizes{Desired: 2, Min: 1, Max: 3}, After: Sizes{Desired: 3, Min: 1, Max: 3}},
	}
	down := scale
	down.Sizes = &ScaleSizes{Before: scale.Sizes.Before, After: Sizes{Desired: 1, Min: 1, Max: 3}}
	got := Diff(planOf(NodegroupScale, scale), planOf(NodegroupScale, down))
	want := []Change{{Step: scale.Ref, Kind: SizesChanged, Message: "nodegroup web now goes to desired 1, min 1, max 3, not desired 3, min 1, max 3"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("a changed goal (-want +got):\n%s", diff)
	}

	attach := addonStep("vpc-cni", Attach, InFlight, "v2", "v2")
	submit := addonStep("vpc-cni", Submit, Repair, "v2", "v2")
	got = Diff(planOf(AddonUpdate, attach), planOf(AddonUpdate, submit))
	want = []Change{{Step: attach.Ref, Kind: StepDiffers, Message: "add-on vpc-cni is now changed by this run, not waited for"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("attach to submit (-want +got):\n%s", diff)
	}
	// A refusal wins over the question of a changed goal.
	blocked := []Change{{Kind: NowBlocked}, {Kind: SizesChanged}}
	if worstEffect(blocked) != Refuse {
		t.Error("a refusal does not win over a question")
	}
}

// requestFieldRows has one row for each field of a request that can
// change what is submitted: a request that differs in that field alone.
// ignoredRequestFields are the fields that cannot. A field in neither
// fails TestSameRequest.
var (
	requestFieldRows = map[string]func(a *AddonUpdateRequest, s *ScaleRequest){
		"Target.Region":  func(a *AddonUpdateRequest, s *ScaleRequest) { a.Region, s.Region = "eu-west-1", "eu-west-1" },
		"Target.Cluster": func(a *AddonUpdateRequest, s *ScaleRequest) { a.Cluster, s.Cluster = "dev", "dev" },
		"Policy.Yes":     func(a *AddonUpdateRequest, s *ScaleRequest) { a.Policy.Yes, s.Policy.Yes = true, true },
		"Policy.RequireHealthy": func(a *AddonUpdateRequest, s *ScaleRequest) {
			a.Policy.RequireHealthy, s.Policy.RequireHealthy = true, true
		},
		"Policy.Force": func(a *AddonUpdateRequest, s *ScaleRequest) { a.Policy.Force, s.Policy.Force = true, true },
		"Policy.Skip":  func(a *AddonUpdateRequest, s *ScaleRequest) { a.Policy.Skip, s.Policy.Skip = true, true },
		"Addons":       func(a *AddonUpdateRequest, _ *ScaleRequest) { a.Addons = []string{"vpc-cni", "coredns"} },
		"All":          func(a *AddonUpdateRequest, _ *ScaleRequest) { a.All = true },
		"Version":      func(a *AddonUpdateRequest, _ *ScaleRequest) { a.Version = cniNew },
		"Skip":         func(a *AddonUpdateRequest, _ *ScaleRequest) { a.Skip = []string{"coredns"} },
		"Nodegroup":    func(_ *AddonUpdateRequest, s *ScaleRequest) { s.Nodegroup = "batch" },
		"Desired":      func(_ *AddonUpdateRequest, s *ScaleRequest) { s.Desired = size(1) },
		"Min":          func(_ *AddonUpdateRequest, s *ScaleRequest) { s.Min = size(0) },
		"Max":          func(_ *AddonUpdateRequest, s *ScaleRequest) { s.Max = size(9) },
	}
	ignoredRequestFields = []string{"Common.NoWait", "Common.Limits", "Common.Expect"}
)

// A reviewed plan counts only for its own request: every field that can
// change what is submitted is compared, and a new field needs a row.
func TestSameRequest(t *testing.T) {
	var fields []string
	add := func(prefix string, typ reflect.Type, skip ...string) {
		for f := range typ.Fields() {
			if !slices.Contains(skip, f.Name) {
				fields = append(fields, prefix+f.Name)
			}
		}
	}
	add("", reflect.TypeFor[AddonUpdateRequest](), "Common", "Target")
	add("", reflect.TypeFor[ScaleRequest](), "Common", "Target")
	add("Target.", reflect.TypeFor[Target]())
	add("Policy.", reflect.TypeFor[health.Policy]())
	add("Common.", reflect.TypeFor[Common](), "Policy")
	for _, name := range fields {
		if requestFieldRows[name] == nil && !slices.Contains(ignoredRequestFields, name) {
			t.Errorf("the request field %s has no row: compare it in differs, or list it as one that cannot change what is submitted", name)
		}
	}
	if len(requestFieldRows)+len(ignoredRequestFields) != len(fields) {
		t.Errorf("%d rows and %d ignored fields for %d fields", len(requestFieldRows), len(ignoredRequestFields), len(fields))
	}

	addons := AddonUpdateRequest{Target: Target{Region: east, Cluster: "prod"}, Addons: []string{"vpc-cni"}}
	scale := ScaleRequest{Target: Target{Region: east, Cluster: "prod"}, Nodegroup: "web", Desired: size(3)}
	reviewed := func(req Request) *Plan { return &Plan{Kind: req.kind(), Request: req} }
	if err := sameRequest(reviewed(addons), addons); err != nil {
		t.Errorf("the same add-on request: %v", err)
	}
	// Equal sizes behind other pointers are the same request.
	twin := scale
	twin.Desired = size(3)
	if err := sameRequest(reviewed(scale), twin); err != nil {
		t.Errorf("the same scale request: %v", err)
	}
	for field, change := range requestFieldRows {
		a, s := addons, scale
		change(&a, &s)
		for _, pair := range [][2]Request{{addons, a}, {scale, s}} {
			if reflect.DeepEqual(pair[0], pair[1]) {
				continue
			}
			if err := sameRequest(reviewed(pair[0]), pair[1]); !errors.Is(err, ErrOtherRequest) {
				t.Errorf("%T with another %s: error = %v, want ErrOtherRequest", pair[1], field, err)
			}
		}
	}
	// The fields that cannot change what is submitted.
	late := addons
	late.NoWait, late.Limits, late.Expect = true, Limits{Wait: time.Hour}, reviewed(addons)
	if err := sameRequest(reviewed(addons), late); err != nil {
		t.Errorf("other limits: %v", err)
	}
	// Another kind of change, and a plan with no request.
	if err := sameRequest(reviewed(addons), scale); !errors.Is(err, ErrOtherRequest) {
		t.Errorf("a plan of another kind: error = %v", err)
	}
	if err := sameRequest(&Plan{Kind: NodegroupScale}, scale); !errors.Is(err, ErrOtherRequest) {
		t.Errorf("a plan with no request: error = %v", err)
	}
}

// A plan that was reviewed for a scale-up is not consent to a scale-down:
// Run returns an error before it reads anything, and sends nothing.
func TestRun_ExpectOfAnotherRequest(t *testing.T) {
	e := newEnv(t, scaleWorld(nil))
	up := scaleTo(3)
	up.Policy.Yes = false
	reviewed, err := NewPlan(testContext(t), e.deps, up)
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewed.Clusters[0].Steps()[0].Sizes; got.Before.Desired != 2 || got.After.Desired != 3 {
		t.Fatalf("the reviewed plan is %+v, want 2 to 3", got)
	}
	read := len(e.srv.Requests())
	for _, tt := range []struct {
		name string
		edit func(req *ScaleRequest)
	}{
		{"a scale-down", func(req *ScaleRequest) { req.Desired = size(1) }},
		{"the same sizes with --yes", func(req *ScaleRequest) { req.Policy.Yes = true }},
		{"the same sizes with --force", func(req *ScaleRequest) { req.Policy.Force = true }},
		{"the same sizes with --skip-health-check", func(req *ScaleRequest) { req.Policy.Skip = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := up
			tt.edit(&req)
			req.Expect = reviewed
			s := &script{t: t}
			res, err := Run(testContext(t), e.deps, req, s.hooks())
			if !errors.Is(err, ErrOtherRequest) {
				t.Fatalf("error = %v, want ErrOtherRequest", err)
			}
			if n := len(e.srv.Requests()); n != read {
				t.Errorf("the run made %d requests, want none: %v", n-read, e.operations(read))
			}
			if res.ExitCode() != 1 || res.Status != Failed || res.Plan != nil || len(s.asked) != 0 {
				t.Errorf("result = %s, exit %d, plan %v, questions %v; want Failed, 1, no plan, no question", res.Status, res.ExitCode(), res.Plan, s.kinds())
			}
			if got := e.web(); got != "ACTIVE 2/1/3" {
				t.Errorf("the nodegroup is %s, want it untouched", got)
			}
		})
	}
}

// scaleDirections returns a request for each direction of a scale.
func scaleDirections() map[string]ScaleRequest {
	return map[string]ScaleRequest{
		"a scale-up":       scaleTo(3),
		"a scale-down":     scaleTo(1),
		"the bounds alone": {Common: scaleTo(3).Common, Target: Target{Cluster: "prod"}, Nodegroup: "web", Min: size(2), Max: size(6)},
	}
}

// A target that is not ACTIVE blocks a scale in every direction, in the
// plan, whatever the flags.
func TestRunScale_TargetNotActive(t *testing.T) {
	for name, req := range scaleDirections() {
		for _, status := range []fleettest.ResourceStatus{fleettest.StatusDegraded, "CREATE_FAILED", "DELETE_FAILED"} {
			t.Run(name+"/in the plan/"+string(status), func(t *testing.T) {
				e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").Status = status }))
				e.withKube(t)
				for _, skip := range []bool{false, true} {
					req.Policy.Skip = skip
					res, err := Run(testContext(t), e.deps, req, Hooks{})
					if err != nil {
						t.Fatal(err)
					}
					if got := e.mutations(); len(got) != 0 {
						t.Errorf("skip %v: the run sent %v", skip, got)
					}
					want := "nodegroup web is " + string(status) + ", not ACTIVE; it was not scaled"
					if res.Status != Refused || res.ExitCode() != 3 || res.Err == nil || res.Err.Error() != want {
						t.Errorf("skip %v: result = %s, exit %d, %v; want Refused, 3, %q", skip, res.Status, res.ExitCode(), res.Err, want)
					}
					cp := res.Plan.Clusters[0]
					if cp.Steps()[0].Action != Blocked || len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerNotReady || cp.Blockers[0].Step == nil {
						t.Errorf("skip %v: step %s, blockers %+v; want Blocked with one NotReady blocker of the step", skip, cp.Steps()[0].Action, cp.Blockers)
					}
					if res.Plan.ExitCode() != 3 {
						t.Errorf("skip %v: the dry run exits %d, want 3", skip, res.Plan.ExitCode())
					}
				}
			})
		}
	}
}

// The same at the fresh read: a target that stops being ACTIVE between
// the gate and the request is sent nothing.
func TestRunScale_TargetNotActiveAtFreshRead(t *testing.T) {
	for name, req := range scaleDirections() {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(nil))
			e.withKube(t)
			// The first describe of the nodegroup is the one of the plan.
			e.onRequest(fleettest.EKSDescribeNodegroup, func(n int) {
				if n == 2 {
					e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("web").Status = fleettest.StatusDegraded })
				}
			})
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if got := e.mutations(); len(got) != 0 {
				t.Errorf("the run sent %v", got)
			}
			s := res.Clusters[0].Steps[0]
			if s.Status != StepBlocked || s.Failure == nil || s.Failure.Error != "nodegroup web is DEGRADED, not ACTIVE; it was not scaled" {
				t.Errorf("step = %s, failure %+v; want Blocked with the status", s.Status, s.Failure)
			}
			if res.ExitCode() != 3 || res.Status != RunBlocked {
				t.Errorf("result = %s, exit %d; want Blocked, 3", res.Status, res.ExitCode())
			}
		})
	}
}

// front puts a server before the fake. answer returns the body to send
// for a request, or "" to pass the request on. It is how a test gets an
// answer that EKS can give and the fake never does.
func (e *env) front(t *testing.T, answer func(r *http.Request) string) Deps {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body := answer(r); body != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
			return
		}
		e.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg := e.srv.Config(east)
	cfg.BaseEndpoint = aws.String(srv.URL)
	deps := depsFor(e.world, cfg)
	deps.Kube = e.deps.Kube
	return deps
}

// describeWeb answers the describe of the nodegroup web with number n,
// from 1, with body.
func describeWeb(n int32, body string) func(r *http.Request) string {
	var seen atomic.Int32
	return func(r *http.Request) string {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/clusters/prod/node-groups/web") && seen.Add(1) == n {
			return body
		}
		return ""
	}
}

// Answers of DescribeNodegroup that lack the scaling sizes.
const (
	webNoScaling = `{"nodegroup":{"nodegroupName":"web","clusterName":"prod","status":"ACTIVE","version":"1.33"}}`
	webNoMin     = `{"nodegroup":{"nodegroupName":"web","clusterName":"prod","status":"ACTIVE","version":"1.33","scalingConfig":{"desiredSize":2,"maxSize":3}}}`
)

// A size that EKS did not return is not 0: it confirms nothing.
func TestDescribe_SizesMissing(t *testing.T) {
	for name, body := range map[string]string{"no scaling config": webNoScaling, "no min size": webNoMin} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(nil))
			deps := e.front(t, describeWeb(1, body))
			clients := deps.AWS.Clients("")
			_, err := describe(testContext(t), clients, Resource{Target: prod, Kind: StepScale, Name: "web"})
			if !errors.Is(err, errNoSizes) {
				t.Errorf("a scale read: error = %v, want errNoSizes", err)
			}
			// A roll reads no size.
			deps = e.front(t, describeWeb(1, body))
			got, err := describe(testContext(t), deps.AWS.Clients(""), Resource{Target: prod, Kind: StepNodegroup, Name: "web"})
			if err != nil || got.Status != "ACTIVE" {
				t.Errorf("a roll read: %+v, %v; want ACTIVE and no error", got, err)
			}
		})
	}
}

// The three places that read sizes: the plan, the fresh read before the
// request, and the wait after it. None takes a missing size for 0.
func TestRunScale_SizesMissing(t *testing.T) {
	zero := func() ScaleRequest {
		req := scaleTo(0)
		req.Min = size(0)
		req.Policy.Skip = true
		return req
	}
	t.Run("the plan: --desired 0 is not what the nodegroup has", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		deps := e.front(t, describeWeb(1, webNoScaling))
		res, err := Run(testContext(t), deps, zero(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.mutations(); len(got) != 0 {
			t.Errorf("the run sent %v", got)
		}
		if res.Status != Refused || res.ExitCode() != 4 {
			t.Errorf("result = %s, exit %d (%v); want Refused, 4", res.Status, res.ExitCode(), res.Err)
		}
		cp := res.Plan.Clusters[0]
		if cp.Steps()[0].Action != Blocked || len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnread ||
			cp.Blockers[0].Message != "EKS did not return every scaling size of nodegroup web" {
			t.Errorf("step %s, blockers %+v; want Blocked and one Unread blocker", cp.Steps()[0].Action, cp.Blockers)
		}
	})
	t.Run("the fresh read: --min 0 is not confirmed by a missing min", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		deps := e.front(t, describeWeb(2, webNoMin))
		req := ScaleRequest{Common: scaleTo(3).Common, Target: Target{Cluster: "prod"}, Nodegroup: "web", Min: size(0)}
		res, err := Run(testContext(t), deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.mutations(); len(got) != 0 {
			t.Errorf("the run sent %v", got)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepFailed || s.Failure == nil || !strings.Contains(s.Failure.Error, errNoSizes.Error()) || res.ExitCode() != 1 {
			t.Errorf("step = %s, failure %+v, exit %d; want Failed as a read that failed, 1", s.Status, s.Failure, res.ExitCode())
		}
		if got := e.web(); got != "ACTIVE 2/1/3" {
			t.Errorf("the nodegroup is %s, want it untouched", got)
		}
	})
	t.Run("the wait: --desired 0 is not confirmed by a missing scaling config", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		// The plan, the fresh read, and then the first poll after the
		// update.
		deps := e.front(t, describeWeb(3, webNoScaling))
		s := &script{t: t}
		res, err := Run(testContext(t), deps, zero(), s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Succeeded || res.ExitCode() != 0 {
			t.Fatalf("result = %s, exit %d (%v); want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		// The answer with no sizes is a poll that says nothing, and the
		// next poll confirms the sizes.
		if n := e.count(fleettest.EKSDescribeNodegroup); n != 3 {
			t.Errorf("the fake got %d DescribeNodegroup requests, want 3: the plan, the fresh read, and the poll after the one with no sizes", n)
		}
		if got := e.web(); got != "ACTIVE 0/0/3" {
			t.Errorf("the nodegroup is %s, want ACTIVE 0/0/3", got)
		}
	})
}

// pollTimes records when each poll of an update arrives, as the time
// since the request that started the update.
func pollTimes(e *env, submit fleettest.Operation) *[]time.Duration {
	var (
		started time.Time
		times   []time.Duration
	)
	e.srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
		switch r.Operation {
		case submit:
			started = e.world.Now()
		case fleettest.EKSDescribeUpdate:
			times = append(times, e.world.Now().Sub(started))
		}
	}
	return &times
}

// The wait budget and the pause between two polls: the last pause is cut
// to the time that is left, the last poll is due at the deadline, and its
// answer counts.
func TestRun_WaitBudgetAndPause(t *testing.T) {
	run := func(t *testing.T, script fleettest.UpdateScript, wait, poll time.Duration) (Result, []time.Duration, *env) {
		t.Helper()
		e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{script} }))
		times := pollTimes(e, fleettest.EKSUpdateAddon)
		req := yes(request("vpc-cni"))
		req.Limits.Wait, req.Limits.Poll = wait, poll
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		return res, *times, e
	}
	t.Run("Successful arrives exactly at the deadline: the change has finished", func(t *testing.T) {
		res, times, _ := run(t, fleettest.UpdateScript{After: 10 * time.Second}, 15*time.Second, 15*time.Second)
		if diff := cmp.Diff([]time.Duration{15 * time.Second}, times); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
		if got := statuses(res)["vpc-cni"]; got != StepSucceeded || res.Status != Succeeded || res.ExitCode() != 0 {
			t.Errorf("step %s, run %s, exit %d; want Succeeded, Succeeded, 0", got, res.Status, res.ExitCode())
		}
	})
	t.Run("Failed arrives exactly at the deadline: the change has finished", func(t *testing.T) {
		res, _, _ := run(t, fleettest.UpdateScript{After: 10 * time.Second, Result: fleettest.UpdateFailed}, 15*time.Second, 15*time.Second)
		s := res.Clusters[0].Steps[0]
		if s.Status != StepFailed || s.Failure == nil || s.Failure.Reason != apidoc.ReasonUpdateFailed || res.ExitCode() != 1 {
			t.Errorf("step %s, failure %+v, exit %d; want Failed with UpdateFailed, 1", s.Status, s.Failure, res.ExitCode())
		}
	})
	t.Run("the deadline passes during a pause: one poll at the deadline, and none after it", func(t *testing.T) {
		res, times, _ := run(t, fleettest.UpdateScript{Never: true}, 10*time.Second, 15*time.Second)
		if diff := cmp.Diff([]time.Duration{10 * time.Second}, times); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonTimeout {
			t.Errorf("step %s, failure %+v; want InProgress with Timeout", s.Status, s.Failure)
		}
		if res.ExitCode() != 4 || res.Status != Incomplete {
			t.Errorf("result = %s, exit %d; want Incomplete, 4", res.Status, res.ExitCode())
		}
	})
	t.Run("a budget of 10s with an interval of 15s polls at 10s", func(t *testing.T) {
		res, times, _ := run(t, fleettest.UpdateScript{After: 5 * time.Second}, 10*time.Second, 15*time.Second)
		if diff := cmp.Diff([]time.Duration{10 * time.Second}, times); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
		if res.Status != Succeeded || res.ExitCode() != 0 {
			t.Errorf("result = %s, exit %d; want Succeeded, 0", res.Status, res.ExitCode())
		}
	})
	t.Run("a budget of 40s with an interval of 15s polls at 15s, 30s, and 40s", func(t *testing.T) {
		_, times, _ := run(t, fleettest.UpdateScript{Never: true}, 40*time.Second, 15*time.Second)
		if diff := cmp.Diff([]time.Duration{15 * time.Second, 30 * time.Second, 40 * time.Second}, times); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
	})
	t.Run("a scale whose update ends at the deadline is still confirmed", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		req := scaleTo(3)
		req.Limits.Wait, req.Limits.Poll = 15*time.Second, 15*time.Second
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Succeeded || res.ExitCode() != 0 || e.web() != "ACTIVE 3/1/3" {
			t.Errorf("result = %s, exit %d, nodegroup %s; want Succeeded, 0, ACTIVE 3/1/3", res.Status, res.ExitCode(), e.web())
		}
	})
}

// degrade makes vpc-cni DEGRADED when its fresh read arrives: the
// describe of an add-on with the number n.
func degrade(e *env, n int) {
	e.onRequest(fleettest.EKSDescribeAddon, func(i int) {
		if i == n {
			e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Status = fleettest.StatusDegraded })
		}
	})
}

// worse is what a run says about vpc-cni when degrade ran.
const worse = "the health checks got worse since the plan (Addons: vpc-cni is DEGRADED; the update will try to repair it)"

// The fresh read of an add-on before its request applies the policy of
// the run to the health that it finds: an add-on that was ACTIVE in the
// plan and is DEGRADED now is a warning that nobody accepted.
func TestRun_AddonDegradesBeforeSubmit(t *testing.T) {
	t.Run("--require-healthy blocks it", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		degrade(e, 4)
		req := yes(request("vpc-cni", "coredns"))
		req.Policy.RequireHealthy = true
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		// The add-on that is blocked does not stop the next one.
		want := map[string]StepStatus{"vpc-cni": StepBlocked, "coredns": StepSucceeded}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		for _, r := range e.srv.Requests() {
			if r.Operation == fleettest.EKSUpdateAddon && r.Input["addonName"] == "vpc-cni" {
				t.Error("an update was sent for the add-on that --require-healthy blocks")
			}
		}
		f := res.Clusters[0].Steps[0].Failure
		if f == nil || !strings.Contains(f.Error, "--require-healthy is set") || !strings.Contains(f.Error, "add-on vpc-cni was not changed") {
			t.Errorf("failure = %+v", f)
		}
		if res.ExitCode() != 3 {
			t.Errorf("exit = %d, want 3", res.ExitCode())
		}
	})
	t.Run("--yes goes on and records it", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		degrade(e, 4)
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
		if !contains(s.events, "Notice "+worse+"; going on with --yes") {
			t.Errorf("no notice about the warning that --yes went past in %v", s.events)
		}
	})
}

// The same warning without --yes: the run asks, or fails the step when
// it cannot ask.
func TestRun_AddonDegradesBeforeSubmit_Question(t *testing.T) {
	t.Run("a person is asked: yes", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		// Two plans of three describes each, around the first question.
		degrade(e, 7)
		s := &script{t: t, answers: []bool{true, true}}
		res, err := Run(testContext(t), e.deps, request("vpc-cni"), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
		if diff := cmp.Diff([]QuestionKind{ConfirmStart, GateWarnings}, s.kinds()); diff != "" {
			t.Fatalf("questions (-want +got):\n%s", diff)
		}
		q := s.asked[1]
		if q.Text != capitalize(worse)+". Continue with add-on vpc-cni?" || q.Step.Name != "vpc-cni" {
			t.Errorf("question = %+v", q)
		}
		// The add-on is read again after the answer, and then updated.
		ops := e.operations(0)
		if diff := cmp.Diff([]string{"eks:DescribeAddon", "eks:DescribeAddon", "eks:UpdateAddon"}, ops[slices.Index(ops, "eks:UpdateAddon")-2:slices.Index(ops, "eks:UpdateAddon")+1]); diff != "" {
			t.Errorf("the requests around the answer (-want +got):\n%s", diff)
		}
	})
	t.Run("a person is asked: no", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		degrade(e, 7)
		s := &script{t: t, answers: []bool{true, false}}
		res, err := Run(testContext(t), e.deps, request("vpc-cni", "coredns"), s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if got := e.mutations(); len(got) != 0 {
			t.Errorf("a declined run sent %v", got)
		}
		want := map[string]StepStatus{"vpc-cni": StepNotAttempted, "coredns": StepNotAttempted}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if res.Status != Declined || res.ExitCode() != 1 || res.Err == nil || res.Err.Error() != "declined; nothing more was changed" {
			t.Errorf("result = %s, exit %d, %v; want Declined, 1", res.Status, res.ExitCode(), res.Err)
		}
	})
	t.Run("the question cannot be asked", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		degrade(e, 7)
		hookErr := errors.New("stdin: input/output error")
		s := &script{t: t, answers: []bool{true}}
		hooks := s.hooks()
		hooks.Ask = func(ctx context.Context, q Question) (bool, error) {
			if q.Kind == GateWarnings {
				return false, hookErr
			}
			return s.ask(ctx, q)
		}
		res, err := Run(testContext(t), e.deps, request("vpc-cni"), hooks)
		if !errors.Is(err, hookErr) {
			t.Fatalf("error = %v, want the error of the hook", err)
		}
		if got := e.mutations(); len(got) != 0 || res.Status != Failed || res.ExitCode() != 1 {
			t.Errorf("result = %s, exit %d, sent %v; want Failed, 1, nothing", res.Status, res.ExitCode(), got)
		}
	})
	t.Run("nobody can be asked: the step fails, and nothing is sent for it", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		req := request("vpc-cni")
		reviewed, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		req.Expect = reviewed
		// The hook counts from here: the plan of the run, then the fresh read.
		degrade(e, 4)
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.mutations(); len(got) != 0 {
			t.Errorf("the run sent %v", got)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepFailed || s.Failure == nil || !strings.Contains(s.Failure.Error, "nobody can be asked; pass --yes or --require-healthy. add-on vpc-cni was not changed") || res.ExitCode() != 1 {
			t.Errorf("step = %s, failure %+v, exit %d; want Failed, 1", s.Status, s.Failure, res.ExitCode())
		}
	})
}

// What the fresh read does not ask about.
func TestRun_AddonDegradesBeforeSubmit_NoQuestion(t *testing.T) {
	t.Run("a warning of the plan that was accepted is not asked about again", func(t *testing.T) {
		e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Addon("vpc-cni").Status = fleettest.StatusDegraded }))
		s := &script{t: t, answers: []bool{true}}
		res, err := Run(testContext(t), e.deps, request("vpc-cni"), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
		if diff := cmp.Diff([]QuestionKind{GateWarnings}, s.kinds()); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
	})
	t.Run("--skip-health-check applies no health policy", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		degrade(e, 4)
		req := yes(request("vpc-cni"))
		req.Policy.Skip, req.Policy.RequireHealthy = true, true
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
	})
}

// during runs f while the update of vpc-cni, the first one, is waited for.
func during(e *env, f func(c *fleettest.Cluster)) {
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 1 {
			e.world.Edit(func(s *fleettest.State) { f(s.Cluster(east, "prod")) })
		}
	})
}

// updated returns the add-ons that got an UpdateAddon request, in order.
func updated(e *env) []any {
	var out []any
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSUpdateAddon {
			out = append(out, r.Input["addonName"])
		}
	}
	return out
}

// all returns a request for every add-on under the policy.
func all(policy health.Policy) AddonUpdateRequest {
	req := request()
	req.Addons, req.All, req.Policy = nil, true, policy
	return req
}

// After the wait of an add-on, the cluster is read again before the next
// request: the busy rule, and the gate over the add-ons that remain.
func TestRun_RecheckAfterWait(t *testing.T) {
	t.Run("the cluster starts to change: the next add-on is blocked", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		during(e, func(c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating })
		res, err := Run(testContext(t), e.deps, all(health.Policy{Yes: true}), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepBlocked, "kube-proxy": StepSkipped}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]any{"vpc-cni"}, updated(e)); diff != "" {
			t.Errorf("updates (-want +got):\n%s", diff)
		}
		f := res.Clusters[0].Steps[1].Failure
		if f == nil || f.Reason != apidoc.ReasonBusy || f.Error != "prod is busy (cluster UPDATING); add-on coredns was not changed. Run it again once that finishes" {
			t.Errorf("failure = %+v", f)
		}
		if res.ExitCode() != 3 || res.Status != RunBlocked {
			t.Errorf("result = %s, exit %d; want Blocked, 3", res.Status, res.ExitCode())
		}
	})
	t.Run("another add-on starts to change: the next add-on is blocked", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		during(e, func(c *fleettest.Cluster) { c.Addon("kube-proxy").Status = fleettest.StatusUpdating })
		res, err := Run(testContext(t), e.deps, yes(request("vpc-cni", "coredns")), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepBlocked}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]any{"vpc-cni"}, updated(e)); diff != "" {
			t.Errorf("updates (-want +got):\n%s", diff)
		}
	})
	t.Run("an add-on that remains degrades, with --require-healthy: blocked", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		during(e, func(c *fleettest.Cluster) { c.Addon("coredns").Status = fleettest.StatusDegraded })
		res, err := Run(testContext(t), e.deps, all(health.Policy{Yes: true, RequireHealthy: true}), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepSucceeded, "coredns": StepBlocked, "kube-proxy": StepSkipped}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]any{"vpc-cni"}, updated(e)); diff != "" {
			t.Errorf("updates (-want +got):\n%s", diff)
		}
	})
}

// The same read, where the run goes on.
func TestRun_RecheckAfterWait_GoesOn(t *testing.T) {
	t.Run("an add-on that remains degrades, with --yes: updated, with a notice", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		during(e, func(c *fleettest.Cluster) { c.Addon("coredns").Status = fleettest.StatusDegraded })
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, all(health.Policy{Yes: true}), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
		notices := 0
		for _, line := range s.events {
			if strings.HasPrefix(line, "Notice the health checks got worse since the plan (Addons: coredns is DEGRADED") {
				notices++
			}
		}
		// The read of the cluster records it, and the fresh read of the
		// add-on finds what was accepted already.
		if notices != 1 {
			t.Errorf("%d notices about the degraded add-on, want 1: %v", notices, s.events)
		}
	})
	t.Run("an add-on that remains degrades: a person is asked once", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		during(e, func(c *fleettest.Cluster) { c.Addon("coredns").Status = fleettest.StatusDegraded })
		s := &script{t: t, answers: []bool{true, true}}
		res, err := Run(testContext(t), e.deps, all(health.Policy{}), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v); want Succeeded", res.Status, err, res.Err)
		}
		if diff := cmp.Diff([]QuestionKind{ConfirmStart, GateWarnings}, s.kinds()); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
	})
	t.Run("an add-on of the run that timed out does not block the next one", func(t *testing.T) {
		e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
		res, err := Run(testContext(t), e.deps, all(health.Policy{Yes: true}), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"vpc-cni": StepInProgress, "coredns": StepSucceeded, "kube-proxy": StepSkipped}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("steps (-want +got):\n%s", diff)
		}
	})
	t.Run("--no-wait reads the cluster once", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		req := all(health.Policy{Yes: true})
		req.NoWait = true
		if _, err := Run(testContext(t), e.deps, req, Hooks{}); err != nil {
			t.Fatal(err)
		}
		if n := e.count(fleettest.EKSDescribeCluster); n != 1 {
			t.Errorf("%d reads of the cluster, want 1: nothing waited", n)
		}
	})
}

// What regate accepts is merged, so one finding is asked about once.
func TestMergeAccepted(t *testing.T) {
	accepted := []health.Result{{Name: "Addons", Outcome: health.Warn, Message: "coredns is DEGRADED", Findings: []string{"coredns"}}}
	cur := []health.Result{{Name: "Addons", Outcome: health.Warn, Message: "vpc-cni is DEGRADED", Findings: []string{"vpc-cni"}}}
	if got := worseThan(accepted, cur); len(got) != 1 {
		t.Fatalf("worseThan = %v, want the new finding", got)
	}
	merged := mergeAccepted(accepted, cur)
	if got := worseThan(merged, cur); len(got) != 0 {
		t.Errorf("after the merge, the same result is worse: %v", got)
	}
	if got := worseThan(merged, accepted); len(got) != 0 {
		t.Errorf("after the merge, the first result is worse: %v", got)
	}
	// A check that nobody saw, and that is not a pass, is worse.
	unseen := []health.Result{{Name: "Quotas", Outcome: health.Warn, Message: "91% in use"}}
	if got := worseThan(accepted, unseen); len(got) != 1 || got[0].Check != "Quotas" {
		t.Errorf("worseThan for a new check = %v", got)
	}
	if got := worseThan(nil, []health.Result{{Name: "Addons", Outcome: health.Pass}}); len(got) != 0 {
		t.Errorf("a new check that passes is worse: %v", got)
	}
}
