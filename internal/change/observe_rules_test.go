package change

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// listingObserver sets up a roll of ng-a whose update never ends, with a
// node observer that lists on each snapshot: the Warning events may not
// be read, so the watches never fill, and the timer of the wait for them
// fires at once. The pause between two snapshots ends at once too.
func listingObserver(t *testing.T) (*env, *kubetest.Server, RollRequest) {
	t.Helper()
	e := newEnv(t, rollWorld(neverEnds))
	srv := e.withKube(t)
	// The snapshots come as fast as the server answers, faster than the
	// default client-side limit of client-go, which would wait on the
	// real clock.
	cfg := srv.Config()
	cfg.QPS = -1
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.deps.Kube = func(kube.Target) (*kube.Client, error) { return &kube.Client{Clientset: client}, nil }
	e.world.SetFault(fleettest.KubeListEvents, fleettest.Fault{Deny: true})
	e.world.SetFault(fleettest.KubeWatchEvents, fleettest.Fault{Deny: true})
	// The wait for the watches ends at once; every other timer is held.
	e.deps.AfterFunc = func(d time.Duration, f func()) (stop func() bool) {
		if d == noderoll.SyncLimit {
			f()
			return func() bool { return false }
		}
		return heldTimer(d, f)
	}
	e.deps.Tick = func(time.Duration) <-chan time.Time {
		now := make(chan time.Time, 1)
		now <- e.world.Now()
		return now
	}
	req := rollOf("ng-a")
	req.Policy.Skip, req.Nodes = true, true
	return e, srv, req
}

// observerList reports whether a request is a node list of the observer
// of ng-a: the other node lists of a run read the whole cluster.
func observerList(r kubetest.Request) bool {
	return r.Operation == fleettest.KubeListNodes && strings.Contains(r.LabelSelector, "ng-a")
}

// Failed node reads in the middle of a roll do not end the node view: a
// run of fewer than maxSnapshotErrors failures in a row is passed over,
// and a snapshot that succeeds starts the count again.
func TestRoll_NodeObserverRecoversFromFailedReads(t *testing.T) {
	e, srv, req := listingObserver(t)
	var (
		mu        sync.Mutex
		failing   bool
		inRun     int // failed lists of the run of failures
		denied    int // failed lists in all
		snapshots int
	)
	// After each of the first two snapshots, the next node lists of the
	// observer fail, one fewer than the limit, and the one after them
	// goes through. The hook runs before the fault, so the request that
	// clears it passes. Together the two runs fail more often than the
	// limit, and neither run reaches it.
	srv.OnRequest = func(_ context.Context, r kubetest.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !failing || !observerList(r) {
			return
		}
		if inRun++; inRun == maxSnapshotErrors {
			failing = false
			e.world.SetFault(fleettest.KubeListNodes, fleettest.Fault{})
			return
		}
		denied++
	}
	var notices []string
	res, _ := watched(t, e, req, func(ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Kind {
		case Notice:
			notices = append(notices, ev.Text)
		case NodeProgress:
			if snapshots++; snapshots < 3 {
				failing, inRun = true, 0
				e.world.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
			}
			return snapshots == 3
		}
		return false
	})
	mu.Lock()
	defer mu.Unlock()
	if snapshots != 3 || denied != 2*(maxSnapshotErrors-1) {
		t.Fatalf("%d snapshots, %d failed lists; want the view back after each of two runs of %d failed lists", snapshots, denied, maxSnapshotErrors-1)
	}
	for _, n := range notices {
		if strings.Contains(n, "nodes of nodegroup ng-a") {
			t.Errorf("the observer gave up after runs of %d failed reads: %q", maxSnapshotErrors-1, n)
		}
	}
	if got := statuses(res)["ng-a"]; got != StepInProgress {
		t.Errorf("ng-a = %s, want InProgress: the observer changes nothing in the roll", got)
	}
}

// maxSnapshotErrors failed node reads in a row end the node view, with
// one notice. The roll goes on.
func TestRoll_NodeObserverGivesUpAfterFailedReadsInARow(t *testing.T) {
	e, srv, req := listingObserver(t)
	var (
		mu      sync.Mutex
		failing bool
		denied  int
	)
	srv.OnRequest = func(_ context.Context, r kubetest.Request) {
		mu.Lock()
		defer mu.Unlock()
		if failing && observerList(r) {
			denied++
		}
	}
	const gaveUp = "could not read the nodes of nodegroup ng-a any more; the roll goes on without the node view"
	res, seen := watched(t, e, req, func(ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case ev.Kind == NodeProgress && !failing:
			failing = true
			e.world.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
		case ev.Kind == NodeProgress:
			t.Error("a snapshot after the node lists began to fail")
		case ev.Kind == Notice && strings.Contains(ev.Text, "nodes of nodegroup ng-a"):
			return true
		}
		return false
	})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0].Text != gaveUp {
		t.Fatalf("notices = %+v, want %q once", seen, gaveUp)
	}
	if denied != maxSnapshotErrors {
		t.Errorf("%d failed node lists before the notice, want %d", denied, maxSnapshotErrors)
	}
	if got := statuses(res)["ng-a"]; got != StepInProgress {
		t.Errorf("ng-a = %s, want InProgress: the roll goes on", got)
	}
}
