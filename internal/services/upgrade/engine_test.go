package upgrade

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// fakeWorld is a mutable in-memory EKS cluster. The mock's Fn closures read
// and write it, so the engine's mutations actually change what BuildPlan
// re-derives — exactly how resume works against a real cluster.
type fakeWorld struct {
	mu             sync.Mutex
	clusterVersion string
	addonVersions  map[string]string // name -> installed version
	ngVersions     map[string]string // name -> k8s version
	failAddons     bool              // make UpdateAddon fail (gate failure simulation)
	hangUpdates    bool              // make DescribeUpdate never complete (SIGINT simulation)
}

// latestFor maps a k8s version to the fake addon catalogue's latest
// compatible version, e.g. 1.32 -> "v1.32.0-eksbuild.1".
func latestFor(k8s string) string { return "v" + k8s + ".0-eksbuild.1" }

func newWorldMock(w *fakeWorld) *mocks.EKSAPI {
	m := mocks.NewEKSAPI().Build()

	m.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
			Name:    in.Name,
			Version: aws.String(w.clusterVersion),
			Status:  ekstypes.ClusterStatusActive,
		}}, nil
	}
	m.UpdateClusterVersionFn = func(_ context.Context, in *eks.UpdateClusterVersionInput, _ ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.clusterVersion = aws.ToString(in.Version)
		return &eks.UpdateClusterVersionOutput{Update: &ekstypes.Update{
			Id: aws.String("u-cp"), Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		status := ekstypes.UpdateStatusSuccessful
		if w.hangUpdates {
			status = ekstypes.UpdateStatusInProgress
		}
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: in.UpdateId, Status: status}}, nil
	}

	m.ListAddonsFn = func(_ context.Context, _ *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := &eks.ListAddonsOutput{}
		for name := range w.addonVersions {
			out.Addons = append(out.Addons, name)
		}
		return out, nil
	}
	m.DescribeAddonFn = func(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		name := aws.ToString(in.AddonName)
		return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{
			AddonName:    in.AddonName,
			AddonVersion: aws.String(w.addonVersions[name]),
			Status:       ekstypes.AddonStatusActive,
		}}, nil
	}
	m.DescribeAddonVersionsFn = func(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		k8s := aws.ToString(in.KubernetesVersion)
		return &eks.DescribeAddonVersionsOutput{Addons: []ekstypes.AddonInfo{{
			AddonName: in.AddonName,
			AddonVersions: []ekstypes.AddonVersionInfo{{
				AddonVersion:    aws.String(latestFor(k8s)),
				Compatibilities: []ekstypes.Compatibility{{ClusterVersion: in.KubernetesVersion}},
			}},
		}}}, nil
	}
	m.UpdateAddonFn = func(_ context.Context, in *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.failAddons {
			return nil, errors.New("addon update rejected by fake world")
		}
		w.addonVersions[aws.ToString(in.AddonName)] = aws.ToString(in.AddonVersion)
		return &eks.UpdateAddonOutput{Update: &ekstypes.Update{
			Id: aws.String("u-addon"), Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}

	m.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := &eks.ListNodegroupsOutput{}
		for name := range w.ngVersions {
			out.Nodegroups = append(out.Nodegroups, name)
		}
		return out, nil
	}
	m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		name := aws.ToString(in.NodegroupName)
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			NodegroupName: in.NodegroupName,
			Version:       aws.String(w.ngVersions[name]),
			AmiType:       ekstypes.AMITypesAl2023X8664Standard,
			Status:        ekstypes.NodegroupStatusActive,
		}}, nil
	}
	m.UpdateNodegroupVersionFn = func(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.ngVersions[aws.ToString(in.NodegroupName)] = aws.ToString(in.Version)
		return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{
			Id: aws.String("u-ng"), Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	return m
}

func newWorld() *fakeWorld {
	return &fakeWorld{
		clusterVersion: "1.31",
		addonVersions:  map[string]string{"vpc-cni": latestFor("1.31")},
		ngVersions:     map[string]string{"workers-a": "1.31"},
	}
}

// Acceptance (REF-102): a simulated mid-plan failure, then a rerun that
// skips completed steps and continues to the end.
func TestExecute_MidPlanFailureThenResumedRerun(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	// First run: the addon phase fails after the control plane upgraded.
	w.failAddons = true
	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil {
		t.Fatal("first run should fail in the addon phase")
	}
	if len(report.Completed) != 1 || !strings.Contains(report.Completed[0], "control plane") {
		t.Fatalf("completed = %v, want the control-plane phase", report.Completed)
	}
	if !strings.Contains(report.FailedAt, "addons") {
		t.Fatalf("failedAt = %q, want the addon phase", report.FailedAt)
	}
	if len(report.Remaining) != 1 || !strings.Contains(report.Remaining[0], "nodegroup") {
		t.Fatalf("remaining = %v, want the nodegroup phase (halted before it)", report.Remaining)
	}
	if w.clusterVersion != "1.32" {
		t.Fatalf("cluster version = %s, want 1.32 (control plane completed)", w.clusterVersion)
	}
	if m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatal("gate failure must halt before the nodegroup phase")
	}
	cpCallsAfterFirstRun := m.Calls.UpdateClusterVersion

	// Rerun after the cause is fixed: the control-plane step derives as
	// completed and is NOT re-executed; the rest continues to success.
	w.failAddons = false
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (rerun): %v", err)
	}
	report, err = svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err != nil {
		t.Fatalf("Execute (rerun): %v", err)
	}
	if m.Calls.UpdateClusterVersion != cpCallsAfterFirstRun {
		t.Fatalf("UpdateClusterVersion calls grew from %d to %d on rerun — completed step was re-executed",
			cpCallsAfterFirstRun, m.Calls.UpdateClusterVersion)
	}
	if w.addonVersions["vpc-cni"] != latestFor("1.32") {
		t.Fatalf("addon version = %s, want %s", w.addonVersions["vpc-cni"], latestFor("1.32"))
	}
	if w.ngVersions["workers-a"] != "1.32" {
		t.Fatalf("nodegroup version = %s, want 1.32", w.ngVersions["workers-a"])
	}
	if report.FailedAt != "" || len(report.Remaining) != 0 {
		t.Fatalf("rerun report = %+v, want clean completion", report)
	}
}

