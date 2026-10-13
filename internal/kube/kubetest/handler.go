package kubetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// collection is one kind the server lists.
type collection struct {
	list, watch fleettest.Operation
	// kind and apiVersion are those of one item. The list's kind is kind
	// plus "List".
	kind, apiVersion string
	// items builds the items of the cluster, in a stable order.
	items func(c *fleettest.Cluster) []item
}

// item is one object of a list, with what the server needs to select it and
// to describe it.
type item struct {
	namespace, name string
	labels          map[string]string
	// fields are the values a field selector can ask for.
	fields map[string]string
	// object is the Kubernetes API object. summary is the word for it in
	// Answer.Observed.
	object  any
	summary string
}

// routes maps the path of a collection, without a namespace, to its
// collection.
var routes = map[string]collection{
	"/api/v1/namespaces":                   {list: fleettest.KubeListNamespaces, kind: "Namespace", apiVersion: "v1", items: namespaces},
	"/api/v1/nodes":                        {list: fleettest.KubeListNodes, watch: fleettest.KubeWatchNodes, kind: "Node", apiVersion: "v1", items: nodes},
	"/api/v1/pods":                         {list: fleettest.KubeListPods, kind: "Pod", apiVersion: "v1", items: pods},
	"/api/v1/events":                       {list: fleettest.KubeListEvents, watch: fleettest.KubeWatchEvents, kind: "Event", apiVersion: "v1", items: events},
	"/apis/policy/v1/poddisruptionbudgets": {list: fleettest.KubeListPDBs, kind: "PodDisruptionBudget", apiVersion: "policy/v1", items: pdbs},
	"/apis/apps/v1/deployments":            {list: fleettest.KubeListDeployments, kind: "Deployment", apiVersion: "apps/v1", items: deployments},
	"/apis/metrics.k8s.io/v1beta1/nodes":   {list: fleettest.KubeListNodeMetrics, kind: "NodeMetrics", apiVersion: "metrics.k8s.io/v1beta1", items: nodeMetrics},
}

// route finds the collection of a path, and the namespace of a namespaced
// path such as /api/v1/namespaces/{ns}/pods.
func route(path string) (res collection, namespace string, ok bool) {
	path = strings.TrimSuffix(path, "/")
	if res, ok = routes[path]; ok && res.items != nil {
		return res, "", true
	}
	const marker = "/namespaces/"
	i := strings.Index(path, marker)
	if i < 0 {
		return collection{}, "", false
	}
	namespace, rest, found := strings.Cut(path[i+len(marker):], "/")
	if !found {
		return collection{}, "", false
	}
	res, ok = routes[path[:i]+"/"+rest]
	return res, namespace, ok && res.items != nil
}

// status is a Kubernetes error.
type status struct {
	code    int
	reason  metav1.StatusReason
	message string
}

func (e *status) Error() string { return string(e.reason) + ": " + e.message }

func (h *Handler) record(r Request) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.ID = len(h.requests) + 1
	h.requests = append(h.requests, r)
	return r.ID
}

func (h *Handler) answered(r Request, a Answer) {
	if h.OnAnswer != nil {
		h.OnAnswer(r, a)
	}
}

