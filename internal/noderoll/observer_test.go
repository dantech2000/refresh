package noderoll

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestScopeWarnings(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	recent := metav1.NewTime(now.Add(-2 * time.Minute))
	stale := metav1.NewTime(now.Add(-30 * time.Minute))
	nodeSet := map[string]bool{"ip-1": true, "ip-2": true}

	events := []corev1.Event{
		// Node-involved warning on a nodegroup node — kept.
		{Type: "Warning", Reason: "NodeNotReady", Message: "kubelet stopped posting status",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: recent},
		// Kubelet-emitted (Source.Host) pod warning on a nodegroup node — kept.
		{Type: "Warning", Reason: "FailedCreatePodSandbox", Message: "network plugin not ready",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-x", Namespace: "default"},
			Source:         corev1.EventSource{Host: "ip-2"}, LastTimestamp: recent},
		// Warning on a node NOT in the nodegroup — dropped.
		{Type: "Warning", Reason: "DiskPressure", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "other"}, LastTimestamp: recent},
		// Normal event — dropped.
		{Type: "Normal", Reason: "Starting", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: recent},
		// Stale warning — dropped.
		{Type: "Warning", Reason: "OldNews", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: stale},
	}

	got := scopeWarnings(events, nodeSet, now, 10*time.Minute)
	if len(got) != 2 {
		t.Fatalf("got %d scoped warnings, want 2: %+v", len(got), got)
	}
	// Sorted by node: ip-1 (NodeNotReady) then ip-2 (FailedCreatePodSandbox).
	if got[0].Node != "ip-1" || got[0].Reason != "NodeNotReady" {
		t.Errorf("first = %+v, want ip-1/NodeNotReady", got[0])
	}
	if got[1].Node != "ip-2" || got[1].Reason != "FailedCreatePodSandbox" || got[1].Object != "Pod/web-x" {
		t.Errorf("second = %+v, want ip-2/FailedCreatePodSandbox/Pod/web-x", got[1])
	}
}

func TestScopeWarnings_DedupKeepsLatest(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	nodeSet := map[string]bool{"ip-1": true}
	older := metav1.NewTime(now.Add(-5 * time.Minute))
	newer := metav1.NewTime(now.Add(-1 * time.Minute))
	obj := corev1.ObjectReference{Kind: "Node", Name: "ip-1"}

	got := scopeWarnings([]corev1.Event{
		{Type: "Warning", Reason: "FailedDraining", Message: "older", InvolvedObject: obj, LastTimestamp: older},
		{Type: "Warning", Reason: "FailedDraining", Message: "newer", InvolvedObject: obj, LastTimestamp: newer},
	}, nodeSet, now, 10*time.Minute)

	if len(got) != 1 || got[0].Message != "newer" {
		t.Errorf("dedup should keep the latest message, got %+v", got)
	}
}

// TestClassify_PressureAdvisory verifies node-pressure conditions are reported
// in a stable order, that False conditions are ignored, and that pressure is
// advisory — it never changes a Ready node's phase.
func TestClassify_PressureAdvisory(t *testing.T) {
	n := mkNode("ip-hot", newAMI, true, false)
	// Out-of-order + a False condition that must be ignored.
	n.Status.Conditions = append(n.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
	)
	v := classify(n, newAMI, nil)
	if v.Phase != PhaseReady || !v.Ready {
		t.Fatalf("pressure must not change phase: got phase=%s ready=%v", v.Phase, v.Ready)
	}
	// Canonical order (Memory, Disk, PID, Network); the False PIDPressure is dropped.
	want := []string{"MemoryPressure", "DiskPressure"}
	if !reflect.DeepEqual(v.Pressure, want) {
		t.Errorf("pressure = %v, want %v", v.Pressure, want)
	}
	// Unstressed node reports no pressure.
	if got := classify(mkNode("ip-cool", newAMI, true, false), newAMI, nil).Pressure; got != nil {
		t.Errorf("healthy node should report no pressure, got %v", got)
	}
}

const (
	oldAMI = "ami-0a1b"
	newAMI = "ami-0f9e"
	ng     = "spot-burst"
)

func mkNode(name, ami string, ready, cordoned bool) *corev1.Node {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				LabelNodegroup: ng,
				LabelImage:     ami,
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: cordoned},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}},
		},
	}
}

