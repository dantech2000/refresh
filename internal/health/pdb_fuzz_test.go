package health_test

import (
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// fuzzCluster is a small cluster that a fuzz input describes: the nodes
// of the nodegroup ng, one node of another nodegroup, pods, and budgets.
type fuzzCluster struct {
	nodes   int
	pods    []*corev1.Pod
	budgets []*policyv1.PodDisruptionBudget
	// nilSelector and emptyBlocks are the names of the budgets with no
	// selector, and of the synced budgets with the selector {} that allow
	// no disruption.
	nilSelector, emptyBlocks []string
	// readyOnNodegroup says that a Ready pod that eviction looks at runs
	// on a node of ng.
	readyOnNodegroup bool
}

// newFuzzCluster reads a cluster from the bytes of a fuzz input. Every
// input is a valid cluster: a byte that is missing is zero.
func newFuzzCluster(data []byte) fuzzCluster {
	next := func() int {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return int(b)
	}
	c := fuzzCluster{nodes: 1 + next()%4}
	for i := range next() % 9 {
		b := next()
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: fmt.Sprintf("pod-%d", i), Labels: map[string]string{"app": []string{"a", "b"}[b&1]}},
			// The last node is the one of the other nodegroup.
			Spec:   corev1.PodSpec{NodeName: fmt.Sprintf("node-%d", (b>>1)%(c.nodes+1))},
			Status: corev1.PodStatus{Phase: []corev1.PodPhase{corev1.PodRunning, corev1.PodRunning, corev1.PodPending, corev1.PodSucceeded}[(b>>4)&3]},
		}
		ready := b&0x40 == 0 && pod.Status.Phase == corev1.PodRunning
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
		if ready && pod.Spec.NodeName != fmt.Sprintf("node-%d", c.nodes) {
			c.readyOnNodegroup = true
		}
		c.pods = append(c.pods, pod)
	}
	for i := range next() % 4 {
		b := next()
		allowed := int32((b >> 2) & 3)
		p := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: fmt.Sprintf("pdb-%d", i), Generation: 1},
			Status: policyv1.PodDisruptionBudgetStatus{
				ObservedGeneration: 1, DisruptionsAllowed: allowed, ExpectedPods: 8, CurrentHealthy: 8, DesiredHealthy: 8 - allowed,
			},
		}
		unsynced := b&0x10 != 0
		if unsynced {
			p.Status.ObservedGeneration = 0
		}
		switch b & 3 {
		case 0:
			c.nilSelector = append(c.nilSelector, "default/"+p.Name)
		case 1:
			p.Spec.Selector = &metav1.LabelSelector{}
			if allowed == 0 && !unsynced {
				c.emptyBlocks = append(c.emptyBlocks, "default/"+p.Name)
			}
		default:
			p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": []string{"a", "b"}[b&3-2]}}
		}
		c.budgets = append(c.budgets, p)
	}
	return c
}

// blockers runs the scale-down gate for the removal of some nodes of ng
// and returns the budgets that block it. reversed gives the API server
// the pods and the budgets in the other order.
func (c fuzzCluster) blockers(t *testing.T, remove int32, reversed bool) []string {
	t.Helper()
	var objects []runtime.Object
	for i := range c.nodes + 1 {
		nodegroup := "ng"
		if i == c.nodes {
			nodegroup = "other"
		}
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i), Labels: map[string]string{"eks.amazonaws.com/nodegroup": nodegroup}}})
	}
	pods, budgets := slices.Clone(c.pods), slices.Clone(c.budgets)
	if reversed {
		slices.Reverse(pods)
		slices.Reverse(budgets)
	}
	for _, p := range pods {
		objects = append(objects, p)
	}
	for _, b := range budgets {
		objects = append(objects, b)
	}
	in := &health.Input{
		Snapshot: &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod", Region: east}},
		Kube:     fake.NewClientset(objects...), Now: now,
	}
	got := health.Run(testContext(t), []health.Check{health.ScaleDownBlockers("ng", remove)}, in)[0]
	if got.Outcome != health.Pass && got.Outcome != health.Fail {
		t.Fatalf("removing %d: outcome %s (%s), want Pass or Fail", remove, got.Outcome, got.Message)
	}
	if (got.Outcome == health.Fail) != (len(got.Findings) > 0) {
		t.Fatalf("removing %d: outcome %s with the findings %v", remove, got.Outcome, got.Findings)
	}
	return slices.Sorted(slices.Values(got.Findings))
}

// The scale-down gate on arbitrary clusters (REF-4): it reads every
// cluster, a budget that blocks the removal of k nodes blocks the removal
// of k+1, a budget with no selector never blocks, a synced budget with
// the selector {} that allows nothing blocks when a Ready pod runs on the
// nodegroup, and the answer does not depend on the order of the lists.
func FuzzScaleDownBlockers(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{1, 2, 0x00, 0x02, 1, 0x02}, // two nodes, a pod on each, a budget with room for none
		{3, 4, 0x00, 0x00, 0x02, 0x04, 2, 0x06, 0x01},       // pods packed on one node, a budget that allows one, and {}
		{2, 3, 0x40, 0x20, 0x30, 3, 0x00, 0x11, 0x03},       // a pod that is not ready, a pending one, a done one
		{0, 8, 0, 1, 2, 3, 4, 5, 6, 7, 3, 0x01, 0x02, 0x0b}, // one node
		{1, 4, 0x00, 0x02, 0x01, 0x03, 2, 0x00, 0x06},       // ready pods on both nodes, a budget with no selector that allows nothing
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := newFuzzCluster(data)
		var last []string
		for remove := int32(1); int(remove) <= c.nodes+1; remove++ {
			got := c.blockers(t, remove, false)
			for _, name := range last {
				if !slices.Contains(got, name) {
					t.Errorf("%s blocks the removal of %d node(s) and not of %d", name, remove-1, remove)
				}
			}
			for _, name := range c.nilSelector {
				if slices.Contains(got, name) {
					t.Errorf("removing %d: %s has no selector, which selects no pod, and blocks", remove, name)
				}
			}
			for _, name := range c.emptyBlocks {
				if c.readyOnNodegroup && !slices.Contains(got, name) {
					t.Errorf("removing %d: %s selects every pod and allows nothing, and does not block (%v)", remove, name, got)
				}
			}
			if other := c.blockers(t, remove, true); !slices.Equal(got, other) {
				t.Errorf("removing %d: the blockers depend on the order of the lists: %v and %v", remove, got, other)
			}
			last = got
		}
	})
}
