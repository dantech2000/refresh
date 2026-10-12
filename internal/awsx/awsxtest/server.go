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
// the world (World.SetFault) decide when an operation fails, waits, or
// pages. COVERAGE.md lists what the tests prove for each operation.
package awsxtest

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Request is one request the server received.
type Request struct {
	// Operation is the operation, such as "eks:ListClusters". For a call
	// the server does not model, it is the service and what identifies the
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

// Server is a fake AWS endpoint.
type Server struct {
	// URL is the endpoint that answers for every service and region.
	URL string

	world  *fleettest.World
	http   *httptest.Server
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	requests []Request
}

// NewServer starts a server that answers from w. The caller must call Close.
func NewServer(w *fleettest.World) *Server {
	s := &Server{world: w, client: &http.Client{Transport: &http.Transport{}}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.http.URL
	return s
}

// Close stops the server. Requests that a fault still holds are released
// first, and the connections of clients built from Config are closed.
func (s *Server) Close() {
	s.cancel()
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

// Requests returns every request received so far, in arrival order. A
// request is recorded before any fault applies to it, so a throttled or
// hanging request is in the list.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// call is one request to an operation the server models.
type call struct {
	world  *fleettest.World
	region string
	in     input
	// pageSize is the page size a fault forces, or 0.
	pageSize int
}

// apiError is an AWS API error response.
type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func badRequest(code, message string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: code, message: message}
}

// credentialScope reads the region and the signing name from a SigV4
// Authorization header.
var credentialScope = regexp.MustCompile(`Credential=[^/]+/[^/]+/([^/]+)/([^/]+)/aws4_request`)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return // the client went away
	}
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

	action, in, err := svc.protocol.decode(r, body)
	if err != nil {
		svc.protocol.writeError(w, badRequest("SerializationException", err.Error()))
		return
	}
	op, known := svc.operations[action]
	name := fleettest.Operation(svc.name + ":" + action)
	s.record(Request{Operation: name, Service: svc.name, Region: region, Mutates: op.mutates, Input: maps.Clone(in)})
	if !known {
		svc.protocol.writeError(w, badRequest("UnsupportedOperation", "awsxtest does not model "+string(name)))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()
	pageSize, err := s.world.Enter(ctx, name)
	var fault *fleettest.FaultError
	switch {
	case errors.As(err, &fault):
		svc.protocol.writeError(w, svc.faultError(fault))
		return
	case err != nil:
		// The client left or the server is closing while a fault held the
		// request. Drop the connection: a plain return would answer 200.
		panic(http.ErrAbortHandler)
	}

	result, err := op.handle(&call{world: s.world, region: region, in: in, pageSize: pageSize})
	if err != nil {
		var apiErr *apiError
		if !errors.As(err, &apiErr) {
			apiErr = &apiError{status: http.StatusInternalServerError, code: "InternalFailure", message: err.Error()}
		}
		svc.protocol.writeError(w, apiErr)
		return
	}
	svc.protocol.writeResult(w, action, result)
}

func (s *Server) record(r Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
}
