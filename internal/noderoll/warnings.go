package noderoll

import (
	"context"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// warningWindow is how old a Warning event may be to count: the
	// snapshot shows what happens in the roll, and no history.
	warningWindow = 10 * time.Minute
	// warningEventRefresh is the least time between two reads of the
	// Warning events while the draining nodes stay the same. The events
	// say why a node is stuck, which does not need the pace of the
	// snapshots, and a read pages through every Warning event of the
	// cluster.
	warningEventRefresh = 15 * time.Second
	// warningFieldSelector makes the API server return Warning events
	// only. Nothing narrower can be asked for: an event of a kubelet is
	// matched by its source host, which is not a field of a selector.
	warningFieldSelector = "type=" + corev1.EventTypeWarning
	// warningEventPageSize and warningEventMaxPages bound one read to
	// their product in events.
	warningEventPageSize = 500
	warningEventMaxPages = 4
)

// warningCache is the last read of the Warning events of the cluster.
type warningCache struct {
	events []corev1.Event
	// at is when the events were read, and draining the draining nodes at
	// that time, joined by commas. read is false before the first read
	// that succeeded.
	at       time.Time
	draining string
	read     bool
	// capped says that the read stopped at its page cap.
	capped bool
}

// fillWarnings sets the Warning events of snap: the ones about the nodes
// in names. It reads the events of the cluster again when the last read
// is warningEventRefresh old, and at once when the draining nodes changed:
// a node that was just cordoned is when the warnings of a drain matter. A
// read that fails uses the last one again, and leaves the warnings empty
// when there is none.
func (o *Observer) fillWarnings(ctx context.Context, snap *Snapshot, names map[string]bool) {
	w := &o.warnings
	now := o.now()
	nodes := strings.Join(drainingNames(snap.Nodes), ",")
	if !w.read || now.Sub(w.at) >= warningEventRefresh || nodes != w.draining {
		events, capped, err := o.listWarningEvents(ctx)
		switch {
		case err == nil:
			*w = warningCache{events: events, at: now, draining: nodes, read: true, capped: capped}
		case !w.read:
			return
		}
	}
	snap.Warnings = scopeWarnings(w.events, names, now, warningWindow)
	if w.capped {
		snap.WarningsCapped = len(w.events)
	}
}

// listWarningEvents returns the Warning events of the cluster: from the
// cache of the watch, which holds only them, or with a paged list of at
// most warningEventMaxPages pages. capped reports that the list stopped at
// that cap. The pages are needed because one list with a limit returns
// the events in key order, and so drops an arbitrary part of them.
func (o *Observer) listWarningEvents(ctx context.Context) (events []corev1.Event, capped bool, err error) {
	if o.inf != nil {
		cached, err := o.inf.events.List(labels.Everything())
		if err != nil {
			return nil, false, err
		}
		events = make([]corev1.Event, len(cached))
		for i := range cached {
			events[i] = *cached[i]
		}
		return events, false, nil
	}
	opts := metav1.ListOptions{FieldSelector: warningFieldSelector, Limit: warningEventPageSize}
	for pages := 1; ; pages++ {
		page, err := o.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		events = append(events, page.Items...)
		switch {
		case page.Continue == "":
			return events, false, nil
		case pages >= warningEventMaxPages:
			return events, true, nil
		}
		opts.Continue = page.Continue
	}
}

// scopeWarnings returns the Warning events about the nodes in names that
// are at most window old at now. An event is about a node when the node
// is its involved object, or when the kubelet of the node sent it. Of
// several events with the same node, object, and reason it keeps the
// newest. The result is sorted by node and then by reason.
func scopeWarnings(events []corev1.Event, names map[string]bool, now time.Time, window time.Duration) []WarnEvent {
	type key struct{ node, object, reason string }
	latest := map[key]*corev1.Event{}
	var order []key
	for i := range events {
		e := &events[i]
		if e.Type != "" && e.Type != corev1.EventTypeWarning {
			continue
		}
		node := warningNode(e, names)
		if node == "" {
			continue
		}
		if at := warningTime(e); !at.IsZero() && now.Sub(at) > window {
			continue
		}
		k := key{node: node, object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, reason: e.Reason}
		prev, seen := latest[k]
		switch {
		case !seen:
			order = append(order, k)
			latest[k] = e
		case warningTime(e).After(warningTime(prev)):
			latest[k] = e
		}
	}
	out := make([]WarnEvent, 0, len(order))
	for _, k := range order {
		out = append(out, WarnEvent{Node: k.node, Object: k.object, Reason: k.reason, Message: latest[k].Message})
	}
	slices.SortStableFunc(out, func(a, b WarnEvent) int {
		if c := strings.Compare(a.Node, b.Node); c != 0 {
			return c
		}
		return strings.Compare(a.Reason, b.Reason)
	})
	return out
}

// warningNode returns the node in names that an event is about, or "".
func warningNode(e *corev1.Event, names map[string]bool) string {
	if e.InvolvedObject.Kind == "Node" && names[e.InvolvedObject.Name] {
		return e.InvolvedObject.Name
	}
	if names[e.Source.Host] {
		return e.Source.Host
	}
	return ""
}

// warningTime returns when an event last happened.
func warningTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.EventTime.Time
}
