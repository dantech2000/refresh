package upgrade

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/legacy/internal/diag"
	"github.com/dantech2000/refresh/legacy/internal/mocks"
)

// twoNodegroupWorld is a cluster at 1.32 with two nodegroups at 1.31 whose
// rolls succeed.
func twoNodegroupWorld() *mocks.EKSAPI {
	return mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithUpdateStatuses("u-workers-a", ekstypes.UpdateStatusSuccessful).
		WithUpdateStatuses("u-workers-b", ekstypes.UpdateStatusSuccessful).
		Build()
}

func verifyWith(kube kubernetes.Interface) *PostRollVerify {
	return &PostRollVerify{Kube: func(context.Context) kubernetes.Interface { return kube }}
}

// progressLines collects progress lines; rolls and the scaling watch call
// it from more than one goroutine.
func progressLines() (ProgressFunc, func() string) {
	var mu sync.Mutex
	var lines []string
	return func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, sprintf(format, args...))
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return strings.Join(lines, "\n")
		}
}

// Each roll is verified, as `nodegroup update` verifies its rolls: the
// nodegroup is ACTIVE and no pod became Pending.
func TestUpgradeNodegroups_VerifiesEachRoll(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	progress, lines := progressLines()

	err := newTestService(m).UpgradeNodegroups(context.Background(), "prod", "1.32",
		NodegroupRollOptions{Verify: verifyWith(fakek8s.NewSimpleClientset())}, progress)
	if err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if len(*rolls) != 2 {
		t.Fatalf("rolls = %d, want 2", len(*rolls))
	}
	got := lines()
	for _, want := range []string{
		"post-roll verification: nodegroup workers-a is ACTIVE",
		"post-roll verification: nodegroup workers-b is ACTIVE",
		"post-roll verification: no new Pending pods",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress has no %q:\n%s", want, got)
		}
	}
}

// A pod that became Pending during a roll stops the phase: the next
// nodegroup is not rolled, and the error is ErrVerifyFailed.
func TestUpgradeNodegroups_NewPendingPodStopsThePhase(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	kube := fakek8s.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "old-pending"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	})
	start := m.UpdateNodegroupVersionFn
	m.UpdateNodegroupVersionFn = func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		// The roll leaves a pod with no node to run on.
		_, err := kube.CoreV1().Pods("shop").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-0"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("creating the pending pod: %v", err)
		}
		return start(ctx, in, optFns...)
	}

	err := newTestService(m).UpgradeNodegroups(context.Background(), "prod", "1.32",
		NodegroupRollOptions{Verify: verifyWith(kube)}, nil)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyFailed", err)
	}
	if !strings.Contains(err.Error(), "shop/checkout-0") || strings.Contains(err.Error(), "old-pending") {
		t.Errorf("err = %v, want the new Pending pod and not the old one", err)
	}
	if len(*rolls) != 1 {
		t.Errorf("rolls = %d, want 1: the next nodegroup must not roll", len(*rolls))
	}
}

// Without Kubernetes access the pod check is skipped and the nodegroup
// check still runs. A nodegroup that is not ACTIVE after its roll is an
// issue.
func TestUpgradeNodegroups_NotActiveAfterRollIsAnIssue(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	describe := m.DescribeNodegroupFn
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		out, err := describe(ctx, in, optFns...)
		if err == nil && len(*rolls) > 0 {
			out.Nodegroup.Status = ekstypes.NodegroupStatusDegraded
		}
		return out, err
	}
	progress, lines := progressLines()

	err := newTestService(m).UpgradeNodegroups(context.Background(), "prod", "1.32",
		NodegroupRollOptions{Verify: verifyWith(nil)}, progress)
	if !errors.Is(err, ErrVerifyFailed) || !strings.Contains(err.Error(), "workers-a status is DEGRADED") {
		t.Fatalf("err = %v, want ErrVerifyFailed that names the DEGRADED nodegroup", err)
	}
	if !strings.Contains(lines(), "pod verification skipped (no Kubernetes access)") {
		t.Errorf("progress does not say the pod check was skipped:\n%s", lines())
	}
}

