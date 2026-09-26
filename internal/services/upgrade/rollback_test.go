package upgrade

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// rollbackWorld is the fake world plus what a rollback reads: the update
// history, the rollback readiness insights, the upgrade policy, the support
// status of the previous version, and Auto Mode.
type rollbackWorld struct {
	*fakeWorld
	mu            sync.Mutex
	history       []ekstypes.Update
	insights      []ekstypes.InsightSummary
	supportType   ekstypes.SupportType
	versionStatus ekstypes.VersionStatus
	autoMode      bool
	cpInputs      []eks.UpdateClusterVersionInput
}

// testNow is the pinned clock of the rollback tests.
var testNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func versionUpdate(id string, typ ekstypes.UpdateType, version string, created time.Time) ekstypes.Update {
	return ekstypes.Update{
		Id: aws.String(id), Type: typ, Status: ekstypes.UpdateStatusSuccessful, CreatedAt: aws.Time(created),
		Params: []ekstypes.UpdateParam{{Type: ekstypes.UpdateParamTypeVersion, Value: aws.String(version)}},
	}
}

// newRollbackWorld is a 1.33 cluster upgraded in place two days ago, with
// nodegroup ng-new at 1.33 and ng-old at 1.32, kube-proxy at a 1.33-only
// build, and vpc-cni at a build 1.32 lists.
func newRollbackWorld() *rollbackWorld {
	return &rollbackWorld{
		fakeWorld: &fakeWorld{
			clusterVersion: "1.33",
			addonVersions:  map[string]string{"kube-proxy": latestFor("1.33"), "vpc-cni": latestFor("1.32")},
			ngVersions:     map[string]string{"ng-new": "1.33", "ng-old": "1.32"},
		},
		history: []ekstypes.Update{
			versionUpdate("u-old", ekstypes.UpdateTypeVersionUpdate, "1.32", testNow.Add(-40*24*time.Hour)),
			versionUpdate("u-up", ekstypes.UpdateTypeVersionUpdate, "1.33", testNow.Add(-48*time.Hour)),
		},
		versionStatus: ekstypes.VersionStatusStandardSupport,
	}
}

func (w *rollbackWorld) mock() *mocks.EKSAPI {
	m := newWorldMock(w.fakeWorld)
	describe, updateCP := m.DescribeClusterFn, m.UpdateClusterVersionFn
	m.DescribeClusterFn = func(ctx context.Context, in *eks.DescribeClusterInput, o ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		out, err := describe(ctx, in, o...)
		if err != nil {
			return out, err
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.supportType != "" {
			out.Cluster.UpgradePolicy = &ekstypes.UpgradePolicyResponse{SupportType: w.supportType}
		}
		if w.autoMode {
			out.Cluster.ComputeConfig = &ekstypes.ComputeConfigResponse{Enabled: aws.Bool(true)}
		}
		return out, nil
	}
	m.UpdateClusterVersionFn = func(ctx context.Context, in *eks.UpdateClusterVersionInput, o ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		w.mu.Lock()
		w.cpInputs = append(w.cpInputs, *in)
		typ := ekstypes.UpdateTypeVersionUpdate
		if versionAtLeast(w.clusterVersion, aws.ToString(in.Version)) {
			typ = ekstypes.UpdateTypeVersionRollback
		}
		w.history = append(w.history, versionUpdate("u-cp", typ, aws.ToString(in.Version), testNow))
		w.mu.Unlock()
		return updateCP(ctx, in, o...)
	}
	m.ListUpdatesFn = func(_ context.Context, _ *eks.ListUpdatesInput, _ ...func(*eks.Options)) (*eks.ListUpdatesOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := &eks.ListUpdatesOutput{}
		for _, u := range w.history {
			out.UpdateIds = append(out.UpdateIds, aws.ToString(u.Id))
		}
		return out, nil
	}
	inner := m.DescribeUpdateFn
	m.DescribeUpdateFn = func(ctx context.Context, in *eks.DescribeUpdateInput, o ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		w.mu.Lock()
		for _, u := range w.history {
			if aws.ToString(u.Id) == aws.ToString(in.UpdateId) && in.NodegroupName == nil {
				w.mu.Unlock()
				return &eks.DescribeUpdateOutput{Update: &u}, nil
			}
		}
		w.mu.Unlock()
		return inner(ctx, in, o...)
	}
	m.ListInsightsFn = func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if in.Filter == nil || len(in.Filter.Categories) != 1 || in.Filter.Categories[0] != ekstypes.CategoryRollbackReadiness {
			return &eks.ListInsightsOutput{}, nil
		}
		return &eks.ListInsightsOutput{Insights: w.insights}, nil
	}
	m.DescribeClusterVersionsFn = func(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := &eks.DescribeClusterVersionsOutput{}
		for _, v := range in.ClusterVersions {
			out.ClusterVersions = append(out.ClusterVersions, ekstypes.ClusterVersionInformation{ClusterVersion: aws.String(v), VersionStatus: w.versionStatus})
		}
		return out, nil
	}
	return m
}

