package kubetest_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const region = "us-east-1"

// newWorld returns a world with one cluster: two nodegroups of two nodes, a
// few pods, a PDB, a deployment, and two events.
func newWorld() *fleettest.World {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.32", Region: region,
			Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}, {Name: "ng-b", Version: "1.32"}},
			Nodes: []*fleettest.Node{
				{Name: "a-1", Nodegroup: "ng-a", ImageID: "ami-old", UsedMilliCPU: 500},
				{Name: "a-2", Nodegroup: "ng-a", ImageID: "ami-old"},
				{Name: "b-1", Nodegroup: "ng-b", ImageID: "ami-new", NotReady: true},
				{Name: "b-2", Nodegroup: "ng-b", ImageID: "ami-new", Unschedulable: true},
			},
			Pods: []*fleettest.Pod{
				{Namespace: "default", Name: "web-1", Node: "a-1", Labels: map[string]string{"app": "web"}},
				{Namespace: "default", Name: "web-2", Node: "a-2", Labels: map[string]string{"app": "web"}},
				{Namespace: "default", Name: "job-1", Phase: "Pending"},
				{Namespace: "kube-system", Name: "coredns-1", Node: "b-1", NotReady: true},
			},
			PDBs:        []fleettest.PDB{{Namespace: "default", Name: "web", Selector: map[string]string{"app": "web"}, MinAvailable: 2, CurrentHealthy: 2, DesiredHealthy: 2, ExpectedPods: 2}},
			Deployments: []fleettest.Deployment{{Namespace: "default", Name: "web", Replicas: 2, Selector: map[string]string{"app": "web"}}},
			Events: []fleettest.Event{
				{Namespace: "default", Name: "e-1", Type: "Warning", Reason: "FailedScheduling", Kind: "Pod", Object: "job-1"},
				{Namespace: "default", Name: "e-2", Type: "Normal", Reason: "Scheduled", Kind: "Pod", Object: "web-1"},
			},
		}}
	})
	return w
}

func newServer(t *testing.T) (*fleettest.World, *kubetest.Server, kubernetes.Interface) {
	t.Helper()
	w := newWorld()
	srv := kubetest.NewServer(w, region, "prod")
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(srv.Config())
	if err != nil {
		t.Fatal(err)
	}
	return w, srv, client
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestLists makes each list request the legacy binary makes, with a real
// clientset, and checks what the client decodes.
func TestLists(t *testing.T) {
	_, srv, client := newServer(t)
	ctx := testContext(t)
	tests := []struct {
		name string
		list func() ([]string, error)
		want []string
	}{
		{"namespaces", func() ([]string, error) {
			l, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
			return names(l.Items, func(n corev1.Namespace) string { return n.Name }), err
		}, []string{"default", "kube-node-lease", "kube-public", "kube-system"}},
		{"every node", func() ([]string, error) {
			l, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			return names(l.Items, func(n corev1.Node) string {
				return n.Name + " " + n.Labels["eks.amazonaws.com/nodegroup-image"] + " ready=" + string(n.Status.Conditions[0].Status) + " cordoned=" + boolText(n.Spec.Unschedulable)
			}), err
		}, []string{"a-1 ami-old ready=True cordoned=false", "a-2 ami-old ready=True cordoned=false", "b-1 ami-new ready=False cordoned=false", "b-2 ami-new ready=True cordoned=true"}},
		{"the nodes of one nodegroup", func() ([]string, error) {
			l, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "eks.amazonaws.com/nodegroup=ng-b"})
			return names(l.Items, func(n corev1.Node) string { return n.Name }), err
		}, []string{"b-1", "b-2"}},
		{"the nodes of several nodegroups", func() ([]string, error) {
			l, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "eks.amazonaws.com/nodegroup in (ng-a,ng-z)"})
			return names(l.Items, func(n corev1.Node) string { return n.Name }), err
		}, []string{"a-1", "a-2"}},
		{"the pods of one namespace", func() ([]string, error) {
			l, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
			return names(l.Items, func(p corev1.Pod) string {
				return p.Name + " " + string(p.Status.Phase) + " ready=" + boolText(p.Status.ContainerStatuses[0].Ready)
			}), err
		}, []string{"coredns-1 Running ready=false"}},
		{"the pods on one node", func() ([]string, error) {
			l, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=a-1"})
			return names(l.Items, func(p corev1.Pod) string { return p.Namespace + "/" + p.Name }), err
		}, []string{"default/web-1"}},
		{"the pending pods", func() ([]string, error) {
			l, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Pending"})
			return names(l.Items, func(p corev1.Pod) string { return p.Name }), err
		}, []string{"job-1"}},
		{"PDBs", func() ([]string, error) {
			l, err := client.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			p := l.Items[0]
			return []string{p.Namespace + "/" + p.Name + " " + p.Spec.MinAvailable.String() + " " + p.Spec.Selector.MatchLabels["app"] + " allowed=" + fmt.Sprint(p.Status.DisruptionsAllowed)}, nil
		}, []string{"default/web 2 web allowed=0"}},
		{"deployments", func() ([]string, error) {
			l, err := client.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return []string{l.Items[0].Name + " " + fmt.Sprint(*l.Items[0].Spec.Replicas)}, nil
		}, []string{"web 2"}},
		{"warning events", func() ([]string, error) {
			l, err := client.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: "type=Warning", Limit: 500})
			return names(l.Items, func(e corev1.Event) string { return e.Reason + " " + e.InvolvedObject.Name }), err
		}, []string{"FailedScheduling job-1"}},
		{"node metrics", func() ([]string, error) {
			mc, err := metricsclient.NewForConfig(srv.Config())
			if err != nil {
				return nil, err
			}
			l, err := mc.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			return []string{l.Items[0].Name + " " + l.Items[0].Usage.Cpu().String()}, nil
		}, []string{"a-1 500m"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.list()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}

	logged := srv.Requests()
	if len(logged) != len(tests) {
		t.Fatalf("the server logged %d requests, want %d", len(logged), len(tests))
	}
	want := kubetest.Request{ID: 6, Operation: fleettest.KubeListPods, FieldSelector: "spec.nodeName=a-1"}
	if diff := cmp.Diff(want, logged[5]); diff != "" {
		t.Errorf("the sixth logged request (-want +got):\n%s", diff)
	}
}

