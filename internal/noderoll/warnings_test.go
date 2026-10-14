package noderoll

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestScopeWarnings(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	recent := metav1.NewTime(now.Add(-2 * time.Minute))
	stale := metav1.NewTime(now.Add(-30 * time.Minute))
	names := map[string]time.Time{"ip-1": {}, "ip-2": {}}

	events := []corev1.Event{
		// About a node of the nodegroup: kept.
		{
			Type: "Warning", Reason: "NodeNotReady", Message: "kubelet stopped posting status",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: recent,
		},
		// About a pod, sent by the kubelet of a node of the nodegroup: kept.
		{
			Type: "Warning", Reason: "FailedCreatePodSandbox", Message: "network plugin not ready",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-x", Namespace: "default"},
			Source:         corev1.EventSource{Host: "ip-2"}, LastTimestamp: recent,
		},
		// About a node of another nodegroup: dropped.
		{Type: "Warning", Reason: "DiskPressure", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "other"}, LastTimestamp: recent},
		// Not a warning: dropped.
		{Type: "Normal", Reason: "Starting", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: recent},
		// Too old: dropped.
		{Type: "Warning", Reason: "OldNews", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"}, LastTimestamp: stale},
	}

	// Sorted by node.
	want := []WarnEvent{
		{Node: "ip-1", Object: "Node/ip-1", Reason: "NodeNotReady", Message: "kubelet stopped posting status"},
		{Node: "ip-2", Object: "Pod/web-x", Reason: "FailedCreatePodSandbox", Message: "network plugin not ready"},
	}
	if diff := cmp.Diff(want, scopeWarnings(events, names, now, 10*time.Minute)); diff != "" {
		t.Errorf("scopeWarnings (-want +got):\n%s", diff)
	}
}

// A kubelet reports InvalidDiskCapacity once as it starts, before it has
// the stats of the image filesystem. On a new node that event is noise; on
// a node that has run for a while it is kept, and so is every other
// warning of a new node.
func TestScopeWarnings_StartupDiskCapacity(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	created := now.Add(-3 * time.Minute)
	names := map[string]time.Time{"new": created, "old": now.Add(-time.Hour)}
	at := func(d time.Duration) metav1.Time { return metav1.NewTime(created.Add(d)) }
	startup := func(node string, d time.Duration) corev1.Event {
		return corev1.Event{
			Type: "Warning", Reason: "InvalidDiskCapacity", Message: "invalid capacity 0 on image filesystem",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: node}, LastTimestamp: at(d),
		}
	}
	events := []corev1.Event{
		startup("new", 10*time.Second),
		startup("old", 10*time.Second),
		{
			Type: "Warning", Reason: "NodeNotReady", Message: "kubelet stopped posting status",
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "new"}, LastTimestamp: at(time.Minute),
		},
	}
	want := []WarnEvent{
		{Node: "new", Object: "Node/new", Reason: "NodeNotReady", Message: "kubelet stopped posting status"},
		{Node: "old", Object: "Node/old", Reason: "InvalidDiskCapacity", Message: "invalid capacity 0 on image filesystem"},
	}
	if diff := cmp.Diff(want, scopeWarnings(events, names, now, 10*time.Minute)); diff != "" {
		t.Errorf("scopeWarnings (-want +got):\n%s", diff)
	}
}

func TestScopeWarnings_DedupKeepsLatest(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	names := map[string]time.Time{"ip-1": {}}
	older := metav1.NewTime(now.Add(-5 * time.Minute))
	newer := metav1.NewTime(now.Add(-1 * time.Minute))
	obj := corev1.ObjectReference{Kind: "Node", Name: "ip-1"}

	for _, events := range [][]corev1.Event{
		{
			{Type: "Warning", Reason: "FailedDraining", Message: "older", InvolvedObject: obj, LastTimestamp: older},
			{Type: "Warning", Reason: "FailedDraining", Message: "newer", InvolvedObject: obj, LastTimestamp: newer},
		},
		// The order of the list does not decide.
		{
			{Type: "Warning", Reason: "FailedDraining", Message: "newer", InvolvedObject: obj, LastTimestamp: newer},
			{Type: "Warning", Reason: "FailedDraining", Message: "older", InvolvedObject: obj, LastTimestamp: older},
		},
	} {
		got := scopeWarnings(events, names, now, 10*time.Minute)
		if len(got) != 1 || got[0].Message != "newer" {
			t.Errorf("scopeWarnings = %+v, want the newest of the two alone", got)
		}
	}
}

