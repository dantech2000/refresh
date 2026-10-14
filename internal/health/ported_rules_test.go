package health_test

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// newNodes replaces the nodes of the harness: the first ready of ng-a are
// Ready and settled, then come broken ones, which are old and not Ready,
// and then the ones that still join: not Ready and a minute old.
func newNodes(ready, broken, joining int) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodes = nil
		add := func(prefix string, n int, node fleettest.Node) {
			for i := range n {
				next := node
				next.Name, next.Nodegroup = prefix+string(rune('1'+i)), "ng-a"
				c.Nodes = append(c.Nodes, &next)
			}
		}
		add("ready-", ready, fleettest.Node{})
		add("broken-", broken, fleettest.Node{NotReady: true})
		add("joining-", joining, fleettest.Node{NotReady: true, CreatedAt: now.Add(-time.Minute)})
	}
}

// Nodes that still join after a scale-up do not count against the 50
// percent rule, and a node whose kubelet stopped is not joining.
func TestNodes_JoiningNodes(t *testing.T) {
	stopped := func(_ *fleettest.State, c *fleettest.Cluster) {
		for _, n := range c.Nodes {
			if n.NotReady {
				n.ReadyReason = "NodeStatusUnknown"
			}
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "two of six ready while four join", edits: []edit{newNodes(2, 0, 4)},
			want: want{outcome: health.Warn, message: "2/6 nodes ready", findings: []string{"joining-1", "joining-2", "joining-3", "joining-4"}}},
		{name: "only joining nodes", edits: []edit{newNodes(0, 0, 4)}, want: want{outcome: health.Warn, message: "no ready nodes yet, 4 joining"}},
		{name: "broken nodes still fail beside joining nodes", edits: []edit{newNodes(1, 3, 2)}, want: want{outcome: health.Fail, message: "1/6 nodes ready, below the 50% minimum"}},
		{name: "half of the settled nodes ready is enough", edits: []edit{newNodes(1, 1, 4)}, want: want{outcome: health.Warn, message: "1/6 nodes ready"}},
		{name: "new nodes whose kubelet stopped are not joining", edits: []edit{newNodes(2, 0, 4), stopped}, want: want{outcome: health.Fail, message: "2/6 nodes ready, below the 50% minimum"}},
		{name: "only new nodes whose kubelet stopped", edits: []edit{newNodes(0, 0, 4), stopped}, want: want{outcome: health.Fail, message: "no ready nodes"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.Nodes()), tt.want)
		})
	}
}

// second is a second budget for the web pods, which run on ng-a.
func second(alwaysAllow bool) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		c.PDBs[0].AlwaysAllow = alwaysAllow
		c.PDBs = append(c.PDBs, fleettest.PDB{
			Namespace: "default", Name: "web-too", Selector: map[string]string{"app": "web"}, AlwaysAllow: alwaysAllow,
			MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2,
		})
	}
}

// A pod that two budgets select blocks a drain, also when it is not Ready
// and the budgets are AlwaysAllow: the eviction API refuses it before it
// reads a policy. A pod that eviction ignores does not count. Without
// targets every such pod of the cluster is reported.
func TestDrainBlockers_PodWithTwoBudgets(t *testing.T) {
	webPods := func(f func(p *fleettest.Pod)) edit {
		return func(_ *fleettest.State, c *fleettest.Cluster) {
			for _, p := range c.Pods {
				if p.Labels["app"] == "web" {
					f(p)
				}
			}
		}
	}
	notReady := webPods(func(p *fleettest.Pod) { p.NotReady = true })
	both := []string{"default/web-1", "default/web-2"}
	tests := []struct {
		name    string
		edits   []edit
		targets []string
		want    want
	}{
		{name: "not ready under AlwaysAllow", edits: []edit{second(true), notReady}, targets: []string{"ng-a"},
			want: want{outcome: health.Fail, message: "2 pods covered by more than one PDB on the target nodegroups", findings: both}},
		{name: "succeeded pods", edits: []edit{second(false), webPods(func(p *fleettest.Pod) { p.Phase = "Succeeded" })}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "pending pods", edits: []edit{second(false), webPods(func(p *fleettest.Pod) { p.Phase = "Pending" })}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "one pending and one running", edits: []edit{second(false), func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods[3].Phase = "Pending" }}, targets: []string{"ng-a"},
			want: want{outcome: health.Fail, message: "1 pod covered by more than one PDB", findings: []string{"default/web-2"}}},
		{name: "without targets", edits: []edit{second(true), notReady},
			want: want{outcome: health.Fail, message: "2 pods covered by more than one PDB; a drain would stall", findings: both}},
		{name: "on another nodegroup", edits: []edit{second(true), notReady}, targets: []string{"ng-b"}, want: want{outcome: health.Pass}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.DrainBlockers(), tt.targets...), tt.want)
		})
	}
}