func (w *rollbackWorld) service() (*Service, *mocks.EKSAPI) {
	m := w.mock()
	svc := newStrictTestService(m)
	svc.now = func() time.Time { return testNow }
	return svc, m
}

func insight(name string, st ekstypes.InsightStatusValue) ekstypes.InsightSummary {
	return ekstypes.InsightSummary{Id: aws.String("id-" + name), Name: aws.String(name), Category: ekstypes.CategoryRollbackReadiness,
		InsightStatus: &ekstypes.InsightStatus{Status: st}}
}

func buildRollback(t *testing.T, svc *Service, opts RollbackOptions) *RollbackPlan {
	t.Helper()
	plan, err := svc.BuildRollbackPlan(context.Background(), "prod-east", opts)
	if err != nil {
		t.Fatalf("BuildRollbackPlan: %v", err)
	}
	return plan
}

func eligibility(t *testing.T, plan *RollbackPlan) Step {
	t.Helper()
	for _, s := range plan.Steps {
		if s.Type == StepReadiness && s.Description == "rollback eligibility" {
			return s
		}
	}
	t.Fatalf("no eligibility step in %+v", plan.Steps)
	return Step{}
}

func insightsStep(t *testing.T, plan *RollbackPlan) Step {
	t.Helper()
	for _, s := range plan.Steps {
		if s.Type == StepReadiness && s.Description == "rollback readiness insights" {
			return s
		}
	}
	t.Fatalf("no insights step in %+v", plan.Steps)
	return Step{}
}

func TestRollbackPlan_Eligible(t *testing.T) {
	svc, _ := newRollbackWorld().service()
	plan := buildRollback(t, svc, RollbackOptions{})

	if plan.CurrentVersion != "1.33" || plan.TargetVersion != "1.32" || plan.Blocked() {
		t.Fatalf("plan %s → %s blocked=%v: %v", plan.CurrentVersion, plan.TargetVersion, plan.Blocked(), plan.Blockers())
	}
	if want := testNow.Add(-48 * time.Hour).Add(RollbackWindow); plan.AvailableUntil == nil || !plan.AvailableUntil.Equal(want) {
		t.Errorf("availableUntil = %v, want %v", plan.AvailableUntil, want)
	}
	if e := eligibility(t, plan); !strings.Contains(e.Reason, "rollback available until about 2026-09-30") {
		t.Errorf("eligibility reason = %q", e.Reason)
	}
	if s := findStep(t, plan.Steps, StepNodegroup, "ng-new"); s.Status != StatusPending || s.Version != "1.32" {
		t.Errorf("ng-new = %+v, want pending → 1.32", s)
	}
	if s := findStep(t, plan.Steps, StepNodegroup, "ng-old"); s.Status != StatusCompleted {
		t.Errorf("ng-old = %+v, want completed", s)
	}
	if s := findStep(t, plan.Steps, StepAddon, "kube-proxy"); s.Status != StatusPending || s.Version != latestFor("1.32") {
		t.Errorf("kube-proxy = %+v, want pending → %s (newest compatible with 1.32)", s, latestFor("1.32"))
	}
	if s := findStep(t, plan.Steps, StepAddon, "vpc-cni"); s.Status != StatusCompleted {
		t.Errorf("vpc-cni = %+v, want completed (compatible with 1.32)", s)
	}
	if s := findStep(t, plan.Steps, StepControlPlane, ""); s.Status != StatusPending || s.Version != "1.32" {
		t.Errorf("control plane = %+v", s)
	}
	if plan.PendingSteps() != 3 {
		t.Errorf("pending = %d, want 3", plan.PendingSteps())
	}
	// Order: checks, nodegroups, add-ons, control plane.
	var kinds []string
	for _, s := range plan.Steps {
		if len(kinds) == 0 || kinds[len(kinds)-1] != string(s.Type) {
			kinds = append(kinds, string(s.Type))
		}
	}
	if got := strings.Join(kinds, ","); got != "Readiness,Nodegroup,Addon,ControlPlane" {
		t.Errorf("step order = %s", got)
	}
}

