package change

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// addonCheck is the name of the check that confirms an add-on after its
// update.
const addonCheck = "AddonHealth"

// runAddonStep runs the step of one add-on: for a Submit step a read of
// the cluster when a step before it waited, a fresh read of the add-on,
// and the request; then the wait, and the confirmation. An add-on that is
// already changing is not touched, unless it is changing to the version
// of the step: the run then waits for that update and sends none.
func (r *runner) runAddonStep(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	resource := Resource{Target: cp.Target, Kind: StepAddon, Name: s.Ref.Name}
	clients := r.d.AWS.Clients(cp.Region)
	if s.busy() {
		return r.busyAddon(cp, res, clients, "")
	}
	if s.Action == Submit {
		// The steps before this one waited, so the read of the plan is old.
		if r.waited {
			if out, ok := r.recheckAddons(cp, res); !ok {
				return out
			}
		}
		out, d, ok := r.freshAddon(cp, res, clients, resource)
		if !ok {
			return out
		}
		s.Action, s.To = d.Action, d.To
	}
	res.Attached = s.Action == Attach
	if s.Action == Submit {
		// A run that ended sends nothing: the step was not attempted.
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err)
		}
		res.StartedAt = r.d.now()
		id, err := submitAddon(r.readCtx, clients, resource, s.To)
		switch {
		case err != nil:
			return r.unsent(cp, res, err)
		case id == "":
			res.Status = StepInProgress
			res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the update of add-on "+s.Ref.Name+" and returned no update ID; it cannot be followed")
			return res
		}
		res.Status, res.UpdateID = StepStarted, id
		// Without a wait, this is the result of the step, and the phase
		// reports it.
		if !r.common.NoWait {
			r.emit(Event{Kind: StepChanged, Phase: s.Ref.Phase, Step: s.Ref, Result: &res})
		}
	}
	if res.Attached {
		res.StartedAt = r.d.now()
	}
	// The read budget stops here: the wait has its own.
	r.read.pause()
	if r.common.NoWait {
		if res.Attached {
			res.Status = StepInProgress
		}
		return res
	}
	ctx, wait := r.waitBudget()
	defer wait.stop()
	poll := r.d.AWS.PollClients(cp.Region)
	if res.Attached {
		settled, err := WaitSettled(ctx, poll, resource, addonSettled, r.waitOptions(s, wait))
		if err == nil && fleet.CompareAddonVersions(settled.Version, s.To) != 0 {
			err = fmt.Errorf("add-on %s settled at %s, not %s", s.Ref.Name, settled.Version, s.To)
		}
		if err != nil {
			return r.waitFailed(ctx, cp, res, err)
		}
		return r.confirmAddon(cp, res, s, settled, nil)
	}
	if err := WaitUpdate(ctx, poll, resource, res.UpdateID, r.waitOptions(s, wait)); err != nil {
		return r.waitFailed(ctx, cp, res, err)
	}
	settled, err := describe(ctx, clients, resource)
	return r.confirmAddon(cp, res, s, settled, err)
}

// freshAddon is the fresh read of an add-on before its submit: after the
// gate, and after the waits of the steps before this one. It runs the
// step table on what it reads, and then the policy of the run on the
// health that it reads: an add-on that was ACTIVE in the plan and is
// DEGRADED now is a warning that nobody accepted. See regate.
//
// After a person accepted such a warning it reads again, as Run does
// after every answer. It returns false when the step ends here, with its
// result. Otherwise the decision is Submit or Attach.
func (r *runner) freshAddon(cp *ClusterPlan, res StepResult, clients *awsx.Clients, resource Resource) (StepResult, addonDecision, bool) {
	name := res.Ref.Name
	for asked := 0; ; asked++ {
		fresh, err := describe(r.readCtx, clients, resource)
		if err != nil {
			return r.unsent(cp, res, err), addonDecision{}, false
		}
		d := decideAddon(fleet.Addon{Name: name, Version: fresh.Version, Status: fresh.Status}, cp.addonTargets[name], "")
		switch {
		case d.Action == Skip && d.Reason == InFlight:
			return r.busyAddon(cp, res, clients, fresh.Status), d, false
		case d.Action == Skip:
			r.notice(fmt.Sprintf("add-on %s is %s at %s now; nothing was sent", name, fresh.Status, fresh.Version))
			res.Status = StepSkipped
			return res, d, false
		case d.Action == Attach:
			r.notice(fmt.Sprintf("add-on %s is already %s to %s; waiting for that update", name, fresh.Status, d.To))
			return res, d, true
		case r.common.Policy.Skip:
			return res, d, true
		}
		out, ok, answered := r.regate(cp, res, r.freshAddonHealth(cp, name, fresh))
		switch {
		case !ok:
			return out, d, false
		case !answered:
			return res, d, true
		case asked+1 >= maxQuestions:
			res.Status, res.FinishedAt = StepFailed, r.d.now()
			res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, "add-on "+name+" keeps changing; it was not changed. Run the command again")
			return res, d, false
		}
	}
}

