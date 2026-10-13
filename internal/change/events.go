package change

import (
	"context"
	"time"

	"github.com/dantech2000/refresh/internal/health"
)

// Hooks are how a run reports and asks. The zero value runs a change that
// reports nothing and can ask nobody.
type Hooks struct {
	// Event receives every event, one call at a time. It must not block
	// for long: the polls of a wait go through it.
	Event func(Event)
	// Ask asks a person and returns the answer. It returns the error of
	// ctx when ctx ends first, and any other error when the question
	// could not be asked or read. With a nil Ask nobody can be asked: the
	// run does not ask to confirm its start, and it refuses where it
	// would need an answer about health warnings or a changed plan.
	Ask func(ctx context.Context, q Question) (yes bool, err error)
	// Checkpoint is called before each phase after the first and before
	// each cluster of a fleet after the first. It may block: that is a
	// pause. A change of one phase never calls it.
	Checkpoint func(ctx context.Context, next PhaseRef) (Checkpoint, error)
}

// Checkpoint is the answer of the checkpoint hook.
type Checkpoint struct {
	// Stop ends the run here. Nothing more is submitted.
	Stop bool
	// Waited says that time passed in the hook, so the run reads the
	// cluster again.
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
	// PhaseStarted, PhaseEnded: Phase.
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
	Text    string
	Update  *UpdateProgress
}

// QuestionKind names a question.
type QuestionKind string

// The questions.
const (
	// ConfirmStart: every change asks it once before it starts, without
	// --yes.
	ConfirmStart QuestionKind = "ConfirmStart"
	// ConfirmPhase: an upgrade asks it before each phase.
	ConfirmPhase QuestionKind = "ConfirmPhase"
	// GateWarnings: the health gate has warnings. A yes also confirms the
	// start.
	GateWarnings QuestionKind = "GateWarnings"
	// PlanChanged: the plan differs from the one that was confirmed.
	PlanChanged QuestionKind = "PlanChanged"
)

// Question is what a run asks a person. The answer is yes or no.
type Question struct {
	Kind  QuestionKind
	Phase PhaseRef
	// Text is the question, without the "[y/N]".
	Text string
	// Reasons are the gate reasons or the plan changes behind it.
	Reasons []string
}
