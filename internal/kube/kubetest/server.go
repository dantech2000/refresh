// Package kubetest serves the Kubernetes API of one cluster of a
// fleettest.World, over HTTP, as far as refresh reads it. Tests reach it with
// a real client-go clientset, in the test's own process or from a refresh
// binary that runs as a subprocess with the kubeconfig the server writes.
//
//	world.Edit(func(s *fleettest.State) { ... nodes, pods, PDBs ... })
//	srv := kubetest.NewServer(world, "us-east-1", "prod")
//	defer srv.Close()
//	client, err := kubernetes.NewForConfig(srv.Config())
//
// # What it serves
//
// These are the requests the legacy binary makes, found by reading its
// health checks, its post-roll verification, and its live node view:
//
//	list   namespaces                      (the connection probe, PDB coverage)
//	list   nodes                           (label selectors "k=v" and "k in (a,b)")
//	watch  nodes                           (the live view's informer)
//	list   pods, all namespaces or one     (field selectors spec.nodeName, status.phase)
//	list   poddisruptionbudgets (policy/v1)
//	list   deployments (apps/v1)
//	list   events                          (field selector type=Warning)
//	watch  events                          (the live view's informer)
//	list   nodes (metrics.k8s.io/v1beta1)  (drain headroom)
//
// It serves nothing else. refresh writes nothing to Kubernetes: EKS drains
// the nodes. A request for anything else gets 404, and a field selector the
// server does not know gets 400, so a test never reads a list that ignored
// part of the request.
//
// Lists carry the world's generation as their resource version. The world's
// generation grows on every change of the world, most of which do not
// touch the listed collection, so a watch rarely starts from the current
// one. The handler remembers its recent lists: a watch that starts from one
// of them first gets what changed in the collection since that list, and
// then what changes afterwards. A watch from a generation the handler does
// not remember gets 410 Gone, and the informer lists again. A watch that
// asks for the initial events (the streaming list of newer client-go
// versions) gets them, then the bookmark that ends them.
//
// # Faults
//
// A request is an operation of the service "kube" (fleettest.KubeListPods
// and the others), so World.SetScopedFault applies to it: Deny answers 403
// Forbidden, Code answers that reason, Delay and Hang hold the request, and
// Reset drops the connection.
//
// # The spike: this server, or envtest
//
// REWRITE.md asked for a spike between a hand-written list and watch server
// and a pinned envtest API server (kube-apiserver and etcd) that the world
// writes to. This server was built first, as the smaller one, and it was
// enough, so envtest was assessed from its documentation and not run.
//
//   - Informers: client-go's shared informers work against this server with
//     no change, on both the list-then-watch path and the streaming-list
//     path (TestInformer). The legacy binary's own node and event informers
//     run against it in the parity scenario nodegroup-update-live-view.
//   - Determinism: the server holds no state of its own. Every answer is
//     the world at that moment, so nodes and pods move with the world's
//     virtual clock, and a scenario scripts a stuck drain or a node that
//     never becomes Ready through the update script. With envtest the
//     runner would have to write each change of the world to etcd and wait
//     for it to be visible, and resource versions and timestamps would come
//     from the API server.
//   - Start-up: an httptest server, well under a millisecond. envtest
//     starts etcd and kube-apiserver, which takes seconds per scenario, or
//     one shared server and a namespace per scenario, which does not fit
//     cluster-scoped nodes.
//   - Dependencies: none new; client-go, api, apimachinery, and metrics are
//     in go.mod for the old tree. envtest adds controller-runtime and, in
//     CI and on every laptop, a download of the two server binaries.
//   - Fidelity: this is where envtest wins. This server does not validate
//     selectors as the API server does, pages nothing, and serves no
//     discovery, so it only fits clients that use the typed clientset for
//     the resources above. That is all the product does today.
//
// The choice is this server. It should be revisited if the product starts
// to write to Kubernetes (evictions, cordons) or to use discovery or
// dynamic clients.
package kubetest

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"sync"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Request is one request the server received.
type Request struct {
	// ID numbers the requests of a handler in arrival order, from 1.
	ID int
	// Operation is the request as a fault names it, such as
	// fleettest.KubeListPods.
	Operation fleettest.Operation
	// Namespace is the namespace of a namespaced request, or "".
	Namespace string
	// LabelSelector and FieldSelector are the request's selectors.
	LabelSelector string
	FieldSelector string
}

