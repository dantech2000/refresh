package fleettest

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Operation names one AWS API operation as IAM does, such as
// "eks:ListClusters". See the constants in this package.
type Operation string

// Service returns the IAM prefix of the operation, such as "eks".
func (op Operation) Service() string {
	service, _, _ := strings.Cut(string(op), ":")
	return service
}

// Scope selects the requests a Fault applies to. A request must match every
// field that is set. The zero Scope matches every request.
type Scope struct {
	Operation Operation
	// Service is an IAM prefix, such as "eks".
	Service string
	Region  string
	// Cluster, Nodegroup, and Addon match the resource an EKS request
	// names.
	Cluster   string
	Nodegroup string
	Addon     string
}

// Call describes one request, for World.Enter.
type Call struct {
	Operation Operation
	Region    string
	// Cluster, Nodegroup, and Addon are the resources the request names, or
	// "".
	Cluster   string
	Nodegroup string
	Addon     string
}

func (s Scope) matches(c Call) bool {
	return (s.Operation == "" || s.Operation == c.Operation) &&
		(s.Service == "" || s.Service == c.Operation.Service()) &&
		(s.Region == "" || s.Region == c.Region) &&
		(s.Cluster == "" || s.Cluster == c.Cluster) &&
		(s.Nodegroup == "" || s.Nodegroup == c.Nodegroup) &&
		(s.Addon == "" || s.Addon == c.Addon)
}

// rank orders scopes from general to specific. See World.SetScopedFault.
func (s Scope) rank() int {
	rank := 0
	for i, set := range []bool{s.Region != "", s.Service != "", s.Operation != "", s.Cluster != "", s.Nodegroup != "" || s.Addon != ""} {
		if set {
			rank |= 1 << i
		}
	}
	return rank
}

// Fault changes how the requests in its scope are answered. The zero value
// answers normally. The fields combine in this order: a request first waits
// (Delay, Hang), then fails (Reset, Throttle, Unauthenticated, Deny, Code).
// PageSize and LoseResponse apply to a request that does not fail.
type Fault struct {
	// Deny answers every request with the service's access-denied error.
	Deny bool
	// Unauthenticated answers every request with the error the service
	// gives for credentials it does not accept. With a Region scope it
	// models a region that is closed to the account.
	Unauthenticated bool
	// Throttle answers this many requests with the service's throttling
	// error. Requests after them go through.
	Throttle int
	// Code answers every request with this API error code. The server
	// picks the HTTP status the service uses for the code, unless Status
	// is set.
	Code   string
	Status int
	// Reset drops the connection of every request without an answer, as a
	// network failure does. Nothing in the world changes.
	Reset bool
	// LoseResponse lets this many requests run, then drops the connection
	// in place of the answer. The change the request made stays, so the
	// client retries a call that already took effect.
	LoseResponse int
	// Delay holds each request until the world's clock has advanced by it.
	Delay time.Duration
	// Hang holds each request until its context ends.
	Hang bool
	// PageSize makes a paged operation return at most this many items per
	// page, whatever the request asks for.
	PageSize int
}

// rule is a fault and its own counters.
type rule struct {
	scope Scope
	Fault
	throttled int
	lost      int
}

// SetFault makes every request of op answer as f says. It is
// SetScopedFault with a scope of that one operation.
func (w *World) SetFault(op Operation, f Fault) {
	w.SetScopedFault(Scope{Operation: op}, f)
}

// SetScopedFault makes the requests in scope answer as f says. It replaces
// an earlier fault with the same scope, and a zero f removes it.
//
// One fault applies to a request: the one with the most specific scope that
// matches. A scope is more specific when it names a nodegroup or an add-on;
// then when it names a cluster; then an operation; then a service; then a
// region. Each fault counts its own throttles and lost responses, so a fault
// for one cluster does not use up the count of a fault for the operation.
func (w *World) SetScopedFault(scope Scope, f Fault) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, r := range w.faults {
		if r.scope == scope {
			w.faults = append(w.faults[:i], w.faults[i+1:]...)
			break
		}
	}
	if f != (Fault{}) {
		w.faults = append(w.faults, &rule{scope: scope, Fault: f})
	}
}

