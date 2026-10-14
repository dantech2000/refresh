package change

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// runEngineAddon runs the step of one add-on inside an upgrade or a
// rollback. The version that it sends is the version of the plan.
//
//  1. A fresh read of the add-on.
//  2. CREATING or UPDATING, at any version: the run attaches and waits
//     for that update. A wait that fails fails the step, and no update is
//     sent after it. When the add-on settles ACTIVE at a version that
//     meets the phase, the step is done. When it settles ACTIVE at a
//     version that does not, the step goes on to send its own update.
//  3. DELETING: blocked.
//  4. At a version that meets the phase: nothing is sent.
//  5. guardSubmit: a new read of the cluster, the busy rule, and the
//     add-on once more. An update of the control plane that someone else
//     started while the run waited in row 2 stops the request here.
//  6. The request, the wait for the update, and the check of the add-on
//     after it, as for an add-on update.
//
// An upgrade is met by the version of the step or a newer one, so it
// never sends an older version than the installed one. A rollback is met
// by the version of the step, and by any other version that the catalogue
// of the rollback target lists.
func (r *runner) runEngineAddon(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	name, resource := s.Ref.Name, s.Ref.Resource()
	clients, poll := r.d.AWS.Clients(cp.Region), r.d.AWS.PollClients(cp.Region)
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	fresh, err := describe(r.readCtx, clients, resource)
	if err != nil {
		return r.unsent(cp, res, err)
	}
	attached := false
	if fresh.Status == "CREATING" || fresh.Status == "UPDATING" {
		r.notice(fmt.Sprintf("add-on %s is %s already; waiting for that update", name, fresh.Status))
		res.Attached, res.StartedAt = true, r.d.now()
		ctx, wait := r.waitBudget()
		r.read.pause()
		settled, err := WaitSettled(ctx, poll, resource, addonSettled, r.waitOptions(s, wait))
		if err != nil {
			// No update follows an attachment that failed.
			return r.waitFailed(ctx, cp, res, err)
		}
		r.newRead(r.common.Limits.Read)
		fresh, attached = settled, true
	}
	if fresh.Status == "DELETING" {
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonBusy, "add-on "+name+" is DELETING; it was not changed")
		return res
	}
	keep, err := r.keepsAddon(cp, s, fresh.Version)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case keep && attached:
		kept := s
		kept.To = fresh.Version
		return r.confirmAddon(cp, res, kept, fresh, nil)
	case keep:
		r.notice(fmt.Sprintf("add-on %s is at %s now; nothing was sent", name, fresh.Version))
		res.Status = StepSkipped
		return res
	case attached:
		r.notice(fmt.Sprintf("the update of add-on %s that was already running ended at %s, which does not meet the phase; updating it to %s", name, fresh.Version, s.To))
		res.Attached, res.StartedAt = false, res.FinishedAt
	}
	if out, ok := r.guardSubmit(cp, s, res); !ok {
		return out
	}
	res.StartedAt = r.d.now()
	id, err := submitAddon(r.readCtx, clients, resource, s.To)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the update of add-on "+name+" and returned no update ID; it cannot be followed, and it may still be running")
		return res
	}
	// The update is accepted, and from here it is always followed.
	res.Status, res.UpdateID = StepStarted, id
	r.reportStep(res)
	ctx, wait := r.waitBudget()
	r.read.pause()
	if err := WaitUpdate(ctx, poll, resource, id, r.waitOptions(s, wait)); err != nil {
		return r.waitFailed(ctx, cp, res, err)
	}
	settled, err := describe(ctx, clients, resource)
	return r.confirmAddon(cp, res, s, settled, err)
}

// keepsAddon reports whether an add-on at version needs no update in the
// phase of the step. See runEngineAddon for the rule of each direction. A
// rollback reads the catalogue of its target, and only for a version that
// is neither the one that the plan read nor the one that it moves to.
func (r *runner) keepsAddon(cp *ClusterPlan, s Step, version string) (bool, error) {
	kind := r.req.kind()
	if addonDone(kind, version, s.To) {
		return true, nil
	}
	if kind != ClusterRollback || version == s.From {
		return false, nil
	}
	fetch := r.d.fetcher()
	versions, err := fetch.AddonVersions(r.readCtx, r.d.AWS.Clients(cp.Region), s.Ref.Name, cp.To)
	if err != nil {
		return false, err
	}
	return addonVersionListed(version, versions), nil
}
