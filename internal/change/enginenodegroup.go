package change

import (
	"context"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// maxAttachRounds is how often a nodegroup step waits for an update that
// someone else started before it gives up.
const maxAttachRounds = 2

// runEngineNodegroup runs the step of one nodegroup inside an upgrade or
// a rollback. snap is the read of the cluster before the step. The first
// row that matches the fresh read of the nodegroup decides, and the
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
//  5. Otherwise the roll: the health gate with this nodegroup as its
//     target, compared with the health that the plan showed, and then a
//     batch of one. The batch takes the baseline, reads the nodegroup
//     again, sends the roll with the version of the plan, waits, and
//     verifies. A nodegroup that is not ACTIVE is refused by the gate,
//     and by the fresh read of the batch with --skip-health-check.
//
// The goal has a direction: see versionDone. reported says that the batch
// sent the events of the step.
func (r *runner) runEngineNodegroup(cp *ClusterPlan, ph Phase, s Step, snap *fleet.Snapshot) (res StepResult, reported bool) {
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
				r.notice(fmt.Sprintf("the update of nodegroup %s that was already running did not reach %s (it is %s at %s)", name, s.To, settled.Status, orUnknown(settled.Version)))
				// The read of the cluster is from before that wait.
				if snap, err = r.phaseRead(cp); err != nil {
					return r.unsent(cp, res, err), false
				}
			}
			continue
		}
		break
	}
	// The roll is this run's own request.
	res.Attached, res.StartedAt = false, res.FinishedAt
	if out, ok := r.gateNodegroup(cp, res, snap); !ok {
		return out, false
	}
	s.Action = Submit
	batch := Phase{Ref: ph.Ref, Batch: true, Steps: []Step{s}}
	return r.runBatch(cp, batch)[0], true
}

// gateNodegroup runs the health gate right before the roll of one
// nodegroup: health.Preflight on the read of the cluster before the step,
// with the nodegroup as the target. regate compares the result with the
// health that the consent covers. After a person accepted something that
// got worse, the cluster is read and judged once more. It returns false
// with the result of the step when the roll must not start.
func (r *runner) gateNodegroup(cp *ClusterPlan, res StepResult, snap *fleet.Snapshot) (StepResult, bool) {
	if r.common.Policy.Skip {
		return res, true
	}
	clients := r.d.AWS.Clients(cp.Region)
	for asked := 0; ; asked++ {
		in := &health.Input{Snapshot: snap, Targets: []string{res.Ref.Name}, Clients: clients, Now: snap.FetchedAt}
		in.Kube, in.Metrics, in.NoKube = kubeAccess(r.d, snap)
		checks := health.Run(r.readCtx, health.Preflight(), in)
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false
		}
		out, ok, answered := r.regate(cp, res, checks, asked < maxQuestions)
		switch {
		case !ok:
			return out, false
		case !answered:
			return res, true
		}
		var err error
		if snap, err = r.phaseRead(cp); err != nil {
			return r.unsent(cp, res, err), false
		}
	}
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
// upgrade hop or of a rollback: a fresh read of the nodegroup, after the
// gate and the baseline, and then the request. The first row that matches
// the fresh read decides, and only the last one sends a request.
//
//  1. The read fails: Failed.
//  2. The nodegroup is changing: Blocked. Someone started a change after
//     the gate ran. The next run attaches to it.
//  3. The nodegroup is not ACTIVE: Blocked.
//  4. Its version meets the goal already: Skipped.
//  5. The roll would go the wrong way: Failed. An upgrade never sends a
//     version below the live one, and a rollback never one above it.
//  6. Otherwise: UpdateNodegroupVersion with the version and the force of
//     the plan.
func (r *runner) submitEngineRoll(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	resource := s.Ref.Resource()
	clients := r.d.AWS.Clients(cp.Region)
	kind := r.req.kind()
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	fresh, err := describe(r.readCtx, clients, resource)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case nodegroupInFlight(fresh.Status):
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonBusy,
			fmt.Sprintf("nodegroup %s started changing after its health gate (%s); it was not rolled. Run the command again to wait for that change", s.Ref.Name, fresh.Status))
		return res
	case fresh.Status != string(ekstypes.NodegroupStatusActive):
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, notRollable(s.Ref.Name, fresh.Status))
		return res
	case versionDone(kind, fresh.Version, s.To):
		r.notice(fmt.Sprintf("nodegroup %s is at %s already; nothing was sent", s.Ref.Name, fresh.Version))
		res.Status = StepSkipped
		return res
	case !rollDirection(kind, fresh.Version, s.To):
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown,
			fmt.Sprintf("nodegroup %s runs Kubernetes %s now, and the plan moves it to %s; nothing was sent. Run the command again", s.Ref.Name, orUnknown(fresh.Version), s.To))
		return res
	}
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	res.StartedAt = r.d.now()
	id, err := StartNodegroupRoll(r.readCtx, clients, cp.Target, s.Ref.Name, s.To, s.Force)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the roll of nodegroup "+s.Ref.Name+" and returned no update ID; it cannot be followed, and it may still be running")
		return res
	}
	res.Status, res.UpdateID = StepStarted, id
	return res
}

// rollDirection reports whether a roll from the version live to goal goes
// the way of the change: up for an upgrade, and down for a rollback. A
// version that is not known goes no way.
func rollDirection(kind Kind, live, goal string) bool {
	v, err := fleet.ParseVersion(live)
	g, gerr := fleet.ParseVersion(goal)
	if err != nil || gerr != nil || v.Major != g.Major {
		return false
	}
	if kind == ClusterRollback {
		return v.Minor > g.Minor
	}
	return v.Minor < g.Minor
}
