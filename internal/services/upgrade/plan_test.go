package upgrade

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTestService(m *mocks.EKSAPI) *Service {
	s := NewService(m, testLogger())
	s.PollInterval = time.Millisecond
	return s
}

// twoHopMock builds a cluster two minors behind target with one addon and
// one nodegroup, everything healthy.
func twoHopMock() *mocks.EKSAPI {
	return mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
}

// Acceptance (REF-98): a fixture cluster two minors behind yields a two-cycle
// plan with the gates shown per hop.
func TestBuildPlan_TwoMinorsBehindYieldsTwoHops(t *testing.T) {
	svc := newTestService(twoHopMock())

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if len(plan.Hops) != 2 {
		t.Fatalf("hops = %d, want 2 (1.31→1.32→1.33)", len(plan.Hops))
	}
	if plan.Hops[0].From != "1.31" || plan.Hops[0].To != "1.32" ||
		plan.Hops[1].From != "1.32" || plan.Hops[1].To != "1.33" {
		t.Fatalf("hop versions = %+v", plan.Hops)
	}

	// Every hop shows the full gated sequence: readiness → control plane →
	// addons → nodegroups, in that order.
	for _, hop := range plan.Hops {
		var order []StepType
		for _, s := range hop.Steps {
			order = append(order, s.Type)
		}
		want := []StepType{StepReadiness, StepControlPlane, StepAddon, StepNodegroup}
		if len(order) != len(want) {
			t.Fatalf("hop %s→%s steps = %v, want %v", hop.From, hop.To, order, want)
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("hop %s→%s step order = %v, want %v", hop.From, hop.To, order, want)
			}
		}
	}

	if plan.Blocked() {
		t.Fatalf("plan unexpectedly blocked: %v", plan.Blockers())
	}
	if plan.PendingSteps() == 0 {
		t.Fatal("plan should have pending steps")
	}
}

// Acceptance (REF-98): a blocker (lagging nodegroup beyond the kubelet skew)
// renders the plan with the blocker called out; the command layer exits
// non-zero on Blocked().
func TestBuildPlan_LaggingNodegroupBlocks(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("ancient", "1.28", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !plan.Blocked() {
		t.Fatal("plan should be blocked: nodegroup at 1.28 vs target 1.32 exceeds skew 3")
	}
	blockers := plan.Blockers()
	if len(blockers) == 0 || !strings.Contains(blockers[0], "ancient") {
		t.Fatalf("blockers = %v, want mention of nodegroup 'ancient'", blockers)
	}
}

func TestBuildPlan_OlderTargetRejected(t *testing.T) {
	svc := newTestService(twoHopMock())

	if _, err := svc.BuildPlan(context.Background(), "prod-east", "1.30", PlanOptions{}); err == nil {
		t.Fatal("expected error for target < current")
	}
}

// Rerunning against the version the cluster already runs (with addons and
// nodegroups current too) derives every step completed: a no-op, not an error.
func TestBuildPlan_FullySatisfiedClusterIsNoOp(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.33.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.31").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.31", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan to current version: %v", err)
	}
	if plan.PendingSteps() != 0 {
		t.Fatalf("PendingSteps = %d, want 0 (no-op): %+v", plan.PendingSteps(), plan.Hops)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}
}

func TestBuildPlan_VersionNotOfferedRejected(t *testing.T) {
	m := twoHopMock()
	m.DescribeClusterVersionsFn = func(_ context.Context, _ *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
		return &eks.DescribeClusterVersionsOutput{}, nil
	}
	svc := newTestService(m)

	_, err := svc.BuildPlan(context.Background(), "prod-east", "1.99", PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("err = %v, want 'not offered'", err)
	}
}

// A blocking (ERROR) upgrade insight blocks the hop's readiness gate.
func TestBuildPlan_BlockingInsight(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithInsight("Deprecated APIs removed in 1.32", ekstypes.InsightStatusValueError, "1.32").
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !plan.Blocked() {
		t.Fatal("plan should be blocked by ERROR insight")
	}
	if b := plan.Blockers(); !strings.Contains(b[0], "Deprecated APIs") {
		t.Fatalf("blockers = %v, want the insight named", b)
	}
}

// WARNING insights don't block; they surface as plan warnings.
func TestBuildPlan_WarningInsightDoesNotBlock(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithInsight("Deprecated APIs in 2 manifests", ekstypes.InsightStatusValueWarning, "1.32").
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("WARNING insight must not block: %v", plan.Blockers())
	}
	found := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, "Deprecated APIs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want the insight surfaced", plan.Warnings)
	}
}

// Custom-AMI nodegroups appear as manual steps, never as API mutations.
func TestBuildPlan_CustomAMINodegroupIsManual(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("byo-ami", "1.31", ekstypes.AMITypesCustom).
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	step := findStep(t, plan.Hops[0].Steps, StepNodegroup, "byo-ami")
	if step.Status != StatusManual {
		t.Fatalf("custom-AMI nodegroup step status = %s, want manual", step.Status)
	}
	if !strings.Contains(step.Reason, "custom AMI") {
		t.Fatalf("reason = %q, want custom AMI explanation", step.Reason)
	}
}

// An addon with no version compatible with the hop target blocks the plan.
func TestBuildPlan_AddonWithoutCompatibleVersionBlocks(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("legacy-addon", "v0.9.0", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	// No WithAddonVersions: DescribeAddonVersions returns nothing for it.
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !plan.Blocked() {
		t.Fatal("plan should be blocked: no compatible addon version")
	}
}

// Resume-by-re-derivation: when control plane, addons, and nodegroups all
// already satisfy a hop, every step in it derives as completed.
func TestBuildPlan_PartiallyUpgradedClusterMarksCompletedSteps(t *testing.T) {
	// Cluster mid-upgrade: control plane already on 1.32, addon and
	// nodegroup still behind.
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.33.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 1 {
		t.Fatalf("hops = %d, want 1", len(plan.Hops))
	}
	var cp Step
	for _, s := range plan.Hops[0].Steps {
		if s.Type == StepControlPlane {
			cp = s
		}
	}
	if cp.Status != StatusCompleted {
		t.Fatalf("control-plane step status = %s, want completed (already at 1.32)", cp.Status)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "vpc-cni"); s.Status != StatusPending {
		t.Fatalf("addon step status = %s, want pending", s.Status)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepNodegroup, "workers-a"); s.Status != StatusPending {
		t.Fatalf("nodegroup step status = %s, want pending", s.Status)
	}
}

// Skipped addons and nodegroups surface as manual steps.
func TestBuildPlan_SkipListsAreManualSteps(t *testing.T) {
	svc := newTestService(twoHopMock())

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{
		SkipAddons:     []string{"vpc-cni"},
		SkipNodegroups: []string{"workers"},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "vpc-cni"); s.Status != StatusManual {
		t.Fatalf("skipped addon status = %s, want manual", s.Status)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepNodegroup, "workers-a"); s.Status != StatusManual {
		t.Fatalf("skipped nodegroup status = %s, want manual", s.Status)
	}
}

func findStep(t *testing.T, steps []Step, typ StepType, target string) Step {
	t.Helper()
	for _, s := range steps {
		if s.Type == typ && s.Target == target {
			return s
		}
	}
	t.Fatalf("no %s step for %q in %+v", typ, target, steps)
	return Step{}
}
