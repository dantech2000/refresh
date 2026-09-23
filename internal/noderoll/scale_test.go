package noderoll

import (
	"context"
	"fmt"

	"strconv"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var (
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	podsGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
)

// podListRecorder makes the fake clientset honor spec.nodeName field
// selectors on pod Lists (the fake tracker ignores field selectors) and
// records every selector it was asked for.
type podListRecorder struct {
	mu        sync.Mutex
	selectors []string
}

func (r *podListRecorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.selectors...)
}

func enforcePodFieldSelector(t *testing.T, c *fake.Clientset) *podListRecorder {
	t.Helper()
	rec := &podListRecorder{}
	c.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		la := a.(k8stesting.ListAction)
		sel := la.GetListRestrictions().Fields
		rec.mu.Lock()
		rec.selectors = append(rec.selectors, sel.String())
		rec.mu.Unlock()
		obj, err := c.Tracker().List(podsGVR, podsGVK, la.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		all := obj.(*corev1.PodList)
		out := &corev1.PodList{}
		for _, p := range all.Items {
			if sel.Matches(fields.Set{"spec.nodeName": p.Spec.NodeName}) {
				out.Items = append(out.Items, p)
			}
		}
		return true, out, nil
	})
	return rec
}

// Drain accounting must List pods per draining node with a spec.nodeName
// field selector, never an unscoped cluster-wide pod List, and must skip the
// pod read entirely when nothing drains.
func TestKubeObserver_PodReadsScopedToDrainingNodes(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true),  // draining
		mkNode("ip-2", oldAMI, true, false), // serving
		mkNode("ip-3", oldAMI, true, true),  // draining
		pod("a", "ip-1", corev1.PodRunning, false, false),
		pod("b", "ip-2", corev1.PodRunning, false, false),
		pod("c", "ip-3", corev1.PodRunning, false, false),
		pod("d", "ip-3", corev1.PodRunning, false, false),
	)
	rec := enforcePodFieldSelector(t, client)
	obs := NewKubeObserver(client, ng, newAMI)

	s, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := rec.calls()
	want := map[string]bool{podsOnNodeSelector("ip-1"): true, podsOnNodeSelector("ip-3"): true}
	if len(got) != len(want) {
		t.Fatalf("pod List selectors = %q, want one per draining node %v", got, want)
	}
	for _, sel := range got {
		if !want[sel] {
			t.Errorf("unexpected pod List selector %q (unscoped or non-draining node)", sel)
		}
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 || v.PodsTotal != 1 {
		t.Errorf("ip-1 eviction = %d/%d, want 1/1", v.Pods, v.PodsTotal)
	}
	if v := nodeOf(s, "ip-3"); v.Pods != 2 || v.PodsTotal != 2 {
		t.Errorf("ip-3 eviction = %d/%d, want 2/2", v.Pods, v.PodsTotal)
	}
	if v := nodeOf(s, "ip-2"); v.Pods != 0 || v.PodsTotal != 0 {
		t.Errorf("serving node ip-2 got pod accounting %d/%d, want none", v.Pods, v.PodsTotal)
	}

	// Nothing draining → no pod reads at all.
	quiet := fake.NewClientset(mkNode("ip-9", oldAMI, true, false))
	qrec := enforcePodFieldSelector(t, quiet)
	if _, err := NewKubeObserver(quiet, ng, newAMI).Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := qrec.calls(); len(calls) != 0 {
		t.Errorf("pod Lists with nothing draining = %q, want none", calls)
	}
}

// When watch-backed, repaints are fast, so pod counts are refreshed at most
// once per podRefresh for an unchanged draining set, and re-read at once when
// the draining set changes.
func TestKubeObserver_PodCountsThrottledWhenWatching(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, true),
		mkNode("ip-2", oldAMI, true, false),
		pod("a", "ip-1", corev1.PodRunning, false, false),
	)
	rec := enforcePodFieldSelector(t, client)
	obs := NewKubeObserver(client, ng, newAMI)
	obs.podRefresh = time.Hour

	for range 3 {
		if _, err := obs.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(rec.calls()); n != 1 {
		t.Fatalf("pod Lists over 3 snapshots = %d, want 1 (throttled)", n)
	}

	cordon(ctx, t, client, "ip-2") // draining set changes → re-read now
	s, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.calls()); n != 3 {
		t.Fatalf("pod Lists after the draining set changed = %d, want 3", n)
	}
	if v := nodeOf(s, "ip-1"); v.Pods != 1 {
		t.Errorf("ip-1 pods = %d, want 1", v.Pods)
	}
}

// pagedEvents serves Warning events over the fake clientset in pages of one
// event each (a Continue token per page) and records every List call. pages
// < 0 serves pages without end.
func pagedEvents(t *testing.T, c *fake.Clientset, pages int) *[]metav1.ListOptions {
	t.Helper()
	now := metav1.NewTime(time.Now())
	seen := &[]metav1.ListOptions{}
	c.PrependReactor("list", "events", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		// The fake's ListAction exposes the selectors but not Limit/Continue,
		// so read the page cursor from the raw ListOptions.
		opts := listOptionsOf(t, a)
		*seen = append(*seen, opts)
		page := 0
		if opts.Continue != "" {
			page, _ = strconv.Atoi(opts.Continue)
		}
		list := &corev1.EventList{Items: []corev1.Event{{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("e%d", page), Namespace: "default"},
			Type:           corev1.EventTypeWarning,
			Reason:         fmt.Sprintf("Reason%d", page),
			InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "ip-1"},
			LastTimestamp:  now,
		}}}
		if pages < 0 || page+1 < pages {
			list.Continue = strconv.Itoa(page + 1)
		}
		return true, list, nil
	})
	return seen
}

