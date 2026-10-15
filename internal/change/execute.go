package change

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
)

// execute runs the phases of a confirmed plan and returns the result.
//
// The clusters of a fleet run one after another. Each begins with its
// turn, which plans it again and can leave it out: see turn. A cluster
// that is left out, or that fails, does not stop the next one. A run that
// was halted does: the steps of every later cluster are NotAttempted.
func (r *runner) execute(p *Plan) Result {
	res := Result{Plan: p, ended: ending{executed: true}}
	fleet := p.fleet()
	for i := range p.Clusters {
		outcome, why := turnRun, ""
		if fleet {
			outcome, why = r.turn(p, i)
		}
		cp := &p.Clusters[i]
		cr := ClusterResult{Target: cp.Target}
		r.accepted, r.waited = slices.Clone(cp.Checks), false
		switch {
		case outcome == turnRun && !p.Kind.independent():
			cr.Steps = r.runEngine(cp)
		case outcome == turnRun:
			for _, ph := range cp.Phases {
				r.emit(Event{Kind: PhaseStarted, Phase: ph.Ref})
				cr.Steps = append(cr.Steps, r.runPhase(cp, ph)...)
				r.emit(Event{Kind: PhaseEnded, Phase: ph.Ref})
			}
		case outcome == turnBlocked:
			cr.Steps = r.blockedResults(cp, why)
			for i := range cr.Steps {
				r.emit(Event{Kind: StepChanged, Phase: cr.Steps[i].Ref.Phase, Step: cr.Steps[i].Ref, Result: &cr.Steps[i]})
			}
		default:
			for _, s := range cp.Steps() {
				cr.Steps = append(cr.Steps, StepResult{Ref: s.Ref, Status: StepNotAttempted})
			}
		}
		cr.Verification, r.verification = r.verification, nil
		failures := slices.Concat(planFailures(cp, cr.Steps), r.failures)
		r.failures = nil
		cr.Status = statusOf(p.Kind, cr.Steps, failures)
		res.Clusters = append(res.Clusters, cr)
		res.Failures = append(res.Failures, failures...)
	}
	res.Failures = append(res.Failures, p.Failures...)
	res.ended.interrupted = r.interrupted || res.interruptedStep()
	res.Status = statusOf(p.Kind, allSteps(res.Clusters), res.Failures)
	res.Err = summary(res)
	switch {
	case res.ended.interrupted:
		res.Status = Interrupted
		// A cluster whose steps were all left out by the interrupt had
		// work to do: it is not NothingToDo.
		for i := range res.Clusters {
			c := &res.Clusters[i]
			if c.Status == NothingToDo && slices.ContainsFunc(c.Steps, func(s StepResult) bool { return s.Status == StepNotAttempted }) {
				c.Status = Interrupted
			}
		}
	case r.stop != nil && r.stop.status == Stopped:
		// A run that a checkpoint stopped did not do what it was asked to
		// do, so it is no success: it exits 1, and the highest code of the
		// steps and the failures still wins. The label is Stopped only
		// when nothing else went wrong: a failure after a stop request is
		// still a failure.
		clean := res.ExitCode() == exitcode.OK
		res.ended.code = exitcode.Failure
		if clean {
			res.Status, res.Err = Stopped, r.stop.err
		}
	case r.stop != nil:
		// The steps keep their results, and the highest code still wins.
		res.ended.code = exitcode.Failure
		res.Status, res.Err = r.stop.status, r.stop.err
	}
	// The message of the run is final here. An upgrade or a rollback that
	// stopped part way, a declined later phase included, says after it
	// what completed and how to go on.
	if hint := r.resumeHint(res); hint != "" && res.Err != nil {
		res.Err = fmt.Errorf("%w; %s", res.Err, hint)
	}
	return res
}

// planFailures returns the failures of a cluster plan that belong to no
// step of the run: the reads that failed and the reads of the gate. A
// target that the plan listed as busy has its failure on its step.
func planFailures(cp *ClusterPlan, steps []StepResult) []apidoc.Failure {
	own := func(f apidoc.Failure) bool {
		return f.Reason == apidoc.ReasonBusy && slices.ContainsFunc(steps, func(s StepResult) bool {
			return s.Failure != nil && s.Ref.Name == f.Name
		})
	}
	var out []apidoc.Failure
	for _, f := range slices.Concat(cp.Failures, cp.Gate.Failures) {
		if !own(f) {
			out = append(out, f)
		}
	}
	return out
}

