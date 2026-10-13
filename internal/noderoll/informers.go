package noderoll

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// informerSyncLimit is how long Attach waits for the caches of the
	// watches to fill.
	informerSyncLimit = 10 * time.Second
	// watchPodRefresh is the least time between two reads of the pods of
	// the same draining nodes while the observer watches. See drainCache.
	watchPodRefresh = 3 * time.Second
)

// syncLimiter returns the context that bounds the first sync of the
// caches.
type syncLimiter func(parent context.Context) (context.Context, context.CancelFunc)

// syncLimit is the limit of Attach.
func syncLimit(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, informerSyncLimit)
}

// informerSet is the caches of two watches: the nodes of one nodegroup,
// and the Warning events of the cluster.
//
// The pods have no watch. Its cache would hold every pod of the cluster,
// and filling it is what times out on a large cluster. The observer lists
// the pods of each draining node.
type informerSet struct {
	nodes     corelisters.NodeLister
	events    corelisters.EventLister
	factories []informers.SharedInformerFactory
	stop      context.CancelFunc
}

// startInformers starts the two watches and waits for their caches to
// fill, as long as limit allows. It returns nil when they do not fill:
// the watches are stopped, and the observer lists.
func startInformers(ctx context.Context, client kubernetes.Interface, nodegroup string, limit syncLimiter) *informerSet {
	stopCtx, cancel := context.WithCancel(ctx)
	nodeFactory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = LabelNodegroup + "=" + nodegroup
		}))
	// A factory applies its options to every informer that it makes, and
	// only a node has the nodegroup label. So the events have a factory of
	// their own.
	eventFactory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = warningFieldSelector
		}))
	nodes := nodeFactory.Core().V1().Nodes()
	events := eventFactory.Core().V1().Events()
	synced := []cache.InformerSynced{nodes.Informer().HasSynced, events.Informer().HasSynced}
	set := &informerSet{
		nodes:     nodes.Lister(),
		events:    events.Lister(),
		factories: []informers.SharedInformerFactory{nodeFactory, eventFactory},
		stop:      cancel,
	}
	nodeFactory.Start(stopCtx.Done())
	eventFactory.Start(stopCtx.Done())

	syncCtx, done := limit(stopCtx)
	defer done()
	if !cache.WaitForCacheSync(syncCtx.Done(), synced...) {
		set.shutdown()
		return nil
	}
	return set
}

// shutdown stops the watches and waits for their goroutines to end.
func (s *informerSet) shutdown() {
	s.stop()
	for _, f := range s.factories {
		f.Shutdown()
	}
}
