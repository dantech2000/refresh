package change

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// runControlPlane runs the control-plane step of an upgrade hop or of a
// rollback. snap is the read of the phase.
//
//  1. The gate of the phase, on that read: see controlPlaneGate. A gate
//     that refuses blocks the step, and nothing is sent.
//  2. A fresh read of the cluster.
//  3. At the goal: nothing is sent.
//  4. UPDATING: the run attaches to the update in flight and waits for
//     it. At the goal after it, the step is done. Otherwise a notice says
//     that the update did not reach the goal, and the step goes on to
//     send its own.
//  5. Not ACTIVE: blocked.
//  6. The version is not the one that the request starts from: failed.
//     EKS moves one minor version at a time, so an upgrade is sent only
//     from the minor version before its goal, and a rollback only from
//     the one after it. An upgrade therefore never sends a version below
//     the live one.
//  7. The request, with the fields of the plan: the version of the step,
//     its force, and for a rollback the timeout of the request.
//  8. The wait for the update, the wait for ACTIVE, and the version by
//     direction.
func (r *runner) runControlPlane(cp *ClusterPlan, s Step, snap *fleet.Snapshot) StepResult {
	res := StepResult{Ref: s.Ref}
	kind := r.req.kind()
	clients, poll := r.d.AWS.Clients(cp.Region), r.d.AWS.PollClients(cp.Region)
	resource := s.Ref.Resource()
	if out, ok := r.controlPlaneGate(cp, res, s, snap); !ok {
		return out
	}
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	fresh, err := describe(r.readCtx, clients, resource)
	if err != nil {
		return r.unsent(cp, res, err)
	}
	if versionDone(kind, fresh.Version, s.To) && fresh.Status == clusterActive {
		r.notice(fmt.Sprintf("the control plane of %s is at %s already; nothing was sent", cp.Cluster, fresh.Version))
		res.Status = StepSkipped
		return res
	}
	if fresh.Status == clusterUpdating {
		settled, out, done := r.attachControlPlane(cp, res, s, clients, poll)
		if done {
			return out
		}
		res, fresh = out, settled
		// From here the step is its own request, which has not started.
		res.Attached, res.StartedAt = false, time.Time{}
	}
	switch {
	case fresh.Status != clusterActive:
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, fmt.Sprintf("cluster %s is %s, not ACTIVE; its control plane was not changed", cp.Cluster, fresh.Status))
		return res
	case !adjacent(kind, fresh.Version, s.To):
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown,
			fmt.Sprintf("the control plane of %s runs %s now, and the plan moves it from %s to %s; nothing was sent. Run the command again", cp.Cluster, orUnknown(fresh.Version), s.From, s.To))
		return res
	}
	// A run that ended sends nothing.
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	res.StartedAt = r.d.now()
	id, err := r.submitControlPlane(clients, cp.Target, s)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the update of the control plane of "+cp.Cluster+" and returned no update ID; it cannot be followed, and it may still be running")
		return res
	}
	// The update is accepted, and from here it is always followed: the
	// wait has its own budget, and records an interrupt or a deadline.
	res.Status, res.UpdateID = StepStarted, id
	r.reportStep(res)
	ctx, wait := r.waitBudget()
	r.read.pause()
	if err := WaitUpdate(ctx, poll, resource, id, r.waitOptions(s, wait)); err != nil {
		return r.waitFailed(ctx, cp, res, err)
	}
	settled, err := WaitSettled(ctx, poll, resource, clusterSettled, r.waitOptions(s, wait))
	switch {
	case err != nil:
		return r.waitFailed(ctx, cp, res, err)
	case !versionDone(kind, settled.Version, s.To):
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUpdateFailed,
			fmt.Sprintf("update %s reported Successful, but the control plane of %s reports version %s, not %s", id, cp.Cluster, orUnknown(settled.Version), s.To))
		return res
	}
	res.Status, res.FinishedAt = StepSucceeded, r.d.now()
	return res
}

// adjacent reports whether a control plane at version live can take the
// request of a step to goal: an upgrade from the minor version before the
// goal, and a rollback from the minor version after it.
func adjacent(kind Kind, live, goal string) bool {
	v, err := fleet.ParseVersion(live)
	g, gerr := fleet.ParseVersion(goal)
	if err != nil || gerr != nil || v.Major != g.Major {
		return false
	}
	if kind == ClusterRollback {
		return v.Minor == g.Minor+1
	}
	return v.Minor+1 == g.Minor
}

