// Package noderoll observes a managed-nodegroup rolling update in real time —
// which nodes are draining, terminating, and coming online — by reconciling
// against live Kubernetes Node state. It is the data source behind the live
// roll panel; rendering lives in internal/render.
//
// EKS's UpdateNodegroupVersion API only reports one coarse status for the whole
// roll (via DescribeUpdate). The per-node truth comes from the cluster: managed
// nodegroup nodes carry the labels used below, so a label-scoped Node list/watch
// gives us exactly the nodegroup being rolled, classified by AMI and lifecycle.
package noderoll

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// EKS-managed nodegroup node labels.
const (
	// LabelNodegroup scopes nodes to a single managed nodegroup.
	LabelNodegroup = "eks.amazonaws.com/nodegroup"
	// LabelImage is the AMI ID a node was launched with — the signal for
	// old-vs-new during an AMI roll.
	LabelImage = "eks.amazonaws.com/nodegroup-image"
)

// Phase is a node's lifecycle position during a roll.
type Phase string

const (
	PhaseReady    Phase = "Ready"    // serving (Ready, not cordoned)
	PhaseJoining  Phase = "Joining"  // new node not yet Ready
	PhaseDraining Phase = "Draining" // cordoned / tainted for removal
)

// NodeView is one node's observed state.
type NodeView struct {
	Name     string `json:"name"`
	OnTarget bool   `json:"onTarget"` // launched with the target (new) AMI
	Ready    bool   `json:"ready"`
	Phase    Phase  `json:"phase"`
}

// Snapshot is the nodegroup's state at one instant, plus roll aggregates.
type Snapshot struct {
	Nodes       []NodeView `json:"nodes"`
	Total       int        `json:"total"`
	ReadyTarget int        `json:"readyTarget"` // Ready nodes already on the target AMI
	Draining    int        `json:"draining"`
	Joining     int        `json:"joining"`
}

// Observer yields successive snapshots of a roll. Implementations: a
// Kubernetes-backed one (below), an ASG-activities fallback, and a scripted
// one for tests/--simulate.
type Observer interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// KubeObserver reads node state from the cluster's Kubernetes API, scoped to a
// single managed nodegroup. It is read-only and safe to poll.
type KubeObserver struct {
	client    kubernetes.Interface
	nodegroup string
	targetAMI string
}

// NewKubeObserver returns an Observer for nodegroup, treating targetAMI as the
// "new" AMI the roll is moving toward.
func NewKubeObserver(client kubernetes.Interface, nodegroup, targetAMI string) *KubeObserver {
	return &KubeObserver{client: client, nodegroup: nodegroup, targetAMI: targetAMI}
}

// Snapshot lists the nodegroup's nodes and classifies each. Nodes from other
// nodegroups are excluded by the label selector.
func (o *KubeObserver) Snapshot(ctx context.Context) (Snapshot, error) {
	list, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: LabelNodegroup + "=" + o.nodegroup,
	})
	if err != nil {
		return Snapshot{}, err
	}

	var snap Snapshot
	for i := range list.Items {
		v := classify(&list.Items[i], o.targetAMI)
		snap.Nodes = append(snap.Nodes, v)
		snap.Total++
		switch v.Phase {
		case PhaseDraining:
			snap.Draining++
		case PhaseJoining:
			snap.Joining++
		case PhaseReady:
			if v.OnTarget {
				snap.ReadyTarget++
			}
		}
	}
	// Stable order so renders/golden tests are deterministic.
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	return snap, nil
}

// classify derives a node's phase: cordoned/tainted-for-removal => Draining;
// not Ready => Joining; otherwise Ready.
func classify(n *corev1.Node, targetAMI string) NodeView {
	ready := nodeReady(n)
	phase := PhaseReady
	switch {
	case n.Spec.Unschedulable || hasDrainTaint(n):
		phase = PhaseDraining
	case !ready:
		phase = PhaseJoining
	}
	return NodeView{
		Name:     n.Name,
		OnTarget: n.Labels[LabelImage] == targetAMI,
		Ready:    ready,
		Phase:    phase,
	}
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// hasDrainTaint reports whether a node carries a taint indicating it is being
// removed (cluster-autoscaler / drain markers), in addition to the cordon flag.
func hasDrainTaint(n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if strings.HasPrefix(t.Key, "ToBeDeletedByClusterAutoscaler") ||
			strings.HasPrefix(t.Key, "DeletionCandidateOfClusterAutoscaler") ||
			t.Key == "node.kubernetes.io/unschedulable" {
			return true
		}
	}
	return false
}