// fault returns the rule that applies to c, or nil. The caller holds w.mu.
func (w *World) fault(c Call) *rule {
	var best *rule
	for _, r := range w.faults {
		if r.scope.matches(c) && (best == nil || r.scope.rank() > best.scope.rank()) {
			best = r
		}
	}
	return best
}

// FaultKind says how a Fault fails a request.
type FaultKind int

// The fault kinds.
const (
	Denied FaultKind = iota + 1
	Throttled
	Unauthenticated
	Coded
	// ConnectionReset means the server must drop the connection.
	ConnectionReset
)

// FaultError is the error Enter returns for a request that a Fault fails.
type FaultError struct {
	Kind FaultKind
	// Code and Status are the API error code and the HTTP status of a Coded
	// fault. A zero Status leaves the status to the server.
	Code   string
	Status int
}

func (e *FaultError) Error() string {
	switch e.Kind {
	case Denied:
		return "fault: access denied"
	case Throttled:
		return "fault: throttled"
	case Unauthenticated:
		return "fault: credentials not accepted"
	case ConnectionReset:
		return "fault: connection reset"
	default:
		return "fault: " + e.Code
	}
}

// Admission is what Enter tells a server about a request it lets through.
type Admission struct {
	// PageSize is the page size a fault forces, or 0.
	PageSize int
	// LoseResponse tells the server to run the request and then drop the
	// connection in place of the answer.
	LoseResponse bool
}

// Enter applies the fault that matches a request. A server calls it before
// it answers. Enter blocks for a Delay or a Hang, and returns ctx's error
// when ctx ends first. It returns a *FaultError when the request must fail.
func (w *World) Enter(ctx context.Context, c Call) (Admission, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := w.fault(c)
	if f == nil {
		return Admission{}, nil
	}
	if f.Hang || f.Delay > 0 {
		if err := w.block(ctx, f.Hang, w.now.Add(f.Delay)); err != nil {
			return Admission{}, err
		}
	}
	switch {
	case f.Reset:
		return Admission{}, &FaultError{Kind: ConnectionReset}
	case f.throttled < f.Throttle:
		f.throttled++
		return Admission{}, &FaultError{Kind: Throttled}
	case f.Unauthenticated:
		return Admission{}, &FaultError{Kind: Unauthenticated}
	case f.Deny:
		return Admission{}, &FaultError{Kind: Denied}
	case f.Code != "":
		return Admission{}, &FaultError{Kind: Coded, Code: f.Code, Status: f.Status}
	}
	a := Admission{PageSize: f.PageSize}
	if f.lost < f.LoseResponse {
		f.lost++
		a.LoseResponse = true
	}
	return a, nil
}

// block waits until the clock reaches until, or forever when hang is set,
// and returns ctx's error if ctx ends first. The caller holds w.mu; block
// releases it while it waits.
func (w *World) block(ctx context.Context, hang bool, until time.Time) error {
	w.blocked++
	w.broadcast()
	defer func() {
		w.blocked--
		w.broadcast()
	}()
	for hang || w.now.Before(until) {
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			w.mu.Lock()
			return ctx.Err()
		case <-changed:
		}
		w.mu.Lock()
	}
	return nil
}

// AwaitBlocked waits until exactly n requests are held by a Delay or a
// Hang. A test uses it as the handshake before it advances the clock or
// cancels a request, and to see that a cancelled request has left.
func (w *World) AwaitBlocked(ctx context.Context, n int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.blocked != n {
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			w.mu.Lock()
			return fmt.Errorf("waiting for %d blocked requests, have %d: %w", n, w.blocked, ctx.Err())
		case <-changed:
		}
		w.mu.Lock()
	}
	return nil
}
