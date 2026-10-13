package noderoll

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

const (
	oldAMI = "ami-0a1b"
	newAMI = "ami-0f9e"
	ng     = "spot-burst"
)

// clock is the clock of an observer in a test. It moves only when the
// test moves it.
type clock struct{ t time.Time }

func newClock() *clock {
	return &clock{t: time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// testContext bounds every wait of a test, so a missing handshake fails
// the test and does not hang it.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// listing returns an observer that lists on each snapshot: it starts no
// watch. It is what Attach returns when the caches do not sync, built
// without the wait for them.
func listing(t *testing.T, client kubernetes.Interface, id Identity, c *clock) *Observer {
	t.Helper()
	o := &Observer{client: client, nodegroup: ng, id: id, now: c.now}
	if err := o.identify(testContext(t)); err != nil {
		t.Fatalf("the first node list: %v", err)
	}
	return o
}

// watching returns an observer from attach, which must watch. The test
// ends its watches.
func watching(t *testing.T, client kubernetes.Interface, id Identity, c *clock) *Observer {
	t.Helper()
	o, err := attach(testContext(t), client, ng, id, c.now, syncLimit)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(o.Close)
	if !o.Watching() {
		t.Fatal("the observer lists; want it to watch")
	}
	return o
}

// cacheChanges returns a channel that gets a value after each change of
// the caches of o. An informer updates its cache before it tells its
// handlers, so a snapshot that follows a value sees the change.
func cacheChanges(t *testing.T, o *Observer) <-chan struct{} {
	t.Helper()
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { signal() },
		UpdateFunc: func(_, _ any) { signal() },
		DeleteFunc: func(any) { signal() },
	}
	for _, informer := range []cache.SharedIndexInformer{
		o.inf.factories[0].Core().V1().Nodes().Informer(),
		o.inf.factories[1].Core().V1().Events().Informer(),
	} {
		if _, err := informer.AddEventHandler(handler); err != nil {
			t.Fatal(err)
		}
	}
	return changed
}

// waitForSnapshot takes snapshots of o until one holds for pred, and
// waits for a change of the caches between two of them. It does not
// sleep.
func waitForSnapshot(t *testing.T, o *Observer, changed <-chan struct{}, pred func(Snapshot) bool) Snapshot {
	t.Helper()
	ctx := testContext(t)
	for {
		s, err := o.Snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if pred(s) {
			return s
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("no snapshot held; the last one is %+v", s)
		}
	}
}

func mkNode(name, ami string, ready, cordoned bool) *corev1.Node {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{LabelNodegroup: ng, LabelImage: ami},
		},
		Spec: corev1.NodeSpec{Unschedulable: cordoned},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}},
		},
	}
}

// versioned returns a node with a kubelet version and a creation time.
func versioned(name, kubelet string, created time.Time) *corev1.Node {
	n := mkNode(name, "", true, false)
	n.CreationTimestamp = metav1.NewTime(created)
	n.Status.NodeInfo.KubeletVersion = kubelet
	return n
}

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

func cordon(t *testing.T, c *fake.Clientset, name string) {
	t.Helper()
	ctx := testContext(t)
	n, err := c.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Spec.Unschedulable = true
	if _, err := c.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func setReady(t *testing.T, c *fake.Clientset, name string) {
	t.Helper()
	ctx := testContext(t)
	n, err := c.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if _, err := c.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func createNode(t *testing.T, c *fake.Clientset, n *corev1.Node) {
	t.Helper()
	if _, err := c.CoreV1().Nodes().Create(testContext(t), n, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func deleteNode(t *testing.T, c *fake.Clientset, name string) {
	t.Helper()
	if err := c.CoreV1().Nodes().Delete(testContext(t), name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, o *Observer) Snapshot {
	t.Helper()
	s, err := o.Snapshot(testContext(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s
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

var (
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	podsGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
)

// podListRecorder makes the fake clientset apply the spec.nodeName field
// selector of a pod list, which its tracker ignores, and records every
// selector that it was asked for.
type podListRecorder struct {
	mu        sync.Mutex
	selectors []string
}

func (r *podListRecorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.selectors...)
}

func enforcePodFieldSelector(t *testing.T, c *fake.Clientset) *podListRecorder {
	t.Helper()
	rec := &podListRecorder{}
	c.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		la, ok := a.(k8stesting.ListAction)
		if !ok {
			t.Errorf("action %T is not a list", a)
			return false, nil, nil
		}
		sel := la.GetListRestrictions().Fields
		rec.mu.Lock()
		rec.selectors = append(rec.selectors, sel.String())
		rec.mu.Unlock()
		obj, err := c.Tracker().List(podsGVR, podsGVK, la.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		all, ok := obj.(*corev1.PodList)
		if !ok {
			t.Errorf("the tracker returned %T for the pods", obj)
			return false, nil, nil
		}
		out := &corev1.PodList{}
		for _, p := range all.Items {
			if sel.Matches(fields.Set{"spec.nodeName": p.Spec.NodeName}) {
				out.Items = append(out.Items, p)
			}
		}
		return true, out, nil
	})
	return rec
}
