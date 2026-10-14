package change

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
)

// QuestionID names one question of one handle. It is never reused.
type QuestionID uint64

// RunState is where a run is between Start and Finished.
type RunState string

// The run states.
const (
	Preparing RunState = "Preparing" // reading, revalidating, gating; nothing submitted
	Running   RunState = "Running"   // a phase runs
	Paused    RunState = "Paused"    // held at a checkpoint
	Asking    RunState = "Asking"    // a question is open
	Finished  RunState = "Finished"  // Result is set
)

// PhaseStatus is the state of one phase, derived from the events.
type PhaseStatus string

// The phase statuses.
const (
	PhasePending PhaseStatus = "Pending"
	PhaseRunning PhaseStatus = "Running"
	PhaseDone    PhaseStatus = "Done"
	PhaseSkipped PhaseStatus = "Skipped"
	PhaseFailed  PhaseStatus = "Failed"
	// PhaseStopped: the run ended before the phase, or in it, with steps
	// that were not reached.
	PhaseStopped PhaseStatus = "Stopped"
)

// The errors of the controls of a handle.
var (
	ErrStaleQuestion = errors.New("the question is no longer open")
	ErrNoControls    = errors.New("the change was started elsewhere and cannot be steered")
	ErrFinished      = errors.New("the change has finished")
	ErrTooLate       = errors.New("too late to stop: the change has no point left where it can stop")
	ErrNotPaused     = errors.New("the change is not paused")
)

// maxLog is how many log entries a handle keeps. Lifecycle events are few,
// and the entries since the newest plan are kept whatever the cap says,
// so a question and a step result are never dropped; the Auto Scaling
// notices of a roll that waits on capacity for hours are what the cap is
// for.
const maxLog = 4096

// Controls says what a front end may do with the handle now.
type Controls struct {
	// Answer is true while a question is open.
	Answer bool
	// Pause and Stop are true while a checkpoint can still come: for a
	// kind with checkpoints, until the last one has passed. They are
	// false for a watched change.
	Pause bool
	Stop  bool
}

// PendingQuestion is a question that waits for its answer.
type PendingQuestion struct {
	ID      QuestionID
	AskedAt time.Time
	Question
}

// StepProgress is the latest poll and the latest node view of one step.
type StepProgress struct {
	Update *UpdateProgress
	Nodes  *NodeView
}

// LogEntry is one lifecycle event of a run, numbered from 1.
type LogEntry struct {
	Seq uint64
	Event
}

// PhaseState is one phase of the plan and where it is.
type PhaseState struct {
	Ref                PhaseRef
	Status             PhaseStatus
	StartedAt, EndedAt time.Time
}

// View is a handle at one moment. It is immutable: every slice and map is
// a copy, or a reference to data that the handle never changes again.
type View struct {
	Version   uint64
	State     RunState
	Kind      Kind
	Target    Target
	Elsewhere bool // from Watch: no controls, no plan of ours
	StartedAt time.Time
	// Plan is the last plan announced, nil before PlanReady.
	Plan *Plan
	// Steps has the latest result of every step of Plan, in plan order.
	Steps  []StepResult
	Phases []PhaseState
	// Question is the open question, or nil.
	Question *PendingQuestion
	// PauseRequested and StopRequested are the intents, whether or not a
	// checkpoint has read them yet.
	PauseRequested, StopRequested bool
	Progress                      map[StepRef]StepProgress
	Controls                      Controls
	// Result is set at Finished.
	Result *Result
	// LastSeq is the sequence number of the newest log entry, and Dropped
	// how many of the oldest the cap removed.
	LastSeq, Dropped uint64
}

// Handle follows and steers one run. Every method is safe for concurrent
// use. Nothing a front end calls blocks the executor: an event is a map
// write under the lock, and a question or a pause waits on a channel.
type Handle struct {
	kind      Kind
	target    Target
	elsewhere bool
	now       func() time.Time
	// checkpoints says that the kind calls the checkpoint hook, so a pause
	// and a stop have somewhere to take effect.
	checkpoints bool

	mu      sync.Mutex
	version uint64
	changed chan struct{} // closed and replaced on every change
	state   RunState
	// before is the state that a pause or a question interrupted.
	before    RunState
	startedAt time.Time
	plan      *Plan
	steps     []StepResult
	phases    []PhaseState
	progress  map[StepRef]StepProgress
	log       []LogEntry
	seq       uint64
	dropped   uint64
	// lastPlan is the index in log of the newest PlanReady entry.
	lastPlan int

	question     *pendingQuestion
	nextQuestion QuestionID
	pause, stop  bool
	// resume is closed when a pause ends, and replaced.
	resume chan struct{}
	// lastCheckpoint is the checkpoint of the plan after which a stop is
	// too late, when hasLast is set. pastLast is set once it went by.
	lastCheckpoint StepRef
	hasLast        bool
	pastLast       bool

	result *Result
	done   chan struct{}
}

