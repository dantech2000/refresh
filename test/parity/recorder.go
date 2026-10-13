//go:build unix

package parity

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// defaultClock are the operations only a wait loop calls.
var defaultClock = []fleettest.Operation{fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh}

// event is one request of the run, to AWS or to Kubernetes.
//
// The recorder numbers what happens in one sequence: a request gets a
// number when it arrives, and another when its answer is ready. The numbers
// say what the binary can have known when it sent a request: only the
// answers that were complete by then.
type event struct {
	aws  *awsxtest.Request
	kube *kubetest.Request
	// arrived and completed are sequence numbers. completed is 0 for a
	// request that got no answer.
	arrived, completed int
	awsAnswer          awsxtest.Answer
	kubeAnswer         kubetest.Answer
	// serialised is set while the request holds the recorder's serial lock.
	serialised bool
}

// operation returns the operation of the event's request.
func (e *event) operation() fleettest.Operation {
	if e.aws != nil {
		return e.aws.Operation
	}
	return e.kube.Operation
}

// recorder numbers the requests and their answers, moves the world's clock,
// sends the scenario's signal, makes the scenario's moves, and holds the
// scenario's held requests, as the binary's requests arrive.
//
// It answers one request at a time: a request takes the serial lock when it
// arrives and gives it up when its answer is ready. So the clock never moves
// under a request that is being answered, and what a request reads does not
// depend on how the hooks of two parallel requests are scheduled. A request
// that the scenario holds gives the lock up while it waits.
type recorder struct {
	sc     Scenario
	world  *fleettest.World
	signal func(os.Signal) error

	serial sync.Mutex

	mu sync.Mutex
	// paused is set while the scenario's preview runs: its requests move
	// the clock and are not recorded.
	paused bool
	seq    int
	events []*event
	// byAWS and byKube find the event of a request by its ID.
	byAWS, byKube map[int]*event
	// changed is closed and replaced whenever an event arrives or
	// completes, to wake the held requests.
	changed chan struct{}
	// polled holds what the binary has polled since the clock last moved.
	polled  map[string]bool
	matched int // requests the scenario's Signal matched
	// signalAt is the sequence number at which the signal was sent, or 0.
	signalAt int
	// confirmations are the sequence numbers at which a prompt was answered.
	confirmations []int
	// moved says which of the scenario's moves were made.
	moved []bool
	// open counts the requests that arrived and have no answer yet.
	open int
	// progressed is when, on the real clock, the binary last did something
	// that the world's pending updates depend on: a poll of a clock
	// operation, or a mutation. recent are its last few requests.
	progressed time.Time
	recent     []fleettest.Operation
	failure    error
}

func newRecorder(sc Scenario, world *fleettest.World, signal func(os.Signal) error) *recorder {
	return &recorder{
		sc: sc, world: world, signal: signal,
		byAWS: map[int]*event{}, byKube: map[int]*event{},
		changed: make(chan struct{}), moved: make([]bool, len(sc.Moves)),
	}
}

// clock returns the scenario's clock operations.
func (rec *recorder) clock() []fleettest.Operation {
	if rec.sc.Clock != nil {
		return rec.sc.Clock
	}
	return defaultClock
}

// idle returns how long ago the binary last polled a clock operation or
// sent a mutation, whether it waits for an answer right now, and its most
// recent requests.
func (rec *recorder) idle() (since time.Duration, waiting bool, recent []fleettest.Operation) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.progressed.IsZero() {
		rec.progressed = time.Now()
	}
	return time.Since(rec.progressed), rec.open > 0, slices.Clone(rec.recent)
}

// next returns the next sequence number and wakes the held requests. The
// caller holds rec.mu.
func (rec *recorder) next() int {
	rec.seq++
	close(rec.changed)
	rec.changed = make(chan struct{})
	return rec.seq
}

// arrive records a request. It returns nil while the preview runs.
func (rec *recorder) arrive(e *event, progress bool) *event {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.open++
	rec.recent = append(rec.recent, e.operation())
	rec.recent = rec.recent[max(0, len(rec.recent)-8):]
	if progress {
		rec.progressed = time.Now()
	}
	if rec.paused {
		return nil
	}
	e.arrived = rec.next()
	rec.events = append(rec.events, e)
	if e.aws != nil {
		rec.byAWS[e.aws.ID] = e
	} else {
		rec.byKube[e.kube.ID] = e
	}
	return e
}

// lock takes the serial lock for a request, and unlock gives it up. e is
// nil for a request of the preview, which is serialised all the same.
func (rec *recorder) lock(e *event) {
	rec.serial.Lock()
	if e != nil {
		rec.mu.Lock()
		e.serialised = true
		rec.mu.Unlock()
	}
}

func (rec *recorder) unlock(e *event) {
	if e != nil {
		rec.mu.Lock()
		held := e.serialised
		e.serialised = false
		rec.mu.Unlock()
		if !held {
			return
		}
	}
	rec.serial.Unlock()
}

func (rec *recorder) onRequest(ctx context.Context, r awsxtest.Request) {
	isClock := slices.Contains(rec.clock(), r.Operation)
	e := rec.arrive(&event{aws: &r}, r.Mutates || isClock)
	rec.lock(e)
	if isClock {
		rec.poll(fmt.Sprint(r.Operation, r.Region, r.Input["name"], r.Input["clusterName"], r.Input["nodegroupName"], r.Input["addonName"], r.Input["updateId"]))
	}
	if e == nil {
		return
	}
	if r.Mutates {
		rec.move(func(a Anchor) bool { return a.kind == beforeRequest && a.matches(r) })
	}
	rec.hold(ctx, e, r)
	rec.interrupt(ctx, e, r)
}

