package noderoll

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// waitForSnapshot polls obs until pred(snapshot) holds — fake-clientset watch
// events propagate to informer caches asynchronously, so tests must wait for
// the cache rather than assert immediately after a mutation.
func waitForSnapshot(t *testing.T, obs Observer, pred func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last Snapshot
	for time.Now().Before(deadline) {
		s, err := obs.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if pred(s) {
			return s
		}
		last = s
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot never matched predicate; last = %+v", last)
	return Snapshot{}
}

// TestKubeObserver_InformerRollTransitions mirrors the polling roll-transition
// test but serves every snapshot from informer caches: the same per-node
// truths must surface, arriving via watch events instead of List calls.
func TestKubeObserver_InformerRollTransitions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	other := mkNode("ip-other", newAMI, true, false)
	other.Labels[LabelNodegroup] = "general"
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		mkNode("ip-2", oldAMI, true, false),
		mkNode("ip-3", oldAMI, true, false),
		other,
	)
	obs := NewKubeObserver(client, ng, newAMI)
	if err := obs.StartInformers(ctx); err != nil {
		t.Fatalf("StartInformers: %v", err)
	}
	defer obs.StopInformers()

	// t0: steady state — 3 old-AMI nodes; ip-other excluded by the label-scoped
	// node informer.
	s := waitForSnapshot(t, obs, func(s Snapshot) bool { return s.Total == 3 })
	if s.ReadyTarget != 0 {
		t.Fatalf("ReadyTarget = %d, want 0 (all still on old AMI)", s.ReadyTarget)
	}

	// t1: surge a new node (joining) and cordon an old one (draining); the
	// changes must reach snapshots without any further List calls.
	if _, err := client.CoreV1().Nodes().Create(ctx, mkNode("ip-4", newAMI, false, false), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cordon(ctx, t, client, "ip-1")

	s = waitForSnapshot(t, obs, func(s Snapshot) bool {
		return s.Total == 4 && s.Draining == 1 && s.Joining == 1
	})
	if got := phaseOf(s, "ip-1"); got != PhaseDraining {
		t.Fatalf("ip-1 phase = %s, want Draining", got)
	}
	if got := phaseOf(s, "ip-4"); got != PhaseJoining {
		t.Fatalf("ip-4 phase = %s, want Joining", got)
	}

	// t2: new node Ready; drained node terminated.
	setReady(ctx, t, client, "ip-4")
	if err := client.CoreV1().Nodes().Delete(ctx, "ip-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	s = waitForSnapshot(t, obs, func(s Snapshot) bool {
		return s.Total == 3 && s.ReadyTarget == 1 && s.Draining == 0 && s.Joining == 0
	})
	if v := nodeOf(s, "ip-4"); !v.OnTarget || v.Phase != PhaseReady {
		t.Fatalf("ip-4 = %+v, want OnTarget Ready", v)
	}
}

// TestKubeObserver_InformerWarningsAndEviction verifies the cache-backed paths
// for the secondary reads: Warning events scoped to the nodegroup's nodes and
// pod-eviction progress on a draining node.
func TestKubeObserver_InformerWarningsAndEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := metav1.NewTime(time.Now())
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true), // cordoned → Draining
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-a", Namespace: "default"},
			Spec:       corev1.PodSpec{NodeName: "ip-1"},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
			Type:           "Warning",
			Reason:         "FailedDraining",
			Message:        "Cannot evict pod as it would violate the pod's disruption budget",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"},
			LastTimestamp:  now,
		},
	)
	obs := NewKubeObserver(client, ng, newAMI)
	if err := obs.StartInformers(ctx); err != nil {
		t.Fatalf("StartInformers: %v", err)
	}
	defer obs.StopInformers()

	s := waitForSnapshot(t, obs, func(s Snapshot) bool {
		return s.Draining == 1 && len(s.Warnings) == 1
	})
	if s.Warnings[0].Node != "ip-1" || s.Warnings[0].Reason != "FailedDraining" {
		t.Errorf("warning = %+v, want ip-1/FailedDraining", s.Warnings[0])
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 || v.PodsTotal != 1 {
		t.Errorf("ip-1 eviction = %d/%d, want 1/1", v.Pods, v.PodsTotal)
	}
}

// TestStopInformers_Idempotent guards the teardown paths: stop without start,
// double stop, and reads after stop (which must fall back to List calls).
func TestStopInformers_Idempotent(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	obs := NewKubeObserver(client, ng, newAMI)

	obs.StopInformers() // never started
	if err := obs.StartInformers(ctx); err != nil {
		t.Fatalf("StartInformers: %v", err)
	}
	obs.StopInformers()
	obs.StopInformers() // double stop

	s, err := obs.Snapshot(ctx)
	if err != nil || s.Total != 1 {
		t.Fatalf("post-stop snapshot = %+v, %v; want 1 node via List fallback", s, err)
	}
}