// ServeHTTP answers one Kubernetes API request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res, namespace, ok := route(r.URL.Path)
	if !ok || r.Method != http.MethodGet {
		writeStatus(w, &status{http.StatusNotFound, metav1.StatusReasonNotFound, "kubetest does not serve " + r.Method + " " + r.URL.Path})
		return
	}
	q := r.URL.Query()
	watch := q.Get("watch") == "true" || q.Get("watch") == "1"
	req := Request{Operation: res.list, Namespace: namespace, LabelSelector: q.Get("labelSelector"), FieldSelector: q.Get("fieldSelector")}
	if watch {
		req.Operation = res.watch
	}
	req.ID = h.record(req)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(h.ctx, cancel)()
	drop := func() {
		h.answered(req, Answer{Dropped: true})
		panic(http.ErrAbortHandler) // a plain return would answer 200
	}
	fail := func(e *status) {
		h.answered(req, Answer{Reason: string(e.reason)})
		writeStatus(w, e)
	}

	if h.OnRequest != nil {
		h.OnRequest(ctx, req)
		if ctx.Err() != nil {
			drop()
		}
	}
	if watch && res.watch == "" {
		fail(&status{http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "kubetest does not watch " + res.kind})
		return
	}
	_, err := h.world.Enter(ctx, fleettest.Call{Operation: req.Operation, Region: h.region, Cluster: h.cluster})
	var fault *fleettest.FaultError
	switch {
	case errors.As(err, &fault) && fault.Kind != fleettest.ConnectionReset:
		fail(faultStatus(fault, res.kind))
		return
	case err != nil:
		drop()
	}

	sel, err := newSelection(namespace, req.LabelSelector, req.FieldSelector)
	if err != nil {
		fail(&status{http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error()})
		return
	}
	if watch {
		h.watch(ctx, w, req, res, sel, q)
		return
	}
	items, generation, err := h.snapshot(res, sel)
	if err != nil {
		fail(&status{http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error()})
		return
	}
	objects := make([]any, 0, len(items))
	words := make([]string, 0, len(items))
	for _, it := range items {
		objects = append(objects, it.object)
		words = append(words, it.summary)
	}
	h.answered(req, Answer{Observed: strings.Join(words, " ")})
	writeJSON(w, http.StatusOK, map[string]any{
		"kind": res.kind + "List", "apiVersion": res.apiVersion,
		"metadata": map[string]any{"resourceVersion": strconv.FormatInt(generation, 10)},
		"items":    objects,
	})
}

// faultStatus words a fault as the Kubernetes API does.
func faultStatus(f *fleettest.FaultError, kind string) *status {
	switch f.Kind {
	case fleettest.Denied:
		return &status{http.StatusForbidden, metav1.StatusReasonForbidden, kind + " is forbidden: User \"fake\" cannot list the resource at the cluster scope"}
	case fleettest.Unauthenticated:
		return &status{http.StatusUnauthorized, metav1.StatusReasonUnauthorized, "Unauthorized"}
	case fleettest.Throttled:
		return &status{http.StatusInternalServerError, metav1.StatusReasonInternalError, "kubetest does not model throttling: client-go would wait on the real clock"}
	default:
		code := f.Status
		if code == 0 {
			code = http.StatusInternalServerError
		}
		return &status{code, metav1.StatusReason(f.Code), "kubetest: injected " + f.Code}
	}
}

// selection is what a request asks for.
type selection struct {
	namespace string
	labels    labels.Selector
	fields    fields.Selector
}

func newSelection(namespace, labelSelector, fieldSelector string) (selection, error) {
	sel := selection{namespace: namespace, labels: labels.Everything(), fields: fields.Everything()}
	var err error
	if labelSelector != "" {
		if sel.labels, err = labels.Parse(labelSelector); err != nil {
			return sel, fmt.Errorf("unable to parse the label selector %q: %w", labelSelector, err)
		}
	}
	if fieldSelector != "" {
		if sel.fields, err = fields.ParseSelector(fieldSelector); err != nil {
			return sel, fmt.Errorf("unable to parse the field selector %q: %w", fieldSelector, err)
		}
	}
	return sel, nil
}

