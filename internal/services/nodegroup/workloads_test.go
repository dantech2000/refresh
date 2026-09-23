package nodegroup

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// analyzeWorkloads reads the client the caller resolved (from --kubeconfig /
// --kube-context), not a client it builds itself.
func TestAnalyzeWorkloads_UsesGivenClient(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "ip-10-0-0-1",
		Labels: map[string]string{"eks.amazonaws.com/nodegroup": "web"},
	}}
	pod := func(ns, name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec:       corev1.PodSpec{NodeName: node.Name},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	client := fake.NewClientset(
		node,
		pod("shop", "checkout", corev1.PodRunning),
		pod("kube-system", "coredns", corev1.PodRunning),
		pod("shop", "done", corev1.PodSucceeded),
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
	)

	got, ok := analyzeWorkloads(context.Background(), client, "web", nil)
	if !ok {
		t.Fatal("ok = false, want the workloads read from the given client")
	}
	if got.TotalPods != 2 || got.CriticalPods != 1 || got.PodDisruption != "1 PDBs observed" {
		t.Errorf("got %+v, want 2 pods, 1 critical, 1 PDB", got)
	}
}

// Without a client the workloads are unavailable, not read from some other
// cluster.
func TestAnalyzeWorkloads_NilClient(t *testing.T) {
	if _, ok := analyzeWorkloads(context.Background(), nil, "web", []string{"i-1"}); ok {
		t.Error("ok = true with no Kubernetes client")
	}
}
