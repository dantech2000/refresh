package health

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

func appPod(namespace, name, app, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: node},
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
