package noderoll

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// Identity says how to tell the old nodes of a roll from the new ones.
type Identity struct {
	// TargetVersion is the Kubernetes minor version that an upgrade or a
	// rollback roll moves the nodes to, such as "1.35". It is "" for a
	// roll that keeps the version: a new AMI.
	TargetVersion string
	// TargetAMI is the ID of the AMI that the roll moves to, or "" when it
	// is not known. Attach does not tell the nodes apart by it: see
	// Attach.
	TargetAMI string
	// StartedAt is when the roll began. The zero time means now: the roll
	// begins as the observer attaches.
	StartedAt time.Time
}

// Observer gives snapshots of the nodes of one nodegroup. It only reads.
// It is not safe for concurrent use: a snapshot updates what the observer
// remembers of the roll. One goroutine drives it.
type Observer struct {
	client    kubernetes.Interface
	nodegroup string
	id        Identity
	// now is the clock: time.Now, or the clock of a test.
	now func() time.Time

	// baseline holds the names of the old nodes: see Attach.
	baseline map[string]bool
	// byVersion says that the kubelet version of a node decides whether it
	// is new. Attach sets it once.
	byVersion bool

	// inf serves the nodes and the Warning events from the caches of two
	// watches. It is nil when the observer lists on each snapshot.
	inf *informerSet

	drains   drainCache
	warnings warningCache
}

// Attach returns an observer for the nodes of nodegroup. The caller must
// call Close.
//
// It starts a watch of the nodes and one of the Warning events, and
// serves the snapshots from their caches. When the caches do not sync
// within SyncLimit, as with credentials that may list and not watch, it
// lists on each snapshot: the watch makes the observer faster, and nothing
// depends on it. Attach fails only when its first list of the nodes fails.
//
// Attach chooses once how a node counts as new, from that first list. The
// first row that matches decides.
//
//  1. id.TargetVersion is set, and a node runs a kubelet of another minor
//     version: a node is new when its kubelet runs TargetVersion. A node
//     whose kubelet version cannot be read follows row 2.
//  2. Otherwise a node is old when it was created before id.StartedAt,
//     and with a zero StartedAt when it is there as Attach lists. Every
//     node that comes later is new.
//
// Row 2 needs no AMI, and the image label of a node is not read. That is
// why TargetAMI changes nothing here.
func Attach(ctx context.Context, kube kubernetes.Interface, nodegroup string, id Identity, o AttachOptions) (*Observer, error) {
	return attach(ctx, kube, nodegroup, id, time.Now, o.syncLimit())
}

// AttachOptions are the options of Attach. The zero value uses the real
// clock.
type AttachOptions struct {
	// AfterFunc starts the timer of the limit of the first sync: when it
	// fires before the caches fill, the observer lists on each snapshot.
	// It returns the function that stops the timer. Nil means
	// time.AfterFunc.
	AfterFunc func(time.Duration, func()) (stop func() bool)
}

// attach is Attach with the clock and the limit of the first sync, which
// a test sets.
func attach(ctx context.Context, kube kubernetes.Interface, nodegroup string, id Identity, now func() time.Time, limit syncLimiter) (*Observer, error) {
	o := &Observer{client: kube, nodegroup: nodegroup, id: id, now: now}
	if o.inf = startInformers(ctx, kube, nodegroup, limit); o.inf != nil {
		o.drains.refresh = watchPodRefresh
	}
	if err := o.identify(ctx); err != nil {
		o.Close()
		return nil, err
	}
	return o, nil
}

// identify makes the first list of the nodes and chooses from it how a
// node counts as new: the rows of Attach.
func (o *Observer) identify(ctx context.Context) error {
	nodes, err := o.listNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing the nodes of nodegroup %s: %w", o.nodegroup, err)
	}
	o.baseline = make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if o.id.StartedAt.IsZero() || n.CreationTimestamp.Time.Before(o.id.StartedAt) {
			o.baseline[n.Name] = true
		}
		if m := kubeletMinor(n); o.id.TargetVersion != "" && m != "" && m != o.id.TargetVersion {
			o.byVersion = true
		}
	}
	return nil
}

// Watching reports whether the observer reads from the caches of its
// watches. A snapshot then costs no request for the nodes and the events,
// so a caller can take one more often.
func (o *Observer) Watching() bool { return o.inf != nil }

// Close stops the watches and waits for their goroutines. The observer
// lists on each snapshot after it. Close may be called more than once.
func (o *Observer) Close() {
	if o.inf == nil {
		return
	}
	o.inf.shutdown()
	o.inf = nil
	o.drains.refresh = 0
}

// listNodes returns the nodes of the nodegroup: from the cache of the
// watch, which holds only them, or with a list by the nodegroup label.
func (o *Observer) listNodes(ctx context.Context) ([]*corev1.Node, error) {
	if o.inf != nil {
		return o.inf.nodes.List(labels.Everything())
	}
	list, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: LabelNodegroup + "=" + o.nodegroup})
	if err != nil {
		return nil, err
	}
	nodes := make([]*corev1.Node, len(list.Items))
	for i := range list.Items {
		nodes[i] = &list.Items[i]
	}
	return nodes, nil
}

// Snapshot returns the nodes of the nodegroup as they are now. The error
// is the one of the node list. The pods of the draining nodes and the
// Warning events are read as far as they can be: a read of them that
// fails leaves its part of the snapshot empty.
func (o *Observer) Snapshot(ctx context.Context) (Snapshot, error) {
	nodes, err := o.listNodes(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("listing the nodes of nodegroup %s: %w", o.nodegroup, err)
	}
	var snap Snapshot
	names := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		names[n.Name] = true
		v := classify(n, o.id.TargetAMI, o.baseline)
		if m := kubeletMinor(n); o.byVersion && m != "" {
			v.OnTarget = m == o.id.TargetVersion
		}
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
	if snap.Draining > 0 {
		o.fillPodEviction(ctx, &snap)
	}
	o.fillWarnings(ctx, &snap, names)
	slices.SortFunc(snap.Nodes, func(a, b NodeView) int { return strings.Compare(a.Name, b.Name) })
	return snap, nil
}

// drainingNames returns the names of the Draining nodes, sorted.
func drainingNames(nodes []NodeView) []string {
	var names []string
	for _, n := range nodes {
		if n.Phase == PhaseDraining {
			names = append(names, n.Name)
		}
	}
	slices.Sort(names)
	return names
}
