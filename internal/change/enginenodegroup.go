package change

import (
	"context"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// maxAttachRounds is how often a nodegroup step waits for an update that
// someone else started before it gives up.
const maxAttachRounds = 2

// runEngineNodegroup runs the step of one nodegroup inside an upgrade or
// a rollback. The first row that matches the fresh read of the nodegroup
// decides, and the
// version is judged before the status:
//
//  1. DELETING: blocked.
//  2. At the goal and ACTIVE, for a step that the plan would roll:
//     nothing is sent.
//  3. At the goal: the run waits while the nodegroup is changing, and
//     then verifies it with no baseline, because the roll began before
//     this run. A nodegroup that is not ACTIVE is a verification issue.
//  4. Below the goal and UPDATING: the run waits for that update and
//     reads again.
//  5. Otherwise the roll: a batch of one. The batch takes the baseline,
//     runs guardSubmit, sends the roll with the version of the plan,
//     waits, and verifies. guardSubmit reads the cluster again and runs
//     the health gate with this nodegroup as its target, compared with
//     the health that the consent covers. A nodegroup that is not ACTIVE
//     is refused by the gate, and by guardSubmit with --skip-health-check.
//
// The goal has a direction: see versionDone. reported says that the batch
// sent the events of the step.
func (r *runner) runEngineNodegroup(cp *ClusterPlan, ph Phase, s Step) (res StepResult, reported bool) {
	res = StepResult{Ref: s.Ref}
	name, resource := s.Ref.Name, s.Ref.Resource()
	clients, poll := r.d.AWS.Clients(cp.Region), r.d.AWS.PollClients(cp.Region)
	kind := r.req.kind()
	for round := 0; ; round++ {
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false
		}
		fresh, err := describe(r.readCtx, clients, resource)
		if err != nil {
			return r.unsent(cp, res, err), false
		}
		changing, _ := nodegroupSettled(fresh)
		changing = !changing
		atGoal := versionDone(kind, fresh.Version, s.To)
		switch {
		case fresh.Status == string(ekstypes.NodegroupStatusDeleting):
			res.Status = StepBlocked
			res.Failure = r.stepFailure(cp, res, apidoc.ReasonBusy, "nodegroup "+name+" is DELETING; it was not changed")
			return res, false
		case atGoal && fresh.Status == nodegroupActive && s.Action == Submit && !res.Attached:
			r.notice(fmt.Sprintf("nodegroup %s is at %s already; nothing was sent", name, fresh.Version))
			res.Status = StepSkipped
			return res, false
		case atGoal:
			ctx := r.readCtx
			var wait *budget
			if changing {
				r.notice(fmt.Sprintf("nodegroup %s is %s at %s; waiting for it to settle", name, fresh.Status, fresh.Version))
				res.Attached = true
				if res.StartedAt.IsZero() {
					res.StartedAt = r.d.now()
				}
				ctx, wait = r.waitBudget()
				r.read.pause()
				if _, err := WaitSettled(ctx, poll, resource, nodegroupSettled, r.waitOptions(s, wait)); err != nil {
					return r.waitFailed(ctx, cp, res, err), false
				}
			}
			return r.verifySettled(ctx, cp, ph, s, res, wait), false
		case fresh.Status == nodegroupUpdating:
			if round >= maxAttachRounds {
				res.Status, res.FinishedAt = StepFailed, r.d.now()
				res.Failure = r.stepFailure(cp, res, apidoc.ReasonBusy, "nodegroup "+name+" keeps changing; it was not rolled. Run the command again")
				return res, false
			}
			r.notice(fmt.Sprintf("nodegroup %s is UPDATING already; waiting for that update", name))
			res.Attached, res.StartedAt = true, r.d.now()
			ctx, wait := r.waitBudget()
			r.read.pause()
			settled, err := WaitSettled(ctx, poll, resource, nodegroupSettled, r.waitOptions(s, wait))
			if err != nil {
				return r.waitFailed(ctx, cp, res, err), false
			}
			r.newRead(r.common.Limits.Read)
			if !versionDone(kind, settled.Version, s.To) {
				// The roll that follows reads the cluster again: guardSubmit.
				r.notice(fmt.Sprintf("the update of nodegroup %s that was already running did not reach %s (it is %s at %s)", name, s.To, settled.Status, orUnknown(settled.Version)))
			}
			continue
		}
		break
	}
	// The roll is this run's own request, with the force of the request:
	// the step may have been planned as an attachment.
	if s.Action != Submit {
		r.notice(fmt.Sprintf("nodegroup %s is not at %s and no update of it is running, so the run rolls it to %s itself; the plan would %s",
			name, s.To, s.To, map[Action]string{Attach: "wait for an update that was running", Verify: "only verify it"}[s.Action]))
	}
	s.Action, s.Force = Submit, ownedForce(r.req, StepNodegroup)
	batch := Phase{Ref: ph.Ref, Batch: true, Steps: []Step{s}}
	return r.runBatch(cp, batch)[0], true
}

// verifySettled verifies a nodegroup that is at its goal and was not
// rolled by this run: the checks of a roll with no baseline, so only the
// status of the nodegroup is judged. wait is the wait budget, or nil when
// the step waited for nothing. With --skip-verify the step succeeds
// unchecked.
func (r *runner) verifySettled(ctx context.Context, cp *ClusterPlan, ph Phase, s Step, res StepResult, wait *budget) StepResult {
	res.Status, res.FinishedAt = StepSucceeded, r.d.now()
	if r.common.SkipVerify {
		return res
	}
	results := []StepResult{res}
	kube, _, noKube := kubeAccess(r.d, cp.snapshot)
	one := Phase{Ref: ph.Ref, Steps: []Step{s}}
	r.verifyRoll(ctx, cp, one, []int{0}, results, false, verifyAccess{kube: kube, noKube: noKube, wait: wait})
	return results[0]
}

// submitEngineRoll starts the roll of one nodegroup to the version of an
// upgrade hop or of a rollback, after the baseline of the batch:
// guardSubmit, and then the request. guardSubmit reads the cluster again
// and decides whether the roll may start: the control plane, the health
// gate, and the nodegroup itself. sendRoll sends the request with the
// version and the force of the step.
func (r *runner) submitEngineRoll(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	if out, ok := r.guardSubmit(cp, s, res); !ok {
		return out
	}
	return r.sendRoll(cp, res, s, s.To)
}