// poll moves the clock when a poll starts a new round. The caller holds the
// serial lock, so no request is being answered while the clock moves.
func (rec *recorder) poll(key string) {
	rec.mu.Lock()
	round := rec.polled[key]
	if round || rec.polled == nil {
		rec.polled = map[string]bool{}
	}
	rec.polled[key] = true
	rec.mu.Unlock()
	if round {
		// A second poll of the same thing: a new round.
		rec.world.Advance(cmp.Or(rec.sc.Step, time.Minute))
		rec.move(func(a Anchor) bool { return a.kind == updateStatus && rec.updateIs(a) })
	}
}

// updateIs reports whether the update an anchor names has the status it
// names.
func (rec *recorder) updateIs(a Anchor) bool {
	is := false
	rec.world.View(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			if u := c.Update(updateID(a.update)); u != nil {
				is = u.Status == a.status
			}
		}
	})
	return is
}

// hold keeps a request that one of the scenario's Holds names from being
// answered until what the Hold waits for has happened. It gives up the
// serial lock while it waits, and takes it again.
func (rec *recorder) hold(ctx context.Context, e *event, r awsxtest.Request) {
	for _, h := range rec.sc.Holds {
		if !h.Request(r) {
			continue
		}
		rec.unlock(e)
		for {
			rec.mu.Lock()
			done := slices.ContainsFunc(rec.events, func(o *event) bool { return o.aws != nil && o != e && h.Until(*o.aws, o.completed != 0) })
			changed := rec.changed
			rec.mu.Unlock()
			if done {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
		rec.lock(e)
	}
}

// interrupt sends the scenario's signal at the request it names, and then
// holds that request until the binary gives it up, so the signal always
// lands with the same request unanswered.
func (rec *recorder) interrupt(ctx context.Context, e *event, r awsxtest.Request) {
	s := rec.sc.Signal
	if s == nil || !s.Match(r) {
		return
	}
	rec.mu.Lock()
	rec.matched++
	send := rec.matched == s.Nth
	if send {
		rec.signalAt = rec.next()
	}
	rec.mu.Unlock()
	if !send {
		return
	}
	if err := rec.signal(s.Signal); err != nil {
		rec.fail(fmt.Errorf("sending %s: %w", s.Signal, err))
		return
	}
	rec.unlock(e)
	<-ctx.Done()
}

func (rec *recorder) fail(err error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.failure == nil {
		rec.failure = err
	}
}

func (rec *recorder) onAnswer(r awsxtest.Request, a awsxtest.Answer) {
	rec.mu.Lock()
	rec.open--
	e := rec.byAWS[r.ID]
	if e != nil {
		e.completed, e.awsAnswer = rec.next(), a
	}
	rec.mu.Unlock()
	if e != nil && r.Mutates && a.Committed {
		rec.move(func(an Anchor) bool { return an.kind == afterAccepted && an.matches(r) })
		// An update with no duration has ended by now.
		rec.move(func(an Anchor) bool { return an.kind == updateStatus && rec.updateIs(an) })
	}
	rec.unlock(e)
}

func (rec *recorder) onKubeRequest(_ context.Context, r kubetest.Request) {
	e := rec.arrive(&event{kube: &r}, false)
	rec.lock(e)
}

func (rec *recorder) onKubeAnswer(r kubetest.Request, a kubetest.Answer) {
	rec.mu.Lock()
	rec.open--
	e := rec.byKube[r.ID]
	if e != nil {
		e.completed, e.kubeAnswer = rec.next(), a
	}
	rec.mu.Unlock()
	rec.unlock(e)
}

// confirmed records that the nth prompt was answered, and makes the moves
// that wait for it. The terminal calls it before it types the reply, while
// the binary waits for that reply.
func (rec *recorder) confirmed(n int) {
	rec.mu.Lock()
	rec.confirmations = append(rec.confirmations, rec.next())
	rec.mu.Unlock()
	rec.serial.Lock()
	defer rec.serial.Unlock()
	rec.move(func(a Anchor) bool { return a.kind == promptAnswered && a.prompt == n })
}

// resume ends the preview: from here on the requests are recorded.
func (rec *recorder) resume() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.paused, rec.polled = false, nil
}

// move makes each of the scenario's moves whose anchor is due and that was
// not made yet, once. The caller holds the serial lock, so the world
// changes between two requests and not under one.
func (rec *recorder) move(due func(Anchor) bool) {
	for i, m := range rec.sc.Moves {
		rec.mu.Lock()
		run := !rec.moved[i] && due(m.When)
		if run {
			rec.moved[i] = true
		}
		rec.mu.Unlock()
		if !run {
			continue
		}
		if err := m.Do(rec.world); err != nil {
			rec.fail(fmt.Errorf("the move %s: %w", m.When, err))
		}
	}
}

func (rec *recorder) err() error {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.failure != nil {
		return rec.failure
	}
	if s := rec.sc.Signal; s != nil && rec.matched < s.Nth {
		return fmt.Errorf("the signal was never sent: %d matching requests arrived, want %d", rec.matched, s.Nth)
	}
	for i, m := range rec.sc.Moves {
		if !rec.moved[i] {
			return fmt.Errorf("a move was never made: the run did not reach the point %s", m.When)
		}
	}
	return nil
}
