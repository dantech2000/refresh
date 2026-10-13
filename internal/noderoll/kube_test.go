package noderoll

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

const region = "us-east-1"

// kubeWorld returns a world with the cluster prod: the nodegroup of the
// tests with two nodes, one node of another nodegroup, and a pod on each.
func kubeWorld() *fleettest.World {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.33", Region: region,
			Nodegroups: []*fleettest.Nodegroup{{Name: ng, Version: "1.33"}, {Name: "general", Version: "1.33"}},
			Nodes: []*fleettest.Node{
				{Name: "a-1", Nodegroup: ng, ImageID: oldAMI},
				{Name: "a-2", Nodegroup: ng, ImageID: oldAMI},
				{Name: "b-1", Nodegroup: "general", ImageID: oldAMI},
			},
			Pods: []*fleettest.Pod{
				{Namespace: "default", Name: "web-1", Node: "a-1"},
				{Namespace: "default", Name: "web-2", Node: "a-2"},
				{Namespace: "default", Name: "api-1", Node: "b-1"},
			},
		}}
	})
	return w
}

// serve starts the Kubernetes API of the world, with a hook for each
// answer, and returns it with a clientset.
func serve(t *testing.T, w *fleettest.World, onAnswer func(kubetest.Request, kubetest.Answer)) (*kubetest.Server, kubernetes.Interface) {
	t.Helper()
	h := kubetest.NewHandler(w, region, "prod")
	h.OnAnswer = onAnswer
	srv := kubetest.Serve(h)
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(srv.Config())
	if err != nil {
		t.Fatal(err)
	}
	return srv, client
}

// requests returns the field selector of every request for op.
func requests(srv *kubetest.Server, op fleettest.Operation) []string {
	var out []string
	for _, r := range srv.Requests() {
		if r.Operation == op {
			out = append(out, r.FieldSelector)
		}
	}
	return out
}

// Against a real API of the fake world: Attach watches, a roll arrives
// through the watches with no further list of the nodes, and the pods are
// listed for the draining nodes alone, each with its field selector.
func TestAttach_WatchesARoll(t *testing.T) {
	w := kubeWorld()
	srv, client := serve(t, w, nil)
	obs, err := Attach(testContext(t), client, ng, Identity{})
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close()
	if !obs.Watching() {
		t.Fatal("the observer lists; want it to watch")
	}
	obs.now = w.Now
	changed := cacheChanges(t, obs)
	s := waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == 2 })
	if s.ReadyTarget != 0 || s.Draining != 0 {
		t.Fatalf("before the roll: %+v", s)
	}
	if got := requests(srv, fleettest.KubeListPods); len(got) != 0 {
		t.Fatalf("pod lists with no draining node: %q", got)
	}
	nodeLists := len(requests(srv, fleettest.KubeListNodes))

	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: 4 * time.Minute} })
	if _, err := w.UpdateNodegroupVersion(region, "prod", ng, fleettest.NodegroupVersionRequest{}); err != nil {
		t.Fatal(err)
	}
	// The new nodes join.
	s = waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == 4 && s.Joining == 2 })
	if nodeOf(s, "a-1").OnTarget || !nodeOf(s, ng+"-u1-1").OnTarget {
		t.Fatalf("old and new: %+v", s.Nodes)
	}
	// The new nodes are Ready, and the old ones are cordoned.
	w.Advance(time.Minute)
	s = waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.ReadyTarget == 2 && s.Draining == 2 })
	if v := nodeOf(s, "a-1"); v.Pods != 1 || v.PodsTotal != 1 || len(v.PodList) != 1 || v.PodList[0].Name != "default/web-1" {
		t.Errorf("a-1 = %+v, want its one pod", v)
	}
	// The cordons arrive one at a time, so a snapshot between them reads
	// the pods of the first node alone. Every list is for one draining
	// node.
	asked := map[string]bool{}
	for _, sel := range requests(srv, fleettest.KubeListPods) {
		asked[sel] = true
	}
	want := map[string]bool{podsOnNodeSelector("a-1"): true, podsOnNodeSelector("a-2"): true}
	if diff := cmp.Diff(want, asked); diff != "" {
		t.Errorf("the selectors of the pod lists (-want +got):\n%s", diff)
	}
	// The pods move. No node changes, and the pods have no watch: the
	// next read of them, after the refresh time, shows it.
	w.Advance(time.Minute)
	s = snapshot(t, obs)
	if v := nodeOf(s, "a-1"); v.Pods != 0 || v.PodsTotal != 1 {
		t.Errorf("a-1 after the drain: %d of %d pods, want 0 of 1", v.Pods, v.PodsTotal)
	}
	// The old nodes go.
	w.Advance(time.Minute)
	s = waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == 2 })
	if s.ReadyTarget != 2 || s.Draining != 0 || s.Joining != 0 {
		t.Errorf("after the roll: %+v", s)
	}
	if got := len(requests(srv, fleettest.KubeListNodes)); got != nodeLists {
		t.Errorf("%d more lists of the nodes during the roll; want none, the watch has them", got-nodeLists)
	}
	for _, sel := range requests(srv, fleettest.KubeListPods) {
		if sel == "" {
			t.Error("a list of the pods of the whole cluster")
		}
	}
}