// A budget in kube-system that allows no disruption, as a coredns budget
// with one replica down, blocks a drain as a budget of a user does. The
// system namespaces are left out of the coverage count only.
func TestDrainBlockers_SystemNamespace(t *testing.T) {
	coredns := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.PDBs = append(c.PDBs, fleettest.PDB{
			Namespace: "kube-system", Name: "coredns", Selector: map[string]string{"k8s-app": "kube-dns"},
			MinAvailable: 2, DisruptionsAllowed: 0, CurrentHealthy: 2, DesiredHealthy: 2, ExpectedPods: 2,
		})
	}
	found := []string{"kube-system/coredns"}
	t.Run("a roll", func(t *testing.T) {
		h := newHarness(t, coredns)
		check(t, h.run(testContext(t), health.DrainBlockers(), "ng-a"), want{outcome: health.Fail, message: "1 PDB allow 0 disruptions on the target nodegroups", findings: found})
	})
	t.Run("without targets", func(t *testing.T) {
		h := newHarness(t, coredns)
		check(t, h.run(testContext(t), health.DrainBlockers()), want{outcome: health.Fail, findings: found})
	})
	t.Run("a scale-down", func(t *testing.T) {
		h := newHarness(t, coredns)
		check(t, h.run(testContext(t), health.ScaleDownBlockers("ng-b", 1)), want{outcome: health.Fail, findings: found})
	})
}

// A scale-down whose pod list fails does not pass the gate: the check is
// Unreadable, which blocks.
func TestScaleDownBlockers_PodListFails(t *testing.T) {
	h := newHarness(t)
	h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
	got := h.run(testContext(t), health.ScaleDownBlockers("ng-b", 1))
	check(t, got, want{outcome: health.Unreadable, message: "could not list the pods of default", failures: 1})
	if !got.Blocking || !got.FailClosed {
		t.Errorf("the result is blocking %t, fail closed %t; want both, so the gate does not pass it", got.Blocking, got.FailClosed)
	}
}

// One shed request among thousands does not warn: seen on a new real
// cluster, 1 of 4348. The warning starts at one percent.
func TestControlPlane_AStray429Passes(t *testing.T) {
	metrics := func(requests, throttled float64) edit {
		return func(s *fleettest.State, _ *fleettest.Cluster) {
			s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/EKS" })
			s.Metrics = append(s.Metrics, eksMetrics("prod", 1<<30, requests, 0, throttled)...)
		}
	}
	tests := []struct {
		name                string
		requests, throttled float64
		want                want
	}{
		{"one of 4348", 4348, 1, want{outcome: health.Pass, message: "etcd 12% of 8 GiB"}},
		{"just below one percent", 10000, 99, want{outcome: health.Pass}},
		{"one percent", 10000, 100, want{outcome: health.Warn, message: "throttling requests (1.0% 429)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, metrics(tt.requests, tt.throttled))
			check(t, h.run(testContext(t), health.ControlPlane()), tt.want)
		})
	}
}

// pendingFor returns a Pending DaemonSet pod that is not bound yet and
// that its node affinity pins to a node by name, as the DaemonSet
// controller makes it.
func pendingFor(name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name},
		Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}},
			}}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func nodeNamed(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// After a real roll, DaemonSet pods made for a node that was terminated
// sat Pending for a moment and failed the verification with exit code 5.
// Such a pod is not bound: its node affinity names the node. It is not
// stuck when that node is gone, and it is when the node is there.
func TestVerify_PendingPodPinnedByAffinity(t *testing.T) {
	ctx := testContext(t)
	before := fake.NewClientset(nodeNamed("old-1"), nodeNamed("old-2"), pendingFor("aws-node-gone-before", "gone-0"))
	base := health.TakeBaseline(ctx, before, "prod")
	if _, ok := base.Pending["kube-system/aws-node-gone-before"]; ok || len(base.Pending) != 0 {
		t.Errorf("baseline = %v; a pod pinned to a node that is gone is not in it", base.Pending)
	}
	tests := []struct {
		name string
		pods []runtime.Object
		want want
	}{
		{"for a node that the roll removed", []runtime.Object{pendingFor("aws-node-old", "old-1")}, want{outcome: health.Pass, message: "no new Pending pods"}},
		{"for a node that is there", []runtime.Object{pendingFor("aws-node-new", "new-1")},
			want{outcome: health.Fail, message: "newly Pending after the roll", findings: []string{"kube-system/aws-node-new"}}},
		{"for both", []runtime.Object{pendingFor("aws-node-old", "old-1"), pendingFor("aws-node-new", "new-1")},
			want{outcome: health.Fail, findings: []string{"kube-system/aws-node-new"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			in := h.input(ctx, "ng-a")
			in.Kube = fake.NewClientset(append([]runtime.Object{nodeNamed("new-1"), nodeNamed("new-2")}, tt.pods...)...)
			results := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), in)
			check(t, results[2], tt.want)
		})
	}
}
