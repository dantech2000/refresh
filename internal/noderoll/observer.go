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
	// Pods/PodsTotal track eviction progress while a node is Draining (0 when
	// not draining or when pod accounting isn't available).
	Pods      int `json:"pods,omitempty"`
	PodsTotal int `json:"podsTotal,omitempty"`
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
	// baseline, when set, holds the node names present at roll start; any node
	// NOT in it is treated as "on target" (new). This makes old-vs-new robust
	// for live rolls where the target AMI ID isn't known up front. nil → fall
	// back to the nodegroup-image AMI label.
	baseline map[string]bool
	// drainStart remembers the evictable-pod count when a node first appears
	// Draining, so the panel can show evicted/total as pods leave.
	drainStart map[string]int
}

// NewKubeObserver returns an Observer for nodegroup, treating targetAMI as the
// "new" AMI the roll is moving toward.
func NewKubeObserver(client kubernetes.Interface, nodegroup, targetAMI string) *KubeObserver {
	return &KubeObserver{client: client, nodegroup: nodegroup, targetAMI: targetAMI}
}

// CaptureBaseline records the nodegroup's current node set as "old" so that
// nodes appearing afterward count as the new (on-target) nodes — for live rolls
// where the target AMI ID isn't known in advance.
func (o *KubeObserver) CaptureBaseline(ctx context.Context) error {
	list, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: LabelNodegroup + "=" + o.nodegroup,
	})
	if err != nil {
		return err
	}
	o.baseline = make(map[string]bool, len(list.Items))
	for i := range list.Items {
		o.baseline[list.Items[i].Name] = true
	}
	return nil
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
		v := classify(&list.Items[i], o.targetAMI, o.baseline)
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
	// Best-effort pod-eviction progress for draining nodes.
	if snap.Draining > 0 {
		o.fillPodEviction(ctx, &snap)
	}

	// Stable order so renders/golden tests are deterministic.
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	return snap, nil
}

// fillPodEviction counts the evictable pods on each draining node and records
// the count at drain start, so the panel can show evicted/total. Best-effort:
// a list failure leaves the pod fields zero (the panel just omits the bar).
func (o *KubeObserver) fillPodEviction(ctx context.Context, snap *Snapshot) {
	pods, err := o.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	counts := make(map[string]int)
	for i := range pods.Items {
		p := &pods.Items[i]
		if isEvictablePod(p) {
			counts[p.Spec.NodeName]++
		}
	}
	if o.drainStart == nil {
		o.drainStart = make(map[string]int)
	}
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Phase != PhaseDraining {
			continue
		}
		cur := counts[n.Name]
		if _, seen := o.drainStart[n.Name]; !seen {
			o.drainStart[n.Name] = cur
		}
		n.Pods = cur
		n.PodsTotal = o.drainStart[n.Name]
	}
}

// isEvictablePod reports whether a pod counts toward drain progress: DaemonSet
// pods and static/mirror pods aren't drained, and terminal pods are already
// gone.
func isEvictablePod(p *corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false
	}
	if _, mirror := p.Annotations["kubernetes.io/config.mirror"]; mirror {
		return false
	}
	for _, ref := range p.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return false
		}
	}
	return true
}

// classify derives a node's phase: cordoned/tainted-for-removal => Draining;
// not Ready => Joining; otherwise Ready. onTarget comes from the baseline set
// when present (node appeared after roll start), else the nodegroup-image AMI
// label.
func classify(n *corev1.Node, targetAMI string, baseline map[string]bool) NodeView {
	ready := nodeReady(n)
	phase := PhaseReady
	switch {
	case n.Spec.Unschedulable || hasDrainTaint(n):
		phase = PhaseDraining
	case !ready:
		phase = PhaseJoining
	}
	onTarget := n.Labels[LabelImage] == targetAMI
	if baseline != nil {
		onTarget = !baseline[n.Name]
	}
	return NodeView{
		Name:     n.Name,
		OnTarget: onTarget,
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