// pendingQuestion is a question and the channel that its answer closes.
type pendingQuestion struct {
	PendingQuestion
	done    chan struct{}
	outcome answer
}

// Start runs the change on a goroutine of its own and returns at once.
// ctx owns the run: cancelling it is an interrupt. The handle reports
// through its own log, so Hooks are not a parameter. The consent rule of
// Run holds: a request without Policy.Yes and without a reviewed plan in
// Common.Expect is refused before any change, because the handle answers
// only the questions that a person answers through Answer.
func Start(ctx context.Context, d Deps, req Request) *Handle {
	kind := req.kind()
	h := newHandle(kind, req.cluster(), false, d.now)
	h.checkpoints = kind == ClusterUpgrade || kind == ClusterRollback
	if roll, ok := req.(RollRequest); ok && roll.Fleet != nil {
		h.checkpoints = true
	}
	go h.run(func(hooks Hooks) (Result, error) { return Run(ctx, d, req, hooks) })
	return h
}

// start is Start over any run: the tests of the state table script one
// that calls the hooks in an order of their own, with no network.
func start(kind Kind, target Target, now func() time.Time, checkpoints bool, run func(Hooks) (Result, error)) *Handle {
	h := newHandle(kind, target, false, now)
	h.checkpoints = checkpoints
	go h.run(run)
	return h
}

func newHandle(kind Kind, target Target, elsewhere bool, now func() time.Time) *Handle {
	return &Handle{
		kind: kind, target: target, elsewhere: elsewhere, now: now,
		changed: make(chan struct{}), state: Preparing, startedAt: now(),
		progress: map[StepRef]StepProgress{}, lastPlan: -1, done: make(chan struct{}),
	}
}

// run runs the change with the hooks of the handle, then finishes.
func (h *Handle) run(fn func(Hooks) (Result, error)) {
	res, _ := fn(Hooks{Event: h.event, Ask: h.ask, Checkpoint: h.checkpoint})
	h.finish(res)
}

// hooks are the three hooks of Run over the handle.

// event takes one event of the run: the two progress kinds replace the
// progress of their step, every other kind is logged, and a plan, a
// phase, or a step result updates the view.
func (h *Handle) event(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch e.Kind {
	case UpdatePolled:
		p := h.progress[e.Step]
		p.Update = e.Update
		h.progress[e.Step] = p
		h.bumpLocked()
		return
	case NodeProgress:
		p := h.progress[e.Step]
		p.Nodes = e.Nodes
		h.progress[e.Step] = p
		h.bumpLocked()
		return
	case PlanReady:
		h.plan = e.Plan
		h.steps, h.phases = nil, nil
		for i := range e.Plan.Clusters {
			for _, ph := range e.Plan.Clusters[i].Phases {
				h.phases = append(h.phases, PhaseState{Ref: ph.Ref, Status: PhasePending})
				for _, s := range ph.Steps {
					h.steps = append(h.steps, StepResult{Ref: s.Ref, Status: StepNotAttempted})
				}
			}
		}
		h.lastCheckpoint, h.hasLast = lastCheckpointOf(e.Plan)
	case PhaseStarted:
		if h.state == Preparing {
			h.state = Running
		}
		if i := h.phaseIndex(e.Phase); i >= 0 {
			h.phases[i].Status, h.phases[i].StartedAt = PhaseRunning, e.At
		}
	case PhaseEnded:
		if i := h.phaseIndex(e.Phase); i >= 0 {
			h.phases[i].EndedAt = e.At
			h.phases[i].Status = h.phaseOutcome(e.Phase, e.Skipped)
		}
	case StepChanged:
		if e.Result != nil {
			// Run keeps writing to the result it emits: the handle copies.
			r := *e.Result
			if i := slices.IndexFunc(h.steps, func(s StepResult) bool { return s.Ref == r.Ref }); i >= 0 {
				h.steps[i] = r
			} else {
				h.steps = append(h.steps, r)
			}
			e.Result = &r
		}
	}
	h.seq++
	h.log = append(h.log, LogEntry{Seq: h.seq, Event: e})
	if e.Kind == PlanReady {
		h.lastPlan = len(h.log) - 1
	}
	if len(h.log) > maxLog && h.lastPlan > 0 {
		h.dropped += uint64(h.lastPlan) //nolint:gosec // an index
		h.log = slices.Clone(h.log[h.lastPlan:])
		h.lastPlan = 0
	}
	h.bumpLocked()
}