func names[T any](items []T, name func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, name(it))
	}
	return out
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestErrors checks what the server refuses, and the error client-go makes
// of it.
func TestErrors(t *testing.T) {
	w, srv, client := newServer(t)
	ctx := testContext(t)
	var observed []string
	srv.OnAnswer = func(r kubetest.Request, a kubetest.Answer) {
		observed = append(observed, string(r.Operation)+": "+a.Reason+a.Observed)
	}

	w.SetFault(fleettest.KubeListPDBs, fleettest.Fault{Deny: true})
	if _, err := client.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("a denied list returned %v, want Forbidden", err)
	}
	w.SetScopedFault(fleettest.Scope{Service: "kube", Cluster: "prod"}, fleettest.Fault{Unauthenticated: true})
	if _, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); !apierrors.IsUnauthorized(err) {
		t.Errorf("a list with rejected credentials returned %v, want Unauthorized", err)
	}
	w.SetScopedFault(fleettest.Scope{Service: "kube", Cluster: "prod"}, fleettest.Fault{})
	if _, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.hostNetwork=true"}); !apierrors.IsBadRequest(err) {
		t.Errorf("a field selector the server does not know returned %v, want BadRequest", err)
	}
	if _, err := client.CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("a resource the server does not serve returned %v, want NotFound", err)
	}
	if _, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err != nil {
		t.Error(err)
	}

	want := []string{
		"kube:ListPodDisruptionBudgets: Forbidden",
		"kube:ListNodes: Unauthorized",
		"kube:ListPods: BadRequest",
		"kube:ListNamespaces: default kube-node-lease kube-public kube-system",
	}
	if diff := cmp.Diff(want, observed); diff != "" {
		t.Errorf("answers (-want +got):\n%s", diff)
	}
}

