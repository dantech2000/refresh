package rollview

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
)

func kn(name string, ready, cordoned bool) *corev1.Node {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{noderoll.LabelNodegroup: "spot-burst"},
		},
		Spec:   corev1.NodeSpec{Unschedulable: cordoned},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}}},
	}
}

// Proves the REAL observer path (KubeObserver over a fake cluster, baseline
// mode) feeds the live panel — no AWS, no live cluster. A mid-roll fake cluster
// renders the expected draining/joining/ready view.
func TestRollPanel_FromKubeObserver(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(
		kn("ip-1", true, false),
		kn("ip-2", true, false),
		kn("ip-3", true, false),
	)
	obs := noderoll.NewKubeObserver(client, "spot-burst", "")
	if err := obs.CaptureBaseline(ctx); err != nil { // ip-1..3 are "old"
		t.Fatal(err)
	}

	// Mid-roll: surge a new node (joining), cordon an old one (draining).
	if _, err := client.CoreV1().Nodes().Create(ctx, kn("ip-4", false, false), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	n1, _ := client.CoreV1().Nodes().Get(ctx, "ip-1", metav1.GetOptions{})
	n1.Spec.Unschedulable = true
	if _, err := client.CoreV1().Nodes().Update(ctx, n1, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	snap, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr := noderoll.NewTracker()
	tr.Observe(snap) // baseline-seed for the panel feed

	th := render.New(render.ColorNone, true)
	m := rollMeta{Nodegroup: "spot-burst", OldAMI: "ami-old", NewAMI: "ami-new", Desired: 3}
	joined := strings.Join(rollPanelLines(th, snap, tr.Recent(6), m), "\n")

	for _, want := range []string{
		"rolling spot-burst",
		"ip-1", "ip-4",
		"draining",           // cordoned old node
		"joining (NotReady)", // new node not ready
		"old",                // baseline nodes are old
		"new",                // ip-4 is new (appeared after baseline)
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel from KubeObserver missing %q in:\n%s", want, joined)
		}
	}
	// ip-4 appeared after the baseline → must be classified on-target (new).
	var ip4 noderoll.NodeView
	for _, n := range snap.Nodes {
		if n.Name == "ip-4" {
			ip4 = n
		}
	}
	if !ip4.OnTarget {
		t.Errorf("ip-4 should be OnTarget (appeared after baseline): %+v", ip4)
	}
}
