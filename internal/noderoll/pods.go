package noderoll

import (
	"context"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// drainCache is what an observer remembers of the pods of its draining
// nodes.
type drainCache struct {
	// start has the number of pods of each node when it was first seen
	// Draining, so a view can show how many have left.
	start map[string]int
	// counts and lists are the last read: the pods that each draining
	// node still has, for the nodes whose pods could be read. at is when
	// it was read.
	counts map[string]int
	lists  map[string][]PodView
	at     time.Time
	// refresh is the least time between two reads for the same draining
	// nodes. It is 0 for an observer that lists, which reads on every
	// snapshot, and watchPodRefresh for one that watches: its snapshots
	// are frequent, and the pods have no watch.
	refresh time.Duration
}

// fillPodEviction sets the pod fields of each Draining node of snap. A
// node whose pods could not be read keeps them zero.
func (o *Observer) fillPodEviction(ctx context.Context, snap *Snapshot) {
	counts, lists := o.drainingPods(ctx, drainingNames(snap.Nodes))
	if o.drains.start == nil {
		o.drains.start = map[string]int{}
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
		if _, seen := o.drains.start[n.Name]; !seen {
			o.drains.start[n.Name] = cur
		}
		n.Pods, n.PodsTotal, n.PodList = cur, o.drains.start[n.Name], lists[n.Name]
	}
}

// drainingPods returns the pods that each of the draining nodes still
// has, for the nodes whose pods could be read. It makes one list per node,
// with a field selector on the node name, so the cost follows the number
// of draining nodes and not the number of pods of the cluster. It never
// lists the pods of the whole cluster. With a refresh time, a read for
// the same nodes that is younger than it is used again. A read that
// missed a node is not: the next snapshot tries again.
func (o *Observer) drainingPods(ctx context.Context, nodes []string) (map[string]int, map[string][]PodView) {
	d := &o.drains
	if d.refresh > 0 && d.counts != nil && o.now().Sub(d.at) < d.refresh && sameKeys(d.counts, nodes) {
		return d.counts, d.lists
	}
	counts := make(map[string]int, len(nodes))
	lists := make(map[string][]PodView, len(nodes))
	for _, name := range nodes {
		pods, err := o.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: podsOnNodeSelector(name)})
		if err != nil {
			continue
		}
		counts[name] = 0
		for i := range pods.Items {
			p := &pods.Items[i]
			if !isEvictablePod(p) {
				continue
			}
			counts[name]++
			lists[name] = append(lists[name], PodView{Name: p.Namespace + "/" + p.Name, Terminating: p.DeletionTimestamp != nil})
		}
	}
	d.counts, d.lists, d.at = counts, lists, o.now()
	return counts, lists
}

// sameKeys reports whether the keys of m are exactly the names in keys.
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

// podsOnNodeSelector is the field selector for the pods of one node.
func podsOnNodeSelector(node string) string { return "spec.nodeName=" + node }

// isEvictablePod reports whether a drain has to evict the pod. It leaves
// the pod of a DaemonSet and a mirror pod of a static pod alone, and a pod
// that succeeded or failed is gone already.
func isEvictablePod(p *corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false
	}
	if _, mirror := p.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return false
	}
	return !slices.ContainsFunc(p.OwnerReferences, func(ref metav1.OwnerReference) bool { return ref.Kind == "DaemonSet" })
}
