package health

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// ──────────────────────────────────────────────────────────────────────────────
// maxFloat
// ──────────────────────────────────────────────────────────────────────────────

func TestMaxFloat(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"single", []float64{42}, 42},
		{"ascending", []float64{1, 2, 3}, 3},
		{"descending", []float64{9, 4, 1}, 9},
		{"max in middle", []float64{1, 88, 3}, 88},
		{"negatives", []float64{-5, -2, -9}, -2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxFloat(tc.in); got != tc.want {
				t.Errorf("maxFloat(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// kubernetesNodeCounts (fake k8s client)
// ──────────────────────────────────────────────────────────────────────────────

func readyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func notReadyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
}

func TestKubernetesNodeCounts_NilClient(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	total, ready, notReady, _, ok := hc.kubernetesNodeCounts(context.Background())
	if ok {
		t.Error("nil client should report ok=false")
	}
	if total != 0 || ready != 0 || notReady != nil {
		t.Errorf("nil client should return zero values, got total=%d ready=%d notReady=%v", total, ready, notReady)
	}
}

func TestKubernetesNodeCounts_AllReady(t *testing.T) {
	client := fakek8s.NewSimpleClientset(readyNode("n1"), readyNode("n2"), readyNode("n3"))
	hc := NewChecker(nil, client, nil, nil)
	total, ready, notReady, _, ok := hc.kubernetesNodeCounts(context.Background())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if total != 3 || ready != 3 {
		t.Errorf("total=%d ready=%d, want 3/3", total, ready)
	}
	if len(notReady) != 0 {
		t.Errorf("expected no NotReady nodes, got %v", notReady)
	}
}

func TestKubernetesNodeCounts_MixedReadiness(t *testing.T) {
	client := fakek8s.NewSimpleClientset(readyNode("n1"), notReadyNode("n2"), readyNode("n3"))
	hc := NewChecker(nil, client, nil, nil)
	total, ready, notReady, _, ok := hc.kubernetesNodeCounts(context.Background())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if total != 3 || ready != 2 {
		t.Errorf("total=%d ready=%d, want 3/2", total, ready)
	}
	if len(notReady) != 1 || notReady[0] != "n2" {
		t.Errorf("notReady = %v, want [n2]", notReady)
	}
}

func TestKubernetesNodeCounts_NoNodes(t *testing.T) {
	client := fakek8s.NewSimpleClientset()
	hc := NewChecker(nil, client, nil, nil)
	total, ready, _, _, ok := hc.kubernetesNodeCounts(context.Background())
	if !ok {
		t.Fatal("an empty-but-reachable cluster should still report ok=true")
	}
	if total != 0 || ready != 0 {
		t.Errorf("total=%d ready=%d, want 0/0", total, ready)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckClusterCapacity / CheckResourceBalance — no CloudWatch client
// ──────────────────────────────────────────────────────────────────────────────

func TestCheckClusterCapacity_NilCloudWatchWarns(t *testing.T) {
	// No cwClient → clusterCPUByInstance errors → capacity check degrades to a
	// non-fatal WARN with a default score rather than panicking.
	hc := NewChecker(nil, nil, nil, nil)
	result := hc.CheckClusterCapacity(context.Background(), "my-cluster")
	if result.Status != StatusWarn {
		t.Errorf("no CloudWatch: status = %s, want WARN", result.Status)
	}
	if result.Score != 70 {
		t.Errorf("no CloudWatch: score = %d, want default 70", result.Score)
	}
	if !result.IsBlocking {
		t.Error("capacity check is declared blocking")
	}
}

func TestCheckResourceBalance_NilCloudWatchWarns(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	result := hc.CheckResourceBalance(context.Background(), "my-cluster")
	if result.Status != StatusWarn {
		t.Errorf("no CloudWatch: status = %s, want WARN", result.Status)
	}
	if result.IsBlocking {
		t.Error("resource balance is warning-level, not blocking")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ListPodDisruptionBudgets
// ──────────────────────────────────────────────────────────────────────────────

func pdbWithStatus(namespace, name string, allowed, healthy, desired, expected int32) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: policyv1.PodDisruptionBudgetStatus{
			DisruptionsAllowed: allowed,
			CurrentHealthy:     healthy,
			DesiredHealthy:     desired,
			ExpectedPods:       expected,
		},
	}
}

func TestListPodDisruptionBudgets_NilClient(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	pdbs, err := hc.ListPodDisruptionBudgets(context.Background())
	if err != nil {
		t.Fatalf("nil client should degrade gracefully, got err: %v", err)
	}
	if pdbs != nil {
		t.Errorf("nil client should return nil slice, got %v", pdbs)
	}
}

func TestListPodDisruptionBudgets_CopiesStatus(t *testing.T) {
	client := fakek8s.NewSimpleClientset(pdbWithStatus("my-app", "frontend", 2, 5, 4, 5))
	hc := NewChecker(nil, client, nil, nil)
	pdbs, err := hc.ListPodDisruptionBudgets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pdbs) != 1 {
		t.Fatalf("expected 1 PDB, got %d: %+v", len(pdbs), pdbs)
	}
	got := pdbs[0]
	if got.Namespace != "my-app" || got.Name != "frontend" {
		t.Errorf("got %s/%s, want my-app/frontend", got.Namespace, got.Name)
	}
	if got.DisruptionsAllowed != 2 || got.CurrentHealthy != 5 || got.DesiredHealthy != 4 || got.ExpectedPods != 5 || got.StatusNotSynced {
		t.Errorf("status fields not copied through: %+v", got)
	}
}

func TestListPodDisruptionBudgets_IncludesSystemNamespaces(t *testing.T) {
	// Regression: the scale dry-run said "nothing constrains this scale-down"
	// while pre-flight reported kube-system/coredns as a drain blocker.
	client := fakek8s.NewSimpleClientset(
		pdbWithStatus("my-app", "frontend", 2, 5, 4, 5),
		pdbWithStatus("kube-system", "coredns", 0, 2, 2, 2),
	)
	hc := NewChecker(nil, client, nil, nil)
	pdbs, err := hc.ListPodDisruptionBudgets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var coredns *PDBInfo
	for i := range pdbs {
		if pdbs[i].Namespace == "kube-system" && pdbs[i].Name == "coredns" {
			coredns = &pdbs[i]
		}
	}
	if len(pdbs) != 2 || coredns == nil {
		t.Fatalf("expected my-app/frontend and kube-system/coredns, got %+v", pdbs)
	}
	if !coredns.AtRisk() {
		t.Errorf("kube-system/coredns should be at risk: %+v", *coredns)
	}
	if r := hc.CheckPodDisruptionBudgets(context.Background()); !hasDetail(r.Details, "kube-system/coredns") {
		t.Errorf("pre-flight and the list should agree on coredns, got %v", r.Details)
	}
}

func TestListPodDisruptionBudgets_MarksUnsyncedStatus(t *testing.T) {
	stale := pdbWithStatus("my-app", "stale", 0, 0, 0, 0)
	stale.Generation, stale.Status.ObservedGeneration = 3, 2
	failed := pdbWithStatus("my-app", "failed", 0, 0, 0, 0)
	failed.Status.Conditions = []metav1.Condition{{
		Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: policyv1.SyncFailedReason,
	}}
	ok := pdbWithStatus("my-app", "ok", 0, 0, 0, 0)
	ok.Status.Conditions = []metav1.Condition{{
		Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: policyv1.InsufficientPodsReason,
	}}
	client := fakek8s.NewSimpleClientset(stale, failed, ok)
	hc := NewChecker(nil, client, nil, nil)
	pdbs, err := hc.ListPodDisruptionBudgets(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]bool{"stale": true, "failed": true, "ok": false}
	for _, p := range pdbs {
		if p.StatusNotSynced != want[p.Name] {
			t.Errorf("%s: StatusNotSynced = %v, want %v", p.Name, p.StatusNotSynced, want[p.Name])
		}
		if p.AtRisk() != want[p.Name] {
			t.Errorf("%s: AtRisk() = %v, want %v", p.Name, p.AtRisk(), want[p.Name])
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// instanceIDsForASGs
// ──────────────────────────────────────────────────────────────────────────────

func TestInstanceIDsForASGs_EmptyNames(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	ids, err := hc.instanceIDsForASGs(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty ASG names should be a no-op, got err: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil ids, got %v", ids)
	}
}

func TestInstanceIDsForASGs_NilClientErrors(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	_, err := hc.instanceIDsForASGs(context.Background(), []string{"asg-1"})
	if err == nil {
		t.Fatal("expected an error when the Auto Scaling client is unavailable")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// checkClusterCapacityWith / checkResourceBalanceWith — scoring branches driven
// from a pre-seeded snapshot (no CloudWatch).
// ──────────────────────────────────────────────────────────────────────────────

// seededSnapshot returns a cpuSnapshot whose CPU-by-instance map is already
// populated, with its sync.Once consumed so get() never touches AWS.
func seededSnapshot(byInstance map[string]float64) *cpuSnapshot {
	s := &cpuSnapshot{byInstance: byInstance}
	s.once.Do(func() {}) // consume Once so the (nil) fetch closure never runs
	return s
}

func TestCheckCapacity_HealthyHeadroomPasses(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 20, "i-2": 30}) // avg 25, peak 30 → ~75% headroom
	result := hc.checkClusterCapacityWith(context.Background(), snap)
	if result.Status != StatusPass {
		t.Errorf("low utilization: status = %s, want PASS (msg: %s)", result.Status, result.Message)
	}
	if result.Score != 100 {
		t.Errorf("score = %d, want 100", result.Score)
	}
}

func TestCheckCapacity_LimitedHeadroomWarns(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 80, "i-2": 80}) // avg 80 → 20% headroom (warn band)
	result := hc.checkClusterCapacityWith(context.Background(), snap)
	if result.Status != StatusWarn {
		t.Errorf("limited headroom: status = %s, want WARN", result.Status)
	}
}

func TestCheckCapacity_InsufficientHeadroomFails(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 90, "i-2": 95}) // avg ~92 → <15% headroom, peak ≥95
	result := hc.checkClusterCapacityWith(context.Background(), snap)
	if result.Status != StatusFail {
		t.Errorf("insufficient headroom: status = %s, want FAIL", result.Status)
	}
}

func TestCheckCapacity_PeakNodeDowngradesPass(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	// Cluster mean is low (lots of idle nodes) but one node is near saturation:
	// the peak-node guard must downgrade the otherwise-PASS verdict to WARN.
	snap := seededSnapshot(map[string]float64{"i-1": 5, "i-2": 5, "i-3": 5, "i-hot": 88})
	result := hc.checkClusterCapacityWith(context.Background(), snap)
	if result.Status != StatusWarn {
		t.Errorf("peak-node guard: status = %s, want WARN", result.Status)
	}
}

func TestCheckResourceBalance_EvenDistributionPasses(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 40, "i-2": 42, "i-3": 41})
	result := hc.checkResourceBalanceWith(context.Background(), snap)
	if result.Status != StatusPass {
		t.Errorf("even, moderate CPU: status = %s, want PASS (msg: %s)", result.Status, result.Message)
	}
}