// denied returns the hook of a server that ends a context at the first
// request that the server refuses, and a sync limit that ends with it: a
// test does not wait 10 seconds for caches that cannot fill.
func denied(t *testing.T) (func(kubetest.Request, kubetest.Answer), syncLimiter) {
	t.Helper()
	refused, refuse := context.WithCancel(context.Background())
	t.Cleanup(refuse)
	var once sync.Once
	onAnswer := func(_ kubetest.Request, a kubetest.Answer) {
		if a.Reason != "" {
			once.Do(refuse)
		}
	}
	limit := func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(refused, cancel)
		return ctx, func() { stop(); cancel() }
	}
	return onAnswer, limit
}

// The caches do not fill, here because the events may not be read: Attach
// goes on with an observer that lists on each snapshot. Its snapshots
// have the nodes, and no warnings.
func TestAttach_ListsWhenTheWatchesDoNotSync(t *testing.T) {
	w := kubeWorld()
	w.Edit(func(s *fleettest.State) { s.Cluster(region, "prod").Nodes[0].Unschedulable = true })
	w.SetFault(fleettest.KubeListEvents, fleettest.Fault{Deny: true})
	w.SetFault(fleettest.KubeWatchEvents, fleettest.Fault{Deny: true})
	onAnswer, limit := denied(t)
	srv, client := serve(t, w, onAnswer)

	obs, err := attach(testContext(t), client, ng, Identity{}, w.Now, limit)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer obs.Close()
	if obs.Watching() {
		t.Fatal("the observer watches; want it to list")
	}
	before := len(requests(srv, fleettest.KubeListNodes))
	s := snapshot(t, obs)
	if s.Total != 2 || s.Draining != 1 || len(s.Warnings) != 0 {
		t.Errorf("snapshot = %+v, want the two nodes, one draining, and no warnings", s)
	}
	if got := len(requests(srv, fleettest.KubeListNodes)); got != before+1 {
		t.Errorf("%d lists of the nodes for one snapshot, want 1", got-before)
	}
	if diff := cmp.Diff([]string{podsOnNodeSelector("a-1")}, requests(srv, fleettest.KubeListPods)); diff != "" {
		t.Errorf("the pod lists (-want +got):\n%s", diff)
	}
	// A node that comes later is new, as for an observer that watches.
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.Nodes = append(c.Nodes, &fleettest.Node{Name: "a-3", Nodegroup: ng, ImageID: newAMI})
	})
	if s = snapshot(t, obs); !nodeOf(s, "a-3").OnTarget || nodeOf(s, "a-2").OnTarget {
		t.Errorf("old and new: %+v", s.Nodes)
	}
}

// Attach fails only when its first list of the nodes fails.
func TestAttach_FailsWhenTheNodesCannotBeListed(t *testing.T) {
	w := kubeWorld()
	w.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
	w.SetFault(fleettest.KubeWatchNodes, fleettest.Fault{Deny: true})
	onAnswer, limit := denied(t)
	_, client := serve(t, w, onAnswer)

	obs, err := attach(testContext(t), client, ng, Identity{}, w.Now, limit)
	if err == nil {
		obs.Close()
		t.Fatal("attach returned no error for nodes that cannot be listed")
	}
	if obs != nil {
		t.Errorf("attach returned an observer with its error: %+v", obs)
	}
}
