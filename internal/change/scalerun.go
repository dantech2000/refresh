package change

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
)

// runScaleStep runs the step of a scale: a fresh read of the nodegroup,
// the request, the wait for the update, and the wait for the nodegroup to
// hold the new sizes. Nothing is sent when the fresh read shows a
// nodegroup that is already changing (Busy), one that is not ACTIVE
// (Blocked), one whose sizes EKS did not return (Failed, as a read that
// failed), or one whose sizes are not the ones of the plan any more
// (Failed): the gate ran for another direction.
func (r *runner) runScaleStep(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	resource := s.Ref.Resource()
	clients := r.d.AWS.Clients(cp.Region)
	req, _ := r.req.(ScaleRequest)
	// The read is fresh: after the gate.
	fresh, err := describe(r.readCtx, clients, resource)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case nodegroupInFlight(fresh.Status):
		return r.busyTarget(cp, res, clients, resource, fresh.Status)
	case fresh.Status != string(ekstypes.NodegroupStatusActive):
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, notActive(s.Ref.Name, fresh.Status))
		return res
	case fresh.Sizes == s.Sizes.After:
		r.notice(fmt.Sprintf("nodegroup %s has %s now; nothing was sent", s.Ref.Name, fresh.Sizes))
		res.Status = StepSkipped
		return res
	case fresh.Sizes != s.Sizes.Before:
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown,
			fmt.Sprintf("nodegroup %s changed to %s since the plan; nothing was sent. Run the command again", s.Ref.Name, fresh.Sizes))
		return res
	}
	// A run that ended sends nothing: the step was not attempted.
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	res.StartedAt = r.d.now()
	id, err := submitScale(r.readCtx, clients, resource, req)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the scale of nodegroup "+s.Ref.Name+" and returned no update ID; it cannot be followed, and it may still be running")
		return res
	}
	res.Status, res.UpdateID = StepStarted, id
	// The read budget stops here: the wait has its own.
	r.read.pause()
	// Without a wait, this is the result of the step, and the phase
	// reports it.
	if r.common.NoWait {
		return res
	}
	r.emit(Event{Kind: StepChanged, Phase: s.Ref.Phase, Step: s.Ref, Result: &res})
	ctx, wait := r.waitBudget()
	defer wait.stop()
	poll := r.d.AWS.PollClients(cp.Region)
	if err := WaitUpdate(ctx, poll, resource, id, r.waitOptions(s, wait)); err != nil {
		return r.waitFailed(ctx, cp, res, err)
	}
	if _, err := WaitSettled(ctx, poll, resource, scaleSettled(s.Ref.Name, id, req), r.waitOptions(s, wait)); err != nil {
		return r.waitFailed(ctx, cp, res, err)
	}
	res.Status, res.FinishedAt = StepSucceeded, r.d.now()
	return res
}

// submitScale sends UpdateNodegroupConfig with a scaling config of the
// sizes that the request gives, and no other. A size that was not given
// keeps its value in EKS. The SDK sets the client request token once, in
// the input, so the retries of the call carry one token and start one
// update.
func submitScale(ctx context.Context, c *awsx.Clients, r Resource, req ScaleRequest) (updateID string, err error) {
	out, err := c.EKS.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(r.Cluster),
		NodegroupName: aws.String(r.Name),
		ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: req.Desired, MinSize: req.Min, MaxSize: req.Max},
	})
	if err != nil {
		return "", err
	}
	if out.Update == nil {
		return "", nil
	}
	return aws.ToString(out.Update.Id), nil
}

// scaleSettled is the done function of the wait after a scale update
// that EKS reported Successful. It judges every answer on the sizes and
// the status together.
//
//   - A size that was sent and that the nodegroup does not have: an error
//     at once. The update did not do what it reported. Every answer that
//     reaches this function has all three sizes: describe turns one
//     without them into a poll that says nothing.
//   - No desired size was sent: done. Only the bounds changed, and no node
//     has to come or go.
//   - ACTIVE: done.
//   - DEGRADED, a failed status, or DELETING: an error.
//   - Anything else: the nodegroup is still changing.
func scaleSettled(nodegroup, updateID string, req ScaleRequest) func(Settled) (bool, error) {
	return func(s Settled) (bool, error) {
		for _, size := range []struct {
			name string
			want *int32
			got  int32
		}{{"desired", req.Desired, s.Sizes.Desired}, {"min", req.Min, s.Sizes.Min}, {"max", req.Max, s.Sizes.Max}} {
			if size.want != nil && *size.want != size.got {
				return false, fmt.Errorf("update %s of nodegroup %s reported Successful, but the %s size of the nodegroup is %d, not %d",
					updateID, nodegroup, size.name, size.got, *size.want)
			}
		}
		if req.Desired == nil {
			return true, nil
		}
		switch ekstypes.NodegroupStatus(s.Status) {
		case ekstypes.NodegroupStatusActive:
			return true, nil
		case ekstypes.NodegroupStatusDegraded, ekstypes.NodegroupStatusCreateFailed,
			ekstypes.NodegroupStatusDeleteFailed, ekstypes.NodegroupStatusDeleting:
			return false, fmt.Errorf("nodegroup %s is %s after the scale to %d nodes", nodegroup, s.Status, *req.Desired)
		}
		return false, nil
	}
}