// warning returns a Warning event about a node, at the time at.
func warning(name, reason, node string, at time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "default"},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        "Cannot evict pod as it would violate the pod's disruption budget",
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: node},
		LastTimestamp:  metav1.NewTime(at),
	}
}

// Of the Warning events of a cluster, a snapshot has the ones about the
// nodes of the nodegroup.
func TestObserver_ScopesWarningEvents(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		warning("e1", "FailedDraining", "ip-1", clock.now()),
		warning("e2", "DiskPressure", "ip-elsewhere", clock.now()),
	)
	s := snapshot(t, listing(t, client, Identity{TargetAMI: newAMI}, clock))
	if len(s.Warnings) != 1 {
		t.Fatalf("%d warnings, want the one about the node of the nodegroup: %+v", len(s.Warnings), s.Warnings)
	}
	if s.Warnings[0].Node != "ip-1" || s.Warnings[0].Reason != "FailedDraining" {
		t.Errorf("warning = %+v, want FailedDraining about ip-1", s.Warnings[0])
	}
}

// pagedEvents makes the fake clientset answer a list of the events in
// pages of one event, each with a continue token, and records the options
// of every list. With pages below 0 the pages never end.
func pagedEvents(t *testing.T, c *fake.Clientset, pages int, at time.Time) *[]metav1.ListOptions {
	t.Helper()
	seen := &[]metav1.ListOptions{}
	c.PrependReactor("list", "events", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		// The list action of the fake gives the selectors and not the
		// limit and the token, so they are read from the options.
		la, ok := a.(k8stesting.ListActionImpl)
		if !ok {
			t.Errorf("action %T is not a list", a)
			return false, nil, nil
		}
		opts := la.ListOptions
		*seen = append(*seen, opts)
		page := 0
		if opts.Continue != "" {
			var err error
			if page, err = strconv.Atoi(opts.Continue); err != nil {
				return true, nil, err
			}
		}
		list := &corev1.EventList{Items: []corev1.Event{*warning(fmt.Sprintf("e%d", page), fmt.Sprintf("Reason%d", page), "ip-1", at)}}
		if pages < 0 || page+1 < pages {
			list.Continue = strconv.Itoa(page + 1)
		}
		return true, list, nil
	})
	return seen
}

// An observer that lists asks the API server for the Warning events only
// and follows the continue tokens, so no warning below the cap is lost.
func TestObserver_WarningEventsPaginated(t *testing.T) {
	const pages = warningEventMaxPages - 1
	clock := newClock()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, pages, clock.now())
	s := snapshot(t, listing(t, client, Identity{TargetAMI: newAMI}, clock))
	seen := *calls
	if len(seen) != pages {
		t.Fatalf("%d event lists, want %d: one per page", len(seen), pages)
	}
	for i, o := range seen {
		if o.FieldSelector != warningFieldSelector {
			t.Errorf("list %d: field selector = %q, want %q", i, o.FieldSelector, warningFieldSelector)
		}
		if o.Limit != warningEventPageSize {
			t.Errorf("list %d: limit = %d, want %d", i, o.Limit, warningEventPageSize)
		}
	}
	if len(s.Warnings) != pages {
		t.Fatalf("warnings = %+v, want one of each of %d pages", s.Warnings, pages)
	}
	if s.WarningsCapped != 0 {
		t.Errorf("WarningsCapped = %d, want 0: the read ended below the cap", s.WarningsCapped)
	}
}