// TestInformer runs client-go's shared informers, as the legacy live view
// builds them, and moves the world under them: a node roll must arrive as
// adds, updates, and deletes, with no change to the client.
func TestInformer(t *testing.T) {
	w, _, client := newServer(t)
	ctx, stop := context.WithCancel(testContext(t))

	nodeFactory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
		lo.LabelSelector = "eks.amazonaws.com/nodegroup=ng-a"
	}))
	eventFactory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
		lo.FieldSelector = "type=Warning"
	}))
	changes := make(chan string, 100)
	nodes := nodeFactory.Core().V1().Nodes()
	if _, err := nodes.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { changes <- "add " + nodeText(obj) },
		UpdateFunc: func(_, obj any) { changes <- "update " + nodeText(obj) },
		DeleteFunc: func(obj any) { changes <- "delete " + nodeText(obj) },
	}); err != nil {
		t.Fatal(err)
	}
	events := eventFactory.Core().V1().Events()
	if _, err := events.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if e, ok := obj.(*corev1.Event); ok {
				changes <- "event " + e.Reason
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	nodeFactory.Start(ctx.Done())
	eventFactory.Start(ctx.Done())
	defer func() {
		stop() // Shutdown waits for the informers, which run until ctx ends
		nodeFactory.Shutdown()
		eventFactory.Shutdown()
	}()
	if !cache.WaitForCacheSync(ctx.Done(), nodes.Informer().HasSynced, events.Informer().HasSynced) {
		t.Fatal("the informer caches did not sync")
	}
	// next collects n changes, in an order that does not depend on which
	// informer delivered first.
	next := func(n int) []string {
		t.Helper()
		var got []string
		for range n {
			select {
			case c := <-changes:
				got = append(got, c)
			case <-ctx.Done():
				t.Fatalf("got %v, then no more changes", got)
			}
		}
		slices.Sort(got)
		return got
	}
	if diff := cmp.Diff([]string{"add a-1 Ready", "add a-2 Ready", "event FailedScheduling"}, next(3)); diff != "" {
		t.Fatalf("the initial list (-want +got):\n%s", diff)
	}

	// Roll ng-a over four minutes of world time.
	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: 4 * time.Minute} })
	if _, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		want []string
	}{
		{"the new nodes join", []string{"add ng-a-u1-1 NotReady", "add ng-a-u1-2 NotReady"}},
		{"the new nodes are Ready and the old are cordoned", []string{"update a-1 Ready cordoned", "update a-2 Ready cordoned", "update ng-a-u1-1 Ready", "update ng-a-u1-2 Ready"}},
		{"the pods move", nil},
		{"the old nodes go", []string{"delete a-1 Ready cordoned", "delete a-2 Ready cordoned"}},
	}
	for i, step := range steps {
		if i > 0 {
			w.Advance(time.Minute)
		}
		if diff := cmp.Diff(step.want, next(len(step.want))); diff != "" {
			t.Fatalf("%s (-want +got):\n%s", step.name, diff)
		}
	}
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.Events = append(c.Events, fleettest.Event{Namespace: "default", Name: "e-3", Type: "Warning", Reason: "FailedEviction", Kind: "Pod", Object: "web-1"})
	})
	if diff := cmp.Diff([]string{"event FailedEviction"}, next(1)); diff != "" {
		t.Errorf("a new warning event (-want +got):\n%s", diff)
	}

	// The lister reads the same cache.
	cached, err := nodes.Lister().List(labels.Everything())
	if err != nil {
		t.Fatal(err)
	}
	if got := names(cached, func(n *corev1.Node) string { return n.Name }); len(got) != 2 {
		t.Errorf("the lister has %v, want the two new nodes", got)
	}
}

func nodeText(obj any) string {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	n, ok := obj.(*corev1.Node)
	if !ok {
		return "?"
	}
	text := n.Name + " Ready"
	if n.Status.Conditions[0].Status != corev1.ConditionTrue {
		text = n.Name + " NotReady"
	}
	if n.Spec.Unschedulable {
		text += " cordoned"
	}
	return text
}

// TestKubeconfig checks that the kubeconfig the server writes loads with
// client-go's own loader and reaches the server, and that the world reports
// the server as the cluster's endpoint.
func TestKubeconfig(t *testing.T) {
	w, srv, _ := newServer(t)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := srv.WriteKubeconfig(path); err != nil {
		t.Fatal(err)
	}
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.CurrentContext != "prod" || raw.Clusters["prod"].Server != srv.URL {
		t.Errorf("kubeconfig context %q with server %q, want prod and %s", raw.CurrentContext, raw.Clusters["prod"].Server, srv.URL)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Namespaces().List(testContext(t), metav1.ListOptions{}); err != nil {
		t.Error(err)
	}
	w.View(func(s *fleettest.State) {
		if got := s.Cluster(region, "prod").Endpoint; got != srv.URL {
			t.Errorf("the cluster's endpoint is %q, want the server %s", got, srv.URL)
		}
	})
}
