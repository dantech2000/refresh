package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// oneHopBuilder is a healthy cluster one minor behind the 1.32 target.
func oneHopBuilder() *mocks.EKSAPIBuilder {
	return mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.32.0-eksbuild.1", "v1.31.0-eksbuild.1"}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard)
}

// readiness returns the first hop's readiness step.
func readiness(t *testing.T, plan *Plan) Step {
	t.Helper()
	step := plan.Hops[0].Steps[0]
	if step.Type != StepReadiness {
		t.Fatalf("first step = %+v, want readiness", step)
	}
	return step
}

func TestReadiness_RefreshCompletedWithInsightsPasses(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress, ekstypes.InsightsRefreshStatusCompleted).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		WithInsight("prod-east", "Deprecated APIs removed in 1.32", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}
	if r := readiness(t, plan); !strings.Contains(r.Reason, "2 insight(s) passing") {
		t.Fatalf("readiness reason = %q", r.Reason)
	}
	if m.Calls.StartInsightsRefresh != 1 || m.Calls.DescribeInsightsRefresh != 2 {
		t.Fatalf("refresh calls: start=%d describe=%d, want 1 and 2 (polled until COMPLETED)",
			m.Calls.StartInsightsRefresh, m.Calls.DescribeInsightsRefresh)
	}
}

// EKS evaluates insights about once a day, so right after a control-plane
// hop the next minor usually has none. No insights must block, not pass as
// "0 blocking insights".
func TestReadiness_RefreshCompletedWithoutInsightsBlocks(t *testing.T) {
	m := oneHopBuilder().Build()
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := readiness(t, plan)
	if r.Status != StatusBlocked {
		t.Fatalf("readiness = %+v, want blocked", r)
	}
	for _, want := range []string{"insights for 1.32 not available yet", "--skip-insights-check"} {
		if !strings.Contains(r.Reason, want) {
			t.Fatalf("reason = %q, want %q", r.Reason, want)
		}
	}
}

func TestReadiness_RefreshFailedBlocks(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress, ekstypes.InsightsRefreshStatusFailed).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := readiness(t, plan)
	if r.Status != StatusBlocked || !strings.Contains(r.Reason, "insights refresh failed") {
		t.Fatalf("readiness = %+v, want blocked on the failed refresh", r)
	}
	if m.Calls.ListInsights != 0 {
		t.Fatalf("ListInsights calls = %d, want 0 (stale insights must not be read)", m.Calls.ListInsights)
	}
}

func TestReadiness_RefreshTimeoutBlocks(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)
	svc.InsightsRefreshTimeout = 30 * time.Millisecond

	var lines []string
	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{
		Progress: func(format string, args ...any) { lines = append(lines, sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := readiness(t, plan)
	if r.Status != StatusBlocked || !strings.Contains(r.Reason, "did not finish in time") {
		t.Fatalf("readiness = %+v, want blocked on the timeout", r)
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "refreshing cluster insights") {
		t.Fatalf("progress = %q, want a refresh progress line", lines)
	}
}

func TestReadiness_UnknownInsightBlocks(t *testing.T) {
	m := oneHopBuilder().
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValueUnknown, "1.32").
		WithInsight("prod-east", "Deprecated APIs removed in 1.32", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	r := readiness(t, plan)
	if r.Status != StatusBlocked || !strings.Contains(r.Reason, "UNKNOWN") || !strings.Contains(r.Reason, "Kubelet version skew") {
		t.Fatalf("readiness = %+v, want blocked naming the UNKNOWN insight", r)
	}
}

func TestReadiness_SkipInsightsCheckPassesWithWarning(t *testing.T) {
	m := oneHopBuilder().Build() // no insights at all
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{SkipInsightsCheck: true})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}
	if r := readiness(t, plan); !strings.Contains(r.Reason, "skipped") {
		t.Fatalf("readiness reason = %q, want the skip noted", r.Reason)
	}
	if len(plan.Notices) == 0 || !strings.Contains(strings.Join(plan.Notices, "\n"), "--skip-insights-check") {
		t.Fatalf("notices = %v, want the skipped check called out", plan.Notices)
	}
	if m.Calls.StartInsightsRefresh != 0 || m.Calls.ListInsights != 0 {
		t.Fatalf("insights calls: refresh=%d list=%d, want none", m.Calls.StartInsightsRefresh, m.Calls.ListInsights)
	}
}

// A refresh that is already running (StartInsightsRefresh refused) is waited
// on rather than treated as a failure.
func TestReadiness_FollowsRefreshAlreadyRunning(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress, ekstypes.InsightsRefreshStatusInProgress, ekstypes.InsightsRefreshStatusCompleted).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	m.StartInsightsRefreshFn = func(context.Context, *eks.StartInsightsRefreshInput, ...func(*eks.Options)) (*eks.StartInsightsRefreshOutput, error) {
		return nil, &ekstypes.InvalidRequestException{Message: aws.String("an insights refresh is already in progress")}
	}
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}
}

// Plan generation refreshes insights for the first hop; the execution-time
// re-gate for that same hop reuses it instead of waiting again.
func TestReadiness_ExecuteReusesPlanTimeRefresh(t *testing.T) {
	m := oneHopBuilder().
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	svc := newStrictTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := svc.checkHopReadiness(ctx, "prod-east", "1.32", false, nil); err != nil {
		t.Fatalf("checkHopReadiness: %v", err)
	}
	if plan.Blocked() || m.Calls.StartInsightsRefresh != 1 {
		t.Fatalf("StartInsightsRefresh calls = %d (blocked=%v), want 1", m.Calls.StartInsightsRefresh, plan.Blocked())
	}
}