// A nodegroup that cannot be read after its roll is unknown, not an issue:
// the error is ErrVerifyIncomplete and the run's failure is the read.
func TestExecute_UnreadVerificationIsIncomplete(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	describe := m.DescribeNodegroupFn
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if len(*rolls) > 0 {
			return nil, mocks.AccessDenied()
		}
		return describe(ctx, in, optFns...)
	}
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	report, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(nil)})
	if !errors.Is(err, ErrVerifyIncomplete) || errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyIncomplete", err)
	}
	if report.Status != RunFailed || report.Verification != nil {
		t.Errorf("report = %+v, want Failed with no verification", report)
	}
	f := report.Failure
	if f == nil || f.Kind != diag.KindNodegroup || f.Name != "workers-a" || f.Cluster != "prod" ||
		f.Operation != diag.OpDescribeNodegroup || f.Reason != diag.ReasonAccessDenied {
		t.Errorf("failure = %+v, want the denied DescribeNodegroup of prod/workers-a", f)
	}
	if len(*rolls) != 1 {
		t.Errorf("rolls = %d, want 1", len(*rolls))
	}
}

// The report of a run that verification stopped has the status VerifyFailed
// and the verification, and names what remains.
func TestExecute_VerifyFailedReport(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	describe := m.DescribeNodegroupFn
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		out, err := describe(ctx, in, optFns...)
		if err == nil && len(*rolls) > 0 {
			out.Nodegroup.Status = ekstypes.NodegroupStatusDegraded
		}
		return out, err
	}
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	report, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(nil)})
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyFailed", err)
	}
	if report.Status != RunVerifyFailed || report.Failure != nil || !strings.HasPrefix(report.StoppedAt, "nodegroup rolls to 1.32") {
		t.Errorf("report = %+v, want VerifyFailed at the nodegroup phase with no failure", report)
	}
	if v := report.Verification; v == nil || len(v.Issues) != 1 || !strings.Contains(v.Issues[0], "workers-a status is DEGRADED") {
		t.Errorf("verification = %+v, want the DEGRADED issue", report.Verification)
	}
}

// Without the option nothing is verified: the TUI's upgrade supplies none.
func TestUpgradeNodegroups_NoVerifyOption(t *testing.T) {
	m := twoNodegroupWorld()
	captureNodegroupRolls(m)
	progress, lines := progressLines()

	if err := newTestService(m).UpgradeNodegroups(context.Background(), "prod", "1.32", NodegroupRollOptions{}, progress); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if strings.Contains(lines(), "post-roll verification") {
		t.Errorf("verification ran without the option:\n%s", lines())
	}
}

// A roll the phase attached to is verified too. No snapshot from before the
// roll exists, so the pod check is skipped and says why.
func TestUpgradeNodegroups_VerifiesAttachedRoll(t *testing.T) {
	cases := []struct {
		name    string
		settled ekstypes.NodegroupStatus
		wantErr error
	}{
		{name: "settled ACTIVE", settled: ekstypes.NodegroupStatusActive},
		{name: "settled DEGRADED", settled: ekstypes.NodegroupStatusDegraded, wantErr: ErrVerifyFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mocks.NewEKSAPI().
				WithCluster("prod", "1.32").
				WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
				Build()
			var mu sync.Mutex
			describes := 0
			m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
				mu.Lock()
				defer mu.Unlock()
				describes++
				ng := &ekstypes.Nodegroup{
					NodegroupName: in.NodegroupName,
					Version:       aws.String("1.31"),
					AmiType:       ekstypes.AMITypesAl2023X8664Standard,
					Status:        ekstypes.NodegroupStatusUpdating,
				}
				if describes > 2 { // the listing and one poll are still UPDATING
					ng.Version = aws.String("1.32")
					ng.Status = tc.settled
				}
				return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
			}
			rolls := captureNodegroupRolls(m)
			progress, lines := progressLines()

			err := newTestService(m).UpgradeNodegroups(context.Background(), "prod", "1.32",
				NodegroupRollOptions{Verify: verifyWith(fakek8s.NewSimpleClientset())}, progress)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(*rolls) != 0 {
				t.Errorf("new rolls = %d, want 0", len(*rolls))
			}
			if !strings.Contains(lines(), "pod verification skipped (the roll started before this run could list the Pending pods)") {
				t.Errorf("progress does not say why the pod check was skipped:\n%s", lines())
			}
		})
	}
}

