package kubetest_test

import (
	"strconv"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// TestListIsOneMomentOfTheWorld lists the pods while the world changes a
// label of one pod, in place, as fast as it can. Each change moves the
// label and the world's generation by one, so in any one moment of the
// world the two differ by the same amount. A list whose resource version
// was read apart from its items breaks that, and a watch from that version
// would then miss a change. A list that encodes its objects after it let go
// of the world is a data race on the label map, which the race detector
// reports.
func TestListIsOneMomentOfTheWorld(t *testing.T) {
	w, _, client := newServer(t)
	ctx := testContext(t)
	set := func(i int) {
		w.Edit(func(s *fleettest.State) { s.Cluster(region, "prod").Pods[0].Labels["n"] = strconv.Itoa(i) })
	}
	set(0)
	base := w.Generation()

	const edits = 2000
	var editor sync.WaitGroup
	done := make(chan struct{})
	editor.Go(func() {
		defer close(done)
		for i := 1; i <= edits; i++ {
			set(i)
		}
	})
	defer editor.Wait()
	for lists := 0; ; lists++ {
		select {
		case <-done:
			if lists == 0 {
				t.Fatal("no list was made while the world changed")
			}
			return
		default:
		}
		list, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{LabelSelector: "app=web"})
		if err != nil {
			t.Fatal(err)
		}
		version, _ := strconv.ParseInt(list.ResourceVersion, 10, 64)
		n, _ := strconv.ParseInt(list.Items[0].Labels["n"], 10, 64)
		if version-n != base {
			t.Fatalf("the list has resource version %d and the label n=%d: they are %d apart, and the world keeps them %d apart", version, n, version-n, base)
		}
	}
}

// TestThrottledListIsRetried checks that a throttled request gets the
// answer client-go retries by itself: 429 with Retry-After.
func TestThrottledListIsRetried(t *testing.T) {
	w, srv, client := newServer(t)
	w.SetScopedFault(fleettest.Scope{Operation: fleettest.KubeListPods}, fleettest.Fault{Throttle: 1})
	list, err := client.CoreV1().Pods("").List(testContext(t), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("a list that was throttled once failed: %v", err)
	}
	if len(list.Items) != 4 {
		t.Errorf("the list has %d pods, want 4", len(list.Items))
	}
	if got := len(srv.Requests()); got != 2 {
		t.Errorf("the server got %d requests, want 2: the throttled one and its retry", got)
	}
}
