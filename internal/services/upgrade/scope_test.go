package upgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

func TestParseParts(t *testing.T) {
	got, err := ParseParts([]string{"control-plane,ADDONS", " nodegroups ", "addons"})
	if err != nil || len(got) != 3 || got[0] != PartControlPlane || got[1] != PartAddons || got[2] != PartNodegroups {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := ParseParts(nil); err != nil || got != nil {
		t.Fatalf("empty: got %v, %v", got, err)
	}
	if _, err := ParseParts([]string{"controlplane"}); err == nil || !strings.Contains(err.Error(), "control-plane, addons, nodegroups") {
		t.Fatalf("bad part: err = %v", err)
	}
	for _, empty := range [][]string{{""}, {","}, {" , "}} {
		if _, err := ParseParts(empty); err == nil {
			t.Fatalf("%q: an empty --only means every part", empty)
		}
	}
}

// --only control-plane runs the control plane alone: the add-ons and the
// nodegroups stay where they are, as manual steps with a notice that names
// the next command. A second run with --only nodegroups rolls them to the
// control plane's version, and nothing else moves.
func TestExecute_ControlPlaneThenNodegroupsSeparately(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepNodegroup, "workers-a"); s.Status != StatusManual || !strings.Contains(s.Reason, "--only control-plane") {
		t.Fatalf("nodegroup step = %+v, want manual, left out by --only", s)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepAddon, "vpc-cni"); s.Status != StatusManual {
		t.Fatalf("addon step = %+v, want manual", s)
	}
	notices := strings.Join(plan.Notices, "\n")
	if !strings.Contains(notices, "--only nodegroups") || !strings.Contains(notices, "refresh cluster upgrade -c prod-east --to 1.32 --only addons") {
		t.Fatalf("notices = %q, want the next commands", notices)
	}
	if len(plan.Blockers()) > 0 {
		t.Fatalf("blockers = %v", plan.Blockers())
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if w.clusterVersion != "1.32" || m.Calls.UpdateClusterVersion != 1 || m.Calls.UpdateAddon != 0 || m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatalf("after --only control-plane: cluster %s, calls cp=%d addon=%d ng=%d", w.clusterVersion, m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}})
	if err != nil {
		t.Fatalf("BuildPlan nodegroups: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute nodegroups: %v", err)
	}
	if w.ngVersions["workers-a"] != "1.32" || m.Calls.UpdateClusterVersion != 1 || m.Calls.UpdateAddon != 0 || m.Calls.UpdateNodegroupVersion != 1 {
		t.Fatalf("after --only nodegroups: ng %s, calls cp=%d addon=%d ng=%d", w.ngVersions["workers-a"], m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartAddons}})
	if err != nil {
		t.Fatalf("BuildPlan addons: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute addons: %v", err)
	}
	if m.Calls.UpdateAddon != 1 || m.Calls.UpdateNodegroupVersion != 1 || m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("after --only addons: calls cp=%d addon=%d ng=%d", m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}
}

// Scopes the plan cannot keep safe are refused before anything is read
// past the cluster: catching up beyond the control plane, and moving the
// control plane two versions while the add-ons stay behind.
func TestBuildPlan_ScopeRefusals(t *testing.T) {
	svc := newTestService(twoHopMock())
	ctx := context.Background()
	if _, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}}); err == nil || !strings.Contains(err.Error(), "use --to 1.31") {
		t.Fatalf("nodegroups past the control plane: err = %v", err)
	}
	if _, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane, PartNodegroups}}); err == nil || !strings.Contains(err.Error(), "use --to 1.32") {
		t.Fatalf("two hops without add-ons: err = %v", err)
	}
	// Without nodegroups, two hops are fine while the skew holds.
	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane, PartAddons}})
	if err != nil {
		t.Fatalf("two hops without nodegroups: %v", err)
	}
	for _, h := range plan.Hops {
		for _, s := range h.Steps {
			if s.Type == StepNodegroup && s.Status != StatusManual && s.Status != StatusCompleted {
				t.Fatalf("hop %s→%s nodegroup step %+v, want manual", h.From, h.To, s)
			}
		}
	}
}

