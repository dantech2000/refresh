package noderoll

import (
	"testing"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The roll of TestObserver_RollTransitions, with every snapshot served
// from the caches of the watches: the same facts about each node arrive,
// through a watch and with no further list.
func TestObserver_InformerRollTransitions(t *testing.T) {
	other := mkNode("ip-other", newAMI, true, false)
	other.Labels[LabelNodegroup] = "general"
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		mkNode("ip-2", oldAMI, true, false),
		mkNode("ip-3", oldAMI, true, false),
		other,
	)
	obs := watching(t, client, Identity{TargetAMI: newAMI}, newClock())
	changed := cacheChanges(t, obs)

	// Before the roll: three old nodes. The watch of the nodes has the
	// label selector, so ip-other is not in its cache.
	s := waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == 3 })
	if s.ReadyTarget != 0 {
		t.Fatalf("ReadyTarget = %d, want 0: every node is old", s.ReadyTarget)
	}

	// A new node joins, and an old one is cordoned.
	createNode(t, client, mkNode("ip-4", newAMI, false, false))
	cordon(t, client, "ip-1")
	s = waitForSnapshot(t, obs, changed, func(s Snapshot) bool {
		return s.Total == 4 && s.Draining == 1 && s.Joining == 1
	})
	if got := phaseOf(s, "ip-1"); got != PhaseDraining {
		t.Fatalf("ip-1 is %s, want Draining", got)
	}
	if got := phaseOf(s, "ip-4"); got != PhaseJoining {
		t.Fatalf("ip-4 is %s, want Joining", got)
	}

	// The new node is Ready, and the drained node is gone.
	setReady(t, client, "ip-4")
	deleteNode(t, client, "ip-1")
	s = waitForSnapshot(t, obs, changed, func(s Snapshot) bool {
		return s.Total == 3 && s.ReadyTarget == 1 && s.Draining == 0 && s.Joining == 0
	})
	if v := nodeOf(s, "ip-4"); !v.OnTarget || v.Phase != PhaseReady {
		t.Fatalf("ip-4 = %+v, want new and Ready", v)
	}
}

// The other reads of an observer that watches: the Warning events about
// the nodes of the nodegroup come from a cache, and the pods of a
// draining node from a list.
func TestObserver_InformerWarningsAndEviction(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true), // cordoned: draining
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-a", Namespace: "default"},
			Spec:       corev1.PodSpec{NodeName: "ip-1"},
		},
		warning("e1", "FailedDraining", "ip-1", clock.now()),
	)
	obs := watching(t, client, Identity{TargetAMI: newAMI}, clock)
	changed := cacheChanges(t, obs)

	s := waitForSnapshot(t, obs, changed, func(s Snapshot) bool {
		return s.Draining == 1 && len(s.Warnings) == 1
	})
	if s.Warnings[0].Node != "ip-1" || s.Warnings[0].Reason != "FailedDraining" {
		t.Errorf("warning = %+v, want FailedDraining about ip-1", s.Warnings[0])
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 || v.PodsTotal != 1 {
		t.Errorf("ip-1: %d of %d pods, want 1 of 1", v.Pods, v.PodsTotal)
	}
}

// Close on every path: with no watch, twice, and before a read, which
// then lists.
func TestClose_Idempotent(t *testing.T) {
	defer goleak.VerifyNone(t)
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))

	listing(t, client, Identity{}, newClock()).Close() // it never watched

	obs := watching(t, client, Identity{TargetAMI: newAMI}, newClock())
	obs.Close()
	obs.Close() // twice
	if obs.Watching() {
		t.Error("the observer still watches after Close")
	}
	if obs.drains.refresh != 0 {
		t.Errorf("the pod refresh time = %s after Close, want 0: every snapshot lists", obs.drains.refresh)
	}
	if s := snapshot(t, obs); s.Total != 1 {
		t.Fatalf("the snapshot after Close = %+v, want the node, from a list", s)
	}
}

// Close waits for the informers to end. It does not only tell them to:
// when it returns, the run of each informer has ended. The number of
// goroutines is no measure here, because a reflector of client-go starts
// goroutines that its run does not wait for.
func TestClose_WaitsForInformers(t *testing.T) {
	defer goleak.VerifyNone(t)
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	for range 2 {
		obs := watching(t, client, Identity{TargetAMI: newAMI}, newClock())
		f := obs.inf.factories
		informers := []interface{ IsStopped() bool }{
			f[0].Core().V1().Nodes().Informer(),
			f[1].Core().V1().Events().Informer(),
		}
		obs.Close()
		for i, informer := range informers {
			if !informer.IsStopped() {
				t.Fatalf("informer %d still runs after Close returned", i)
			}
		}
	}
}
