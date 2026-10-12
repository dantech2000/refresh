package noderoll

import (
	"context"
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// informerSyncTimeout bounds the initial cache sync in StartInformers. A var
// (not const) so tests can shrink it.
var informerSyncTimeout = 10 * time.Second

// watchPodRefresh is the minimum interval between per-node pod Lists while
// watch-backed. Repaints then read local caches about once a second; pod
// counts for the drain bar don't need that cadence, and re-listing every
// draining node's pods each repaint would cost more than the old poll did.
const watchPodRefresh = 3 * time.Second

// informerSet holds cache-backed listers fed by long-lived watch streams — the
// push-based alternative to issuing List calls on every Snapshot.
type informerSet struct {
	nodes     corelisters.NodeLister
	events    corelisters.EventLister
	factories []informers.SharedInformerFactory
	stop      context.CancelFunc
}

// shutdown stops the watch streams and waits for the informer goroutines to
// exit, so no goroutine outlives the observer's teardown.
func (s *informerSet) shutdown() {
	s.stop()
	for _, f := range s.factories {
		f.Shutdown()
	}
}

// StartInformers switches the observer from per-Snapshot List calls to shared
// informers: one watch stream each for the nodegroup's Nodes and for cluster
// Warning events, with every Snapshot read served from the informer's local
// cache. Changes arrive as they happen instead of on the next poll.
//
// Pods are deliberately not watched: a pod informer would cache every pod in
// the cluster, and its initial sync is what times out on large clusters. Drain
// progress instead Lists pods per draining node (a spec.nodeName field
// selector), at most once per watchPodRefresh.
//
// On error (e.g. credentials whose RBAC grants list but not watch) the
// observer is unchanged and keeps its polling behavior; callers treat watch as
// an upgrade, never a requirement. Callers must StopInformers when done.
func (o *KubeObserver) StartInformers(ctx context.Context) error {
	if o.inf != nil {
		return nil
	}
	stopCtx, cancel := context.WithCancel(ctx)

	nodeFactory := informers.NewSharedInformerFactoryWithOptions(o.client, 0,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = LabelNodegroup + "=" + o.nodegroup
		}))
	// Events need their own factory: a factory's tweak applies to every
	// informer it creates, and the nodegroup label above only exists on Nodes.
	eventFactory := informers.NewSharedInformerFactoryWithOptions(o.client, 0,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = warningFieldSelector
		}))

	nodes := nodeFactory.Core().V1().Nodes()
	events := eventFactory.Core().V1().Events()
	synced := []cache.InformerSynced{
		nodes.Informer().HasSynced,
		events.Informer().HasSynced,
	}

	set := &informerSet{
		nodes:     nodes.Lister(),
		events:    events.Lister(),
		factories: []informers.SharedInformerFactory{nodeFactory, eventFactory},
		stop:      cancel,
	}
	nodeFactory.Start(stopCtx.Done())
	eventFactory.Start(stopCtx.Done())

	syncCtx, syncCancel := context.WithTimeout(stopCtx, informerSyncTimeout)
	defer syncCancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), synced...) {
		set.shutdown()
		return errors.New("informer caches did not sync")
	}

	o.inf = set
	o.podRefresh = watchPodRefresh
	return nil
}

// StopInformers tears down the watch streams started by StartInformers, waits
// for the informer goroutines to exit, and returns the observer to polling.
// Safe to call when informers were never started (or already stopped).
func (o *KubeObserver) StopInformers() {
	if o.inf != nil {
		o.inf.shutdown()
		o.inf = nil
		o.podRefresh = 0
	}
}
