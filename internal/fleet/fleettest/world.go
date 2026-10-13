// Package fleettest is a fake fleet for tests: EKS clusters, their
// nodegroups and add-ons, and the EC2, Auto Scaling, SSM, CloudWatch, and
// Service Quotas data that refresh reads next to them.
//
// A World holds the state and a virtual clock. Updates started in the world
// progress only when a test calls Advance, so a test decides when an update
// ends and what the product sees in between. The package has no network
// code; awsxtest serves a World over the AWS wire protocols.
package fleettest

import (
	"sync"
	"time"
)

// World is a fake fleet and its clock. It is safe for concurrent use.
type World struct {
	mu         sync.Mutex
	now        time.Time
	state      State
	lastUpdate int
	faults     []*rule
	blocked    int
	changed    chan struct{} // closed and replaced when the clock, the state, or blocked changes
	generation int64
	reads      Reads // see ScriptStatus
}

// New returns an empty world whose clock reads 2026-01-01T00:00:00Z.
func New() *World {
	return &World{
		now:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		changed: make(chan struct{}),
	}
}

// Now returns the world's time.
func (w *World) Now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

// Advance moves the clock forward by d. Updates and insights refreshes that
// are due by the new time finish, in the order they are due, and requests
// that a Fault delayed until then continue.
func (w *World) Advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
	w.settle()
	w.changedState()
}

// Edit runs f on the state with the world locked. It is how a test seeds the
// world and changes it mid-scenario. Empty fields get their defaults when f
// returns (see State). f must not call methods of the world, which is locked,
// and must not keep the pointer, or anything reached through it, after it
// returns.
func (w *World) Edit(f func(s *State)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.state)
	w.state.applyDefaults(w.now)
	w.changedState()
}

// Generation returns a number that grows each time the state may have
// changed: on every Edit, Advance, and update. A server uses it as the
// resource version of what it serves.
func (w *World) Generation() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.generation
}

// Changes returns a channel that is closed the next time the state may
// change. A server that streams the state takes the channel, reads the
// state, and waits on the channel before it reads again.
func (w *World) Changes() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changed
}

// changedState records that the state may have changed. The caller holds
// w.mu.
func (w *World) changedState() {
	w.generation++
	w.broadcast()
}

// View runs f on the state with the world locked, for reading. f must not
// change the state, call methods of the world, or keep anything reached
// through the pointer.
func (w *World) View(f func(s *State)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.state)
}

// broadcast wakes every goroutine that waits on the clock or on the blocked
// count. The caller holds w.mu.
func (w *World) broadcast() {
	close(w.changed)
	w.changed = make(chan struct{})
}