// A nodegroup the control plane would leave beyond the kubelet skew is not
// rolled early when --only leaves nodegroups out: the readiness step blocks
// instead.
func TestBuildPlan_ControlPlaneOnlyStillGatesTheSkew(t *testing.T) {
	m := newWorldMock(&fakeWorld{
		clusterVersion: "1.31",
		addonVersions:  map[string]string{"vpc-cni": latestFor("1.31")},
		ngVersions:     map[string]string{"workers-a": "1.28"},
	})
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if b := strings.Join(plan.Blockers(), "\n"); !strings.Contains(b, "kubelet skew") {
		t.Fatalf("blockers = %q, want the kubelet skew", b)
	}
}

// Leaving the add-ons out cannot hide an add-on the new control plane cannot
// run and that has no compatible version: that step stays blocked. A
// blocked nodegroup step that turns manual keeps its reason.
func TestBuildPlan_ScopeKeepsHardAddonBlockers(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("coredns", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("coredns", []string{"v1.31.0-eksbuild.1"}, "1.31").
		WithAddonVersions("coredns", []string{}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepAddon, "coredns"); s.Status != StatusBlocked {
		t.Fatalf("coredns step = %+v, want still blocked", s)
	}
	if len(plan.Blockers()) == 0 {
		t.Fatal("a control-plane-only plan hid the add-on blocker")
	}
}

// Two control-plane-only runs in a row: the first leaves vpc-cni on a build
// the new control plane cannot run, so the second is blocked until the
// add-ons catch up, instead of leaving vpc-cni two versions behind.
func TestBuildPlan_SecondControlPlaneOnlyRunWaitsForTheAddons(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()
	cpOnly := PlanOptions{Only: []Part{PartControlPlane}}

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", cpOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane, PartNodegroups}})
	if err != nil {
		t.Fatal(err)
	}
	b := strings.Join(plan.Blockers(), "\n")
	if !strings.Contains(b, "vpc-cni") || !strings.Contains(b, "--only addons") {
		t.Fatalf("blockers = %q, want vpc-cni to catch up first", b)
	}

	// The add-ons catch up; the next control-plane-only run goes ahead.
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartAddons}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.33", cpOnly)
	if err != nil {
		t.Fatal(err)
	}
	if b := plan.Blockers(); len(b) > 0 {
		t.Fatalf("blockers after the add-ons caught up = %v", b)
	}
}

// The guard fails closed: when the add-on versions for the live control
// plane cannot be read, a control-plane move that leaves the add-ons out is
// blocked.
func TestBuildPlan_ControlPlaneOnlyBlocksOnUnreadAddonVersions(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.32"
	w.addonVersions["vpc-cni"] = latestFor("1.32")
	w.ngVersions["workers-a"] = "1.32"
	m := newWorldMock(w)
	inner := m.DescribeAddonVersionsFn
	m.DescribeAddonVersionsFn = func(ctx context.Context, in *eks.DescribeAddonVersionsInput, opts ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		if aws.ToString(in.KubernetesVersion) == "1.32" {
			return nil, mocks.AccessDenied()
		}
		return inner(ctx, in, opts...)
	}
	// In every scope, add-ons included or not, and with the read on the
	// plan's failures.
	for _, only := range [][]Part{{PartControlPlane}, {PartControlPlane, PartAddons}, nil} {
		plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{Only: only})
		if err != nil {
			t.Fatalf("%v: BuildPlan: %v", only, err)
		}
		b := strings.Join(plan.Blockers(), "\n")
		if !strings.Contains(b, "vpc-cni") || !strings.Contains(b, "could not be read: rerun to retry") {
			t.Fatalf("%v: blockers = %q, want the unread vpc-cni versions", only, b)
		}
		if strings.Contains(b, "--only") {
			t.Fatalf("%v: blockers = %q, advice to change --only cannot fix an unread catalogue", only, b)
		}
		found := false
		for _, f := range plan.Failures {
			if f.Name == "vpc-cni" && f.Operation == diag.OpDescribeAddonVersions && f.Reason == diag.ReasonAccessDenied {
				found = true
			}
		}
		if !found {
			t.Fatalf("%v: failures = %+v, want the vpc-cni read", only, plan.Failures)
		}
	}
}