// phaseOutcome is the status of a phase that ended, from its steps.
func (h *Handle) phaseOutcome(ref PhaseRef, skipped bool) PhaseStatus {
	if skipped {
		return PhaseSkipped
	}
	for _, s := range h.steps {
		if s.Ref.Phase != ref {
			continue
		}
		switch s.Status {
		case StepSucceeded, StepSkipped, StepStarted:
		default:
			return PhaseFailed
		}
	}
	return PhaseDone
}

func (h *Handle) phaseIndex(ref PhaseRef) int {
	return slices.IndexFunc(h.phases, func(p PhaseState) bool { return p.Ref == ref })
}

// lastCheckpointOf returns the checkpoint of the plan after which a stop
// has no point left to take effect: the last work step of the last phase
// with work when that phase runs its steps one by one, else that phase
// itself. A plan with no work has none.
func lastCheckpointOf(p *Plan) (StepRef, bool) {
	var last StepRef
	found := false
	for i := range p.Clusters {
		for _, ph := range p.Clusters[i].Phases {
			var work []Step
			for _, s := range ph.Steps {
				if s.work() {
					work = append(work, s)
				}
			}
			if len(work) == 0 {
				continue
			}
			found = true
			last = StepRef{Phase: ph.Ref}
			if !ph.Batch && len(work) > 1 {
				last = work[len(work)-1].Ref
			}
		}
	}
	return last, found
}

// ask registers the question and waits for its answer, or for ctx to end.
func (h *Handle) ask(ctx context.Context, q Question) (bool, error) {
	h.mu.Lock()
	h.nextQuestion++
	pending := &pendingQuestion{PendingQuestion: PendingQuestion{ID: h.nextQuestion, AskedAt: h.now(), Question: q}, done: make(chan struct{})}
	h.question = pending
	h.before, h.state = h.state, Asking
	h.bumpLocked()
	h.mu.Unlock()

	select {
	case <-pending.done:
	case <-ctx.Done():
		h.mu.Lock()
		if h.question == pending {
			h.question, h.state = nil, h.before
			h.bumpLocked()
		}
		h.mu.Unlock()
		return false, ctx.Err()
	}
	return pending.outcome == answeredYes, nil
}

// resolve closes the open question with an outcome. It returns
// ErrStaleQuestion when id is not the open question: the first answer
// wins, and a later one finds no question. The caller holds h.mu.
func (h *Handle) resolve(id QuestionID, outcome answer) error {
	q := h.question
	if q == nil || q.ID != id {
		return ErrStaleQuestion
	}
	q.outcome = outcome
	h.question, h.state = nil, h.before
	close(q.done)
	h.bumpLocked()
	return nil
}

// checkpoint reads the intents before next: a stop ends the run, a pause
// holds it until Resume or Stop. The precedence is cancel, then stop,
// then pause, read under the one lock.
func (h *Handle) checkpoint(ctx context.Context, next StepRef) (Checkpoint, error) {
	h.mu.Lock()
	if h.stop {
		h.mu.Unlock()
		return Checkpoint{Stop: true}, nil
	}
	waited := false
	if h.pause {
		waited = true
		h.resume = make(chan struct{})
		h.before, h.state = h.state, Paused
		resume := h.resume
		h.bumpLocked()
		h.mu.Unlock()
		select {
		case <-resume:
		case <-ctx.Done():
			h.mu.Lock()
			h.state = h.before
			h.bumpLocked()
			h.mu.Unlock()
			return Checkpoint{}, ctx.Err()
		}
		h.mu.Lock()
		h.state = h.before
		if h.stop {
			h.bumpLocked()
			h.mu.Unlock()
			return Checkpoint{Stop: true, Waited: true}, nil
		}
	}
	if h.hasLast && next == h.lastCheckpoint {
		h.pastLast = true
	}
	h.bumpLocked()
	h.mu.Unlock()
	return Checkpoint{Waited: waited}, nil
}