// A rerun at the target version moves no control plane, so it needs no
// insights (EKS has none for the version the cluster already runs).
func TestReadiness_NoControlPlaneMoveNeedsNoInsights(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}
	if m.Calls.StartInsightsRefresh != 0 || m.Calls.ListInsights != 0 {
		t.Fatalf("insights calls: refresh=%d list=%d, want none", m.Calls.StartInsightsRefresh, m.Calls.ListInsights)
	}
}

// The live re-gate before hop 2 refreshes insights against the new control
// plane. When EKS still has none for the hop target, the hop must not start.
func TestExecute_LaterHopWithoutInsightsBlocks(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	m.ListInsightsFn = func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		out := &eks.ListInsightsOutput{}
		// Only 1.32 was ever evaluated.
		if in.Filter != nil && len(in.Filter.KubernetesVersions) == 1 && in.Filter.KubernetesVersions[0] == "1.32" {
			out.Insights = []ekstypes.InsightSummary{{
				Name:          aws.String("Kubelet version skew"),
				InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValuePassing},
			}}
		}
		return out, nil
	}
	svc := newStrictTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan blocked: %v", plan.Blockers())
	}

	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "insights for 1.33 not available yet") {
		t.Fatalf("err = %v, want the missing 1.33 insights named", err)
	}
	if !strings.Contains(report.StoppedAt, "1.32 → 1.33") {
		t.Fatalf("stoppedAt = %q, want the 1.33 control-plane phase", report.StoppedAt)
	}
	if m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 1 (hop 1 only)", m.Calls.UpdateClusterVersion)
	}
	if m.Calls.StartInsightsRefresh != 2 {
		t.Fatalf("StartInsightsRefresh calls = %d, want 2 (plan time, then live before hop 2)", m.Calls.StartInsightsRefresh)
	}

	// --skip-insights-check lets the same run through.
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true, SkipInsightsCheck: true}); err != nil {
		t.Fatalf("Execute with SkipInsightsCheck: %v", err)
	}
	if w.clusterVersion != "1.33" {
		t.Fatalf("cluster version = %s, want 1.33", w.clusterVersion)
	}
}

// --dry-run starts no refresh (a write API). Missing insights are a warning
// in the preview, since a real run refreshes and then blocks on them.
func TestReadiness_PreviewDoesNotRefresh(t *testing.T) {
	for _, tc := range []struct {
		name        string
		insight     ekstypes.InsightStatusValue // "" = no insights
		wantBlocked bool
		wantReason  string
	}{
		{name: "missing insights warn", wantReason: "insights for 1.32 not evaluated yet; a real run will refresh them and block"},
		{name: "unknown insight warns", insight: ekstypes.InsightStatusValueUnknown, wantReason: "UNKNOWN"},
		{name: "passing insight passes", insight: ekstypes.InsightStatusValuePassing, wantReason: "not refreshed in a dry run"},
		{name: "error insight still blocks", insight: ekstypes.InsightStatusValueError, wantBlocked: true, wantReason: "blocking insight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := oneHopBuilder()
			if tc.insight != "" {
				b = b.WithInsight("prod-east", "Deprecated APIs removed in 1.32", tc.insight, "1.32")
			}
			m := b.Build()
			svc := newStrictTestService(m)

			plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Preview: true})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			r := readiness(t, plan)
			if (r.Status == StatusBlocked) != tc.wantBlocked || !strings.Contains(r.Reason, tc.wantReason) {
				t.Fatalf("readiness = %+v, want blocked=%v reason containing %q", r, tc.wantBlocked, tc.wantReason)
			}
			if m.Calls.StartInsightsRefresh != 0 {
				t.Fatalf("StartInsightsRefresh calls = %d, want 0 in a dry run", m.Calls.StartInsightsRefresh)
			}
		})
	}
}

// Ctrl+C during the plan-time refresh is an interrupt, not a blocked plan.
func TestBuildPlan_InterruptDuringRefreshIsAnError(t *testing.T) {
	m := oneHopBuilder().
		WithInsightsRefresh(ekstypes.InsightsRefreshStatusInProgress).
		WithInsight("prod-east", "Kubelet version skew", ekstypes.InsightStatusValuePassing, "1.32").
		Build()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	describe := m.DescribeInsightsRefreshFn
	m.DescribeInsightsRefreshFn = func(c context.Context, in *eks.DescribeInsightsRefreshInput, o ...func(*eks.Options)) (*eks.DescribeInsightsRefreshOutput, error) {
		cancel()
		return describe(c, in, o...)
	}
	svc := newStrictTestService(m)

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if !errors.Is(err, context.Canceled) || plan != nil {
		t.Fatalf("BuildPlan = (%v, %v), want (nil, context.Canceled)", plan, err)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want it to say interrupted", err)
	}

	// The execution-time re-gate reports the interrupt the same way.
	if err := svc.checkHopReadiness(ctx, "prod-east", "1.32", false, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkHopReadiness err = %v, want context.Canceled", err)
	}
}
