package noderoll

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// informerSyncTimeout bounds the initial cache sync in StartInformers. A var
// (not const) so tests can shrink it.
var informerSyncTimeout = 10 * time.Second

// informerSet holds cache-backed listers fed by long-lived watch streams — the
// push-based alternative to issuing List calls on every Snapshot.
type informerSet struct {
	nodes  corelisters.NodeLister
	pods   corelisters.PodLister
	events corelisters.EventLister
	stop   context.CancelFunc
}

// StartInformers switches the observer from per-Snapshot List calls to shared
// informers: one watch stream per resource (nodes, pods, Warning events), with
// every Snapshot read served from the informer's local cache. Changes arrive
// as they happen instead of on the next poll, and the repeated cluster-wide
// pod List — the expensive call on large clusters — collapses into a single
// stream. On error (e.g. credentials whose RBAC grants list but not watch) the
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
	// Pods and events need their own factories: a factory's tweak applies to
	// every informer it creates, and the nodegroup label above only exists on
	// Nodes.
	podFactory := informers.NewSharedInformerFactoryWithOptions(o.client, 0)
	eventFactory := informers.NewSharedInformerFactoryWithOptions(o.client, 0,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = "type=" + corev1.EventTypeWarning
		}))

	nodes := nodeFactory.Core().V1().Nodes()
	pods := podFactory.Core().V1().Pods()
	events := eventFactory.Core().V1().Events()
	synced := []cache.InformerSynced{
		nodes.Informer().HasSynced,
		pods.Informer().HasSynced,
		events.Informer().HasSynced,
	}

	nodeFactory.Start(stopCtx.Done())
	podFactory.Start(stopCtx.Done())
	eventFactory.Start(stopCtx.Done())

	syncCtx, syncCancel := context.WithTimeout(stopCtx, informerSyncTimeout)
	defer syncCancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), synced...) {
		cancel()
		return errors.New("informer caches did not sync")
	}

	o.inf = &informerSet{
		nodes:  nodes.Lister(),
		pods:   pods.Lister(),
		events: events.Lister(),
		stop:   cancel,
	}
	return nil
}

// StopInformers tears down the watch streams started by StartInformers and
// returns the observer to polling. Safe to call when informers were never
// started (or already stopped).
func (o *KubeObserver) StopInformers() {
	if o.inf != nil {
		o.inf.stop()
		o.inf = nil
	}
}
