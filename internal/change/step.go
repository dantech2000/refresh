package change

import (
	"context"
	"errors"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The results of a step that every kind of step shares: a failure about
// its resource or its update, a request that was not sent or got no
// answer, a request that EKS accepted, and a wait that did not succeed.

// resourceKind returns the kind of the resource of a step, for a failure
// about it.
func resourceKind(r StepRef) apidoc.FailureKind {
	switch r.Kind {
	case StepAddon:
		return apidoc.FailureAddon
	case StepNodegroup, StepScale:
		return apidoc.FailureNodegroup
	}
	return apidoc.FailureCluster
}

// changeVerb returns the verb for what a step does to its resource: a
// scale scales, and every other step updates.
func changeVerb(r StepRef) (verb, done string) {
	if r.Kind == StepScale {
		return "scale", "scaled"
	}
	return "update", "updated"
}

// requestNoun names the request of a step: a scale, a roll, or an update.
func requestNoun(r StepRef) string {
	switch r.Kind {
	case StepScale:
		return "scale"
	case StepNodegroup:
		return "roll"
	}
	return "update"
}

// stepFailure returns a failure of the step of res, about its resource
// or, once an update was accepted, about that update.
func (r *runner) stepFailure(cp *ClusterPlan, res StepResult, reason apidoc.Reason, msg string) *apidoc.Failure {
	return failureOf(cp.Target, res, reason, msg)
}

// failureOf is stepFailure for a step of the cluster t.
func failureOf(t Target, res StepResult, reason apidoc.Reason, msg string) *apidoc.Failure {
	kind := resourceKind(res.Ref)
	if res.UpdateID != "" {
		kind = apidoc.FailureUpdate
	}
	f := apidoc.NewFailure(kind, res.Ref.Name, reason, msg)
	f.Cluster, f.Region, f.UpdateID = t.Cluster, t.Region, res.UpdateID
	return &f
}

// submitted returns the result of a step whose request was sent, and
// reports whether the step goes on to its wait. A request that failed is
// unanswered. A request that EKS accepted with no update ID is
// InProgress: it cannot be followed. Otherwise the step is Started with
// the ID of its update.
func (r *runner) submitted(cp *ClusterPlan, res StepResult, id string, err error) (StepResult, bool) {
	switch {
	case err != nil:
		return r.unanswered(cp, res, err), false
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored,
			"EKS accepted the "+requestNoun(res.Ref)+" of "+stepName(res.Ref)+" and returned no update ID; it cannot be followed, and it may still be running")
		return res, false
	}
	res.Status, res.UpdateID = StepStarted, id
	return res, true
}

// unsent returns the result of a step whose read before its request
// failed, or that the run ended before its request. Nothing was sent for
// the step, so a run that ended leaves it NotAttempted, also after the
// step waited for an update that someone else started: that update is
// not a request of this run. See unanswered for a request that failed.
func (r *runner) unsent(cp *ClusterPlan, res StepResult, err error) StepResult {
	return r.notSent(cp, res, err, false)
}

// unanswered returns the result of a step whose request failed. A
// request that never left, because the run ended first, leaves the step
// NotAttempted. A request that left and got no answer may have started
// the update, so the step is InProgress.
func (r *runner) unanswered(cp *ClusterPlan, res StepResult, err error) StepResult {
	return r.notSent(cp, res, err, true)
}

// notSent is unsent and unanswered. requested says that err is the error
// of the request of the step.
func (r *runner) notSent(cp *ClusterPlan, res StepResult, err error, requested bool) StepResult {
	f := fleet.FailureFromError(resourceKind(res.Ref), res.Ref.Name, err)
	f.Cluster, f.Region = cp.Cluster, cp.Region
	ended := r.readCtx.Err() != nil
	verb, done := changeVerb(res.Ref)
	switch {
	case ended && (!requested || errors.Is(err, awsx.ErrNotSent)):
		res.Status = StepNotAttempted
		if timedOut(r.readCtx) {
			f.Reason, f.Retryable = apidoc.ReasonTimeout, true
			f.Error = "the --timeout budget ran out before " + stepName(res.Ref) + " was " + done + " (increase --timeout)"
			if errors.Is(context.Cause(r.readCtx), ErrWaitTimeout) {
				f.Error = "--wait-timeout passed before " + stepName(res.Ref) + " was " + done + "; nothing was sent for it"
			}
			r.failures = append(r.failures, f)
		} else {
			r.interrupted = true
		}
		return res
	case ended:
		res.Status = StepInProgress
		f.Error = "the request to " + verb + " " + stepName(res.Ref) + " got no answer before the run ended; the update may be running"
		if timedOut(r.readCtx) {
			f.Reason = apidoc.ReasonTimeout
		}
	default:
		res.Status, res.FinishedAt = StepFailed, r.d.now()
	}
	res.Failure = &f
	return res
}

// waitFailed returns the result of a step whose wait did not end in
// success. ctx is the context of the wait.
func (r *runner) waitFailed(ctx context.Context, cp *ClusterPlan, res StepResult, err error) StepResult {
	return waitOutcome(ctx, cp.Target, res, err, r.d.now())
}

// waitOutcome is the one table of a wait that did not end in success, for
// a step of a run and for the step of a watch. ctx is the context of the
// wait, t the cluster of the step, and now the time the wait ended. The
// first row that matches decides:
//
//  1. The update ended Failed or Cancelled: the step is Failed or
//     Cancelled.
//  2. The update could not be followed: the step is InProgress, and the
//     failure names the call that did not answer.
//  3. A budget ran out: the step is InProgress, and the failure names the
//     budget, --wait-timeout or --timeout.
//  4. The run was interrupted: the step is InProgress.
//  5. Anything else, such as the done function of the wait, or a version
//     that it settled at: the step is Failed.
func waitOutcome(ctx context.Context, t Target, res StepResult, err error, now time.Time) StepResult {
	var ended *UpdateError
	var unobserved *UnobservedError
	switch {
	case errors.As(err, &ended):
		res.Status, res.FinishedAt = StepFailed, now
		reason := apidoc.ReasonUpdateFailed
		if ended.Cancelled() {
			res.Status, reason = StepCancelled, apidoc.ReasonUpdateCancelled
		}
		res.Failure = failureOf(t, res, reason, err.Error())
	case errors.As(err, &unobserved):
		res.Status = StepInProgress
		err = unobserved.of(res.UpdateID)
		res.Failure = failureOf(t, res, apidoc.ReasonNotMonitored, err.Error())
		res.Failure.Operation, res.Failure.AWSErrorCode = awsx.Operation(err), awsx.ErrorCode(err)
	case ctx.Err() != nil && timedOut(ctx):
		res.Status = StepInProgress
		budget := "--wait-timeout passed"
		if errors.Is(context.Cause(ctx), ErrReadTimeout) {
			budget = "the --timeout budget ran out"
		}
		res.Failure = failureOf(t, res, apidoc.ReasonTimeout,
			budget+" before the update of "+stepName(res.Ref)+" finished; it may still be running")
	case ctx.Err() != nil:
		res.Status = StepInProgress
		res.Failure = failureOf(t, res, apidoc.ReasonInterrupted,
			"interrupted while the update of "+stepName(res.Ref)+" was running; it may still be running")
	default:
		res.Status, res.FinishedAt = StepFailed, now
		res.Failure = failureOf(t, res, apidoc.ReasonUpdateFailed, err.Error())
	}
	return res
}