func TestRollbackPlan_Ineligible(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *rollbackWorld)
		want  string
	}{
		{"window closed", func(w *rollbackWorld) { w.history[1].CreatedAt = aws.Time(testNow.Add(-8 * 24 * time.Hour)) }, "the 7-day rollback window closed about 2026-09-24"},
		{"created at its version", func(w *rollbackWorld) { w.history = w.history[:1] }, "a cluster created at its version cannot roll back"},
		{"failed upgrade does not count", func(w *rollbackWorld) { w.history[1].Status = ekstypes.UpdateStatusFailed }, "cannot roll back"},
		{"extended support without the policy", func(w *rollbackWorld) {
			w.versionStatus = ekstypes.VersionStatusExtendedSupport
			w.supportType = ekstypes.SupportTypeStandard
		}, "change it to EXTENDED first"},
		{"unsupported target", func(w *rollbackWorld) { w.versionStatus = ekstypes.VersionStatusUnsupported }, "no longer supported"},
		{"update in progress", func(w *rollbackWorld) {
			u := versionUpdate("u-cfg", ekstypes.UpdateTypeVersionUpdate, "1.34", testNow)
			u.Status = ekstypes.UpdateStatusInProgress
			w.history = append(w.history, u)
		}, "no update in progress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			tt.setup(w)
			svc, _ := w.service()
			plan := buildRollback(t, svc, RollbackOptions{})
			e := eligibility(t, plan)
			if e.Status != StatusBlocked || !strings.Contains(e.Reason, tt.want) {
				t.Fatalf("eligibility = %+v, want blocked with %q", e, tt.want)
			}
		})
	}
}

func TestRollbackPlan_ExtendedSupportWithPolicy(t *testing.T) {
	w := newRollbackWorld()
	w.versionStatus = ekstypes.VersionStatusExtendedSupport
	w.supportType = ekstypes.SupportTypeExtended
	svc, _ := w.service()
	plan := buildRollback(t, svc, RollbackOptions{})
	if plan.Blocked() {
		t.Fatalf("blocked: %v", plan.Blockers())
	}
	if !strings.Contains(strings.Join(plan.Notices, "\n"), "extended support charges apply") {
		t.Errorf("notices = %v", plan.Notices)
	}
}

