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
	"time"

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
	// Pressure lists active node-pressure conditions (MemoryPressure, etc.) — an
	// advisory layered over Phase. A node can be Ready yet under pressure when the
	// replacement instance is undersized; the roll looks healthy while it isn't.
	Pressure []string `json:"pressure,omitempty"`
}

// Snapshot is the nodegroup's state at one instant, plus roll aggregates.
type Snapshot struct {
	Nodes       []NodeView `json:"nodes"`
	Total       int        `json:"total"`
	ReadyTarget int        `json:"readyTarget"` // Ready nodes already on the target AMI
	Draining    int        `json:"draining"`
	Joining     int        `json:"joining"`
	// Warnings are recent Kubernetes Warning events scoped to this nodegroup's
	// nodes — the "why is a node stuck" signal (failed drain/eviction, sandbox
	// failures) that the coarse lifecycle phases can't show.
	Warnings []WarnEvent `json:"warnings,omitempty"`
}

// WarnEvent is a Kubernetes Warning event scoped to a nodegroup node.
type WarnEvent struct {
	Node    string `json:"node"`    // the nodegroup node it concerns
	Object  string `json:"object"`  // involved object, "Kind/name"
	Reason  string `json:"reason"`  // e.g. FailedDraining, FailedCreatePodSandbox
	Message string `json:"message"` // human detail (truncated for display)
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

	// Best-effort Warning events scoped to this nodegroup's nodes.
	nodeSet := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		nodeSet[list.Items[i].Name] = true
	}
	o.fillWarnings(ctx, &snap, nodeSet)

	// Stable order so renders/golden tests are deterministic.
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	return snap, nil
}

// warningWindow bounds how recent a Warning event must be to surface, so the
// panel shows what's happening during the roll, not stale history.
const warningWindow = 10 * time.Minute

// fillWarnings lists Warning events and keeps those scoped to the nodegroup's
// nodes. Best-effort: a list failure leaves Warnings empty (the panel omits the
// section) rather than failing the snapshot.
func (o *KubeObserver) fillWarnings(ctx context.Context, snap *Snapshot, nodeSet map[string]bool) {
	evList, err := o.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
		Limit:         200,
	})
	if err != nil {
		return
	}
	snap.Warnings = scopeWarnings(evList.Items, nodeSet, time.Now(), warningWindow)
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

// scopeWarnings filters cluster Warning events down to those concerning the
// nodegroup's nodes (involvedObject is one of the nodes, or the event was
// emitted by a kubelet on one of them) and recent enough to matter, deduped by
// node+object+reason keeping the latest. Pure for testability.
func scopeWarnings(events []corev1.Event, nodeSet map[string]bool, now time.Time, window time.Duration) []WarnEvent {
	type key struct{ node, object, reason string }
	latest := make(map[key]corev1.Event)
	order := make([]key, 0)

	for i := range events {
		e := events[i]
		if e.Type != "" && e.Type != corev1.EventTypeWarning {
			continue
		}
		node := warningNode(&e, nodeSet)
		if node == "" {
			continue // not scoped to this nodegroup
		}
		if ts := warningTime(&e); !ts.IsZero() && now.Sub(ts) > window {
			continue // stale
		}
		k := key{node: node, object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, reason: e.Reason}
		if prev, seen := latest[k]; !seen {
			order = append(order, k)
			latest[k] = e
		} else if warningTime(&e).After(warningTime(&prev)) {
			latest[k] = e
		}
	}

	out := make([]WarnEvent, 0, len(order))
	for _, k := range order {
		e := latest[k]
		out = append(out, WarnEvent{
			Node:    k.node,
			Object:  k.object,
			Reason:  e.Reason,
			Message: e.Message,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// warningNode returns the nodegroup node an event concerns, or "" if it isn't
// scoped to one: either the involved object is a Node in the set, or the event
// was emitted by a kubelet (Source.Host) on a node in the set.
func warningNode(e *corev1.Event, nodeSet map[string]bool) string {
	if e.InvolvedObject.Kind == "Node" && nodeSet[e.InvolvedObject.Name] {
		return e.InvolvedObject.Name
	}
	if nodeSet[e.Source.Host] {
		return e.Source.Host
	}
	return ""
}

// warningTime is the event's most recent occurrence (LastTimestamp, falling
// back to EventTime).
func warningTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.EventTime.Time
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
		Pressure: pressureConditions(n),
	}
}

// pressureNodeConditions are the conditions signalling a node is unhealthy under
// load. Advisory during a roll: a node can be Ready yet under one of these when
// the replacement AMI/instance is undersized.
var pressureNodeConditions = []corev1.NodeConditionType{
	corev1.NodeMemoryPressure,
	corev1.NodeDiskPressure,
	corev1.NodePIDPressure,
	corev1.NodeNetworkUnavailable,
}

// pressureConditions returns the names of active (Status=True) pressure
// conditions on a node, in a stable order. Empty when the node is unstressed.
func pressureConditions(n *corev1.Node) []string {
	active := make(map[corev1.NodeConditionType]bool, len(n.Status.Conditions))
	for _, c := range n.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			active[c.Type] = true
		}
	}
	var out []string
	for _, t := range pressureNodeConditions {
		if active[t] {
			out = append(out, string(t))
		}
	}
	return out
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