// --nodegroup rolls only the named nodegroups: the others are manual steps
// and keep their version. An unknown name fails before anything changes.
func TestExecute_OneNodegroupToTheControlPlane(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.32"
	w.addonVersions["vpc-cni"] = latestFor("1.32")
	w.ngVersions = map[string]string{"workers-a": "1.31", "workers-b": "1.31"}
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	if _, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}, Nodegroups: []string{"workers-c"}}); err == nil || !strings.Contains(err.Error(), "workers-a, workers-b") && !strings.Contains(err.Error(), "workers-b, workers-a") {
		t.Fatalf("unknown nodegroup: err = %v", err)
	}
	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}, Nodegroups: []string{"workers-a"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	last := plan.Hops[len(plan.Hops)-1].Steps
	if s := findStep(t, last, StepNodegroup, "workers-b"); s.Status != StatusManual || !strings.Contains(s.Reason, "--nodegroup workers-a") {
		t.Fatalf("workers-b step = %+v, want manual, left out by --nodegroup", s)
	}
	if s := findStep(t, last, StepNodegroup, "workers-a"); s.Status != StatusPending {
		t.Fatalf("workers-a step = %+v, want pending", s)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if w.ngVersions["workers-a"] != "1.32" || w.ngVersions["workers-b"] != "1.31" || m.Calls.UpdateNodegroupVersion != 1 || m.Calls.UpdateClusterVersion != 0 {
		t.Fatalf("after: a=%s b=%s ng calls=%d cp calls=%d", w.ngVersions["workers-a"], w.ngVersions["workers-b"], m.Calls.UpdateNodegroupVersion, m.Calls.UpdateClusterVersion)
	}
}

// A nodegroup left out by --nodegroup still counts in the readiness gate:
// moving the control plane past its kubelet skew is blocked.
func TestBuildPlan_NodegroupSelectionStillGatesTheSkew(t *testing.T) {
	m := newWorldMock(&fakeWorld{
		clusterVersion: "1.31",
		addonVersions:  map[string]string{"vpc-cni": latestFor("1.31")},
		ngVersions:     map[string]string{"workers-a": "1.31", "workers-old": "1.28"},
	})
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Nodegroups: []string{"workers-a"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if b := strings.Join(plan.Blockers(), "\n"); !strings.Contains(b, "workers-old") {
		t.Fatalf("blockers = %q, want workers-old's kubelet skew", b)
	}
}

// The commands the plan names keep the run's account, region, and
// exclusions: following them never changes what the user left out.
func TestBuildPlan_FollowUpsKeepExclusionsAndTarget(t *testing.T) {
	w := newWorld()
	w.ngVersions = map[string]string{"workers-a": "1.31", "protected": "1.31"}
	svc := newTestService(newWorldMock(w))
	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{
		Only:           []Part{PartControlPlane},
		SkipAddons:     []string{"kube-proxy"},
		SkipNodegroups: []string{"protected"},
		CommandPrefix:  "refresh --profile 'prod admin' --region eu-west-1",
		CommandSuffix:  "--kube-context prod-ctx",
	})
	if err != nil {
		t.Fatal(err)
	}
	notices := strings.Join(plan.Notices, "\n")
	for _, want := range []string{
		"refresh --profile 'prod admin' --region eu-west-1 cluster upgrade -c prod-east --to 1.32 --only addons --skip kube-proxy --skip-nodegroup protected --kube-context prod-ctx",
		"refresh --profile 'prod admin' --region eu-west-1 cluster upgrade -c prod-east --to 1.32 --only nodegroups --skip kube-proxy --skip-nodegroup protected --kube-context prod-ctx",
	} {
		if !strings.Contains(notices, want) {
			t.Fatalf("notices lack %q:\n%s", want, notices)
		}
	}
}

// An add-on with no version at all for the live control plane blocks a
// control-plane move that leaves the add-ons out: nothing in that plan
// would update it.
func TestBuildPlan_ControlPlaneOnlyBlocksOnAnEmptyLiveCatalogue(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("legacy", "v1.0.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	catalogues(m, map[string]map[string][]string{"legacy": {"1.32": {}, "1.33": {"v2.0.0-eksbuild.1"}}})
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatal(err)
	}
	// The remedy that works: updating the add-ons cannot, there is no
	// version for the live control plane.
	if b := strings.Join(plan.Blockers(), "\n"); !strings.Contains(b, "EKS lists no version of add-on(s) legacy") || !strings.Contains(b, "--skip legacy") {
		t.Fatalf("blockers = %q, want legacy with --skip", b)
	}
	// With the add-ons in the plan, its own add-on step decides: the 1.33
	// version exists, so nothing blocks.
	for _, only := range [][]Part{nil, {PartControlPlane, PartAddons}} {
		plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{Only: only})
		if err != nil {
			t.Fatal(err)
		}
		if b := plan.Blockers(); len(b) > 0 {
			t.Fatalf("%v: blockers = %v, want none", only, b)
		}
	}
}
