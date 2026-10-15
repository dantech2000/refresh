package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/health"
)

// maxQuestions is how many plans in a row may each need a new answer.
// The plan after that many is refused: the cluster keeps changing.
const maxQuestions = 2

// prepare computes the plan and gets it confirmed. It returns the plan to
// execute, or a nil plan with the result and the error of a run that ends
// before it submits anything.
func (r *runner) prepare() (*Plan, Result, error) {
	prev := r.common.Expect
	// A plan that a person reviewed before the call is confirmed already.
	// Run has checked that it is a plan of this request, and next compares
	// the first plan with it.
	confirmed := prev != nil
	asked := 0
	for {
		r.newRead(r.planLimit())
		p, err := plan(r.readCtx, r.d, r.req, r.refresh)
		if err == nil && r.userInterrupted() {
			// The interrupt ended some of the reads, and the planner took
			// what they left for a cluster that cannot be read: a plan
			// with blockers. An interrupted run has no plan.
			err = fmt.Errorf("the plan was not finished: %w", context.Cause(r.ctx))
		}
		if err != nil {
			res, err := r.unplanned(prev, err)
			r.read.stop()
			return nil, res, err
		}
		// A rollback that would now return to another version than the
		// one that was confirmed is refused, whatever the answer and the
		// flags: the nodes and the add-ons would go to a version that
		// nobody confirmed.
		if prev != nil {
			if changed := RollbackTargetChanged(prev, p); changed != nil {
				// A refusal is a result of the run, and not an error of Run.
				r.announce(p)
				res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
				res.Err = changed
				r.read.stop()
				return nil, res, nil //nolint:nilerr // see above
			}
		}
		var diff []Change
		if prev != nil {
			diff = Diff(prev, p)
		}
		if prev == nil || len(diff) > 0 {
			r.announce(p)
		}
		q, end := r.next(p, diff, confirmed, asked)
		if end != nil {
			r.read.stop()
			return nil, *end, nil
		}
		if q == nil {
			accept(p)
			return p, Result{}, nil
		}
		// The budgets do not run while a person answers.
		r.read.stop()
		switch a, err := r.ask(*q); a {
		case askInterrupted:
			// An interrupt is a result of the run, not an error of Run.
			return nil, r.stopped(p, "before anything was changed"), nil
		case askFailed:
			res := r.unstarted(p, Failed, ending{code: exitcode.Failure})
			res.Err = fmt.Errorf("could not ask %q: %w; nothing was changed", q.Text, err)
			return nil, res, err
		case answeredNo:
			res := r.unstarted(p, Declined, ending{code: exitcode.Failure})
			res.Err = errors.New("declined; nothing was changed")
			return nil, res, nil
		case answeredYes:
			if q.Kind == ConfirmPhase {
				phase := q.Step.Phase
				r.confirmedPhase = &phase
			}
		}
		prev, confirmed = p, true
		asked++
	}
}

// accept records in the gate of each cluster what the consent to the
// plan went past: the warnings of a gate that asks, which a person
// accepted with an answer or with the plan in Common.Expect. The gate then
// reads as the gate of a run with --yes does, and the Run document says
// what was accepted. A plan reaches this only with every question
// answered.
func accept(p *Plan) {
	for i := range p.Clusters {
		acceptCluster(&p.Clusters[i])
	}
}

// acceptCluster is accept for the plan of one cluster.
func acceptCluster(cp *ClusterPlan) {
	if gate := &cp.Gate; gate.Decision == health.Ask {
		gate.Decision, gate.Accepted = health.Go, slices.Clone(gate.Reasons)
	}
}

// announce reports a plan, its gate, and its notices.
func (r *runner) announce(p *Plan) {
	r.emit(Event{Kind: PlanReady, Plan: p})
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		if cp.Gate.Decision != "" && !cp.Gate.Skipped {
			r.emit(Event{Kind: GateReport, Phase: PhaseRef{Target: cp.Target}, Checks: cp.Checks, Verdict: &cp.Gate})
		}
	}
}