// controlPlaneGate is the gate of a control-plane phase, on the read of
// the phase. It returns false with the Blocked result when the control
// plane must not move.
//
// An upgrade judges the readiness of the hop again, live: the plan judged
// a later hop from versions that it only predicted. Two rules read the
// kubelet skew, and either one blocks. health.VersionSkew runs first, on
// the live versions. Then HopReadiness applies the rule of the planner,
// and after it the insights, which a real run refreshes first. The skew
// is judged before the insights by both, so a hop that the skew blocks
// sends no insights refresh. The step is sent only from the minor version
// before its goal, and for that hop the two rules are the same condition:
// a nodegroup three or more minor versions behind the live control plane.
//
// A rollback checks that no managed nodegroup is above its target.
func (r *runner) controlPlaneGate(cp *ClusterPlan, res StepResult, s Step, snap *fleet.Snapshot) (StepResult, bool) {
	var blockers []Blocker
	switch req := r.req.(type) {
	case UpgradeRequest:
		hop, err := fleet.ParseVersion(s.To)
		if err != nil || atLeast(snap.Cluster.Version, hop) {
			// The control plane is at the hop: the fresh read decides.
			return res, true
		}
		clients := r.d.AWS.Clients(cp.Region)
		skew := health.Run(r.readCtx, []health.Check{health.VersionSkew()}, &health.Input{Snapshot: snap, Clients: clients, Now: snap.FetchedAt})
		for _, c := range skew {
			if c.Outcome == health.Fail {
				ref := s.Ref
				blockers = append(blockers, Blocker{Kind: BlockerNotReady, Step: &ref, Message: c.Name + ": " + c.Message})
			}
		}
		if len(blockers) > 0 {
			break
		}
		ready := HopReadiness(snap, hop, req.SkipInsights, upgradeInsights(r.readCtx, clients, cp.Target, snap.Cluster.Version.String(), r.refresh))
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false
		}
		for _, n := range ready.Notices {
			r.notice(n)
		}
		r.addFailures(ready.Failures)
		if ready.Blocker != nil {
			blockers = append(blockers, *ready.Blocker)
		}
	case RollbackRequest:
		blockers = RollbackControlPlaneBlockers(snap, cp.To)
		if len(blockers) > 0 {
			r.addFailures(snap.Failures)
		}
	}
	if len(blockers) == 0 {
		return res, true
	}
	parts := make([]string, len(blockers))
	for i, b := range blockers {
		parts[i] = b.Message
	}
	res.Status = StepBlocked
	res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, strings.Join(parts, "; ")+"; the control plane of "+cp.Cluster+" was not changed")
	return res, false
}

// attachControlPlane waits for the update of the control plane that is
// running already. It looks the update up, so that it can follow it by
// its ID and say how it ended. An update that EKS does not list yet is
// followed by the status of the cluster alone.
//
// done says that the step ends here, with out as its result: the update
// reached the goal, or the wait did not end in a state that the run can
// go on from. Otherwise settled is the cluster after the update, ACTIVE
// and not at the goal, and a notice has said so: the caller sends its own
// update. An update that ended Failed or Cancelled gets its own notice
// first, with its ID.
func (r *runner) attachControlPlane(cp *ClusterPlan, res StepResult, s Step, clients *awsx.Clients, poll awsx.PollClients) (settled Settled, out StepResult, done bool) {
	resource := s.Ref.Resource()
	u, err := inFlightUpdate(r.readCtx, clients, resource)
	if err != nil && r.readCtx.Err() != nil {
		return Settled{}, r.unsent(cp, res, err), true
	}
	res.Attached, res.StartedAt = true, r.d.now()
	if err == nil {
		res.UpdateID = u.ID
	}
	r.notice(fmt.Sprintf("cluster %s is UPDATING already; waiting for that update", cp.Cluster))
	ctx, wait := r.waitBudget()
	r.read.pause()
	if res.UpdateID != "" {
		err := WaitUpdate(ctx, poll, resource, res.UpdateID, r.waitOptions(s, wait))
		var ended *UpdateError
		switch {
		case errors.As(err, &ended):
			r.notice(fmt.Sprintf("the control-plane update %s that was already running %s", res.UpdateID, strings.TrimPrefix(ended.Error(), "update "+res.UpdateID+" of "+resource.String()+" ")))
		case err != nil:
			return Settled{}, r.waitFailed(ctx, cp, res, err), true
		}
	}
	settled, err = WaitSettled(ctx, poll, resource, clusterSettled, r.waitOptions(s, wait))
	if err != nil {
		return settled, r.waitFailed(ctx, cp, res, err), true
	}
	if versionDone(r.req.kind(), settled.Version, s.To) {
		res.Status, res.FinishedAt = StepSucceeded, r.d.now()
		return settled, res, true
	}
	r.notice(fmt.Sprintf("the control-plane update that was already running did not reach %s (the cluster is %s at %s); starting a new one", s.To, settled.Status, orUnknown(settled.Version)))
	// The step now sends its own request, under a new read budget.
	r.newRead(r.common.Limits.Read)
	res.UpdateID = ""
	return settled, res, false
}

// submitControlPlane sends UpdateClusterVersion with the fields of the
// plan and nothing else: the version of the step, force when the step has
// it, which only a rollback with --skip-insights-check does, and the
// rollback timeout of a rollback request. The SDK sets the client request
// token once, so the retries of the call start one update.
func (r *runner) submitControlPlane(clients *awsx.Clients, t Target, s Step) (updateID string, err error) {
	in := &eks.UpdateClusterVersionInput{Name: aws.String(t.Cluster), Version: aws.String(s.To), Force: s.Force}
	if req, ok := r.req.(RollbackRequest); ok && req.RollbackTimeout > 0 {
		in.RollbackConfig = &ekstypes.RollbackConfig{TimeoutMinutes: aws.Int32(rollbackMinutes(req))}
	}
	out, err := clients.EKS.UpdateClusterVersion(r.readCtx, in)
	if err != nil {
		return "", err
	}
	if out.Update == nil {
		return "", nil
	}
	return aws.ToString(out.Update.Id), nil
}
