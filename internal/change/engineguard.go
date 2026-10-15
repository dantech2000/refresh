package change

import (
	"errors"
	"fmt"
	"slices"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// guardSubmit is the one boundary before every request that an upgrade or
// a rollback sends of its own: the update of the control plane, of an
// add-on, and the roll of a nodegroup. The caller sends the request right
// after it, with nothing in between. Time passed before it: a phase before
// this one, an update of someone else that the step waited for, a question
// that a person answered. So nothing that was read before counts here.
//
//  1. One read of the whole cluster. A rollback reads the update history
//     too.
//  2. A rollback is judged again against the target of the plan: the
//     rollback window, the offer of the target version, and the rollback
//     insights. See RollbackStillEligible. A target that changed refuses,
//     and no answer accepts it.
//  3. The busy rule on that read, with the steps of the phase as the
//     steps of the run, and this step as a Submit: an update of the
//     control plane that someone else started refuses the request.
//  4. What the step itself needs, on that read: see guardControlPlane,
//     guardAddon, and guardNodegroup.
//  5. After a person answered a question of the gate, and after the gate
//     waited for an insights refresh, everything runs once more: both
//     took time. After the health checks of a nodegroup passed, the read
//     and every rule but the checks run once more for the same reason.
//
// It returns false with the result of the step when nothing must be sent:
// Blocked or Failed, Skipped when the target met its goal in the meantime,
// and NotAttempted when the run ended.
func (r *runner) guardSubmit(cp *ClusterPlan, s Step, res StepResult) (StepResult, bool) {
	steps := slices.Clone(r.steps)
	for i := range steps {
		if steps[i].Ref == s.Ref {
			steps[i].Action = Submit
		}
	}
	gated := false
	for asked := 0; ; asked++ {
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false
		}
		snap, err := r.guardRead(cp)
		if err != nil {
			return r.unsent(cp, res, err), false
		}
		if out, ok := r.guardRollback(cp, res, snap); !ok {
			return out, false
		}
		if blocked, stop := r.phaseBusy(cp, res, snap, steps); stop {
			return blocked, false
		}
		out, ok, answered := res, true, false
		switch s.Ref.Kind {
		case StepControlPlane:
			// The gate of an upgrade may start an insights refresh and
			// wait for it, which takes minutes: everything is then read
			// and judged once more, and the refresh is reused.
			refreshes := r.refreshes()
			out, ok = r.guardControlPlane(cp, res, s, snap)
			answered = r.refreshes() != refreshes
		case StepAddon:
			out, ok = r.guardAddon(cp, res, s, snap)
		case StepNodegroup:
			out, ok, answered = r.guardNodegroup(cp, res, s, snap, asked < maxQuestions, gated)
			// The health checks call AWS and Kubernetes, which takes
			// time: everything is read and judged once more, without the
			// checks, so the request follows a read with no wait after it.
			if ok && !answered && !gated && !r.common.Policy.Skip {
				gated = true
				continue
			}
		}
		if !ok {
			return out, false
		}
		if answered {
			continue
		}
		// A run that ended sends nothing.
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false
		}
		return res, true
	}
}

// refreshes returns how many insights refreshes the run has started or
// waited for.
func (r *runner) refreshes() int {
	if r.refresh == nil {
		return 0
	}
	return r.refresh.ran
}

// guardRead reads the whole cluster for guardSubmit.
func (r *runner) guardRead(cp *ClusterPlan) (*fleet.Snapshot, error) {
	fetch := r.d.fetcher()
	return fetch.Fresh().Fetch(r.readCtx, r.d.AWS.Clients(cp.Region), cp.Cluster, engineNeed(r.req.kind() == ClusterRollback))
}

// blockStep returns the result of a step that guardSubmit refuses.
func (r *runner) blockStep(cp *ClusterPlan, res StepResult, status StepStatus, reason apidoc.Reason, message string) StepResult {
	res.Status = status
	if status == StepFailed {
		res.FinishedAt = r.d.now()
	}
	res.Failure = r.stepFailure(cp, res, reason, message)
	return res
}