func TestCheckResourceBalance_HighUtilizationWarns(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 95, "i-2": 95})
	result := hc.checkResourceBalanceWith(context.Background(), snap)
	if result.Status != StatusWarn {
		t.Errorf("high utilization: status = %s, want WARN", result.Status)
	}
}

func TestCheckResourceBalance_UnevenDistributionWarns(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	// Wide spread (std dev > 40 pts) but no single node above the utilization
	// thresholds → uneven-distribution warning branch.
	snap := seededSnapshot(map[string]float64{"i-1": 5, "i-2": 75})
	result := hc.checkResourceBalanceWith(context.Background(), snap)
	if result.Status != StatusWarn {
		t.Errorf("uneven distribution: status = %s, want WARN", result.Status)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// nodeMetricsFromSnapshot
// ──────────────────────────────────────────────────────────────────────────────

func TestNodeMetricsFromSnapshot_MapsInstances(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{"i-1": 10, "i-2": 20})
	metrics, err := hc.nodeMetricsFromSnapshot(context.Background(), snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 node metrics, got %d", len(metrics))
	}
}

func TestNodeMetricsFromSnapshot_EmptyErrors(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	snap := seededSnapshot(map[string]float64{})
	_, err := hc.nodeMetricsFromSnapshot(context.Background(), snap)
	if err == nil {
		t.Fatal("expected an error when no CPU metrics are available")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// PDBInfo.AtRisk
// ──────────────────────────────────────────────────────────────────────────────

func TestPDBInfo_AtRisk(t *testing.T) {
	cases := []struct {
		allowed   int32
		expected  int32
		notSynced bool
		want      bool
	}{
		{-1, 3, false, true},
		{0, 3, false, true},
		{1, 3, false, false},
		{5, 5, false, false},
		// A synced PDB that matches no pods blocks nothing.
		{0, 0, false, false},
		{-1, 0, false, false},
		// An unsynced PDB's ExpectedPods of 0 does not mean "no pods".
		{0, 0, true, true},
		{1, 0, true, false},
	}
	for _, tc := range cases {
		p := PDBInfo{DisruptionsAllowed: tc.allowed, ExpectedPods: tc.expected, StatusNotSynced: tc.notSynced}
		if got := p.AtRisk(); got != tc.want {
			t.Errorf("AtRisk(allowed=%d, expected=%d, notSynced=%v) = %v, want %v", tc.allowed, tc.expected, tc.notSynced, got, tc.want)
		}
	}
}