// The polling Warning-event read must narrow server-side to type=Warning and
// follow Continue tokens across pages, so no warning under the cap is dropped.
func TestKubeObserver_WarningEventsPaginated(t *testing.T) {
	const pages = warningEventMaxPages - 1
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, pages)
	s, err := NewKubeObserver(client, ng, newAMI).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := *calls
	if len(seen) != pages {
		t.Fatalf("event List calls = %d, want %d (one per page)", len(seen), pages)
	}
	for i, o := range seen {
		if o.FieldSelector != warningFieldSelector {
			t.Errorf("call %d field selector = %q, want %q", i, o.FieldSelector, warningFieldSelector)
		}
		if o.Limit != warningEventPageSize {
			t.Errorf("call %d limit = %d, want %d", i, o.Limit, warningEventPageSize)
		}
	}
	if len(s.Warnings) != pages {
		t.Fatalf("warnings = %+v, want one from each of %d pages", s.Warnings, pages)
	}
	if s.WarningsCapped != 0 {
		t.Errorf("WarningsCapped = %d, want 0 (read finished under the cap)", s.WarningsCapped)
	}
}

// A refresh reads at most warningEventMaxPages pages, keeps what it read, and
// reports the cap on the snapshot.
func TestKubeObserver_WarningEventsPageCap(t *testing.T) {
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, -1) // never-ending pages
	s, err := NewKubeObserver(client, ng, newAMI).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n != warningEventMaxPages {
		t.Fatalf("event List calls = %d, want the cap %d", n, warningEventMaxPages)
	}
	if len(s.Warnings) != warningEventMaxPages {
		t.Errorf("warnings = %d, want the %d events read before the cap", len(s.Warnings), warningEventMaxPages)
	}
	if s.WarningsCapped != warningEventMaxPages {
		t.Errorf("WarningsCapped = %d, want %d", s.WarningsCapped, warningEventMaxPages)
	}
}

// Warning events are re-read at most every warningEventRefresh; snapshots in
// between reuse the last read.
func TestKubeObserver_WarningEventsRefreshInterval(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	calls := pagedEvents(t, client, 1)
	obs := NewKubeObserver(client, ng, newAMI)

	for range 4 {
		s, err := obs.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Warnings) != 1 {
			t.Fatalf("warnings = %+v, want the cached event on every snapshot", s.Warnings)
		}
	}
	if n := len(*calls); n != 1 {
		t.Fatalf("event List calls over 4 snapshots = %d, want 1", n)
	}

	obs.warnAt = obs.warnAt.Add(-warningEventRefresh) // interval elapsed
	if _, err := obs.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n != 2 {
		t.Fatalf("event List calls after the interval = %d, want 2", n)
	}
}

// A change in the draining-node set re-reads Warning events at once, inside
// the refresh interval: a newly cordoned node is when drain warnings matter.
func TestKubeObserver_WarningEventsRefreshOnDrainingChange(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false), mkNode("ip-2", oldAMI, true, false))
	calls := pagedEvents(t, client, 1)
	obs := NewKubeObserver(client, ng, newAMI)

	if _, err := obs.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	cordon(ctx, t, client, "ip-2")
	if _, err := obs.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(*calls); n != 2 {
		t.Fatalf("event List calls = %d, want 2 (re-read on draining change)", n)
	}
	if _, err := obs.Snapshot(ctx); err != nil { // same set → cached
		t.Fatal(err)
	}
	if n := len(*calls); n != 2 {
		t.Fatalf("event List calls = %d, want still 2 (draining set unchanged)", n)
	}
}

// listOptionsOf extracts the full ListOptions the fake client was called with.
func listOptionsOf(t *testing.T, a k8stesting.Action) metav1.ListOptions {
	t.Helper()
	la, ok := a.(k8stesting.ListActionImpl)
	if !ok {
		t.Fatalf("action %T is not a ListActionImpl", a)
	}
	return la.ListOptions
}

// StopInformers must wait for the informers to finish, not just signal them:
// when it returns, every informer's Run has exited. (A goroutine count is not
// a reliable check here, because client-go's reflector starts helper
// goroutines that Run does not wait for.)
func TestStopInformers_WaitsForInformers(t *testing.T) {
	client := fake.NewClientset(mkNode("ip-1", oldAMI, true, false))
	for range 2 {
		obs := NewKubeObserver(client, ng, newAMI)
		if err := obs.StartInformers(context.Background()); err != nil {
			t.Fatalf("StartInformers: %v", err)
		}
		f := obs.inf.factories
		watched := []interface{ IsStopped() bool }{
			f[0].Core().V1().Nodes().Informer(),
			f[1].Core().V1().Events().Informer(),
		}
		obs.StopInformers()
		for i, inf := range watched {
			if !inf.IsStopped() {
				t.Fatalf("informer %d still running after StopInformers returned", i)
			}
		}
	}
}