// One read takes at most warningEventMaxPages pages, keeps what it read,
// and reports the cap in the snapshot.
func TestObserver_WarningEventsPageCap(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, -1, clock.now()) // pages without end
	s := snapshot(t, listing(t, client, Identity{TargetAMI: newAMI}, clock))
	if n := len(*calls); n != warningEventMaxPages {
		t.Fatalf("%d event lists, want the cap %d", n, warningEventMaxPages)
	}
	if len(s.Warnings) != warningEventMaxPages {
		t.Errorf("%d warnings, want the %d that were read before the cap", len(s.Warnings), warningEventMaxPages)
	}
	if s.WarningsCapped != warningEventMaxPages {
		t.Errorf("WarningsCapped = %d, want %d", s.WarningsCapped, warningEventMaxPages)
	}
}

// The Warning events are read again at most every warningEventRefresh.
// The snapshots between two reads use the last one.
func TestObserver_WarningEventsRefreshInterval(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, 1, clock.now())
	obs := listing(t, client, Identity{TargetAMI: newAMI}, clock)

	for range 4 {
		if s := snapshot(t, obs); len(s.Warnings) != 1 {
			t.Fatalf("warnings = %+v, want the event of the last read in every snapshot", s.Warnings)
		}
		clock.advance(time.Second)
	}
	if n := len(*calls); n != 1 {
		t.Fatalf("%d event lists over 4 snapshots, want 1", n)
	}

	clock.advance(warningEventRefresh) // the refresh time has passed
	snapshot(t, obs)
	if n := len(*calls); n != 2 {
		t.Fatalf("%d event lists after the refresh time, want 2", n)
	}
}

// A change of the draining nodes reads the Warning events at once, also
// inside the refresh time: a node that was just cordoned is when the
// warnings of a drain matter.
func TestObserver_WarningEventsRefreshOnDrainingChange(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false), mkNode("ip-2", oldAMI, true, false))
	calls := pagedEvents(t, client, 1, clock.now())
	obs := listing(t, client, Identity{TargetAMI: newAMI}, clock)

	snapshot(t, obs)
	cordon(t, client, "ip-2")
	snapshot(t, obs)
	if n := len(*calls); n != 2 {
		t.Fatalf("%d event lists, want 2: the draining nodes changed", n)
	}
	snapshot(t, obs) // the same draining nodes: the last read is used
	if n := len(*calls); n != 2 {
		t.Fatalf("%d event lists, want 2 still: the draining nodes did not change", n)
	}
}

// A read of the events that fails does not fail the snapshot. It uses the
// last read again, and the next snapshot tries once more.
func TestObserver_WarningEventsReadFails(t *testing.T) {
	clock := newClock()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false), warning("e1", "FailedDraining", "ip-1", clock.now()))
	fail := false
	lists := 0
	client.PrependReactor("list", "events", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		lists++
		if fail {
			return true, nil, fmt.Errorf("the server is currently unable to handle the request")
		}
		return false, nil, nil
	})
	// With no read yet, the snapshot has no warnings.
	fail = true
	obs := listing(t, client, Identity{}, clock)
	if s := snapshot(t, obs); len(s.Warnings) != 0 || s.Total != 1 {
		t.Fatalf("snapshot = %+v, want the node and no warnings", s)
	}
	fail = false
	if s := snapshot(t, obs); len(s.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one of the read that worked", s.Warnings)
	}
	// The read after the refresh time fails: the last one is used, and
	// every snapshot tries again.
	fail = true
	clock.advance(warningEventRefresh)
	before := lists
	for range 2 {
		if s := snapshot(t, obs); len(s.Warnings) != 1 {
			t.Fatalf("warnings = %+v, want the one of the last read", s.Warnings)
		}
	}
	if lists != before+2 {
		t.Errorf("%d event lists while the read fails, want one per snapshot", lists-before)
	}
}
