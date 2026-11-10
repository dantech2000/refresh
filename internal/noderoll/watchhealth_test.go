package noderoll

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// reflector returns a reflector for the error handler of a watch, which
// names it in its log.
func reflector() *cache.Reflector {
	return cache.NewReflector(&cache.ListWatch{}, &corev1.Node{}, cache.NewStore(cache.MetaNamespaceKeyFunc), 0)
}

// A watch is broken by an error that the reflector retries, and not by
// one that only makes it list again. An event clears it.
func TestWatchHealth(t *testing.T) {
	gone := schema.GroupResource{Resource: "nodes"}
	tests := []struct {
		name   string
		err    error
		broken bool
	}{
		{name: "closed by the server", err: io.EOF},
		{name: "an expired resource version", err: apierrors.NewResourceExpired("too old resource version")},
		{name: "gone", err: &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonGone, Code: 410}}},
		{name: "connection refused", err: fmt.Errorf("dial tcp 10.0.0.1:443: %w", syscall.ECONNREFUSED), broken: true},
		{name: "forbidden", err: apierrors.NewForbidden(gone, "", errors.New("expired credentials")), broken: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, broken: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w watchHealth
			w.failed(testContext(t), reflector(), tt.err)
			if got := !w.ok(); got != tt.broken {
				t.Fatalf("broken = %t, want %t", got, tt.broken)
			}
			w.delivered()
			if !w.ok() {
				t.Error("an event did not clear the error")
			}
		})
	}
}

// When the watch of the nodes breaks after the first sync, the observer
// lists the nodes, so a snapshot fails while the API does not answer and
// does not show the last content of the cache as current. Once the watch
// delivers again, the snapshots come from the cache.
func TestObserver_ListsWhileTheWatchIsBroken(t *testing.T) {
	w := kubeWorld()
	srv, client := serve(t, w, nil)
	obs, err := Attach(testContext(t), client, ng, Identity{}, AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close()
	obs.now = w.Now
	changed := cacheChanges(t, obs)
	waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == 2 })
	lists := len(requests(srv, fleettest.KubeListNodes))

	obs.inf.nodesWatch.failed(testContext(t), reflector(), fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED))
	w.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
	if _, err := obs.Snapshot(testContext(t)); err == nil {
		t.Fatal("a snapshot with a broken watch and a list that fails has no error")
	}
	w.SetFault(fleettest.KubeListNodes, fleettest.Fault{})
	if s := snapshot(t, obs); s.Total != 2 {
		t.Fatalf("the snapshot from the list = %+v, want the two nodes", s)
	}
	if got := len(requests(srv, fleettest.KubeListNodes)) - lists; got != 2 {
		t.Fatalf("%d lists of the nodes with a broken watch, want 2", got)
	}

	// An event arrives through the watch: the cache is current again.
	delivered := make(chan struct{}, 1)
	obs.inf.nodesWatch.mu.Lock()
	obs.inf.nodesWatch.delivery = delivered
	obs.inf.nodesWatch.mu.Unlock()
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.Nodes = append(c.Nodes, &fleettest.Node{Name: "a-3", Nodegroup: ng, ImageID: newAMI})
	})
	select {
	case <-delivered:
	case <-testContext(t).Done():
		t.Fatal("the watch delivered no event")
	}
	lists = len(requests(srv, fleettest.KubeListNodes))
	if s := snapshot(t, obs); s.Total != 3 {
		t.Errorf("the snapshot from the cache = %+v, want the three nodes", s)
	}
	if got := len(requests(srv, fleettest.KubeListNodes)) - lists; got != 0 {
		t.Errorf("%d lists of the nodes after the watch delivered again, want none", got)
	}
}
