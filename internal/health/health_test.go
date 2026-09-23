package health

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/internal/mocks"
)

// ──────────────────────────────────────────────────────────────────────────────
// calculateAverage
// ──────────────────────────────────────────────────────────────────────────────

func TestCalculateAverage_EmptySlice(t *testing.T) {
	if got := calculateAverage(nil); got != 0 {
		t.Errorf("empty slice: got %f, want 0", got)
	}
}

func TestCalculateAverage_SingleValue(t *testing.T) {
	if got := calculateAverage([]float64{42.0}); got != 42.0 {
		t.Errorf("got %f, want 42.0", got)
	}
}

func TestCalculateAverage_MultipleValues(t *testing.T) {
	got := calculateAverage([]float64{10, 20, 30})
	if got != 20.0 {
		t.Errorf("got %f, want 20.0", got)
	}
}

func TestCalculateAverage_FloatPrecision(t *testing.T) {
	got := calculateAverage([]float64{1, 2})
	if got != 1.5 {
		t.Errorf("got %f, want 1.5", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// analyzeResourceDistribution
// ──────────────────────────────────────────────────────────────────────────────

func TestAnalyzeResourceDistribution_EmptyMetrics(t *testing.T) {
	hc := &HealthChecker{}
	analysis := hc.analyzeResourceDistribution(nil)
	if analysis.MaxCPU != 0 || analysis.MinCPU != 0 || analysis.CPUStdDev != 0 {
		t.Errorf("empty metrics should yield zero analysis, got %+v", analysis)
	}
}

func TestAnalyzeResourceDistribution_SingleNode(t *testing.T) {
	hc := &HealthChecker{}
	metrics := []NodeMetrics{{NodeName: "node-1", CPUPercent: 50.0}}
	analysis := hc.analyzeResourceDistribution(metrics)
	if analysis.MaxCPU != 50.0 || analysis.MinCPU != 50.0 {
		t.Errorf("single node: MaxCPU=%f MinCPU=%f, both want 50.0", analysis.MaxCPU, analysis.MinCPU)
	}
	if analysis.CPUStdDev != 0 {
		t.Errorf("single node std dev should be 0, got %f", analysis.CPUStdDev)
	}
}

func TestAnalyzeResourceDistribution_MultipleNodes(t *testing.T) {
	hc := &HealthChecker{}
	metrics := []NodeMetrics{
		{CPUPercent: 10},
		{CPUPercent: 20},
		{CPUPercent: 30},
	}
	analysis := hc.analyzeResourceDistribution(metrics)
	if analysis.MinCPU != 10 || analysis.MaxCPU != 30 {
		t.Errorf("min=%f max=%f, want 10/30", analysis.MinCPU, analysis.MaxCPU)
	}
	// Std dev = sqrt(((10-20)^2 + (20-20)^2 + (30-20)^2) / 3) = sqrt(200/3) ≈ 8.165
	wantStdDev := math.Sqrt(200.0 / 3.0)
	if math.Abs(analysis.CPUStdDev-wantStdDev) > 0.001 {
		t.Errorf("CPUStdDev=%f, want ~%f", analysis.CPUStdDev, wantStdDev)
	}
}

func TestAnalyzeResourceDistribution_HighVarianceDetected(t *testing.T) {
	hc := &HealthChecker{}
	metrics := []NodeMetrics{
		{CPUPercent: 5},
		{CPUPercent: 95},
	}
	analysis := hc.analyzeResourceDistribution(metrics)
	if analysis.CPUStdDev <= 40 {
		t.Errorf("expected high std dev for 5/95 split, got %f", analysis.CPUStdDev)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckCriticalWorkloads — nil k8sClient
// ──────────────────────────────────────────────────────────────────────────────

func TestCheckCriticalWorkloads_NilClientReturnsWarn(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("nil k8sClient: status = %s, want WARN", result.Status)
	}
	if result.IsBlocking {
		t.Error("nil k8sClient result should not be blocking")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckCriticalWorkloads — fake k8sClient
// ──────────────────────────────────────────────────────────────────────────────

func allReadyPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
		},
	}
}

func pendingPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func TestCheckCriticalWorkloads_AllRunning(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		allReadyPod("kube-system", "coredns-1"),
		allReadyPod("kube-system", "coredns-2"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	if result.Status != StatusPass {
		t.Errorf("all running pods: status = %s, want PASS", result.Status)
	}
}

func TestCheckCriticalWorkloads_NoPods(t *testing.T) {
	client := fakek8s.NewSimpleClientset()
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	// No pods found → StatusWarn (not a hard failure)
	if result.Status != StatusWarn {
		t.Errorf("no pods: status = %s, want WARN", result.Status)
	}
}

func TestCheckCriticalWorkloads_SomePendingBelowThreshold(t *testing.T) {
	// 1 running, 9 pending = 10% — below 90% threshold → StatusFail
	objects := []runtime.Object{allReadyPod("kube-system", "ok-1")}
	for i := range 9 {
		objects = append(objects, pendingPod("kube-system", metav1.ObjectMeta{}.Name+
			"pending-"+string(rune('a'+i))))
	}
	client := fakek8s.NewSimpleClientset(objects...)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	if result.Status == StatusPass {
		t.Errorf("mostly pending pods should not pass, got %s", result.Status)
	}
}

func TestCheckCriticalWorkloads_SucceededPodsSkipped(t *testing.T) {
	succeeded := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "job-complete"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	running := allReadyPod("kube-system", "coredns-1")
	client := fakek8s.NewSimpleClientset(succeeded, running)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	// Only the running pod counts; succeeded is skipped → 1/1 running → pass
	if result.Status != StatusPass {
		t.Errorf("succeeded pods should be skipped: status = %s, want PASS", result.Status)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckPodDisruptionBudgets — nil k8sClient
// ──────────────────────────────────────────────────────────────────────────────

func TestCheckPodDisruptionBudgets_NilClientReturnsWarn(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("nil k8sClient: status = %s, want WARN", result.Status)
	}
	if result.IsBlocking {
		t.Error("PDB check should never be blocking")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckPodDisruptionBudgets — fake k8sClient
// ──────────────────────────────────────────────────────────────────────────────

func userNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func deploy(namespace, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
			},
		},
	}
}

// pdb returns a PDB whose selector covers pods labeled app=<app>.
func pdb(namespace, name, app string) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
		},
	}
}

func TestCheckPodDisruptionBudgets_NoDeployments(t *testing.T) {
	client := fakek8s.NewSimpleClientset(userNamespace("my-app"))
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass {
		t.Errorf("no deployments: status = %s, want PASS", result.Status)
	}
}

func TestCheckPodDisruptionBudgets_AllDeploymentsProtected(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		pdb("my-app", "frontend-pdb", "frontend"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass {
		t.Errorf("all deployments protected: status = %s, want PASS (got msg: %s)", result.Status, result.Message)
	}
}

func TestCheckPodDisruptionBudgets_SelectorMismatchNotCounted(t *testing.T) {
	// Regression: a PDB in the namespace whose selector does NOT match the
	// deployment's pods must not count as protection (the old implementation
	// counted PDBs instead of covered deployments).
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		pdb("my-app", "other-pdb", "something-else"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("selector mismatch: status = %s, want WARN (got msg: %s)", result.Status, result.Message)
	}
	if result.Score != 0 {
		t.Errorf("selector mismatch: score = %d, want 0", result.Score)
	}
}

func TestCheckPodDisruptionBudgets_UnprotectedDeploymentsWarn(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		deploy("my-app", "backend"),
		// no PDB for either
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("unprotected deployments: status = %s, want WARN", result.Status)
	}
}

func TestCheckPodDisruptionBudgets_SystemNamespacesSkipped(t *testing.T) {
	// Only system namespace deployments — user namespace has no deployments
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		// kube-system has a deployment but should be skipped
		deploy("kube-system", "coredns"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	// my-app has 0 deployments → pass (system ns skipped)
	if result.Status != StatusPass {
		t.Errorf("system namespace skipped: status = %s, want PASS", result.Status)
	}
}

func TestCheckPodDisruptionBudgets_NeverBlocking(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.IsBlocking {
		t.Error("PDB check result should never be blocking regardless of outcome")
	}
}

// pdbAllowing returns a PDB covering app=<app> with the given live status.
func pdbAllowing(namespace, name, app string, allowed, expected int32) *policyv1.PodDisruptionBudget {
	p := pdb(namespace, name, app)
	p.Status = policyv1.PodDisruptionBudgetStatus{
		DisruptionsAllowed: allowed,
		CurrentHealthy:     expected,
		DesiredHealthy:     expected,
		ExpectedPods:       expected,
	}
	return p
}

func hasDetail(details []string, substr string) bool {
	for _, d := range details {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

func TestCheckPodDisruptionBudgets_ZeroDisruptionsAllowedWarns(t *testing.T) {
	// Regression: minAvailable == replicas gives full selector coverage but
	// blocks every drain. The check used to PASS here.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		pdbAllowing("my-app", "frontend-pdb", "frontend", 0, 3),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Fatalf("drain blocker: status = %s, want WARN (msg: %s)", result.Status, result.Message)
	}
	if result.IsBlocking {
		t.Error("drain blocker should be WARN-level, not blocking by default")
	}
	if !strings.Contains(result.Message, "may block a drain") {
		t.Errorf("unscoped message should say the PDB may block a drain, got %q", result.Message)
	}
	if !hasDetail(result.Details, "my-app/frontend-pdb") {
		t.Errorf("details should name the blocking PDB, got %v", result.Details)
	}
	if result.Score > 50 {
		t.Errorf("drain blocker score = %d, want <= 50", result.Score)
	}
	if d := aggregateResults([]HealthResult{result}).Decision; d != DecisionWarn {
		t.Errorf("decision = %s, want WARN", d)
	}
}

func TestCheckPodDisruptionBudgets_DrainBlockerWithNoDeployments(t *testing.T) {
	// A blocking PDB must be reported even when no deployments are counted
	// (e.g. a StatefulSet-only namespace).
	client := fakek8s.NewSimpleClientset(
		userNamespace("data"),
		pdbAllowing("data", "db-pdb", "db", 0, 1),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || !hasDetail(result.Details, "data/db-pdb") {
		t.Errorf("drain blocker without deployments: status = %s, details = %v", result.Status, result.Details)
	}
}

func TestCheckPodDisruptionBudgets_SystemNamespaceDrainBlocker(t *testing.T) {
	// A stuck kube-system PDB blocks a drain just like a user one.
	client := fakek8s.NewSimpleClientset(
		userNamespace("kube-system"),
		pdbAllowing("kube-system", "coredns", "coredns", 0, 2),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || !hasDetail(result.Details, "kube-system/coredns") {
		t.Errorf("kube-system drain blocker: status = %s, details = %v", result.Status, result.Details)
	}
}

func TestCheckPodDisruptionBudgets_EmptyPDBNotABlocker(t *testing.T) {
	// A PDB that matches no pods (ExpectedPods == 0) reports 0 disruptions
	// allowed but blocks nothing.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		pdbAllowing("my-app", "frontend-pdb", "frontend", 1, 3),
		pdbAllowing("my-app", "orphan-pdb", "gone", 0, 0),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass {
		t.Errorf("empty PDB: status = %s, want PASS (msg: %s, details: %v)", result.Status, result.Message, result.Details)
	}
}

func TestCheckPodDisruptionBudgets_HealthyPDBPasses(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "frontend"),
		pdbAllowing("my-app", "frontend-pdb", "frontend", 1, 3),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass || result.Score != 100 {
		t.Errorf("healthy PDB: status = %s score = %d, want PASS/100", result.Status, result.Score)
	}
}

func ngNode(name, nodegroup string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{nodeLabelNodegroup: nodegroup},
	}}
}

// appPod returns a Running, Ready pod labeled app=<app> on node.
func appPod(namespace, name, app, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func TestCheckPodDisruptionBudgets_ScopedToTargetNodegroup(t *testing.T) {
	// web's pods run on ng-a (the roll target); batch's pods run only on ng-b
	// and Fargate. Only web-pdb is a blocker for rolling ng-a.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("node-a1", "ng-a"),
		ngNode("node-b1", "ng-b"),
		appPod("my-app", "web-1", "web", "node-a1"),
		appPod("my-app", "batch-1", "batch", "node-b1"),
		appPod("my-app", "batch-2", "batch", "fargate-ip-10-0-0-1"),
		pdbAllowing("my-app", "web-pdb", "web", 0, 1),
		pdbAllowing("my-app", "batch-pdb", "batch", 0, 2),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Fatalf("scoped blocker: status = %s, want WARN (msg: %s)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "1 PDB(s)") || !strings.Contains(result.Message, "stall on eviction") {
		t.Errorf("scoped message should count 1 PDB and say the roll will stall, got %q", result.Message)
	}
	if !hasDetail(result.Details, "my-app/web-pdb") {
		t.Errorf("details should name web-pdb, got %v", result.Details)
	}
	if hasDetail(result.Details, "batch-pdb") {
		t.Errorf("batch-pdb has no pods on ng-a and must not be reported, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_ScopedNoBlockerOnTarget(t *testing.T) {
	// The only zero-disruption PDB covers pods on ng-b; rolling ng-a is clear.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		deploy("my-app", "batch"),
		ngNode("node-a1", "ng-a"),
		ngNode("node-b1", "ng-b"),
		appPod("my-app", "batch-1", "batch", "node-b1"),
		pdbAllowing("my-app", "batch-pdb", "batch", 0, 1),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass {
		t.Errorf("no blocker on target: status = %s, want PASS (msg: %s, details: %v)", result.Status, result.Message, result.Details)
	}
}

func TestCheckPodDisruptionBudgets_NamespaceListFailureKeepsBlockers(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		pdbAllowing("my-app", "web-pdb", "web", 0, 1),
	)
	client.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || result.Score > 50 {
		t.Errorf("namespace list failure: status = %s score = %d, want WARN <= 50", result.Status, result.Score)
	}
	if !hasDetail(result.Details, "my-app/web-pdb") {
		t.Errorf("blocker details should survive a namespace list failure, got %v", result.Details)
	}
	if !hasDetail(result.Details, "Failed to list namespaces: forbidden") {
		t.Errorf("the namespace list error should survive the blocker message, got %v", result.Details)
	}
}

// syncFailedPDB returns a PDB for app=<app> whose status the disruption
// controller could not compute (bare pods, or a custom resource without a
// /scale subresource): 0 disruptions allowed, 0 expected pods, and the
// DisruptionAllowed condition False with reason SyncFailed.
func syncFailedPDB(namespace, name, app string) *policyv1.PodDisruptionBudget {
	p := pdb(namespace, name, app)
	p.Generation, p.Status.ObservedGeneration = 1, 1
	p.Status.Conditions = []metav1.Condition{{
		Type:   policyv1.DisruptionAllowedCondition,
		Status: metav1.ConditionFalse,
		Reason: policyv1.SyncFailedReason,
	}}
	return p
}

func TestCheckPodDisruptionBudgets_SyncFailedPDBOnTarget(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("n1", "ng-a"),
		appPod("my-app", "op-0", "op", "n1"),
		syncFailedPDB("my-app", "op-pdb", "op"),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || !strings.Contains(result.Message, "stall on eviction") {
		t.Errorf("SyncFailed PDB on the target: status = %s msg = %q, want scoped WARN", result.Status, result.Message)
	}
	if !hasDetail(result.Details, "my-app/op-pdb (PDB status not synced") {
		t.Errorf("details should name op-pdb as not synced, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_SyncFailedPDBOffTarget(t *testing.T) {
	// The SyncFailed PDB's pods run on ng-b only; rolling ng-a is clear.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("n1", "ng-a"),
		ngNode("n2", "ng-b"),
		appPod("my-app", "op-0", "op", "n2"),
		syncFailedPDB("my-app", "op-pdb", "op"),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if hasDetail(result.Details, "op-pdb") {
		t.Errorf("op-pdb has no pods on ng-a and must not be reported, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_UnobservedPDBOnTarget(t *testing.T) {
	// The controller has not yet observed generation 2, so the status is
	// stale. The eviction API refuses evictions until it catches up.
	p := pdbAllowing("my-app", "web-pdb", "web", 0, 0)
	p.Generation, p.Status.ObservedGeneration = 2, 1
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("n1", "ng-a"),
		appPod("my-app", "web-1", "web", "n1"),
		p,
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if !hasDetail(result.Details, "my-app/web-pdb (PDB status not synced") {
		t.Errorf("unobserved PDB on the target should be a blocker, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_SyncFailedPDBUnscoped(t *testing.T) {
	// Without targets there is no pod lookup, so a SyncFailed PDB that allows
	// 0 disruptions is reported: its ExpectedPods of 0 means nothing.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		syncFailedPDB("my-app", "op-pdb", "op"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if !strings.Contains(result.Message, "may block a drain") || !hasDetail(result.Details, "my-app/op-pdb") {
		t.Errorf("unscoped SyncFailed PDB: msg = %q details = %v", result.Message, result.Details)
	}
}

func TestCheckPodDisruptionBudgets_SyncedEmptyPDBScopedNotABlocker(t *testing.T) {
	// A synced PDB with ExpectedPods == 0 matches no pods, even if a pod with
	// its labels is on the target (e.g. created after the last sync).
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("n1", "ng-a"),
		appPod("my-app", "web-1", "web", "n1"),
		pdbAllowing("my-app", "web-pdb", "web", 0, 0),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if hasDetail(result.Details, "web-pdb") {
		t.Errorf("synced empty PDB must not be reported, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_PodsEvictionIgnores(t *testing.T) {
	// Every web pod on the target is one the eviction API lets go without
	// consulting the PDB, so web-pdb does not stall the roll.
	now := metav1.Now()
	succeeded := appPod("my-app", "web-done", "web", "n1")
	succeeded.Status.Phase = corev1.PodSucceeded
	failed := appPod("my-app", "web-failed", "web", "n1")
	failed.Status.Phase = corev1.PodFailed
	pending := appPod("my-app", "web-pending", "web", "n1")
	pending.Status.Phase = corev1.PodPending
	deleting := appPod("my-app", "web-deleting", "web", "n1")
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"example.com/hold"}

	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		ngNode("n1", "ng-a"),
		ngNode("n2", "ng-b"),
		succeeded, failed, pending, deleting,
		appPod("my-app", "web-1", "web", "n2"),
		pdbAllowing("my-app", "web-pdb", "web", 0, 1),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if hasDetail(result.Details, "web-pdb") {
		t.Errorf("false positive: no pod on the target is gated by web-pdb, got %v", result.Details)
	}
}

func TestEvictionGatedByPDB_NotReadyPods(t *testing.T) {
	alwaysAllow := policyv1.AlwaysAllow
	ifHealthy := policyv1.IfHealthyBudget
	notReady := *appPod("my-app", "web-1", "web", "n1")
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	ready := *appPod("my-app", "web-2", "web", "n1")

	cases := []struct {
		name   string
		pod    corev1.Pod
		policy *policyv1.UnhealthyPodEvictionPolicyType
		// healthy, desired are the PDB's CurrentHealthy and DesiredHealthy.
		healthy, desired int32
		want             bool
	}{
		{"ready pod is gated", ready, &alwaysAllow, 1, 2, true},
		{"not ready, AlwaysAllow", notReady, &alwaysAllow, 1, 2, false},
		{"not ready, budget not met", notReady, nil, 1, 2, true},
		{"not ready, IfHealthyBudget, budget not met", notReady, &ifHealthy, 1, 2, true},
		{"not ready, budget met", notReady, nil, 2, 2, false},
		{"not ready, no desired count", notReady, nil, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pdbAllowing("my-app", "web-pdb", "web", 0, 2)
			p.Spec.UnhealthyPodEvictionPolicy = tc.policy
			p.Status.CurrentHealthy, p.Status.DesiredHealthy = tc.healthy, tc.desired
			if got := evictionGatedByPDB(tc.pod, *p); got != tc.want {
				t.Errorf("evictionGatedByPDB() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckPodDisruptionBudgets_NotReadyPodAlwaysAllow(t *testing.T) {
	// The only web pod on the target is not Ready and the PDB lets unhealthy
	// pods go, so rolling ng-a is clear.
	alwaysAllow := policyv1.AlwaysAllow
	pod := appPod("my-app", "web-1", "web", "n1")
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	p := pdbAllowing("my-app", "web-pdb", "web", 0, 2)
	p.Status.CurrentHealthy = 1
	p.Spec.UnhealthyPodEvictionPolicy = &alwaysAllow
	client := fakek8s.NewSimpleClientset(userNamespace("my-app"), ngNode("n1", "ng-a"), pod, p)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if hasDetail(result.Details, "web-pdb") {
		t.Errorf("AlwaysAllow PDB must not block a not-Ready pod, got %v", result.Details)
	}
}

// desiredSizeDescriber answers DescribeNodegroup with the given desired size
// per nodegroup, or with err when it is set.
func desiredSizeDescriber(sizes map[string]int32, err error) *mocks.EKSAPI {
	api := mocks.NewEKSAPI().Build()
	api.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		if err != nil {
			return nil, err
		}
		size, ok := sizes[aws.ToString(in.NodegroupName)]
		if !ok {
			return nil, errors.New("ResourceNotFoundException: no such nodegroup")
		}
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			NodegroupName: in.NodegroupName,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(size)},
		}}, nil
	}
	return api
}

// noTargetNodesClient has one unlabelled node running a pod that an unrelated
// zero-disruption PDB covers; no node belongs to ng-a.
func noTargetNodesClient() *fakek8s.Clientset {
	return fakek8s.NewSimpleClientset(
		userNamespace("kube-system"),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		appPod("kube-system", "coredns-1", "coredns", "n1"),
		pdbAllowing("kube-system", "coredns", "coredns", 0, 1),
	)
}

func TestCheckPodDisruptionBudgets_TargetScaledToZeroHasNoBlockers(t *testing.T) {
	// Regression: a nodegroup scaled to 0 has nothing to drain, so an
	// unrelated zero-disruption PDB elsewhere must not warn.
	hc := NewChecker(nil, noTargetNodesClient(), nil, nil)
	hc.ngDescriber = desiredSizeDescriber(map[string]int32{"ng-a": 0, "ng-b": 0}, nil)
	hc.SetTargetNodegroups([]string{"ng-a", "ng-b"})
	result := hc.checkPodDisruptionBudgets(context.Background(), "prod")
	if result.Status != StatusPass {
		t.Errorf("scaled-to-0 targets: status = %s msg = %q, want PASS", result.Status, result.Message)
	}
	if hasDetail(result.Details, "coredns") {
		t.Errorf("coredns must not be reported for an empty target, got %v", result.Details)
	}
	if !hasDetail(result.Details, "have no nodes") {
		t.Errorf("details should say the targets have no nodes, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_TargetsWithNoLabelledNodes(t *testing.T) {
	// ng-a wants 2 nodes but no node carries its label, so the scoped check
	// can't match pods. It fails open with the cluster-wide wording.
	hc := NewChecker(nil, noTargetNodesClient(), nil, nil)
	hc.ngDescriber = desiredSizeDescriber(map[string]int32{"ng-a": 2}, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.checkPodDisruptionBudgets(context.Background(), "prod")
	if result.Status != StatusWarn || !strings.Contains(result.Message, "may block a drain") {
		t.Errorf("unlabelled nodes: status = %s msg = %q, want unscoped WARN", result.Status, result.Message)
	}
	if !hasDetail(result.Details, "kube-system/coredns") {
		t.Errorf("details should name coredns, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_TargetsDescribeFailsFallsBack(t *testing.T) {
	cases := map[string]func(hc *HealthChecker){
		"describe error": func(hc *HealthChecker) {
			hc.ngDescriber = desiredSizeDescriber(nil, errors.New("AccessDeniedException: not authorized"))
		},
		"no EKS client": func(hc *HealthChecker) { hc.ngDescriber = nil },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			hc := NewChecker(nil, noTargetNodesClient(), nil, nil)
			setup(hc)
			hc.SetTargetNodegroups([]string{"ng-a"})
			result := hc.checkPodDisruptionBudgets(context.Background(), "prod")
			if result.Status != StatusWarn || !strings.Contains(result.Message, "may block a drain") {
				t.Errorf("status = %s msg = %q, want unscoped WARN", result.Status, result.Message)
			}
		})
	}
}

func TestCheckPodDisruptionBudgets_TargetsWithNoNodesFallBackUnscoped(t *testing.T) {
	// No node carries the ng-a label (e.g. the nodegroup is scaled to 0 or the
	// label is missing). The scoped check has nothing to match against, so it
	// falls back to the cluster-wide check instead of passing.
	client := fakek8s.NewSimpleClientset(
		userNamespace("my-app"),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		appPod("my-app", "batch-1", "batch", "n1"),
		pdbAllowing("my-app", "batch-pdb", "batch", 0, 1),
	)
	hc := NewChecker(nil, client, nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || !strings.Contains(result.Message, "may block a drain") {
		t.Errorf("no target nodes: status = %s msg = %q, want unscoped WARN", result.Status, result.Message)
	}
	if !hasDetail(result.Details, "my-app/batch-pdb") {
		t.Errorf("details should name batch-pdb, got %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_DefaultNamespaceCounted(t *testing.T) {
	// Regression: deployments in "default" used to be skipped, so a cluster
	// with all its apps in default and no PDBs reported PASS / score 100.
	client := fakek8s.NewSimpleClientset(
		userNamespace("default"),
		deploy("default", "web"),
		deploy("default", "api"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("default namespace: status = %s, want WARN (msg: %s)", result.Status, result.Message)
	}
	if result.Score != 0 {
		t.Errorf("default namespace: score = %d, want 0", result.Score)
	}
}

func TestCheckPodDisruptionBudgets_ManyUnprotected(t *testing.T) {
	// 6 unprotected deployments → triggers the ">5" truncation branch
	objects := []runtime.Object{userNamespace("big-app")}
	for i := range 6 {
		objects = append(objects, deploy("big-app", fmt.Sprintf("svc-%d", i)))
	}
	client := fakek8s.NewSimpleClientset(objects...)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn {
		t.Errorf("many unprotected: status = %s, want WARN", result.Status)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// analyzeResourceDistribution — memory tracking
// ──────────────────────────────────────────────────────────────────────────────

func TestAnalyzeResourceDistribution_MemoryTracked(t *testing.T) {
	hc := &HealthChecker{}
	metrics := []NodeMetrics{
		{CPUPercent: 10, MemoryPercent: 20},
		{CPUPercent: 30, MemoryPercent: 80},
	}
	analysis := hc.analyzeResourceDistribution(metrics)
	if analysis.MinMemory != 20 || analysis.MaxMemory != 80 {
		t.Errorf("memory: MinMemory=%f MaxMemory=%f, want 20/80", analysis.MinMemory, analysis.MaxMemory)
	}
	if analysis.MemoryStdDev <= 0 {
		t.Errorf("expected positive memory std dev for 20/80 split, got %f", analysis.MemoryStdDev)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckCriticalWorkloads — containers-not-ready path
// ──────────────────────────────────────────────────────────────────────────────

func TestCheckCriticalWorkloads_ContainersNotReady(t *testing.T) {
	// A Running pod whose container is not Ready → counts as problem pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "not-ready"},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Ready: false}},
		},
	}
	client := fakek8s.NewSimpleClientset(pod)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckCriticalWorkloads(context.Background())
	// 1 pod total, 0 running ready → score < 90 → FAIL
	if result.Status == StatusPass {
		t.Errorf("not-ready container pod: status = %s, want FAIL or WARN", result.Status)
	}
}

func TestDrainBlockers_ScopedWithoutMutatingChecker(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		ngNode("node-a1", "ng-a"),
		ngNode("node-b1", "ng-b"),
		appPod("my-app", "web-1", "web", "node-a1"),
		appPod("my-app", "batch-1", "batch", "node-b1"),
		pdbAllowing("my-app", "web-pdb", "web", 0, 1),
		pdbAllowing("my-app", "batch-pdb", "batch", 0, 1),
	)
	hc := NewChecker(nil, client, nil, nil)
	report, err := hc.DrainBlockers(context.Background(), "prod", []string{"ng-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Scoped || len(report.Blockers) != 1 || report.Blockers[0].Name != "web-pdb" {
		t.Fatalf("want only web-pdb, scoped; got %+v", report)
	}
	if len(hc.targetNodegroups) != 0 {
		t.Errorf("DrainBlockers must not set the checker's target nodegroups, got %v", hc.targetNodegroups)
	}
	if got := report.Blockers[0].DrainBlockerSummary(); got != "my-app/web-pdb (1/1 pods healthy, 0 disruptions allowed)" {
		t.Errorf("DrainBlockerSummary = %q", got)
	}
}

func TestDrainBlockers_NoKubeClient(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	if _, err := hc.DrainBlockers(context.Background(), "prod", []string{"ng-a"}); !errors.Is(err, ErrNoKubeClient) {
		t.Errorf("want ErrNoKubeClient, got %v", err)
	}
}