func TestRollbackPlan_Insights(t *testing.T) {
	tests := []struct {
		name    string
		status  ekstypes.InsightStatusValue
		skip    bool
		blocked bool
		notice  string
	}{
		{"error blocks", ekstypes.InsightStatusValueError, false, true, ""},
		{"unknown blocks", ekstypes.InsightStatusValueUnknown, false, true, ""},
		{"warning is advisory", ekstypes.InsightStatusValueWarning, false, false, "rollback insight warnings: API usage"},
		{"error with --skip-insights-check", ekstypes.InsightStatusValueError, true, false, "bypassed with --skip-insights-check"},
		{"unknown with --skip-insights-check", ekstypes.InsightStatusValueUnknown, true, false, "bypassed with --skip-insights-check"},
		{"warning with --skip-insights-check", ekstypes.InsightStatusValueWarning, true, false, "rollback insight warnings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			w.insights = []ekstypes.InsightSummary{insight("API usage", tt.status), insight("Kubelet skew", ekstypes.InsightStatusValuePassing)}
			svc, _ := w.service()
			plan := buildRollback(t, svc, RollbackOptions{SkipInsightsCheck: tt.skip})
			s := insightsStep(t, plan)
			if (s.Status == StatusBlocked) != tt.blocked {
				t.Fatalf("insights step = %+v, want blocked=%v", s, tt.blocked)
			}
			if tt.notice != "" && !strings.Contains(strings.Join(plan.Notices, "\n"), tt.notice) {
				t.Errorf("notices = %v, want %q", plan.Notices, tt.notice)
			}
		})
	}
}

func TestRollbackPlan_AutoModeNotice(t *testing.T) {
	w := newRollbackWorld()
	w.autoMode = true
	svc, _ := w.service()
	plan := buildRollback(t, svc, RollbackOptions{})
	if !strings.Contains(strings.Join(plan.Notices, "\n"), "EKS Auto Mode: EKS rolls back the Auto Mode nodes itself") {
		t.Errorf("notices = %v", plan.Notices)
	}
}

func TestRollbackPlan_SkippedAndCustomNodegroupsAreManual(t *testing.T) {
	w := newRollbackWorld()
	w.ngVersions["custom"] = "1.33"
	w.customAMI = map[string]bool{"custom": true}
	svc, _ := w.service()
	plan := buildRollback(t, svc, RollbackOptions{SkipNodegroups: []string{"new"}})
	if s := findStep(t, plan.Steps, StepNodegroup, "ng-new"); s.Status != StatusManual {
		t.Errorf("ng-new = %+v, want manual", s)
	}
	if s := findStep(t, plan.Steps, StepNodegroup, "custom"); s.Status != StatusManual {
		t.Errorf("custom = %+v, want manual", s)
	}
}

// The run changes the cluster in the documented order, and the control-plane
// request carries Force and RollbackConfig.
func TestExecuteRollback_OrderAndRequest(t *testing.T) {
	w := newRollbackWorld()
	svc, m := w.service()
	events := recordMutations(m)
	plan := buildRollback(t, svc, RollbackOptions{SkipInsightsCheck: true})

	report, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true, SkipInsightsCheck: true, RollbackTimeout: 3 * time.Hour})
	if err != nil {
		t.Fatalf("ExecuteRollback: %v", err)
	}
	want := "ng→1.32,addon→" + latestFor("1.32") + ",cp→1.32"
	if got := strings.Join(*events, ","); got != want {
		t.Fatalf("mutations = %s, want %s", got, want)
	}
	if len(report.Completed) != 3 || report.Status != RunSucceeded {
		t.Errorf("report = %+v", report)
	}
	in := w.cpInputs[0]
	if !in.Force || in.RollbackConfig == nil || aws.ToInt32(in.RollbackConfig.TimeoutMinutes) != 180 {
		t.Errorf("UpdateClusterVersion force=%v rollbackConfig=%+v, want force and 180 minutes", in.Force, in.RollbackConfig)
	}
	if n := len(aws.ToString(in.ClientRequestToken)); n < 33 || n > 126 {
		t.Errorf("token length %d, want 33-126", n)
	}
}

func TestExecuteRollback_NoForceByDefault(t *testing.T) {
	w := newRollbackWorld()
	svc, _ := w.service()
	plan := buildRollback(t, svc, RollbackOptions{})
	if _, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("ExecuteRollback: %v", err)
	}
	if in := w.cpInputs[0]; in.Force || in.RollbackConfig != nil {
		t.Errorf("force=%v rollbackConfig=%+v, want neither", in.Force, in.RollbackConfig)
	}
}

