package kubetest_test

import (
	"strconv"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

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
//
// The editor runs until the lists are done, so every list is made while the
// world changes, however the two goroutines are scheduled. (A fixed number
// of edits took half a millisecond, and a list could come after them all.)
func TestListIsOneMomentOfTheWorld(t *testing.T) {
	w, srv, _ := newServer(t)
	ctx := testContext(t)
	cfg := srv.Config()
	cfg.QPS = -1 // no client-side rate limit: the lists run back to back
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set := func(i int) {
		w.Edit(func(s *fleettest.State) { s.Cluster(region, "prod").Pods[0].Labels["n"] = strconv.Itoa(i) })
	}
	set(0)
	base := w.Generation()

	var editor sync.WaitGroup
	stop := make(chan struct{})
	editor.Go(func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
				set(i)
			}
		}
	})
	defer editor.Wait()
	defer close(stop)

	// At least minLists lists, and at least two labels among them: the
	// lists saw the world change.
	const minLists = 20
	seen := map[int64]bool{}
	for lists := 0; lists < minLists || len(seen) < 2; lists++ {
		list, err := client.CoreV1().Pods("default").List(ctx, metav1.ListOptions{LabelSelector: "app=web"})
		if err != nil {
			t.Fatal(err)
		}
		version, _ := strconv.ParseInt(list.ResourceVersion, 10, 64)
		n, _ := strconv.ParseInt(list.Items[0].Labels["n"], 10, 64)
		if version-n != base {
			t.Fatalf("the list has resource version %d and the label n=%d: they are %d apart, and the world keeps them %d apart", version, n, version-n, base)
		}
		seen[n] = true
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