// TestKubeObserver_ScopesWarningEvents drives the observer against a fake
// cluster with Warning events and asserts only the ones concerning the
// nodegroup's nodes surface on the snapshot.
func TestKubeObserver_ScopesWarningEvents(t *testing.T) {
	now := metav1.NewTime(time.Now())
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
			Type:           "Warning",
			Reason:         "FailedDraining",
			Message:        "Cannot evict pod as it would violate the pod's disruption budget",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"},
			LastTimestamp:  now,
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "default"},
			Type:           "Warning",
			Reason:         "DiskPressure",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-elsewhere"},
			LastTimestamp:  now,
		},
	)
	obs := NewKubeObserver(client, ng, newAMI)
	s, err := obs.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1 (only the nodegroup node): %+v", len(s.Warnings), s.Warnings)
	}
	if s.Warnings[0].Node != "ip-1" || s.Warnings[0].Reason != "FailedDraining" {
		t.Errorf("warning = %+v, want ip-1/FailedDraining", s.Warnings[0])
	}
}

// Drives a fake cluster through a managed-nodegroup AMI roll — no AWS, no live
// cluster — and asserts the observer reports the per-node transitions
// (old → draining → gone, joining → ready) the live panel renders.
func TestKubeObserver_RollTransitions(t *testing.T) {
	ctx := context.Background()

	// Start: 3 old-AMI nodes serving, plus one node from a DIFFERENT nodegroup
	// that must be excluded by the label selector.
	other := mkNode("ip-other", newAMI, true, false)
	other.Labels[LabelNodegroup] = "general"
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		mkNode("ip-2", oldAMI, true, false),
		mkNode("ip-3", oldAMI, true, false),
		other,
	)
	obs := NewKubeObserver(client, ng, newAMI)

	// --- t0: steady state ---------------------------------------------------
	s, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if s.Total != 3 {
		t.Fatalf("Total = %d, want 3 (ip-other excluded by nodegroup label)", s.Total)
	}
	if s.ReadyTarget != 0 {
		t.Fatalf("ReadyTarget = %d, want 0 (all still on old AMI)", s.ReadyTarget)
	}
	for _, n := range s.Nodes {
		if n.Phase != PhaseReady || n.OnTarget {
			t.Fatalf("node %s = %+v, want Ready & not OnTarget", n.Name, n)
		}
	}

	// --- t1: surge a new node (joining) and cordon an old one (draining) ----
	if _, err := client.CoreV1().Nodes().Create(ctx, mkNode("ip-4", newAMI, false, false), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cordon(ctx, t, client, "ip-1")

	s, _ = obs.Snapshot(ctx)
	if s.Total != 4 {
		t.Fatalf("Total = %d, want 4", s.Total)
	}
	if s.Draining != 1 || s.Joining != 1 || s.ReadyTarget != 0 {
		t.Fatalf("counts = draining %d / joining %d / readyTarget %d, want 1 / 1 / 0", s.Draining, s.Joining, s.ReadyTarget)
	}
	if got := phaseOf(s, "ip-1"); got != PhaseDraining {
		t.Fatalf("ip-1 phase = %s, want Draining", got)
	}
	if got := phaseOf(s, "ip-4"); got != PhaseJoining {
		t.Fatalf("ip-4 phase = %s, want Joining", got)
	}

	// --- t2: new node becomes Ready; drained old node is terminated ---------
	setReady(ctx, t, client, "ip-4")
	if err := client.CoreV1().Nodes().Delete(ctx, "ip-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	s, _ = obs.Snapshot(ctx)
	if s.Total != 3 {
		t.Fatalf("Total = %d, want 3 (ip-1 terminated)", s.Total)
	}
	if s.ReadyTarget != 1 || s.Draining != 0 || s.Joining != 0 {
		t.Fatalf("counts = readyTarget %d / draining %d / joining %d, want 1 / 0 / 0", s.ReadyTarget, s.Draining, s.Joining)
	}
	if v := nodeOf(s, "ip-4"); !v.OnTarget || v.Phase != PhaseReady {
		t.Fatalf("ip-4 = %+v, want OnTarget Ready", v)
	}
}

func cordon(ctx context.Context, t *testing.T, c *fake.Clientset, name string) {
	t.Helper()
	n, err := c.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Spec.Unschedulable = true
	if _, err := c.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func setReady(ctx context.Context, t *testing.T, c *fake.Clientset, name string) {
	t.Helper()
	n, err := c.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if _, err := c.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func phaseOf(s Snapshot, name string) Phase { return nodeOf(s, name).Phase }
func nodeOf(s Snapshot, name string) NodeView {
	for _, n := range s.Nodes {
		if n.Name == name {
			return n
		}
	}
	return NodeView{}
}
