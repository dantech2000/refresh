package noderoll

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// SyncLimit is how long Attach waits for the caches of the watches
	// to fill.
	SyncLimit = 10 * time.Second
	// watchPodRefresh is the least time between two reads of the pods of
	// the same draining nodes while the observer watches. See drainCache.
	watchPodRefresh = 3 * time.Second
)

// syncLimiter returns the context that bounds the first sync of the
// caches.
type syncLimiter func(parent context.Context) (context.Context, context.CancelFunc)

// syncLimit returns the limit of the first sync: SyncLimit on the timer
// of the options.
func (o AttachOptions) syncLimit() syncLimiter {
	afterFunc := o.AfterFunc
	if afterFunc == nil {
		afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		stop := afterFunc(SyncLimit, cancel)
		return ctx, func() {
			stop()
			cancel()
		}
	}
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
	// nodesWatch and eventsWatch say whether each watch broke since it
	// last delivered an event. The cache of a broken watch keeps its last
	// content while the reflector retries, so the observer lists instead.
	nodesWatch, eventsWatch watchHealth
}

// watchHealth records whether a watch broke. A reflector retries without
// end, and a lister never returns an error, so a cache whose watch broke
// would serve its last content as current: when the API goes away
// mid-roll, the panel would freeze with no error.
type watchHealth struct {
	mu     sync.Mutex
	broken bool
	// delivery, when set, gets a value after each event, without
	// blocking: a test waits on it.
	delivery chan<- struct{}
}

// failed is the watch error handler of an informer. A watch that the
// server closed, or whose resource version expired, is not broken: the
// reflector lists again at once, as client-go's DefaultWatchErrorHandler
// treats them. Every error still goes to that handler.
func (w *watchHealth) failed(ctx context.Context, r *cache.Reflector, err error) {
	cache.DefaultWatchErrorHandler(ctx, r, err)
	if errors.Is(err, io.EOF) || apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.broken = true
}

// delivered records that the watch delivered an event: a reflector that
// listed again after an error replaces its cache, which delivers an update
// for each object.
func (w *watchHealth) delivered() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.broken = false
	select {
	case w.delivery <- struct{}{}:
	default:
	}
}

// ok reports whether the cache of the watch is current.
func (w *watchHealth) ok() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.broken
}

// watch sets the error handler of an informer and the event handler that
// clears it. Both are set before the informer starts, so neither fails.
func (w *watchHealth) watch(informer cache.SharedIndexInformer) {
	_ = informer.SetWatchErrorHandlerWithContext(w.failed)
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { w.delivered() },
		UpdateFunc: func(any, any) { w.delivered() },
		DeleteFunc: func(any) { w.delivered() },
	})
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
	set.nodesWatch.watch(nodes.Informer())
	set.eventsWatch.watch(events.Informer())
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
