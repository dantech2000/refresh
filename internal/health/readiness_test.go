package health

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// node builds a Node labeled for the given managed nodegroup with the supplied
// Ready condition status (corev1.ConditionTrue/False/Unknown). A node with no
// nodegroup label (ng == "") is left unlabeled.
func node(name, ng string, ready corev1.ConditionStatus) *corev1.Node {
	labels := map[string]string{}
	if ng != "" {
		labels[nodeLabelNodegroup] = ng
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
		},
	}
}

func TestNodegroupReadyCounts_NilClient(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	if _, ok := hc.NodegroupReadyCounts(context.Background()); ok {
		t.Fatal("ok should be false with no Kubernetes client (caller falls back to unknown)")
	}
}

func TestNodegroupReadyCounts_BucketsByNodegroupAndReadiness(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		// ng-a: 2 Ready, 1 NotReady-False → 2 ready of 3
		node("a1", "ng-a", corev1.ConditionTrue),
		node("a2", "ng-a", corev1.ConditionTrue),
		node("a3", "ng-a", corev1.ConditionFalse),
		// ng-b: 1 Ready, 1 Unknown (kubelet not reporting → not ready) → 1 ready of 2
		node("b1", "ng-b", corev1.ConditionTrue),
		node("b2", "ng-b", corev1.ConditionUnknown),
		// ng-c: present but all NotReady → 0 ready, still registered
		node("c1", "ng-c", corev1.ConditionFalse),
		// an unlabeled node (e.g. self-managed/Fargate) is ignored
		node("x1", "", corev1.ConditionTrue),
	)
	hc := NewChecker(nil, client, nil, nil)

	counts, ok := hc.NodegroupReadyCounts(context.Background())
	if !ok {
		t.Fatal("ok should be true when the node list succeeds")
	}
	want := map[string]int32{"ng-a": 2, "ng-b": 1, "ng-c": 0}
	if len(counts) != len(want) {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
	for ng, n := range want {
		if counts[ng] != n {
			t.Errorf("counts[%q] = %d, want %d", ng, counts[ng], n)
		}
	}
	// The unlabeled node must not appear under any nodegroup.
	if _, present := counts[""]; present {
		t.Error("unlabeled node was bucketed under an empty nodegroup key")
	}
}
