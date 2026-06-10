package health

import (
	"context"
	"fmt"
	"math"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
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
	if analysis.MaxCPU != 0 || analysis.MinCPU != 0 || analysis.CPUVariance != 0 {
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
	if analysis.CPUVariance != 0 {
		t.Errorf("single node variance should be 0, got %f", analysis.CPUVariance)
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
	// Variance = sqrt(((10-20)^2 + (20-20)^2 + (30-20)^2) / 3) = sqrt(200/3) ≈ 8.165
	wantVariance := math.Sqrt(200.0 / 3.0)
	if math.Abs(analysis.CPUVariance-wantVariance) > 0.001 {
		t.Errorf("CPUVariance=%f, want ~%f", analysis.CPUVariance, wantVariance)
	}
}

func TestAnalyzeResourceDistribution_HighVarianceDetected(t *testing.T) {
	hc := &HealthChecker{}
	metrics := []NodeMetrics{
		{CPUPercent: 5},
		{CPUPercent: 95},
	}
	analysis := hc.analyzeResourceDistribution(metrics)
	if analysis.CPUVariance <= 40 {
		t.Errorf("expected high variance for 5/95 split, got %f", analysis.CPUVariance)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// computeBalanceScore
// ──────────────────────────────────────────────────────────────────────────────

func TestComputeBalanceScore(t *testing.T) {
	tests := []struct {
		name           string
		maxVariance    float64
		maxUtilization float64
		want           int
	}{
		{"healthy cluster scores 100", 10, 50, 100},
		{"variance at threshold scores 100", 30, 85, 100},
		{"moderate variance penalized", 40, 50, 80},   // 100 - (40-30)*2
		{"high utilization penalized", 10, 95, 70},    // 100 - (95-85)*3
		{"worse sub-score wins", 40, 95, 70},          // min(80, 70)
		{"extreme variance clamps to 0", 100, 50, 0},  // would be -40 unclamped
		{"max utilization clamps to 0", 10, 130, 0},   // would be -35 unclamped
		{"both extreme clamps to 0", 100, 130, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeBalanceScore(tt.maxVariance, tt.maxUtilization)
			if got != tt.want {
				t.Errorf("computeBalanceScore(%v, %v) = %d, want %d", tt.maxVariance, tt.maxUtilization, got, tt.want)
			}
			if got < 0 || got > 100 {
				t.Errorf("score %d outside [0,100]", got)
			}
		})
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
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
}

func pdb(namespace, name string) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
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
		pdb("my-app", "frontend-pdb"),
	)
	hc := NewChecker(nil, client, nil, nil)
	result := hc.CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusPass {
		t.Errorf("all deployments protected: status = %s, want PASS (got msg: %s)", result.Status, result.Message)
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
	if analysis.MemoryVariance <= 0 {
		t.Errorf("expected positive memory variance for 20/80 split, got %f", analysis.MemoryVariance)
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
