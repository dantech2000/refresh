package noderoll

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
