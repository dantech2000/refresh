package change

import (
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/health"
)

// RunStatus is how a run ended, as a label for people. The values are the
// ones of the documents.
type RunStatus = apidoc.RunStatus

// The run statuses.
const (
	Succeeded    = apidoc.RunSucceeded
	NothingToDo  = apidoc.RunNothingToDo
	Started      = apidoc.RunStarted
	Refused      = apidoc.RunRefused
	Declined     = apidoc.RunDeclined
	Stopped      = apidoc.RunStopped
	RunBlocked   = apidoc.RunBlocked
	Failed       = apidoc.RunFailed
	VerifyFailed = apidoc.RunVerifyFailed
	Incomplete   = apidoc.RunIncomplete
	Interrupted  = apidoc.RunInterrupted
)

// StepStatus is what a run did with one step. The values are the ones of
// the documents.
type StepStatus = apidoc.StepStatus

// The step statuses.
const (
	StepNotAttempted = apidoc.StepNotAttempted
	StepStarted      = apidoc.StepStarted
	StepSucceeded    = apidoc.StepSucceeded
	StepFailed       = apidoc.StepFailed
	StepCancelled    = apidoc.StepCancelled
	StepInProgress   = apidoc.StepInProgress
	StepBusy         = apidoc.StepBusy
	StepSkipped      = apidoc.StepSkipped
	StepBlocked      = apidoc.StepBlocked
)

// StepResult is what a run did with one step of the plan.
type StepResult struct {
	Ref    StepRef
	Status StepStatus
	// Attached is true when the run waited for a change that it did not
	// start.
	Attached bool
	UpdateID string
	// InFlight is the type of the update that made a Busy step busy, or
	// "" when it was not found.
	InFlight              string
	StartedAt, FinishedAt time.Time
	// Failure is why the step did not succeed.
	Failure *apidoc.Failure
	// VerifyIssue says that the change was applied and the check after it
	// found an issue: the step counts as exit code 5.
	VerifyIssue string
}

// ClusterResult is what a run did in one cluster.
type ClusterResult struct {
	Target
	Status RunStatus
	// Steps has one result for each step of the plan, in plan order.
	Steps []StepResult
	// Verification are the checks that ran after the change.
	Verification []health.Result
}

// Result is how a run ended.
type Result struct {
	// Status is a label for people: the name of the outcome with the
	// highest code. ExitCode does not read it.
	Status RunStatus
	// Plan is the last plan computed, or nil when there was none.
	Plan     *Plan
	Clusters []ClusterResult
	// Failures are the failures that belong to no step: of the plan, of
	// the gate, and of the reads after a change. A step has its own.
	Failures []apidoc.Failure
	// Err is the one-line message for the user, or nil.
	Err error

	// ended is how the run ended when the steps and the failures do not
	// say it: a refusal, an answer, an interrupt.
	ended ending
}

// ending is an outcome of a run that no step carries.
type ending struct {
	// code is the exit code of the outcome: the code of a refused plan, 1
	// for a question answered no and for a hook error, and for a read
	// budget that ran out, 1 when nothing was read and 4 otherwise.
	code int
	// interrupted makes the run exit 1, whatever else happened.
	interrupted bool
}

// stepCode returns the exit code of one step, and false for a step that
// has none: one that was never attempted.
func stepCode(s StepResult) (code int, ok bool) {
	switch s.Status {
	case StepSucceeded:
		if s.VerifyIssue != "" {
			return exitcode.VerifyFailed, true
		}
		return exitcode.OK, true
	case StepSkipped, StepStarted:
		return exitcode.OK, true
	case StepFailed:
		if s.VerifyIssue != "" {
			return exitcode.VerifyFailed, true
		}
		return exitcode.Failure, true
	case StepCancelled:
		return exitcode.Failure, true
	case StepBlocked:
		return exitcode.Blocked, true
	case StepBusy:
		return exitcode.Incomplete, true
	case StepInProgress:
		// An attach under --no-wait is in progress with no failure.
		if s.Failure == nil {
			return exitcode.OK, true
		}
		return exitcode.Incomplete, true
	}
	return 0, false
}

// ExitCode returns the exit code of the run, in three moves.
//
//  1. Every step has a code, and a failure that belongs to no step counts
//     as 4.
//  2. Among independent targets, a step with code 1 beside a step that
//     succeeded or started counts as 4: a partial failure. The phases of
//     an upgrade and of a rollback are a sequence, so the move does not
//     apply to them.
//  3. The highest code wins. An interrupt is always 1.
func (r Result) ExitCode() int {
	if r.ended.interrupted || r.interruptedStep() {
		return exitcode.Failure
	}
	code := r.ended.code
	if len(r.Failures) > 0 {
		code = max(code, exitcode.Incomplete)
	}
	var failed, changed bool
	for _, c := range r.Clusters {
		for _, s := range c.Steps {
			sc, ok := stepCode(s)
			if !ok {
				continue
			}
			if sc == exitcode.Failure {
				failed = true
				continue
			}
			changed = changed || s.Status == StepSucceeded || s.Status == StepStarted
			code = max(code, sc)
		}
	}
	if failed {
		if changed && r.kind().independent() {
			return max(code, exitcode.Incomplete)
		}
		return max(code, exitcode.Failure)
	}
	return code
}

// interruptedStep reports whether a step ended because the user stopped
// the run.
func (r Result) interruptedStep() bool {
	for _, c := range r.Clusters {
		if slices.ContainsFunc(c.Steps, func(s StepResult) bool {
			return s.Failure != nil && s.Failure.Reason == apidoc.ReasonInterrupted
		}) {
			return true
		}
	}
	return false
}

// kind returns the kind of the change, or "" for a run with no plan.
func (r Result) kind() Kind {
	if r.Plan == nil {
		return ""
	}
	return r.Plan.Kind
}

// statusOf returns the label for a set of step results and the failures
// beside them: the name of the outcome with the highest code.
func statusOf(kind Kind, steps []StepResult, failures []apidoc.Failure) RunStatus {
	r := Result{Plan: &Plan{Kind: kind}, Clusters: []ClusterResult{{Steps: steps}}, Failures: failures}
	if r.interruptedStep() {
		return Interrupted
	}
	switch r.ExitCode() {
	case exitcode.VerifyFailed:
		return VerifyFailed
	case exitcode.Incomplete:
		return Incomplete
	case exitcode.Blocked:
		return RunBlocked
	case exitcode.Failure:
		return Failed
	}
	work := func(s StepResult) bool { return s.Status != StepSkipped && s.Status != StepNotAttempted }
	switch {
	case !slices.ContainsFunc(steps, work):
		return NothingToDo
	case slices.ContainsFunc(steps, func(s StepResult) bool { return s.Status == StepStarted || s.Status == StepInProgress }):
		return Started
	}
	return Succeeded
}