// snapshot returns the items of the cluster that the selection keeps, with
// their resource version set, and the world's generation.
func (h *Handler) snapshot(res collection, sel selection) ([]item, int64, error) {
	var all []item
	h.world.View(func(s *fleettest.State) {
		if c := s.Cluster(h.region, h.cluster); c != nil {
			all = res.items(c)
		}
	})
	// The generation is read after the items. If the world changed in
	// between, the list carries the newer number with the older items, and
	// a watch from that number would miss the change. A watch from a
	// generation that is no longer current is refused, so the client lists
	// again; see watch.
	generation := h.world.Generation()
	var out []item
	for _, it := range all {
		if sel.namespace != "" && it.namespace != sel.namespace {
			continue
		}
		if !sel.labels.Matches(labels.Set(it.labels)) {
			continue
		}
		for _, req := range sel.fields.Requirements() {
			if _, known := it.fields[req.Field]; !known {
				return nil, 0, fmt.Errorf("field label not supported: %s (kubetest knows %v)", req.Field, sortedKeys(it.fields))
			}
		}
		if !sel.fields.Matches(fields.Set(it.fields)) {
			continue
		}
		out = append(out, it)
	}
	return out, generation, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// watch streams the changes of a collection until the client leaves.
func (h *Handler) watch(ctx context.Context, w http.ResponseWriter, req Request, res collection, sel selection, q map[string][]string) {
	get := func(key string) string {
		if v := q[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	// Take the channel before the snapshot, so a change after the snapshot
	// wakes the loop.
	changes := h.world.Changes()
	items, generation, err := h.snapshot(res, sel)
	if err != nil {
		h.answered(req, Answer{Reason: string(metav1.StatusReasonBadRequest)})
		writeStatus(w, &status{http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error()})
		return
	}
	initial := get("sendInitialEvents") == "true"
	if from := get("resourceVersion"); !initial && from != "" && from != "0" && from != strconv.FormatInt(generation, 10) {
		// The server keeps no history. The client lists again.
		h.answered(req, Answer{Reason: string(metav1.StatusReasonExpired)})
		writeStatus(w, &status{http.StatusGone, metav1.StatusReasonExpired, "too old resource version: " + from + " (" + strconv.FormatInt(generation, 10) + ")"})
		return
	}
	h.answered(req, Answer{})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(kind string, object any) bool {
		if err := enc.Encode(map[string]any{"type": kind, "object": object}); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	sent := map[string]string{} // namespace/name -> the object as it was sent
	emit := func(items []item, generation int64, replay bool) bool {
		seen := map[string]bool{}
		for _, it := range items {
			key := it.namespace + "/" + it.name
			seen[key] = true
			encoded, _ := json.Marshal(it.object) // an API object built here encodes
			kind := "MODIFIED"
			switch old, had := sent[key]; {
			case !had:
				kind = "ADDED"
			case old == string(encoded):
				continue
			}
			sent[key] = string(encoded)
			if replay && kind == "ADDED" && !initial {
				continue // the client has it from its list
			}
			if !send(kind, withVersion(it.object, generation)) {
				return false
			}
		}
		for key := range sent {
			if !seen[key] {
				var gone any
				_ = json.Unmarshal([]byte(sent[key]), &gone)
				delete(sent, key)
				if !send("DELETED", gone) {
					return false
				}
			}
		}
		return true
	}
	if !emit(items, generation, true) {
		return
	}
	if initial {
		bookmark := map[string]any{"kind": res.kind, "apiVersion": res.apiVersion, "metadata": map[string]any{
			"resourceVersion": strconv.FormatInt(generation, 10),
			"annotations":     map[string]string{metav1.InitialEventsAnnotationKey: "true"},
		}}
		if !send("BOOKMARK", bookmark) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
		}
		changes = h.world.Changes()
		items, generation, err = h.snapshot(res, sel)
		if err != nil || !emit(items, generation, false) {
			return
		}
	}
}

// withVersion returns the object as JSON with its resource version set.
func withVersion(object any, generation int64) any {
	encoded, _ := json.Marshal(object)
	var m map[string]any
	if json.Unmarshal(encoded, &m) != nil {
		return object
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		meta["resourceVersion"] = strconv.FormatInt(generation, 10)
	}
	return m
}

func writeStatus(w http.ResponseWriter, e *status) {
	writeJSON(w, e.code, metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Message: e.message, Reason: e.reason, Code: int32(e.code), //nolint:gosec // an HTTP status
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v) // a write error means the client left
}