// guardRollback judges the rollback again on the read of guardSubmit. It
// does nothing for an upgrade.
func (r *runner) guardRollback(cp *ClusterPlan, res StepResult, snap *fleet.Snapshot) (StepResult, bool) {
	req, ok := r.req.(RollbackRequest)
	if !ok {
		return res, true
	}
	blockers, failures, err := RollbackStillEligible(snap, req, rollbackInputs(r.readCtx, r.d, r.d.AWS.Clients(cp.Region), cp.Cluster), cp.To)
	if ended := r.readCtx.Err(); ended != nil {
		return r.unsent(cp, res, ended), false
	}
	name := stepName(res.Ref)
	var changed *RefusalError
	if errors.As(err, &changed) {
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, changed.Message+"; "+name+" was not changed"), false
	}
	r.addFailures(failures)
	if len(blockers) == 0 {
		return res, true
	}
	return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown,
		"the cluster can no longer roll back to "+cp.To.String()+" ("+blockerMessages(blockers)+"); "+name+" was not changed"), false
}

// blockerMessages joins the messages of blockers.
func blockerMessages(blockers []Blocker) string {
	out := ""
	for i, b := range blockers {
		if i > 0 {
			out += "; "
		}
		out += b.Message
	}
	return out
}

// guardControlPlane is what the request of a control-plane step needs, on
// the read of guardSubmit. The busy rule has shown that the cluster is
// ACTIVE. The first row that matches decides:
//
//  1. At the goal: Skipped.
//  2. The version is not the one that the request starts from: Failed.
//     EKS moves one minor version at a time, so an upgrade is sent only
//     from the minor version before its goal, and a rollback only from
//     the one after it.
//  3. The gate of the phase refuses: Blocked. See controlPlaneGate.
func (r *runner) guardControlPlane(cp *ClusterPlan, res StepResult, s Step, snap *fleet.Snapshot) (StepResult, bool) {
	kind, live := r.req.kind(), snap.Cluster.Version.String()
	switch {
	case versionDone(kind, live, s.To):
		r.notice(fmt.Sprintf("the control plane of %s is at %s already; nothing was sent", cp.Cluster, live))
		res.Status = StepSkipped
		return res, false
	case !adjacent(kind, live, s.To):
		return r.blockStep(cp, res, StepFailed, apidoc.ReasonUnknown,
			fmt.Sprintf("the control plane of %s runs %s now, and the plan moves it from %s to %s; nothing was sent. Run the command again", cp.Cluster, orUnknown(live), s.From, s.To)), false
	}
	return r.controlPlaneGate(cp, res, s, snap)
}

// guardAddon is what the request of an add-on step needs, on the read of
// guardSubmit. The first row that matches decides:
//
//  1. The add-on was not read: Blocked.
//  2. EKS is changing it: Blocked. Someone started a change after the
//     fresh read of the step. The next run attaches to it.
//  3. Its version, or the version of the step, does not parse: Blocked.
//     It is neither at the goal nor behind it.
//  4. Its version meets the phase now: Skipped.
//  5. The request would go the wrong way: Blocked. A rollback never sends
//     a version above the live one, and an upgrade never one below it.
//     Someone moved the add-on since the plan.
func (r *runner) guardAddon(cp *ClusterPlan, res StepResult, s Step, snap *fleet.Snapshot) (StepResult, bool) {
	name := s.Ref.Name
	i := slices.IndexFunc(snap.Addons, func(a fleet.Addon) bool { return a.Name == name })
	if i < 0 || snap.Addons[i].State != fleet.Collected {
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, "could not read add-on "+name+" before its update; it was not changed"), false
	}
	a := snap.Addons[i]
	if addonInFlight(a.Status) {
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonBusy,
			fmt.Sprintf("add-on %s started changing before its update was sent (%s); it was not changed. Run the command again to wait for that change", name, a.Status)), false
	}
	keep, err := r.keepsAddon(cp, s, a.Version)
	var bad *AddonVersionError
	switch {
	case errors.As(err, &bad):
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, bad.Error()+"; it was not changed"), false
	case err != nil:
		return r.unsent(cp, res, err), false
	case keep:
		r.notice(fmt.Sprintf("add-on %s is at %s now; nothing was sent", name, a.Version))
		res.Status = StepSkipped
		return res, false
	case !addonDirection(r.req.kind(), a.Version, s.To):
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, wrongAddonDirection(r.req.kind(), name, a.Version, s.To)), false
	}
	return res, true
}

// wrongAddonDirection is the message of an add-on step whose request
// would go the wrong way from the live version.
func wrongAddonDirection(kind Kind, name, live, goal string) string {
	if kind == ClusterRollback {
		return fmt.Sprintf("add-on %s is at %s now, and the plan moves it to %s, which is newer: a rollback does not upgrade an add-on; it was not changed. Run the command again", name, live, goal)
	}
	return fmt.Sprintf("add-on %s is at %s now, and the plan moves it to %s, which is older: an upgrade does not downgrade an add-on; it was not changed. Run the command again", name, live, goal)
}