// Answer is how a request ended.
type Answer struct {
	// Reason is the Kubernetes status reason of a request that failed, such
	// as Forbidden, or "".
	Reason string
	// Dropped is set when the connection was dropped in place of an answer.
	Dropped bool
	// Observed summarises the items of a list: one word per item with the
	// part of it a decision can rest on. It is empty for a watch.
	Observed string
}

// Handler answers Kubernetes API requests for one cluster of a World.
type Handler struct {
	// OnRequest, when set, is called for each request after it is logged
	// and before a fault or the answer. The request waits until it
	// returns. OnAnswer, when set, is called once for each request when
	// the handler knows how it ends; for a watch, when the stream starts.
	// Set both before the first request.
	OnRequest func(ctx context.Context, r Request)
	OnAnswer  func(r Request, a Answer)

	world   *fleettest.World
	region  string
	cluster string
	ctx     context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	requests []Request
	// lists are the most recent lists the handler answered, newest last. A
	// watch that starts from one of them gets what changed since.
	lists []listed
}

// listed is one list as it was answered.
type listed struct {
	// collection names the kind and the selection of the list.
	collection string
	generation int64
	// objects are the listed objects as JSON, by namespace/name.
	objects map[string]string
}

// listHistory is how many lists the handler remembers. It only has to span
// the gap between an informer's list and its watch.
const listHistory = 64

// NewHandler returns a handler for the cluster of w with the name, as it
// answers in region. The caller must call Close when it stops serving.
func NewHandler(w *fleettest.World, region, cluster string) *Handler {
	h := &Handler{world: w, region: region, cluster: cluster}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h
}

// Close ends the watches and releases the requests that a fault holds.
func (h *Handler) Close() { h.cancel() }

// Requests returns every request received so far, in arrival order.
func (h *Handler) Requests() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.requests)
}

// Server is a Handler on a local listener.
type Server struct {
	*Handler
	// URL is the address of the API server. It is plain HTTP: client-go
	// accepts that, and refresh matches a kubeconfig to a cluster by host
	// and port, so no certificate is needed.
	URL string

	http *httptest.Server
}

// NewServer starts a server for the cluster of w with the name, and sets
// the cluster's Endpoint in the world to the server's URL, so that the fake
// EKS reports this server as the cluster's API. The caller must call Close.
func NewServer(w *fleettest.World, region, cluster string) *Server {
	return Serve(NewHandler(w, region, cluster))
}

// Serve is NewServer for a handler the caller built, to set its hooks
// before the first request.
func Serve(h *Handler) *Server {
	s := &Server{Handler: h}
	s.http = httptest.NewServer(h)
	s.URL = s.http.URL
	h.world.Edit(func(st *fleettest.State) {
		if c := st.Cluster(h.region, h.cluster); c != nil {
			c.Endpoint = s.URL
		}
	})
	return s
}

// Close stops the server.
func (s *Server) Close() {
	s.Handler.Close()
	s.http.CloseClientConnections()
	s.http.Close()
}

// Config returns a client-go config that reaches the server.
func (s *Server) Config() *rest.Config {
	return &rest.Config{Host: s.URL}
}

// WriteKubeconfig writes a kubeconfig file with one context, named after
// the cluster, whose server is this server.
func (s *Server) WriteKubeconfig(path string) error {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[s.cluster] = &clientcmdapi.Cluster{Server: s.URL}
	cfg.AuthInfos[s.cluster] = &clientcmdapi.AuthInfo{Token: "fake-token"}
	cfg.Contexts[s.cluster] = &clientcmdapi.Context{Cluster: s.cluster, AuthInfo: s.cluster}
	cfg.CurrentContext = s.cluster
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return fmt.Errorf("encoding the kubeconfig: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