// statusOf makes DescribeNodegroup report status(name, call) for the
// nodegroups it names; "" keeps the mock's answer. call counts the
// describes of that nodegroup, from 1.
func statusOf(m *mocks.EKSAPI, status func(name string, call int) ekstypes.NodegroupStatus) {
	var mu sync.Mutex
	calls := map[string]int{}
	describe := m.DescribeNodegroupFn
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		out, err := describe(ctx, in, optFns...)
		if err != nil {
			return out, err
		}
		name := aws.ToString(in.NodegroupName)
		mu.Lock()
		calls[name]++
		n := calls[name]
		mu.Unlock()
		if s := status(name, n); s != "" {
			out.Nodegroup.Status = s
		}
		return out, nil
	}
}

// resumeWorld is a cluster at 1.32 where workers-a is already at 1.32 and
// workers-b is still to roll: the state a rerun finds.
func resumeWorld() *mocks.EKSAPI {
	return mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("workers-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("workers-b", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithUpdateStatuses("u-workers-b", ekstypes.UpdateStatusSuccessful).
		Build()
}

func pendingTargets(plan *Plan) []string {
	var out []string
	for _, hop := range plan.Hops {
		for _, st := range hop.Steps {
			if st.Type == StepNodegroup && st.Status == StatusPending {
				out = append(out, st.Target)
			}
		}
	}
	return out
}

// A rerun after a failed verification finds the nodegroup at the target
// version and still DEGRADED. The version does not make it done: the plan
// keeps it pending, and the run checks it again before the next roll.
func TestResume_DegradedNodegroupAtTargetIsVerifiedAgain(t *testing.T) {
	m := resumeWorld()
	statusOf(m, func(name string, _ int) ekstypes.NodegroupStatus {
		if name == "workers-a" {
			return ekstypes.NodegroupStatusDegraded
		}
		return ""
	})
	rolls := captureNodegroupRolls(m)
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if got := pendingTargets(plan); len(got) != 2 || got[0] != "workers-a" {
		t.Fatalf("pending nodegroups = %v, want workers-a and workers-b", got)
	}

	report, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(nil)})
	if !errors.Is(err, ErrVerifyFailed) || report.Status != RunVerifyFailed {
		t.Fatalf("err = %v, report = %+v; want VerifyFailed", err, report)
	}
	if len(*rolls) != 0 {
		t.Errorf("rolls = %d, want 0: workers-b must not roll past a DEGRADED nodegroup", len(*rolls))
	}

	// --skip-verify (no Verify option) goes on, as before.
	if _, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute without Verify: %v", err)
	}
	if len(*rolls) != 1 {
		t.Errorf("rolls = %d, want 1 without verification", len(*rolls))
	}
}

// A nodegroup that is UPDATING and already reports the target version is
// not done either: the plan keeps it pending, and the run attaches, waits,
// and verifies it.
func TestResume_UpdatingNodegroupAtTargetIsAttachedAndVerified(t *testing.T) {
	for _, tc := range []struct {
		name    string
		settled ekstypes.NodegroupStatus
		wantErr error
		rolls   int
	}{
		{"settles ACTIVE", ekstypes.NodegroupStatusActive, nil, 1},
		{"settles DEGRADED", ekstypes.NodegroupStatusDegraded, ErrVerifyFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := resumeWorld()
			statusOf(m, func(name string, call int) ekstypes.NodegroupStatus {
				switch {
				case name != "workers-a":
					return ""
				case call <= 3: // the planner, the phase listing, and one poll
					return ekstypes.NodegroupStatusUpdating
				}
				return tc.settled
			})
			rolls := captureNodegroupRolls(m)
			svc := newTestService(m)
			plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", PlanOptions{})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if got := pendingTargets(plan); len(got) != 2 {
				t.Fatalf("pending nodegroups = %v, want both: an in-flight update is not done", got)
			}
			progress, lines := progressLines()

			_, err = svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(nil), Progress: progress})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !strings.Contains(lines(), "nodegroup workers-a is UPDATING") {
				t.Errorf("the run did not attach to workers-a:\n%s", lines())
			}
			if len(*rolls) != tc.rolls {
				t.Errorf("rolls = %d, want %d", len(*rolls), tc.rolls)
			}
		})
	}
}