// guardNodegroup is what the roll of a nodegroup needs, on the read of
// guardSubmit. answered says that a person accepted a warning of the
// gate, so the caller reads and judges once more. The first row that
// matches decides:
//
//  1. The nodegroup was not read: Blocked.
//  2. The control plane is below the version of the roll: Blocked. Nodes
//     are never newer than the control plane. A plan with --only
//     nodegroups has no control-plane step, and its plan read the control
//     plane some time ago. The rule holds with --skip-health-check too.
//  3. The health gate, with this nodegroup as its target, compared with
//     the health that the consent covers: see regate. --skip-health-check
//     skips this row and no other, and so does gated: the checks passed
//     on the read before this one.
//  4. The nodegroup is changing: Blocked. Someone started a change after
//     the step read it. The next run attaches to it.
//  5. The nodegroup is not ACTIVE: Blocked.
//  6. Its version meets the goal already: Skipped.
//  7. The roll would go the wrong way: Failed. An upgrade never sends a
//     version below the live one, and a rollback never one above it.
//  8. EKS reports a health issue for the nodegroup: Blocked. A nodegroup
//     can be ACTIVE with issues, and the gate of row 3 judges its status.
func (r *runner) guardNodegroup(cp *ClusterPlan, res StepResult, s Step, snap *fleet.Snapshot, mayAsk, gated bool) (out StepResult, ok, answered bool) {
	name, kind := s.Ref.Name, r.req.kind()
	i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
	if i < 0 || snap.Nodegroups[i].State != fleet.Collected {
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, "could not read nodegroup "+name+" before its roll; it was not rolled"), false, false
	}
	n := snap.Nodegroups[i]
	if goal, err := fleet.ParseVersion(s.To); err != nil || !atLeast(snap.Cluster.Version, goal) {
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, fmt.Sprintf(
			"the control plane of %s runs %s, and nodegroup %s would roll to %s: the control plane must be at %s first; it was not rolled",
			cp.Cluster, orUnknown(snap.Cluster.Version.String()), name, s.To, s.To)), false, false
	}
	if !r.common.Policy.Skip && !gated {
		in := &health.Input{Snapshot: snap, Targets: []string{name}, Clients: r.d.AWS.Clients(cp.Region), Now: snap.FetchedAt}
		in.Kube, in.Metrics, in.NoKube = kubeAccess(r.d, snap)
		checks := health.Run(r.readCtx, health.Preflight(), in)
		if err := r.readCtx.Err(); err != nil {
			return r.unsent(cp, res, err), false, false
		}
		if out, ok, answered = r.regate(cp, res, checks, mayAsk); !ok || answered {
			return out, ok, answered
		}
	}
	version := n.Version.String()
	switch {
	case nodegroupInFlight(n.Status):
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonBusy,
			fmt.Sprintf("nodegroup %s started changing after its health gate (%s); it was not rolled. Run the command again to wait for that change", name, n.Status)), false, false
	case n.Status != nodegroupActive:
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown, notRollable(name, n.Status)), false, false
	case versionDone(kind, version, s.To):
		r.notice(fmt.Sprintf("nodegroup %s is at %s already; nothing was sent", name, version))
		res.Status = StepSkipped
		return res, false, false
	case !rollDirection(kind, version, s.To):
		return r.blockStep(cp, res, StepFailed, apidoc.ReasonUnknown,
			fmt.Sprintf("nodegroup %s runs Kubernetes %s now, and the plan moves it to %s; nothing was sent. Run the command again", name, orUnknown(version), s.To)), false, false
	case len(n.HealthIssues) > 0:
		first := n.HealthIssues[0]
		return r.blockStep(cp, res, StepBlocked, apidoc.ReasonUnknown,
			fmt.Sprintf("nodegroup %s has %d health issue(s), first: %s: %s; it was not rolled", name, len(n.HealthIssues), first.Code, first.Message)), false, false
	}
	return res, true, false
}

// ownedForce returns the force of a request that the run sends of its
// own for a step, from the request of the run: --force for the roll of a
// nodegroup, and --skip-insights-check for the control plane of a
// rollback. The planners set the same value on the step, also on a step
// that attaches, so the plan shows what a submit after the attachment
// would send.
func ownedForce(req Request, kind StepKind) bool {
	switch r := req.(type) {
	case UpgradeRequest:
		return kind == StepNodegroup && r.Policy.Force
	case RollbackRequest:
		return (kind == StepNodegroup && r.Policy.Force) || (kind == StepControlPlane && r.SkipInsights)
	}
	return false
}
