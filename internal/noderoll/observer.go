// Package noderoll observes a managed-nodegroup rolling update in real time —
// which nodes are draining, terminating, and coming online — by reconciling
// against live Kubernetes Node state. It is the data source behind the live
// roll panel; rendering lives in internal/rollview.
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
	"k8s.io/apimachinery/pkg/labels"
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
	// PodList names the evictable pods still on a Draining node, when pod
	// accounting is available, so a view can show which pods are left, not
	// only how many.
	PodList []PodView `json:"podList,omitempty"`
	// Pressure lists active node-pressure conditions (MemoryPressure, etc.) — an
	// advisory layered over Phase. A node can be Ready yet under pressure when the
	// replacement instance is undersized; the roll looks healthy while it isn't.
	Pressure []string `json:"pressure,omitempty"`
}

// PodView is one evictable pod on a draining node.
type PodView struct {
	Name        string `json:"name"` // namespace/name
	Terminating bool   `json:"terminating,omitempty"`
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
	// WarningsCapped is the number of cluster Warning events read when the
	// read stopped at its page cap (0 when every event was read). Warnings
	// may then miss events past the cap.
	WarningsCapped int `json:"warningsCapped,omitempty"`
}

// WarnEvent is a Kubernetes Warning event scoped to a nodegroup node.
type WarnEvent struct {
	Node    string `json:"node"`    // the nodegroup node it concerns
	Object  string `json:"object"`  // involved object, "Kind/name"
	Reason  string `json:"reason"`  // e.g. FailedDraining, FailedCreatePodSandbox
	Message string `json:"message"` // human detail (truncated for display)
}

// Observer yields successive snapshots of a roll. Implementations:
// KubeObserver (below) and ScriptedObserver (tests and --simulate).
type Observer interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// KubeObserver reads node state from the cluster's Kubernetes API, scoped to a
// single managed nodegroup. It is read-only against the cluster and safe to
// poll repeatedly, but it is NOT safe for concurrent use: Snapshot and
// CaptureBaseline update per-roll bookkeeping (the baseline, drain-start pod
// counts) without locking. Drive it from a single goroutine, as the live panel
// does.
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
	// podCounts caches the last evictable-pod count per draining node, read at
	// podCountsAt. With podRefresh > 0 (set when watch-backed, where repaints
	// are fast and read a local cache) the per-node pod Lists run at most once
	// per podRefresh; with 0 (polling) every Snapshot re-reads.
	podCounts   map[string]int
	podLists    map[string][]PodView // the evictable pods behind podCounts
	podCountsAt time.Time
	podRefresh  time.Duration
	// warnEvents caches the last cluster Warning-event read, taken at
	// warnAt for the draining set warnDraining; warnCapped records whether
	// that read stopped at the page cap. See fillWarnings.
	warnEvents   []corev1.Event
	warnAt       time.Time
	warnDraining string
	warnCapped   bool
	// inf, when set (StartInformers), serves reads from informer caches fed by
	// watch streams instead of issuing List calls per snapshot.
	inf *informerSet
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
	nodes, err := o.listNodes(ctx)
	if err != nil {
		return err
	}
	o.baseline = make(map[string]bool, len(nodes))
	for _, n := range nodes {
		o.baseline[n.Name] = true
	}
	return nil
}

// listNodes returns the nodegroup's nodes: from the informer cache when
// watching, else via a label-scoped List call.
func (o *KubeObserver) listNodes(ctx context.Context) ([]*corev1.Node, error) {
	if o.inf != nil {
		// The node informer's cache is already scoped to the nodegroup by its
		// factory's label-selector tweak.
		return o.inf.nodes.List(labels.Everything())
	}
	list, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: LabelNodegroup + "=" + o.nodegroup,
	})
	if err != nil {
		return nil, err
	}
	nodes := make([]*corev1.Node, len(list.Items))
	for i := range list.Items {
		nodes[i] = &list.Items[i]
	}
	return nodes, nil
}

// Snapshot lists the nodegroup's nodes and classifies each. Nodes from other
// nodegroups are excluded by the label selector.
func (o *KubeObserver) Snapshot(ctx context.Context) (Snapshot, error) {
	nodes, err := o.listNodes(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	var snap Snapshot
	for _, n := range nodes {
		v := classify(n, o.targetAMI, o.baseline)
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
	nodeSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		nodeSet[n.Name] = true
	}
	o.fillWarnings(ctx, &snap, nodeSet, drainingKey(snap.Nodes))

	// Stable order so renders/golden tests are deterministic.
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].Name < snap.Nodes[j].Name })
	return snap, nil
}

// warningWindow bounds how recent a Warning event must be to surface, so the
// panel shows what's happening during the roll, not stale history.
const warningWindow = 10 * time.Minute

// warningEventRefresh is the minimum interval between Warning-event reads, in
// both watch and polling modes. Paging every cluster Warning event on each
// ~3s poll would multiply API load on a large, noisy cluster for the whole
// roll; warnings explain a stuck node and don't need a faster cadence. A
// change in the draining-node set forces an immediate re-read.
const warningEventRefresh = 15 * time.Second

// warningFieldSelector restricts event reads to Warning events server-side.
const warningFieldSelector = "type=" + corev1.EventTypeWarning

// warningEventPageSize is the page size of the paginated Warning-event List.
const warningEventPageSize = 500

// warningEventMaxPages caps the pages read per Warning-event refresh, which
// bounds one refresh to warningEventMaxPages*warningEventPageSize events.
const warningEventMaxPages = 4

