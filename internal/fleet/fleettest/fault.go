package fleettest

import (
	"context"
	"fmt"
	"time"
)

// Operation names one AWS API operation as IAM does, such as
// "eks:ListClusters". See the constants in this package.
type Operation string

// Fault changes how one operation answers. The zero value answers normally.
// The fields combine: a request first waits (Delay, Hang), then fails
// (Throttle, Deny, Code), and PageSize applies to a request that succeeds.
type Fault struct {
	// Deny answers every request with the service's access-denied error.
	Deny bool
	// Throttle answers this many requests with the service's throttling
	// error. Requests after them go through.
	Throttle int
	// Code answers every request with this API error code.
	Code string
	// Delay holds each request until the world's clock has advanced by it.
	Delay time.Duration
	// Hang holds each request until its context ends.
	Hang bool
	// PageSize makes a paged operation return at most this many items per
	// page, whatever the request asks for.
	PageSize int
}

type faultState struct {
	Fault
	throttled int
}

// SetFault makes op answer as f says, replacing any earlier fault of op. A
// zero f removes it.
func (w *World) SetFault(op Operation, f Fault) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f == (Fault{}) {
		delete(w.faults, op)
		return
	}
	w.faults[op] = &faultState{Fault: f}
}

// FaultKind says which error a Fault answers.
type FaultKind int

// The fault kinds.
const (
	Denied FaultKind = iota + 1
	Throttled
	Coded
)

// FaultError is the error Enter returns for a request that a Fault fails.
type FaultError struct {
	Kind FaultKind
	// Code is the API error code of a Coded fault.
	Code string
}

func (e *FaultError) Error() string {
	switch e.Kind {
	case Denied:
		return "fault: access denied"
	case Throttled:
		return "fault: throttled"
	default:
		return "fault: " + e.Code
	}
}

// Enter applies the fault of op to one request. A server calls it before it
// answers. Enter blocks for a Delay or a Hang, and returns ctx's error when
// ctx ends first. It returns a *FaultError when the request must fail, and
// otherwise the forced page size, or 0 for none.
func (w *World) Enter(ctx context.Context, op Operation) (pageSize int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := w.faults[op]
	if f == nil {
		return 0, nil
	}
	if f.Hang || f.Delay > 0 {
		if err := w.block(ctx, f.Hang, w.now.Add(f.Delay)); err != nil {
			return 0, err
		}
	}
	switch {
	case f.throttled < f.Throttle:
		f.throttled++
		return 0, &FaultError{Kind: Throttled}
	case f.Deny:
		return 0, &FaultError{Kind: Denied}
	case f.Code != "":
		return 0, &FaultError{Kind: Coded, Code: f.Code}
	}
	return f.PageSize, nil
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