func allSteps(clusters []ClusterResult) []StepResult {
	var out []StepResult
	for _, c := range clusters {
		out = append(out, c.Steps...)
	}
	return out
}

// runPhase runs the steps of one phase and returns one result per step,
// in plan order. A step that the run never reaches is NotAttempted: after
// an interrupt, after the read budget ran out, and after a question
// during the run that was answered no or could not be asked.
//
// The steps run one after another, and a step that fails or is blocked
// does not stop the next one: the targets are independent. A phase that
// is one batch runs in runBatch. One that stops at its first failure is
// built with the first change that has one.
func (r *runner) runPhase(cp *ClusterPlan, ph Phase) []StepResult {
	if ph.Batch {
		return r.runBatch(cp, ph)
	}
	results := make([]StepResult, 0, len(ph.Steps))
	halted := false
	for i, s := range ph.Steps {
		if !halted && (s.work() || s.busy()) && r.outOfTime(cp) {
			halted = true
		}
		res := StepResult{Ref: s.Ref, Status: StepNotAttempted}
		if !halted {
			r.steps, r.remaining = ph.Steps, ph.Steps[i:]
			res = r.runStep(cp, s)
			r.stepEnded(ph.Ref, &res)
		}
		results = append(results, res)
		halted = halted || r.stop != nil
	}
	return results
}

// stepEnded reports the result of a step that ran alone, and then what
// the step held back for after it: the check of its confirmation. The
// events are in the order of what happened: the update ended, and then it
// was checked. A front end that prints both shows the outcome of a step
// above the verification of that step.
func (r *runner) stepEnded(ph PhaseRef, res *StepResult) {
	r.emit(Event{Kind: StepChanged, Phase: ph, Step: res.Ref, Result: res})
	held := r.afterStep
	r.afterStep = nil
	for _, e := range held {
		r.emit(e)
	}
}

// outOfTime reports whether the run can start no further step: the user
// stopped it, or the time for its reads ran out, which is a failure of
// the cluster. It runs the clock of the read budget again, which a wait
// stopped.
func (r *runner) outOfTime(cp *ClusterPlan) bool {
	r.read.start()
	switch {
	case r.ctx.Err() != nil && !timedOut(r.ctx):
		r.interrupted = true
		return true
	case r.ctx.Err() == nil && !r.read.check() && r.readCtx.Err() == nil:
		return false
	}
	f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonTimeout,
		"the --timeout budget ran out before every change was started (increase --timeout)")
	f.Region = cp.Region
	r.failures = append(r.failures, f)
	return true
}

// runStep runs one step. A new kind of step adds a case here and a file
// with its submit, its wait, and its confirmation.
func (r *runner) runStep(cp *ClusterPlan, s Step) StepResult {
	if res, ok := cp.decided(s); ok {
		return res
	}
	switch s.Ref.Kind {
	case StepAddon:
		return r.runAddonStep(cp, s)
	case StepScale:
		return r.runScaleStep(cp, s)
	}
	f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonUnknown, "this build cannot run a "+string(s.Ref.Kind)+" step")
	return StepResult{Ref: s.Ref, Status: StepFailed, Failure: &f}
}

// summary is the one line for the user about a run that did not end
// clean, or nil.
func summary(res Result) error {
	if res.ExitCode() == exitcode.OK {
		return nil
	}
	var parts []string
	for _, c := range res.Clusters {
		// The steps of a fleet say which cluster they are of.
		where := ""
		if len(res.Clusters) > 1 {
			where = c.Cluster + ": "
		}
		for _, s := range c.Steps {
			code, ok := stepCode(s)
			if !ok || code == exitcode.OK {
				continue
			}
			switch {
			case s.VerifyIssue != "":
				parts = append(parts, where+s.VerifyIssue)
			case s.Failure != nil:
				parts = append(parts, where+s.Failure.Error)
			default:
				parts = append(parts, where+stepName(s.Ref)+" is "+string(s.Status))
			}
		}
	}
	for _, f := range res.Failures {
		parts = append(parts, f.Error)
	}
	msg := strings.Join(parts, "; ")
	if res.ended.interrupted {
		if msg == "" {
			return interruptError("interrupted before the next change was sent")
		}
		return interruptError("interrupted: " + msg)
	}
	return errors.New(msg)
}