// drainingKey identifies the set of draining nodes (s.Nodes order is not
// relied on).
func drainingKey(nodes []NodeView) string {
	var names []string
	for _, n := range nodes {
		if n.Phase == PhaseDraining {
			names = append(names, n.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// fillWarnings keeps the Warning events scoped to the nodegroup's nodes. It
// re-reads the cluster's events at most every warningEventRefresh, or at once
// when the draining set changes, and reuses the last read in between.
// Best-effort: a failed read reuses the last good read if there is one, else
// leaves Warnings empty (the panel omits the section), and never fails the
// snapshot.
func (o *KubeObserver) fillWarnings(ctx context.Context, snap *Snapshot, nodeSet map[string]bool, draining string) {
	fresh := !o.warnAt.IsZero() && time.Since(o.warnAt) < warningEventRefresh && draining == o.warnDraining
	if !fresh {
		events, capped, err := o.listWarningEvents(ctx)
		if err == nil {
			o.warnEvents, o.warnCapped = events, capped
			o.warnAt, o.warnDraining = time.Now(), draining
		} else if o.warnAt.IsZero() {
			return
		}
	}
	snap.Warnings = scopeWarnings(o.warnEvents, nodeSet, time.Now(), warningWindow)
	if o.warnCapped {
		snap.WarningsCapped = len(o.warnEvents)
	}
}

// listWarningEvents returns cluster Warning events: from the informer cache
// when watching (its factory tweak pre-filters to type=Warning), else via a
// paginated List call capped at warningEventMaxPages. capped reports whether
// the cap cut the read short.
func (o *KubeObserver) listWarningEvents(ctx context.Context) (events []corev1.Event, capped bool, err error) {
	if o.inf != nil {
		ptrs, err := o.inf.events.List(labels.Everything())
		if err != nil {
			return nil, false, err
		}
		events := make([]corev1.Event, len(ptrs))
		for i := range ptrs {
			events[i] = *ptrs[i]
		}
		return events, false, nil
	}
	// Narrow server-side to Warning events and page through them. One
	// unpaged capped List would silently drop an arbitrary subset (items come
	// back in key order, not newest-first); the page cap here is reported so
	// the panel can say so. type=Warning is as narrow as the server allows,
	// because kubelet-emitted pod events are matched by Source.Host, which is
	// not a selectable field.
	opts := metav1.ListOptions{
		FieldSelector: warningFieldSelector,
		Limit:         warningEventPageSize,
	}
	for pages := 1; ; pages++ {
		page, err := o.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		events = append(events, page.Items...)
		if page.Continue == "" {
			return events, false, nil
		}
		if pages >= warningEventMaxPages {
			return events, true, nil
		}
		opts.Continue = page.Continue
	}
}

// fillPodEviction counts the evictable pods on each draining node and records
// the count at drain start, so the panel can show evicted/total. Best-effort:
// a failed read for a node leaves that node's pod fields zero (the panel just
// omits its bar).
func (o *KubeObserver) fillPodEviction(ctx context.Context, snap *Snapshot) {
	counts, lists := o.drainingPodCounts(ctx, snap.Nodes)
	if o.drainStart == nil {
		o.drainStart = make(map[string]int)
	}
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Phase != PhaseDraining {
			continue
		}
		cur, ok := counts[n.Name]
		if !ok {
			continue
		}
		if _, seen := o.drainStart[n.Name]; !seen {
			o.drainStart[n.Name] = cur
		}
		n.Pods = cur
		n.PodsTotal = o.drainStart[n.Name]
		n.PodList = lists[n.Name]
	}
}

// drainingPodCounts returns the evictable-pod count of each draining node
// whose pods could be read. Each read is scoped to one node with a
// spec.nodeName field selector (indexed server-side), so the cost scales with
// the draining set, not with the cluster's pod count. When podRefresh is set,
// a recent result for the same draining set is reused.
func (o *KubeObserver) drainingPodCounts(ctx context.Context, nodes []NodeView) (map[string]int, map[string][]PodView) {
	var draining []string
	for _, n := range nodes {
		if n.Phase == PhaseDraining {
			draining = append(draining, n.Name)
		}
	}
	if o.podRefresh > 0 && o.podCounts != nil && time.Since(o.podCountsAt) < o.podRefresh && sameKeys(o.podCounts, draining) {
		return o.podCounts, o.podLists
	}
	counts := make(map[string]int, len(draining))
	lists := make(map[string][]PodView, len(draining))
	for _, name := range draining {
		pods, err := o.listPodsOnNode(ctx, name)
		if err != nil {
			continue
		}
		c := 0
		for i := range pods {
			if isEvictablePod(&pods[i]) {
				c++
				lists[name] = append(lists[name], PodView{
					Name:        pods[i].Namespace + "/" + pods[i].Name,
					Terminating: pods[i].DeletionTimestamp != nil,
				})
			}
		}
		counts[name] = c
	}
	o.podCounts, o.podLists, o.podCountsAt = counts, lists, time.Now()
	return counts, lists
}

// sameKeys reports whether m holds exactly the names in keys.
func sameKeys(m map[string]int, keys []string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// podsOnNodeSelector is the field selector that scopes a pod List to one node.
func podsOnNodeSelector(node string) string { return "spec.nodeName=" + node }

// listPodsOnNode lists the pods scheduled to node, across all namespaces.
func (o *KubeObserver) listPodsOnNode(ctx context.Context, node string) ([]corev1.Pod, error) {
	list, err := o.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: podsOnNodeSelector(node),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
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