// A rerun after each phase re-derives the plan from live state and does only
// what remains; a rerun after the rollback finished does nothing.
func TestExecuteRollback_ResumeAfterEachPhase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *rollbackWorld)
		want  string
	}{
		{"after the nodegroups", func(w *rollbackWorld) { w.ngVersions["ng-new"] = "1.32" }, "addon→" + latestFor("1.32") + ",cp→1.32"},
		{"after the add-ons", func(w *rollbackWorld) {
			w.ngVersions["ng-new"] = "1.32"
			w.addonVersions["kube-proxy"] = latestFor("1.32")
		}, "cp→1.32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			tt.setup(w)
			svc, m := w.service()
			events := recordMutations(m)
			plan := buildRollback(t, svc, RollbackOptions{})
			if _, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true}); err != nil {
				t.Fatalf("ExecuteRollback: %v", err)
			}
			if got := strings.Join(*events, ","); got != tt.want {
				t.Fatalf("mutations = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("after the control plane", func(t *testing.T) {
		w := newRollbackWorld()
		svc, m := w.service()
		plan := buildRollback(t, svc, RollbackOptions{})
		if _, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true}); err != nil {
			t.Fatalf("ExecuteRollback: %v", err)
		}
		events := recordMutations(m)
		again := buildRollback(t, svc, RollbackOptions{})
		if !again.RolledBack || again.TargetVersion != "1.32" || again.PendingSteps() != 0 || again.Blocked() {
			t.Fatalf("rerun plan = rolledBack %v target %s pending %d blockers %v", again.RolledBack, again.TargetVersion, again.PendingSteps(), again.Blockers())
		}
		if _, err := svc.ExecuteRollback(context.Background(), again, ExecuteOptions{Yes: true}); err != nil || len(*events) != 0 {
			t.Fatalf("rerun: err %v, mutations %v", err, *events)
		}
	})
}

func TestExecuteRollback_BlockedPlanRefuses(t *testing.T) {
	w := newRollbackWorld()
	w.history = nil
	svc, m := w.service()
	plan := buildRollback(t, svc, RollbackOptions{})
	report, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true})
	if err == nil || report.Status != RunBlocked {
		t.Fatalf("err %v, status %s; want blocked", err, report.Status)
	}
	if m.Calls.UpdateClusterVersion+m.Calls.UpdateNodegroupVersion+m.Calls.UpdateAddon != 0 {
		t.Errorf("a blocked plan changed the cluster")
	}
}

func TestRollbackAvailability(t *testing.T) {
	w := newRollbackWorld()
	w.insights = []ekstypes.InsightSummary{insight("a", ekstypes.InsightStatusValueError), insight("b", ekstypes.InsightStatusValuePassing), insight("c", ekstypes.InsightStatusValueWarning)}
	svc, _ := w.service()
	a, err := svc.RollbackAvailability(context.Background(), "prod-east", "1.33")
	if err != nil || a == nil {
		t.Fatalf("availability = %+v, %v", a, err)
	}
	if a.PreviousVersion != "1.32" || !a.AvailableUntil.Equal(testNow.Add(-48*time.Hour).Add(RollbackWindow)) {
		t.Errorf("availability = %+v", a)
	}
	if a.Insights == nil || *a.Insights != (InsightCounts{Passing: 1, Warning: 1, Error: 1}) {
		t.Errorf("insight counts = %+v", a.Insights)
	}

	w.history[1].CreatedAt = aws.Time(testNow.Add(-8 * 24 * time.Hour))
	if a, err := svc.RollbackAvailability(context.Background(), "prod-east", "1.33"); err != nil || a != nil {
		t.Errorf("closed window: %+v, %v; want nil", a, err)
	}
}

func TestValidateRollbackTimeout(t *testing.T) {
	for d, ok := range map[time.Duration]bool{
		0: true, 2 * time.Hour: true, 168 * time.Hour: true, 12 * time.Hour: true,
		time.Hour: false, 169 * time.Hour: false, 2*time.Hour + time.Second: false,
	} {
		if err := ValidateRollbackTimeout(d); (err == nil) != ok {
			t.Errorf("ValidateRollbackTimeout(%s) = %v, want ok=%v", d, err, ok)
		}
	}
}
