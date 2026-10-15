package kubetest_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A node of no managed nodegroup (self-managed, Karpenter, Fargate,
// hybrid) has no eks.amazonaws.com/nodegroup label, and a selector on
// that label leaves it out. Its own labels are served.
func TestNodeOfNoNodegroup(t *testing.T) {
	w, _, client := newServer(t)
	w.Edit(func(s *fleettest.State) {
		c := s.Clusters[0]
		c.Nodes = append(c.Nodes,
			&fleettest.Node{Name: "self-1"},
			&fleettest.Node{Name: "fargate-1", Labels: map[string]string{"eks.amazonaws.com/compute-type": "fargate"}},
		)
	})
	ctx := testContext(t)
	all, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range all.Items {
		_, has := n.Labels["eks.amazonaws.com/nodegroup"]
		switch n.Name {
		case "self-1", "fargate-1":
			if has {
				t.Errorf("%s has the nodegroup label %v", n.Name, n.Labels)
			}
		default:
			if !has {
				t.Errorf("%s has no nodegroup label", n.Name)
			}
		}
		if n.Name == "fargate-1" && n.Labels["eks.amazonaws.com/compute-type"] != "fargate" {
			t.Errorf("fargate-1 lost its own label: %v", n.Labels)
		}
	}
	managed, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "eks.amazonaws.com/nodegroup"})
	if err != nil {
		t.Fatal(err)
	}
	if len(managed.Items) != 4 {
		t.Errorf("%d nodes carry the nodegroup label, want the 4 managed ones", len(managed.Items))
	}
}

// A budget with maxUnavailable has no minAvailable in its spec.
func TestPDBMaxUnavailable(t *testing.T) {
	w, _, client := newServer(t)
	zero := int32(0)
	w.Edit(func(s *fleettest.State) {
		s.Clusters[0].PDBs[0].MaxUnavailable = &zero
	})
	list, err := client.PolicyV1().PodDisruptionBudgets("default").List(testContext(t), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := list.Items[0].Spec
	if spec.MinAvailable != nil || spec.MaxUnavailable == nil || spec.MaxUnavailable.IntValue() != 0 {
		t.Errorf("spec = minAvailable %v, maxUnavailable %v; want maxUnavailable 0 alone", spec.MinAvailable, spec.MaxUnavailable)
	}
}