// Acceptance (REF-102): a double-run of a completed upgrade is a no-op.
func TestExecute_DoubleRunIsNoOp(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mutationsAfterFirst := m.Calls.UpdateClusterVersion + m.Calls.UpdateAddon + m.Calls.UpdateNodegroupVersion

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (second run): %v", err)
	}
	if plan.PendingSteps() != 0 {
		t.Fatalf("second plan has %d pending steps, want 0", plan.PendingSteps())
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute (second run): %v", err)
	}

	mutationsAfterSecond := m.Calls.UpdateClusterVersion + m.Calls.UpdateAddon + m.Calls.UpdateNodegroupVersion
	if mutationsAfterSecond != mutationsAfterFirst {
		t.Fatalf("second run performed %d extra mutations, want 0", mutationsAfterSecond-mutationsAfterFirst)
	}
}

// Acceptance (REF-102): SIGINT mid-phase stops cleanly and the error tells
// the user what continues server-side and how to resume.
func TestExecute_InterruptLeavesResumeGuidance(t *testing.T) {
	w := newWorld()
	w.hangUpdates = true // control-plane update never completes
	m := newWorldMock(w)
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel() // simulates Ctrl+C via signal.NotifyContext in main
	}()

	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil {
		t.Fatal("interrupted run must return an error")
	}
	if !strings.Contains(err.Error(), "continue server-side") || !strings.Contains(err.Error(), "rerun") {
		t.Fatalf("err = %v, want server-side continuation + resume guidance", err)
	}
	if !strings.Contains(report.FailedAt, "control plane") {
		t.Fatalf("failedAt = %q, want the control-plane phase", report.FailedAt)
	}
}

// Confirmation prompts gate every mutating phase; declining aborts cleanly
// before anything is touched.
func TestExecute_DeclineConfirmationAborts(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var prompted []string
	report, err := svc.Execute(ctx, plan, ExecuteOptions{
		Confirm: func(label string) bool {
			prompted = append(prompted, label)
			return false
		},
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if len(prompted) != 1 {
		t.Fatalf("prompted %d times, want 1 (declined the first phase)", len(prompted))
	}
	if m.Calls.UpdateClusterVersion+m.Calls.UpdateAddon+m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatal("declining the prompt must not mutate anything")
	}
	if len(report.Remaining) == 0 {
		t.Fatalf("report.Remaining = %v, want the declined work listed", report.Remaining)
	}
}

// A blocked plan refuses to execute at all.
func TestExecute_BlockedPlanRefuses(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)

	plan := &Plan{
		ClusterName:    "prod-east",
		CurrentVersion: "1.31",
		TargetVersion:  "1.32",
		Hops: []Hop{{From: "1.31", To: "1.32", Steps: []Step{
			{Type: StepReadiness, Description: "readiness", Status: StatusBlocked, Reason: "2 blocking insights"},
			{Type: StepControlPlane, Description: "control plane → 1.32", Status: StatusPending},
		}}},
	}

	_, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "blockers") {
		t.Fatalf("err = %v, want blocker refusal", err)
	}
	if m.Calls.UpdateClusterVersion != 0 {
		t.Fatal("blocked plan must not mutate anything")
	}
	_ = w
}