// finish records the result and ends the handle. A phase that never
// started, or that started and has steps that were not reached, is
// Stopped: the run ended before it.
func (h *Handle) finish(res Result) {
	h.mu.Lock()
	h.result = &res
	h.state = Finished
	for i := range h.phases {
		switch h.phases[i].Status {
		case PhasePending:
			h.phases[i].Status = PhaseStopped
		case PhaseRunning:
			h.phases[i].Status = PhaseFailed
			if slices.ContainsFunc(h.steps, func(s StepResult) bool { return s.Ref.Phase == h.phases[i].Ref && s.Status == StepNotAttempted }) {
				h.phases[i].Status = PhaseStopped
			}
		}
	}
	h.bumpLocked()
	h.mu.Unlock()
	close(h.done)
}

// bumpLocked records a change and wakes the readers. The caller holds h.mu.
func (h *Handle) bumpLocked() {
	h.version++
	close(h.changed)
	h.changed = make(chan struct{})
}

// View returns the handle at this moment.
func (h *Handle) View() View {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := View{
		Version: h.version, State: h.state, Kind: h.kind, Target: h.target, Elsewhere: h.elsewhere, StartedAt: h.startedAt,
		Plan: h.plan, Steps: slices.Clone(h.steps), Phases: slices.Clone(h.phases),
		PauseRequested: h.pause, StopRequested: h.stop, Progress: maps.Clone(h.progress),
		Result: h.result, LastSeq: h.seq, Dropped: h.dropped,
	}
	if h.question != nil {
		q := h.question.PendingQuestion
		v.Question = &q
	}
	v.Controls = h.controlsLocked()
	return v
}

func (h *Handle) controlsLocked() Controls {
	steer := !h.elsewhere && h.checkpoints && !h.pastLast && h.state != Finished
	return Controls{Answer: h.question != nil && !h.elsewhere, Pause: steer, Stop: steer}
}

// Changed returns a channel that is closed at the next change of the
// handle: an event, a state, a question, an answer, the result. A reader
// takes the channel first, then reads the view, then waits on the
// channel, so no change is missed.
func (h *Handle) Changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

// Events returns the log entries with Seq above after, oldest first. The
// entries are never written to again; the slice shares their memory.
func (h *Handle) Events(after uint64) []LogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	i, _ := slices.BinarySearchFunc(h.log, after+1, func(e LogEntry, seq uint64) int {
		return int(e.Seq) - int(seq) //nolint:gosec // sequence numbers fit
	})
	return h.log[i:]
}

// Answer answers the open question. Exactly one answer wins; any other
// call gets ErrStaleQuestion.
func (h *Handle) Answer(id QuestionID, yes bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.steerableLocked(); err != nil {
		return err
	}
	outcome := answeredNo
	if yes {
		outcome = answeredYes
	}
	return h.resolve(id, outcome)
}

// Pause asks the run to hold at its next checkpoint.
func (h *Handle) Pause() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.checkpointableLocked(); err != nil {
		return err
	}
	h.pause = true
	h.bumpLocked()
	return nil
}

// Resume lets a paused run go on, or withdraws a pause that no
// checkpoint has read yet.
func (h *Handle) Resume() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.steerableLocked(); err != nil {
		return err
	}
	if !h.pause {
		return ErrNotPaused
	}
	h.pause = false
	if h.state == Paused {
		close(h.resume)
	}
	h.bumpLocked()
	return nil
}

// Stop asks the run to end at its next checkpoint: nothing more is
// submitted after it. A stop while the run is paused ends the pause with
// the stop, and a stop while a question is open answers it with no.
// After the last checkpoint of the plan it is ErrTooLate.
func (h *Handle) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.checkpointableLocked(); err != nil {
		return err
	}
	h.stop = true
	if h.state == Paused {
		close(h.resume)
	}
	if q := h.question; q != nil {
		_ = h.resolve(q.ID, answeredNo)
	}
	h.bumpLocked()
	return nil
}

// steerableLocked is the check of every control. The caller holds h.mu.
func (h *Handle) steerableLocked() error {
	switch {
	case h.elsewhere:
		return ErrNoControls
	case h.state == Finished:
		return ErrFinished
	}
	return nil
}

// checkpointableLocked is the check of a pause and a stop: a checkpoint
// must still be able to come.
func (h *Handle) checkpointableLocked() error {
	if err := h.steerableLocked(); err != nil {
		return err
	}
	if !h.checkpoints || h.pastLast {
		return ErrTooLate
	}
	return nil
}

// Done is closed when the run has ended.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Result blocks until the run has ended and returns how. It can be
// called again.
func (h *Handle) Result() Result {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.result
}
