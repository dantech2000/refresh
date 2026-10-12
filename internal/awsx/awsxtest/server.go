// Package awsxtest serves the AWS APIs that refresh calls from a
// fleettest.World, over HTTP and in the real wire protocols, so tests reach
// the fake with real AWS SDK clients.
//
//	world := fleettest.New()
//	world.Edit(func(s *fleettest.State) { ... })
//	srv := awsxtest.NewServer(world)
//	defer srv.Close()
//	client := eks.NewFromConfig(srv.Config("us-east-1"))
//
// The server answers the operations in Operations and nothing else: any
// other call gets a non-retryable UnsupportedOperation error. Faults set on
// the world (World.SetFault, World.SetScopedFault) decide when a request
// fails, waits, or pages. COVERAGE.md lists what the tests prove for each
// operation.
//
// A Handler is the same fake as a plain http.Handler, for a program that
// runs its own listener, as tools/fakefleet does.
package awsxtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Request is one request the fake received.
type Request struct {
	// Operation is the operation, such as "eks:ListClusters". For a call
	// the fake does not model, it is the service and what identifies the
	// call on the wire.
	Operation fleettest.Operation
	// Service is the IAM prefix of the service, such as "eks".
	Service string
	// Region is the region the request was signed for.
	Region string
	// Mutates reports whether the operation changes AWS state.
	Mutates bool
	// Input is the decoded input under its wire names. For EKS it is the
	// JSON body plus the path and query parameters. For the query
	// protocols it holds the flat form keys, such as "InstanceId.1".
	// Numbers from JSON and CBOR are float64; path, query, and form values
	// are strings.
	Input map[string]any
}

// clone returns a copy of r that shares no memory with it.
func (r Request) clone() Request {
	r.Input = cloneInput(r.Input)
	return r
}

// Handler answers AWS API requests from a World. It serves every service
// and region on one endpoint and tells them apart by the request's SigV4
// credential scope.
type Handler struct {
	// Latency, when set, holds each request for the time it returns, on the
	// real clock. Throttle, when set, answers the service's throttling
	// error for each request it returns true for. Both are for a program
	// that serves a fake fleet to a person; set them before the first
	// request. A test uses the world's faults, which follow the virtual
	// clock and count requests.
	Latency  func(Request) time.Duration
	Throttle func(Request) bool

	world  *fleettest.World
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	requests []Request
	active   map[concurrencyKey]int
	peak     map[concurrencyKey]int
}

// concurrencyKey names a group of requests counted together. An empty
// field means any.
type concurrencyKey struct{ service, region string }

// NewHandler returns a handler that answers from w. The caller must call
// Close when it stops serving.
func NewHandler(w *fleettest.World) *Handler {
	h := &Handler{world: w, active: map[concurrencyKey]int{}, peak: map[concurrencyKey]int{}}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h
}

// Close releases the requests that a fault still holds, by dropping their
// connections. Requests that arrive later are served as before.
func (h *Handler) Close() { h.cancel() }

// Requests returns a copy of every request received so far, in arrival
// order. A request is recorded before any fault applies to it, so a
// throttled or hanging request is in the list.
func (h *Handler) Requests() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Request, len(h.requests))
	for i, r := range h.requests {
		out[i] = r.clone()
	}
	return out
}

// PeakConcurrency returns the most requests that were in progress at the
// same time for a service (an IAM prefix) in a region. An empty service or
// region means any. A request is in progress from the moment the handler
// has read its headers, before it reads the body, until its answer is
// written. The time a fault or a hook holds it counts.
func (h *Handler) PeakConcurrency(service, region string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peak[concurrencyKey{service, region}]
}

func (h *Handler) record(r Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, r)
}

// begin counts a request as in progress. It returns the function that ends
// it.
func (h *Handler) begin(service, region string) (end func()) {
	keys := []concurrencyKey{{service, region}, {service, ""}, {"", region}, {"", ""}}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range keys {
		h.active[k]++
		h.peak[k] = max(h.peak[k], h.active[k])
	}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, k := range keys {
			h.active[k]--
		}
	}
}

// call is one request to an operation the fake models.
type call struct {
	world  *fleettest.World
	svc    *service
	region string
	in     input
	// pageSize is the page size a fault forces, or 0.
	pageSize int
}

// sees reports whether an item with the given Region field answers in the
// request's region.
func (c *call) sees(itemRegion string) bool {
	return fleettest.Visible(itemRegion, c.region)
}

