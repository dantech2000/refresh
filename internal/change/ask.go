package change

import (
	"errors"
	"fmt"
)

// checkpoint calls the checkpoint hook before next, and reports whether
// the run may go on. what names next in a message. A hook that says stop
// ends the run as Stopped, and a hook that fails ends it as Failed: the
// steps that were not reached are NotAttempted. A nil hook says go on.
func (r *runner) checkpoint(next StepRef, what string) bool {
	if r.hooks.Checkpoint == nil {
		return true
	}
	release := r.holdBudgets()
	answer, err := r.hooks.Checkpoint(r.ctx, next)
	release()
	switch {
	case err != nil && r.ctx.Err() != nil:
		r.recordInterrupt()
		return false
	case err != nil:
		r.stop = &stop{status: Failed, err: fmt.Errorf("the checkpoint before %s failed: %w; nothing more was changed", what, err)}
		r.hookErr = err
		return false
	case answer.Stop:
		r.stop = &stop{status: Stopped, err: fmt.Errorf("stopped before %s; nothing more was changed", what)}
		return false
	}
	return true
}

// answer is how a question to a person ended.
type answer int

const (
	// answeredYes: the person said yes.
	answeredYes answer = iota
	// answeredNo: the person said no.
	answeredNo
	// askInterrupted: the run ended before an answer came.
	askInterrupted
	// askFailed: the question could not be asked, or its answer not read.
	askFailed
)

// ask asks q through the hook and says how that ended, with the error of
// the hook for the two endings that have one. It is the one reading of
// what Ask returns: every question of a run goes through it. The caller
// stops the budgets, and decides what each ending does to the run.
func (r *runner) ask(q Question) (answer, error) {
	yes, err := r.hooks.Ask(r.ctx, q)
	switch {
	case err != nil && r.ctx.Err() != nil:
		return askInterrupted, err
	case err != nil:
		return askFailed, err
	case !yes:
		return answeredNo, nil
	}
	return answeredYes, nil
}

// askDuringRun asks a question that comes up after the run started: about
// a plan of a cluster of a fleet that changed, or about health that got
// worse. The budgets do not run while a person answers. A no ends the run
// as Declined, and a question that could not be asked ends it as Failed,
// with the error of the hook as the error of Run: either way nothing more
// is submitted. The caller handles an interrupt, and goes on after a yes.
func (r *runner) askDuringRun(q Question) (answer, error) {
	release := r.holdBudgets()
	a, err := r.ask(q)
	release()
	switch a {
	case askFailed:
		r.stop = &stop{status: Failed, err: fmt.Errorf("could not ask %q: %w; nothing more was changed", q.Text, err)}
		r.hookErr = err
	case answeredNo:
		r.stop = &stop{status: Declined, err: errors.New("declined; nothing more was changed")}
	}
	return a, err
}
