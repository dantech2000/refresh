package change

import (
	"context"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// Hooks are how a run reports and asks. The zero value reports nothing
// and can ask nobody, so it runs a change only with consent that needs no
// question: Policy.Yes, or a Common.Expect that the plan matches.
type Hooks struct {
	// Event receives every event, one call at a time. It must not block
	// for long: the polls of a wait go through it.
	Event func(Event)
	// Ask asks a person and returns the answer. It returns the error of
	// ctx when ctx ends first, and any other error when the question
	// could not be asked or read.
	//
	// Consent is positive. Run submits only when Policy.Yes is set, or
	// Common.Expect is a reviewed plan of the same request that the
	// current plan matches, or Ask returned yes for the plan being
	// executed. With a nil Ask nobody can be asked, so a run without
	// Policy.Yes and without a matching Expect is refused before any
	// change, and so is a run that would need an answer about health
	// warnings or a changed plan.
	Ask func(ctx context.Context, q Question) (yes bool, err error)
	// Checkpoint is called before work that the run can still leave
	// undone: before every phase of an upgrade or a rollback that has
	// work, the first included, after consent and before the first
	// submit; before each step of such a phase after its first; and
	// before every cluster of a fleet, the first included. next names
	// what comes: a zero Kind means the phase itself. It may block: that
	// is a pause, and the budgets of the run do not run in it. It returns
	// when the run may go on or must stop. Nil means go on.
	Checkpoint func(ctx context.Context, next StepRef) (Checkpoint, error)
}

// Checkpoint is the answer of the checkpoint hook.
type Checkpoint struct {
	// Stop ends the run here. Nothing more is submitted, and the steps
	// that were not reached are NotAttempted.
	Stop bool
	// Waited says that time passed in the hook, so what the run read
	// before it is old. The run reads again after every checkpoint,
	// whatever the value: a phase and a step of an engine begin with a
	// read, and a cluster of a fleet is planned again at its turn.
	Waited bool
}

// EventKind names an event.
type EventKind string

// The events, each with the fields of Event that it sets.
const (
	// PlanReady: Plan. A plan was computed: the first one, and each later
	// one that differs from the one before.
	PlanReady EventKind = "PlanReady"
	// GateReport: Checks, Verdict. The health gate of a plan ran.
	GateReport EventKind = "GateReport"
	// PhaseStarted: Phase. PhaseEnded: Phase, Skipped.
	PhaseStarted EventKind = "PhaseStarted"
	PhaseEnded   EventKind = "PhaseEnded"
	// StepChanged: Step, Result. An update was accepted, or the step
	// ended.
	StepChanged EventKind = "StepChanged"
	// Notice: Text.
	Notice EventKind = "Notice"
	// VerifyReport: Checks. The checks after a change.
	VerifyReport EventKind = "VerifyReport"
	// UpdatePolled: Step, Update. One for each poll of a wait.
	UpdatePolled EventKind = "UpdateProgress"
	// NodeProgress: Step, Nodes. The nodes of a roll, from the observer
	// beside its wait.
	NodeProgress EventKind = "NodeProgress"
	// Progress: Text. What the run does while nothing else reports, such
	// as the wait for an insights refresh. It is not a warning.
	Progress EventKind = "Progress"
)

// Event is one thing that happened in a run.
type Event struct {
	At    time.Time
	Kind  EventKind
	Phase PhaseRef
	Step  StepRef

	Plan    *Plan
	Verdict *health.Verdict
	Checks  []health.Result
	Result  *StepResult
	// Skipped says that a phase that ended had nothing to do: no step of
	// it was run.
	Skipped bool
	Text    string
	Update  *UpdateProgress
	Nodes   *NodeView
}

// NodeView is the nodes of one roll at one moment.
type NodeView struct {
	Nodegroup string
	Snapshot  noderoll.Snapshot
	// Recent are the events of the roll that the tracker keeps, oldest
	// first, each with its time. A panel shows the last few.
	Recent []noderoll.Event
	// Watching says that the snapshot comes from a watch, and not from a
	// list for each snapshot.
	Watching bool
	// StartedAt is when the roll was accepted.
	StartedAt time.Time
}

// QuestionKind names a question.
type QuestionKind string

// The questions.
const (
	// ConfirmStart: every change asks it once before it starts, without
	// --yes and without a reviewed plan.
	ConfirmStart QuestionKind = "ConfirmStart"
	// GateWarnings: the health gate has warnings, or a check is worse
	// than it was when the plan was accepted. Before the start, a yes
	// also confirms the start.
	GateWarnings QuestionKind = "GateWarnings"
	// PlanChanged: the plan differs from the one that was confirmed.
	PlanChanged QuestionKind = "PlanChanged"
	// ConfirmPhase: an upgrade with ConfirmPhases asks it before each
	// phase that has work. The one before the first phase also confirms
	// the start, and accepts the warnings of the gate that it names.
	ConfirmPhase QuestionKind = "ConfirmPhase"
)

// Question is what a run asks a person. The answer is yes or no.
type Question struct {
	Kind QuestionKind
	// Step is the step that a question during the run is about. It is
	// the zero value for a question before the start.
	Step StepRef
	// Text is the question, without the "[y/N]".
	Text string
	// Reasons are the gate reasons or the plan changes behind it.
	Reasons []string
}
