package noderoll

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The pods that a drain has to evict are counted for a draining node. The
// pod of a DaemonSet, a mirror pod, and a pod that ended are left out, and
// the number at the start of the drain stays as the pods leave.
func TestObserver_PodEviction(t *testing.T) {
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true), // cordoned: draining
		pod("web-1", "ip-1", corev1.PodRunning, false, false),
		pod("web-2", "ip-1", corev1.PodRunning, false, false),
		pod("web-3", "ip-1", corev1.PodRunning, false, false),
		pod("kube-proxy-1", "ip-1", corev1.PodRunning, true, false),   // of a DaemonSet
		pod("done-1", "ip-1", corev1.PodSucceeded, false, false),      // ended
		pod("static-1", "ip-1", corev1.PodRunning, false, true),       // a mirror pod
		pod("elsewhere", "ip-other", corev1.PodRunning, false, false), // on another node
	)
	enforcePodFieldSelector(t, client) // the fake ignores the field selector
	obs := listing(t, client, Identity{TargetAMI: newAMI}, newClock())

	n := nodeOf(snapshot(t, obs), "ip-1")
	if n.Phase != PhaseDraining {
		t.Fatalf("ip-1 is %s, want Draining", n.Phase)
	}
	if n.PodsTotal != 3 || n.Pods != 3 {
		t.Fatalf("at the start of the drain: %d of %d pods, want 3 of 3", n.Pods, n.PodsTotal)
	}
	var names []string
	for _, p := range n.PodList {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "default/web-1,default/web-2,default/web-3" {
		t.Fatalf("pod list = %s", got)
	}

	// One pod is evicted: the total stays 3, and 2 are left.
	if err := client.CoreV1().Pods("default").Delete(testContext(t), "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	n = nodeOf(snapshot(t, obs), "ip-1")
	if n.Pods != 2 || n.PodsTotal != 3 {
		t.Fatalf("after the eviction: %d of %d pods, want 2 of 3", n.Pods, n.PodsTotal)
	}
	if len(n.PodList) != 2 {
		t.Fatalf("after the eviction: pod list = %v", n.PodList)
	}
}

// The rule for a pod that a drain has to evict.
func TestIsEvictablePod(t *testing.T) {
	deleting := pod("going", "ip-1", corev1.PodRunning, false, false)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	owned := pod("owned", "ip-1", corev1.PodRunning, false, false)
	owned.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web"}}
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"Running", pod("a", "ip-1", corev1.PodRunning, false, false), true},
		{"Pending", pod("a", "ip-1", corev1.PodPending, false, false), true},
		{"of a ReplicaSet", owned, true},
		{"with a deletion timestamp", deleting, true},
		{"Succeeded", pod("a", "ip-1", corev1.PodSucceeded, false, false), false},
		{"Failed", pod("a", "ip-1", corev1.PodFailed, false, false), false},
		{"of a DaemonSet", pod("a", "ip-1", corev1.PodRunning, true, false), false},
		{"a mirror pod", pod("a", "ip-1", corev1.PodRunning, false, true), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEvictablePod(tt.pod); got != tt.want {
				t.Errorf("isEvictablePod = %v, want %v", got, tt.want)
			}
		})
	}
}

// The pods are listed for each draining node with a field selector on the
// node name. There is never a list of the pods of the whole cluster, and
// no list at all when no node drains.
func TestObserver_PodReadsScopedToDrainingNodes(t *testing.T) {
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true),  // draining
		mkNode("ip-2", oldAMI, true, false), // serving
		mkNode("ip-3", oldAMI, true, true),  // draining
		pod("a", "ip-1", corev1.PodRunning, false, false),
		pod("b", "ip-2", corev1.PodRunning, false, false),
		pod("c", "ip-3", corev1.PodRunning, false, false),
		pod("d", "ip-3", corev1.PodRunning, false, false),
	)
	rec := enforcePodFieldSelector(t, client)
	s := snapshot(t, listing(t, client, Identity{TargetAMI: newAMI}, newClock()))

	want := []string{podsOnNodeSelector("ip-1"), podsOnNodeSelector("ip-3")}
	if diff := cmp.Diff(want, rec.calls()); diff != "" {
		t.Fatalf("the selectors of the pod lists, one per draining node (-want +got):\n%s", diff)
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 || v.PodsTotal != 1 {
		t.Errorf("ip-1: %d of %d pods, want 1 of 1", v.Pods, v.PodsTotal)
	}
	if v := nodeOf(s, "ip-3"); v.Pods != 2 || v.PodsTotal != 2 {
		t.Errorf("ip-3: %d of %d pods, want 2 of 2", v.Pods, v.PodsTotal)
	}
	if v := nodeOf(s, "ip-2"); v.Pods != 0 || v.PodsTotal != 0 {
		t.Errorf("ip-2 serves and has %d of %d pods, want none", v.Pods, v.PodsTotal)
	}

	// No node drains: no pod is read.
	quiet := fake.NewClientset(mkNode("ip-9", oldAMI, true, false))
	qrec := enforcePodFieldSelector(t, quiet)
	snapshot(t, listing(t, quiet, Identity{TargetAMI: newAMI}, newClock()))
	if calls := qrec.calls(); len(calls) != 0 {
		t.Errorf("pod lists with no draining node = %q, want none", calls)
	}
}

// An observer that watches takes snapshots often, so it reads the pods of
// the same draining nodes at most once in its refresh time, and at once
// when the draining nodes change.
func TestObserver_PodCountsThrottledWhenWatching(t *testing.T) {
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true),
		mkNode("ip-2", oldAMI, true, false),
		pod("a", "ip-1", corev1.PodRunning, false, false),
	)
	rec := enforcePodFieldSelector(t, client)
	clock := newClock()
	obs := listing(t, client, Identity{TargetAMI: newAMI}, clock)
	obs.drains.refresh = watchPodRefresh // as for an observer that watches

	for range 3 {
		snapshot(t, obs)
	}
	if n := len(rec.calls()); n != 1 {
		t.Fatalf("%d pod lists over 3 snapshots, want 1", n)
	}

	cordon(t, client, "ip-2") // the draining nodes change: read now
	s := snapshot(t, obs)
	if n := len(rec.calls()); n != 3 {
		t.Fatalf("%d pod lists after the draining nodes changed, want 3", n)
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 {
		t.Errorf("ip-1 has %d pods, want 1", v.Pods)
	}

	// The refresh time passes: read again, once.
	clock.advance(watchPodRefresh)
	snapshot(t, obs)
	snapshot(t, obs)
	if n := len(rec.calls()); n != 5 {
		t.Fatalf("%d pod lists after the refresh time, want 5", n)
	}
}