// apiError is an AWS API error response.
type apiError struct {
	code    string
	message string
	// status overrides the HTTP status the service uses for the code.
	status int
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func newError(code, message string) *apiError {
	return &apiError{code: code, message: message}
}

// credentialScope reads the region and the signing name from a SigV4
// Authorization header.
var credentialScope = regexp.MustCompile(`Credential=[^/]+/[^/]+/([^/]+)/([^/]+)/aws4_request`)

// ServeHTTP answers one AWS API request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := credentialScope.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		http.Error(w, "awsxtest: the request is not signed", http.StatusForbidden)
		return
	}
	region := m[1]
	svc, ok := services[m[2]]
	if !ok {
		http.Error(w, "awsxtest: no service signs as "+m[2], http.StatusBadRequest)
		return
	}
	defer h.begin(svc.name, region)()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		panic(http.ErrAbortHandler) // the client went away
	}

	action, in, err := svc.protocol.decode(r, body)
	if err != nil {
		svc.writeError(w, newError("SerializationException", err.Error()))
		return
	}
	op, known := svc.operations[action]
	name := fleettest.Operation(svc.name + ":" + action)
	// The log, each hook, and the operation get their own copy of the
	// input, so none can change what another reads.
	req := Request{Operation: name, Service: svc.name, Region: region, Mutates: op.mutates, Input: in}
	h.record(req.clone())

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(h.ctx, cancel)()

	if !h.hooks(ctx, w, svc, req) {
		return
	}
	if !known {
		svc.writeError(w, newError("UnsupportedOperation", "awsxtest does not model "+string(name)))
		return
	}

	admission, err := h.world.Enter(ctx, fleettest.Call{
		Operation: name, Region: region,
		Cluster: in.clusterName(), Nodegroup: in.str("nodegroupName"), Addon: in.str("addonName"),
	})
	var fault *fleettest.FaultError
	switch {
	case errors.As(err, &fault) && fault.Kind != fleettest.ConnectionReset:
		svc.writeError(w, svc.faultError(fault))
		return
	case err != nil:
		// The fault resets the connection, the client left, or the handler
		// is closing while a fault held the request. A plain return would
		// answer 200.
		panic(http.ErrAbortHandler)
	}

	result, err := op.handle(&call{world: h.world, svc: svc, region: region, in: in, pageSize: admission.PageSize})
	if err == nil && h.world.LoseResponse(admission) {
		// The operation ran. The client never learns the result.
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		var apiErr *apiError
		if !errors.As(err, &apiErr) {
			apiErr = newError("InternalFailure", err.Error())
		}
		svc.writeError(w, apiErr)
		return
	}
	svc.protocol.writeResult(w, action, result)
}

// hooks runs Latency and Throttle. It reports whether the request goes on.
func (h *Handler) hooks(ctx context.Context, w http.ResponseWriter, svc *service, req Request) bool {
	if h.Latency != nil {
		if d := h.Latency(req.clone()); d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				panic(http.ErrAbortHandler)
			}
		}
	}
	if h.Throttle != nil && h.Throttle(req.clone()) {
		svc.writeError(w, svc.faultError(&fleettest.FaultError{Kind: fleettest.Throttled}))
		return false
	}
	return true
}

// Server is a Handler on a local listener.
type Server struct {
	*Handler
	// URL is the endpoint that answers for every service and region.
	URL string

	http   *httptest.Server
	client *http.Client
}

// NewServer starts a server that answers from w. The caller must call Close.
func NewServer(w *fleettest.World) *Server {
	s := &Server{Handler: NewHandler(w), client: &http.Client{Transport: &http.Transport{}}}
	s.http = httptest.NewServer(s.Handler)
	s.URL = s.http.URL
	return s
}

// Close stops the server. Requests that a fault still holds are released
// first, and the connections of clients built from Config are closed.
func (s *Server) Close() {
	s.Handler.Close()
	s.client.CloseIdleConnections()
	s.http.Close()
}

// Config returns an SDK config for region that reaches the server, with
// static fake credentials. It retries a request up to 3 times in total with
// no backoff, so a test of retries does not wait.
func (s *Server) Config(region string) aws.Config {
	return aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDFAKEFAKEFAKE", "fake-secret", ""),
		BaseEndpoint: aws.String(s.URL),
		HTTPClient:   s.client,
		Retryer: func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 3
				o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
			})
		},
	}
}