// next decides what follows a plan: a result that ends the run, a
// question, or neither, which means execute. diff compares the plan with
// the one that was confirmed, when there is one.
func (r *runner) next(p *Plan, diff []Change, confirmed bool, asked int) (*Question, *Result) {
	yes := r.common.Policy.Yes
	for _, c := range diff {
		if c.Effect() == Continue || (yes && c.Effect() == AskAgain) {
			r.notice(c.Message)
		}
	}
	switch p.Decision() {
	case DecisionBlocked:
		res := r.unstarted(p, Refused, ending{code: p.ExitCode()})
		res.Err = errors.New(blockedSummary(p))
		return nil, &res
	case DecisionNothingToDo:
		// A target that is already changing is reported by its step, as
		// in a run that has other targets too: the step looks up the
		// update in flight. Nothing is submitted, so nothing is asked.
		if slices.ContainsFunc(allPlanSteps(p), Step.busy) {
			return nil, nil
		}
		res := r.unstarted(p, NothingToDo, ending{})
		// The steps were not attempted, which is not what stops a run
		// with nothing to do: its failures are.
		res.Err = summary(Result{Plan: p, Failures: res.Failures})
		return nil, &res
	}
	if worstEffect(diff) == Refuse {
		res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
		res.Err = fmt.Errorf("the plan changed since it was confirmed (%s); nothing was started", messages(diff, Refuse))
		return nil, &res
	}
	q := r.question(p, diff, confirmed)
	switch {
	case q == nil:
		return nil, nil
	case r.hooks.Ask == nil:
		res := r.unstarted(p, Refused, ending{code: exitcode.Failure})
		res.Err = errors.New(unanswered(q.Kind) + "; nothing was changed")
		return nil, &res
	case asked >= maxQuestions:
		res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
		res.Err = fmt.Errorf("the cluster keeps changing (%s); nothing was started", strings.Join(q.Reasons, "; "))
		return nil, &res
	}
	return q, nil
}

// allPlanSteps returns the steps of every cluster of the plan.
func allPlanSteps(p *Plan) []Step {
	var out []Step
	for i := range p.Clusters {
		out = append(out, p.Clusters[i].Steps()...)
	}
	return out
}

// blockedSummary is the one line of a plan that cannot run: its blockers,
// or the summary of the gate.
func blockedSummary(p *Plan) string {
	var parts []string
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		for _, b := range cp.Blockers {
			parts = append(parts, b.Message)
		}
		if len(cp.Blockers) == 0 && cp.Gate.Decision == health.Block {
			parts = append(parts, cp.Gate.Summary()+"; nothing was started")
		}
	}
	return strings.Join(parts, "; ")
}

// unplanned returns the result of a run whose plan could not be computed.
// A context that ended is an interrupt or a timeout, and a Result. Any
// other error is the error of Run.
func (r *runner) unplanned(prev *Plan, err error) (Result, error) {
	if r.readCtx.Err() == nil {
		return Result{Status: Failed, Plan: prev, Err: err, ended: ending{code: exitcode.Failure}}, err
	}
	res := Result{Status: Interrupted, Plan: prev, ended: ending{interrupted: true}}
	res.Err = interruptError("interrupted before anything was changed")
	if timedOut(r.readCtx) {
		// Nothing was read, so there is no document to call incomplete.
		res.Status, res.ended = Failed, ending{code: exitcode.Failure}
		what := r.req.cluster().Cluster
		if what == "" {
			what = "the fleet"
		}
		res.Err = fmt.Errorf("timed out reading %s (increase --timeout); nothing was changed", what)
	}
	if prev != nil {
		res.Clusters = notAttempted(prev)
	}
	return res, nil
}

// stopped returns the result of a run that an interrupt ended before it
// submitted anything.
func (r *runner) stopped(p *Plan, when string) Result {
	res := r.unstarted(p, Interrupted, ending{interrupted: true})
	res.Err = interruptError("interrupted " + when)
	if timedOut(r.ctx) {
		res.Status, res.ended = Failed, ending{code: exitcode.Failure}
		res.Err = fmt.Errorf("timed out %s", when)
	}
	return res
}

// unstarted returns the result of a run that ends with a plan and with
// nothing submitted: every step is NotAttempted.
func (r *runner) unstarted(p *Plan, status RunStatus, end ending) Result {
	res := Result{Status: status, Plan: p, Clusters: notAttempted(p), Failures: p.allFailures(), ended: end}
	for i := range res.Clusters {
		res.Clusters[i].Status = status
	}
	return res
}

// notAttempted returns one result per step of the plan, none attempted.
func notAttempted(p *Plan) []ClusterResult {
	out := make([]ClusterResult, len(p.Clusters))
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		out[i] = ClusterResult{Target: cp.Target}
		for _, s := range cp.Steps() {
			out[i].Steps = append(out[i].Steps, StepResult{Ref: s.Ref, Status: StepNotAttempted})
		}
	}
	return out
}
