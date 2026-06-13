package noderoll

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(name, node string, phase corev1.PodPhase, daemonset, mirror bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
	if daemonset {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "kube-proxy"}}
	}
	if mirror {
		p.Annotations = map[string]string{"kubernetes.io/config.mirror": "abc"}
	}
	return p
}

// Real pod-eviction accounting on a fake cluster (no AWS): a draining node's
// evictable pods are counted, DaemonSet/mirror/terminal pods excluded, and the
// drain-start total is remembered as pods leave.
func TestKubeObserver_PodEviction(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true), // cordoned -> draining
		// On ip-1: 3 evictable workload pods + a DaemonSet pod + a terminal pod.
		pod("web-1", "ip-1", corev1.PodRunning, false, false),
		pod("web-2", "ip-1", corev1.PodRunning, false, false),
		pod("web-3", "ip-1", corev1.PodRunning, false, false),
		pod("kube-proxy-1", "ip-1", corev1.PodRunning, true, false),   // DaemonSet -> excluded
		pod("done-1", "ip-1", corev1.PodSucceeded, false, false),      // terminal -> excluded
		pod("static-1", "ip-1", corev1.PodRunning, false, true),       // mirror -> excluded
		pod("elsewhere", "ip-other", corev1.PodRunning, false, false), // different node
	)
	obs := NewKubeObserver(client, ng, newAMI)

	s, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := nodeOf(s, "ip-1")
	if n.Phase != PhaseDraining {
		t.Fatalf("ip-1 phase = %s, want Draining", n.Phase)
	}
	if n.PodsTotal != 3 || n.Pods != 3 {
		t.Fatalf("drain start: Pods=%d PodsTotal=%d, want 3/3 (DaemonSet/mirror/terminal/other excluded)", n.Pods, n.PodsTotal)
	}

	// Evict one workload pod; total stays at the remembered start (3), remaining drops to 2.
	if err := client.CoreV1().Pods("default").Delete(ctx, "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	s, _ = obs.Snapshot(ctx)
	n = nodeOf(s, "ip-1")
	if n.Pods != 2 || n.PodsTotal != 3 {
		t.Fatalf("after eviction: Pods=%d PodsTotal=%d, want 2/3", n.Pods, n.PodsTotal)
	}
}