// submitAddon sends UpdateAddon with the version and nothing else. The
// SDK sets the client request token once, in the input, so the retries of
// the call carry one token and start one update.
func submitAddon(ctx context.Context, c *awsx.Clients, r Resource, version string) (updateID string, err error) {
	out, err := c.EKS.UpdateAddon(ctx, &eks.UpdateAddonInput{
		ClusterName:  aws.String(r.Cluster),
		AddonName:    aws.String(r.Name),
		AddonVersion: aws.String(version),
	})
	if err != nil {
		return "", err
	}
	if out.Update == nil {
		return "", nil
	}
	return aws.ToString(out.Update.Id), nil
}

// addonSettled is the done function of a wait for an add-on that is
// already changing: ACTIVE is done, and a failed status is an error.
func addonSettled(s Settled) (bool, error) {
	switch s.Status {
	case string(ekstypes.AddonStatusActive):
		return true, nil
	case string(ekstypes.AddonStatusDegraded), string(ekstypes.AddonStatusCreateFailed),
		string(ekstypes.AddonStatusUpdateFailed), string(ekstypes.AddonStatusDeleteFailed):
		return false, fmt.Errorf("the add-on update failed: status %s", s.Status)
	}
	return false, nil
}

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

// stepFailure returns a failure of the step of res, about its resource
// or, once an update was accepted, about that update.
func (r *runner) stepFailure(cp *ClusterPlan, res StepResult, reason apidoc.Reason, msg string) *apidoc.Failure {
	kind := resourceKind(res.Ref)
	if res.UpdateID != "" {
		kind = apidoc.FailureUpdate
	}
	f := apidoc.NewFailure(kind, res.Ref.Name, reason, msg)
	f.Cluster, f.Region, f.UpdateID = cp.Cluster, cp.Region, res.UpdateID
	return &f
}

// unsent returns the result of a step whose fresh read or request failed.
// A request that never left, because the run ended first, leaves the step
// NotAttempted. A request that left and got no answer may have started
// the update, so the step is InProgress.
func (r *runner) unsent(cp *ClusterPlan, res StepResult, err error) StepResult {
	f := fleet.FailureFromError(resourceKind(res.Ref), res.Ref.Name, err)
	f.Cluster, f.Region = cp.Cluster, cp.Region
	ended := r.readCtx.Err() != nil
	verb, done := changeVerb(res.Ref)
	switch {
	case ended && (errors.Is(err, awsx.ErrNotSent) || res.StartedAt.IsZero()):
		res.Status = StepNotAttempted
		if timedOut(r.readCtx) {
			f.Reason, f.Retryable = apidoc.ReasonTimeout, true
			f.Error = "the --timeout budget ran out before " + stepName(res.Ref) + " was " + done + " (increase --timeout)"
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
	var ended *UpdateError
	var unobserved *UnobservedError
	switch {
	case errors.As(err, &ended):
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		reason := apidoc.ReasonUpdateFailed
		if ended.Cancelled() {
			res.Status, reason = StepCancelled, apidoc.ReasonUpdateCancelled
		}
		res.Failure = r.stepFailure(cp, res, reason, err.Error())
	case errors.As(err, &unobserved):
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, err.Error())
		res.Failure.Operation, res.Failure.AWSErrorCode = awsx.Operation(err), awsx.ErrorCode(err)
	case ctx.Err() != nil && timedOut(ctx):
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonTimeout,
			"--wait-timeout passed before the update of "+stepName(res.Ref)+" finished; it may still be running")
	case ctx.Err() != nil:
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonInterrupted,
			"interrupted while the update of "+stepName(res.Ref)+" was running; it may still be running")
	default:
		// The done function of the wait, or the version that it settled at.
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUpdateFailed, err.Error())
	}
	return res
}