// The rollback planner and engine follow the same rule: a nodegroup at N-1
// that is DEGRADED stops the run before the control plane rolls back.
func TestRollbackResume_DegradedNodegroupAtTargetStopsTheControlPlane(t *testing.T) {
	w := newRollbackWorld()
	svc, m := w.service()
	statusOf(m, func(name string, _ int) ekstypes.NodegroupStatus {
		if name == "ng-old" { // already at 1.32
			return ekstypes.NodegroupStatusDegraded
		}
		return ""
	})
	events := recordMutations(m)
	plan := buildRollback(t, svc, RollbackOptions{SkipInsightsCheck: true})
	pending := 0
	for _, st := range plan.Steps {
		if st.Type == StepNodegroup && st.Status == StatusPending {
			pending++
		}
	}
	if pending != 2 {
		t.Fatalf("pending nodegroup steps = %d, want 2 (ng-new to roll back, ng-old to verify)", pending)
	}

	report, err := svc.ExecuteRollback(context.Background(), plan, ExecuteOptions{Yes: true, SkipInsightsCheck: true, Verify: verifyWith(nil)})
	if !errors.Is(err, ErrVerifyFailed) || report.Status != RunVerifyFailed {
		t.Fatalf("err = %v, report = %+v; want VerifyFailed", err, report)
	}
	if got := strings.Join(*events, ","); strings.Contains(got, "cp→") || strings.Contains(got, "addon→") {
		t.Errorf("mutations = %s, want no add-on downgrade and no control-plane rollback", got)
	}
}

// Issues win over a failed read (exit 5 before exit 4), and the report
// keeps both: the pods the roll left Pending and the nodegroup that could
// not be described.
func TestExecute_VerifyIssuesAndUnreadNodegroupKeepBoth(t *testing.T) {
	m := twoNodegroupWorld()
	rolls := captureNodegroupRolls(m)
	kube := fakek8s.NewSimpleClientset()
	start := m.UpdateNodegroupVersionFn
	m.UpdateNodegroupVersionFn = func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		_, err := kube.CoreV1().Pods("shop").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-0"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("creating the pending pod: %v", err)
		}
		return start(ctx, in, optFns...)
	}
	describe := m.DescribeNodegroupFn
	m.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if len(*rolls) > 0 {
			return nil, mocks.AccessDenied()
		}
		return describe(ctx, in, optFns...)
	}
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	report, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(kube)})
	if !errors.Is(err, ErrVerifyFailed) || errors.Is(err, ErrVerifyIncomplete) {
		t.Fatalf("err = %v, want ErrVerifyFailed", err)
	}
	if !strings.Contains(err.Error(), "shop/checkout-0") || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("err = %v, want the Pending pod and the failed read", err)
	}
	if report.Status != RunVerifyFailed || report.Verification == nil || len(report.Verification.Issues) != 1 {
		t.Errorf("report = %+v, want VerifyFailed with the issue", report)
	}
	if f := report.Failure; f == nil || f.Name != "workers-a" || f.Operation != diag.OpDescribeNodegroup || f.Reason != diag.ReasonAccessDenied {
		t.Errorf("failure = %+v, want the denied DescribeNodegroup of workers-a", report.Failure)
	}
}

// A multi-hop rerun must not move the control plane past a nodegroup the
// last roll left DEGRADED. The nodegroup is at the live control-plane
// version, so no regular hop would look at it before the next control-plane
// step: a catch-up hop verifies it first.
func TestResume_MultiHopVerifiesDegradedNodegroupBeforeTheControlPlane(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.32"
	for name := range w.addonVersions {
		w.addonVersions[name] = latestFor("1.32")
	}
	for name := range w.ngVersions {
		w.ngVersions[name] = "1.32"
	}
	m := newWorldMock(w)
	statusOf(m, func(string, int) ekstypes.NodegroupStatus { return ekstypes.NodegroupStatusDegraded })
	events := recordMutations(m)
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.34", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if first := plan.Hops[0]; first.From != "1.32" || first.To != "1.32" {
		t.Fatalf("first hop = %s→%s, want a 1.32 catch-up hop that verifies the nodegroup", first.From, first.To)
	}

	report, err := svc.Execute(context.Background(), plan, ExecuteOptions{Yes: true, Verify: verifyWith(nil)})
	if !errors.Is(err, ErrVerifyFailed) || report.Status != RunVerifyFailed {
		t.Fatalf("err = %v, report = %+v; want VerifyFailed", err, report)
	}
	if len(*events) != 0 {
		t.Errorf("mutations = %v, want none: the control plane must not move", *events)
	}
}