// confirmAddon judges the add-on after a wait that succeeded. got is the
// add-on as it is now, and err the error of its read.
//
//   - The read failed: the step succeeded, and the failure makes the run
//     incomplete.
//   - The version is not the target: the step failed.
//   - The add-on is not ACTIVE, or has a health issue: the step
//     succeeded with an issue, which is exit code 5.
func (r *runner) confirmAddon(cp *ClusterPlan, res StepResult, s Step, got Settled, err error) StepResult {
	name := s.Ref.Name
	res.Status, res.FinishedAt = StepSucceeded, r.d.now()
	check := health.Result{Name: addonCheck, Outcome: health.Pass, Message: fmt.Sprintf("%s is %s at %s", name, got.Status, got.Version)}
	switch {
	case err != nil:
		f := fleet.FailureFromError(apidoc.FailureAddon, name, err)
		f.Cluster, f.Region, f.UpdateID = cp.Cluster, cp.Region, res.UpdateID
		f.Error = "the update of add-on " + name + " succeeded, and the add-on could not be read after it: " + f.Error
		r.failures = append(r.failures, f)
		check.Outcome, check.Message = health.Unreadable, "could not read "+name+" after its update"
	case fleet.CompareAddonVersions(got.Version, s.To) != 0:
		res.Status = StepFailed
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUpdateFailed,
			fmt.Sprintf("update %s of add-on %s reported Successful, but the add-on is at %s, not %s", res.UpdateID, name, got.Version, s.To))
		check.Outcome, check.Message = health.Fail, fmt.Sprintf("%s is at %s, not %s", name, got.Version, s.To)
	case got.Status != string(ekstypes.AddonStatusActive):
		res.VerifyIssue = fmt.Sprintf("add-on %s ended in status %s (expected ACTIVE)", name, got.Status)
	case len(got.Issues) > 0:
		res.VerifyIssue = fmt.Sprintf("add-on %s is ACTIVE but has %d health issue(s): %s", name, len(got.Issues), strings.Join(got.Issues, "; "))
	}
	if res.VerifyIssue != "" {
		check.Outcome, check.Message, check.Findings = health.Fail, res.VerifyIssue, []string{name}
	}
	r.verification = append(r.verification, check)
	r.emit(Event{Kind: VerifyReport, Phase: s.Ref.Phase, Step: s.Ref, Checks: []health.Result{check}})
	return res
}

// busyAddon returns the result of an add-on target that is already
// changing: it is left alone, and its failure names the type of the
// update in flight. status is the status of a fresh read, or "" to use
// the one of the plan.
func (r *runner) busyAddon(cp *ClusterPlan, res StepResult, c *awsx.Clients, status string) StepResult {
	name, version := res.Ref.Name, ""
	for _, a := range cp.snapshot.Addons {
		if a.Name == name {
			version = a.Version
			if status == "" {
				status = a.Status
			}
		}
	}
	res.Status = StepBusy
	var note string
	res.InFlight, note = r.inFlight(c, Resource{Target: cp.Target, Kind: StepAddon, Name: name})
	f := cp.busyFailure(name, status, version, note)
	res.Failure = &f
	return res
}

// unknownUpdate is what a busy failure says when the update in flight
// could not be looked up.
const unknownUpdate = "an update of unknown type"

// inFlight looks up the update that makes a target busy. It returns the
// type for the result of the step, and the words for the failure: the
// type, unknownUpdate when the lookup failed, and "" when EKS lists no
// update in progress, as for a resource that it is deleting. The lookup
// is two reads that happen only for a busy target, at plan time and at
// the fresh read before a submit alike.
func (r *runner) inFlight(c *awsx.Clients, res Resource) (kind, note string) {
	kind, err := inFlightUpdate(r.readCtx, c, res)
	if err != nil {
		return "", unknownUpdate
	}
	return kind, kind
}

// inFlightUpdate returns the type of the update of a resource that is in
// progress, such as AddonUpdate, or "" when EKS lists none. EKS lists the
// update IDs with no status, so each one is described, newest first.
func inFlightUpdate(ctx context.Context, c *awsx.Clients, r Resource) (string, error) {
	list := &eks.ListUpdatesInput{Name: aws.String(r.Cluster)}
	describe := &eks.DescribeUpdateInput{Name: aws.String(r.Cluster)}
	switch r.Kind {
	case StepAddon:
		list.AddonName, describe.AddonName = aws.String(r.Name), aws.String(r.Name)
	case StepNodegroup, StepScale:
		list.NodegroupName, describe.NodegroupName = aws.String(r.Name), aws.String(r.Name)
	}
	ids, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListUpdatesOutput, error) {
			list.NextToken = token
			return c.EKS.ListUpdates(ctx, list)
		},
		func(out *eks.ListUpdatesOutput) ([]string, *string) { return out.UpdateIds, out.NextToken },
	)
	if err != nil {
		return "", err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		describe.UpdateId = aws.String(ids[i])
		out, err := c.EKS.DescribeUpdate(ctx, describe)
		switch {
		case err != nil:
			return "", err
		case out.Update == nil:
			return "", fmt.Errorf("EKS returned no update for %s", ids[i])
		case out.Update.Status == ekstypes.UpdateStatusInProgress:
			return string(out.Update.Type), nil
		}
	}
	return "", nil
}
