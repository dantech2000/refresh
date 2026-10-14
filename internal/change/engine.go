package change

import (
	"fmt"
	"slices"
	"strings"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// runEngine runs the phases of an upgrade or a rollback, in plan order,
// and returns one result per step of the plan.
//
// The phases are a sequence. The first step that does not end clean ends
// the run: nothing more is read or sent, and every later step is
// NotAttempted. Clean means Succeeded or Skipped, with no verification
// issue and with a verification that could read. So a failed phase is a
// failed run, exit code 1, whatever the phases before it did.
//
// Each phase with work runs in this order:
//
//  1. The checkpoint hook. It runs before every phase, the first
//     included, so a stop there ends the run before its first submit.
//  2. With ConfirmPhases, the question of the phase, unless the start
//     question was about this phase.
//  3. One read of the whole cluster. It is always a new read: time
//     passed in the plan, at the question, and in the phases before.
//  4. The steps are derived again from that read. A phase in which no
//     step has work any more is skipped, and the busy rule does not run.
//  5. The busy rule, with the steps of the phase as the steps of the run.
//  6. The steps, one after another. Each has its gate, its own fresh read
//     of its target, its submit, its wait, and its check. Before each
//     step after the first: the checkpoint hook, and steps 3 and 5 again.
//
// A phase of the plan with no step that submits, attaches, or verifies is
// skipped at once, with no read.
func (r *runner) runEngine(cp *ClusterPlan) []StepResult {
	var out []StepResult
	halted := false
	for _, ph := range cp.Phases {
		results := make([]StepResult, len(ph.Steps))
		for i, s := range ph.Steps {
			results[i] = StepResult{Ref: s.Ref, Status: StepNotAttempted}
		}
		if !halted {
			halted = !r.runEnginePhase(cp, ph, results)
		} else {
			// A step that the plan gave no work was never going to be
			// attempted: it is Skipped, whatever ended the run.
			for i, s := range ph.Steps {
				if !s.work() && s.Action != Blocked {
					results[i].Status = StepSkipped
					r.reportStep(results[i])
				}
			}
		}
		out = append(out, results...)
	}
	return out
}

// engineEnded reports whether the run can start nothing more: a question
// or a checkpoint stopped it, the user interrupted it, or the wait budget
// of the run passed. what names what would have come next.
func (r *runner) engineEnded(cp *ClusterPlan, what string) bool {
	switch {
	case r.stop != nil || r.interrupted:
		return true
	case r.recordInterrupt():
		return true
	}
	r.runWait.check()
	if r.ctx.Err() == nil && (r.runWaitCtx == nil || r.runWaitCtx.Err() == nil) {
		return false
	}
	f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonTimeout,
		"--wait-timeout passed before "+what+"; nothing more was started")
	f.Region = cp.Region
	r.addFailures([]apidoc.Failure{f})
	return true
}

// runEnginePhase runs one phase and fills results, which has one
// NotAttempted row per step. It returns false when the run ends here.
func (r *runner) runEnginePhase(cp *ClusterPlan, ph Phase, results []StepResult) bool {
	skipAll := func() {
		for i, s := range ph.Steps {
			if !s.work() {
				results[i].Status = StepSkipped
				if s.Action != Blocked {
					r.reportStep(results[i])
				}
			}
		}
	}
	if !slices.ContainsFunc(ph.Steps, Step.work) {
		// Nothing to run or to attach to: skipped, with no read and no
		// busy rule. A Blocked step has a blocker, which refused the plan.
		skipAll()
		for i, s := range ph.Steps {
			if s.Action == Blocked {
				results[i].Status = StepBlocked
				return false
			}
		}
		r.emit(Event{Kind: PhaseEnded, Phase: ph.Ref, Skipped: true})
		return true
	}
	title := phaseTitle(ph)
	if r.engineEnded(cp, title) || !r.checkpoint(StepRef{Phase: ph.Ref}, title) || !r.confirmPhase(cp, ph) {
		return false
	}
	r.emit(Event{Kind: PhaseStarted, Phase: ph.Ref})
	ok := r.runEngineSteps(cp, ph, results)
	if ok {
		// The steps that the phase did not have to run.
		for i := range results {
			if results[i].Status == StepNotAttempted {
				results[i].Status = StepSkipped
				r.reportStep(results[i])
			}
		}
	}
	skipped := !slices.ContainsFunc(results, func(s StepResult) bool { return s.Status != StepSkipped && s.Status != StepNotAttempted })
	r.emit(Event{Kind: PhaseEnded, Phase: ph.Ref, Skipped: ok && skipped})
	return ok
}

// runEngineSteps runs the steps of a phase one after another, and returns
// false when the run ends in the phase.
func (r *runner) runEngineSteps(cp *ClusterPlan, ph Phase, results []StepResult) bool {
	first := true
	for i := range ph.Steps {
		if !ph.Steps[i].work() {
			results[i].Status = StepSkipped
			r.reportStep(results[i])
			continue
		}
		if !first && (r.engineEnded(cp, stepName(ph.Steps[i].Ref)) || !r.checkpoint(ph.Steps[i].Ref, stepName(ph.Steps[i].Ref))) {
			return false
		}
		// Each step starts a read budget: its reads follow a wait.
		r.newRead(r.common.Limits.Read)
		res := StepResult{Ref: ph.Steps[i].Ref}
		snap, err := r.phaseRead(cp)
		if err != nil {
			results[i] = r.unsent(cp, res, err)
			r.reportStep(results[i])
			return false
		}
		steps := rederive(r.req.kind(), ph, snap)
		if first && !slices.ContainsFunc(steps, Step.work) {
			// Everything of the phase was done since the plan.
			r.notice(capitalize(phaseTitle(ph)) + ": nothing is left to do")
			return true
		}
		first = false
		if blocked, stop := r.phaseBusy(cp, res, snap, steps); stop {
			results[i] = blocked
			r.reportStep(results[i])
			return false
		}
		s := steps[i]
		if !s.work() {
			if s.Action == Skip {
				r.notice(fmt.Sprintf("%s needs no change any more; nothing was sent", stepName(s.Ref)))
			}
			results[i].Status = StepSkipped
			r.reportStep(results[i])
			continue
		}
		r.steps, r.remaining, r.unverified = steps, steps[i:], false
		reported := false
		results[i], reported = r.runEngineStep(cp, ph, s, snap)
		if !reported {
			r.reportStep(results[i])
		}
		if !cleanStep(results[i]) || r.unverified || r.stop != nil || r.interrupted {
			return false
		}
	}
	return true
}

// reportStep sends the result of a step to the event hook, and after it
// the check that the step held back: see stepEnded.
func (r *runner) reportStep(res StepResult) {
	r.stepEnded(res.Ref.Phase, &res)
}

// cleanStep reports whether a step ended in a way that lets the next
// step of a sequence run.
func cleanStep(res StepResult) bool {
	code, ok := stepCode(res)
	return ok && code == exitcode.OK && (res.Status == StepSucceeded || res.Status == StepSkipped)
}

// phaseRead reads the whole cluster for a phase or a step of an engine.
func (r *runner) phaseRead(cp *ClusterPlan) (*fleet.Snapshot, error) {
	fetch := r.d.fetcher()
	return fetch.Fresh().Fetch(r.readCtx, r.d.AWS.Clients(cp.Region), cp.Cluster, engineNeed(false))
}

// phaseBusy applies the busy rule to the read of a phase, with the steps
// of the phase as the steps of the run. A cluster that someone else is
// changing blocks the step, and a read that is not complete blocks it
// too: nothing then shows that no change is running. It returns true
// with the result when the run must stop.
func (r *runner) phaseBusy(cp *ClusterPlan, res StepResult, snap *fleet.Snapshot, steps []Step) (StepResult, bool) {
	refuse, notices := Busy(snap, r.req.kind(), steps)
	for _, n := range notices {
		// The read of a step and the read before its request find the
		// same thing: it is said once for the step.
		if r.busySaid.step != res.Ref {
			r.busySaid.step, r.busySaid.notices = res.Ref, nil
		}
		if !slices.Contains(r.busySaid.notices, n) {
			r.busySaid.notices = append(r.busySaid.notices, n)
			r.notice(n)
		}
	}
	if len(refuse) == 0 {
		return res, false
	}
	reason := apidoc.ReasonBusy
	parts := make([]string, len(refuse))
	for i, b := range refuse {
		parts[i] = strings.Replace(b.Message, "nothing was started", stepName(res.Ref)+" was not changed", 1)
		if b.Kind != BlockerBusy {
			reason = apidoc.ReasonUnknown
			r.addFailures(snap.Failures)
		}
	}
	res.Status = StepBlocked
	res.Failure = r.stepFailure(cp, res, reason, strings.Join(parts, "; "))
	return res, true
}

// rederive derives the steps of a phase again from a read of the cluster.
// The version that a step moves to is the one of the plan, and never
// changes here: what changes is whether the step still has work.
//
//   - The control plane: Skip at the goal, Attach while the cluster is
//     UPDATING, and Submit otherwise.
//   - An add-on: Skip when it is gone or at the goal, Attach while it is
//     CREATING or UPDATING at any version, and Submit otherwise.
//   - A nodegroup: Skip when it is gone, or at the goal and ACTIVE. Verify
//     at the goal and not ACTIVE, and at the goal for a step that the plan
//     would attach to or verify: its roll ended since the plan, and no
//     run verified it. Attach below the goal while UPDATING,
//     and Submit otherwise: a nodegroup that is CREATING below the goal is
//     not attached to, and its gate refuses it.
//
// The goal has a direction: an upgrade is done at the version of the step
// or above it, and a rollback at it or below it. A rollback never takes a
// newer version as done.
func rederive(kind Kind, ph Phase, snap *fleet.Snapshot) []Step {
	out := slices.Clone(ph.Steps)
	for i := range out {
		s := &out[i]
		if !s.work() {
			continue
		}
		switch s.Ref.Kind {
		case StepControlPlane:
			switch {
			case versionDone(kind, snap.Cluster.Version.String(), s.To):
				s.Action, s.Reason = Skip, AlreadyAtTarget
			case snap.Cluster.Status == clusterUpdating:
				s.Action, s.Reason = Attach, InFlight
			default:
				s.Action = Submit
			}
		case StepAddon:
			j := slices.IndexFunc(snap.Addons, func(a fleet.Addon) bool { return a.Name == s.Ref.Name })
			switch {
			case j < 0:
				s.Action, s.Reason = Skip, NoChange
			case snap.Addons[j].State != fleet.Collected:
				// The fresh read of the step decides.
			case snap.Addons[j].Status == "CREATING" || snap.Addons[j].Status == "UPDATING":
				s.Action, s.Reason = Attach, InFlight
			case addonDone(kind, snap.Addons[j].Version, s.To):
				s.Action, s.Reason = Skip, AlreadyAtTarget
			default:
				s.Action = Submit
			}
		case StepNodegroup:
			j := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == s.Ref.Name })
			switch {
			case j < 0:
				s.Action, s.Reason = Skip, NoChange
			case snap.Nodegroups[j].State != fleet.Collected:
			case versionDone(kind, snap.Nodegroups[j].Version.String(), s.To) && unsettled(snap.Nodegroups[j]):
				s.Action, s.Reason = Verify, Unsettled
			case versionDone(kind, snap.Nodegroups[j].Version.String(), s.To) && (s.Action == Attach || s.Action == Verify):
				// The roll that the plan would wait for ended since the
				// plan. Nobody verified it: this run does.
				s.Action, s.Reason = Verify, Unsettled
			case versionDone(kind, snap.Nodegroups[j].Version.String(), s.To):
				s.Action, s.Reason = Skip, AlreadyAtTarget
			case snap.Nodegroups[j].Status == nodegroupUpdating:
				s.Action, s.Reason = Attach, InFlight
			default:
				s.Action = Submit
			}
		}
	}
	return out
}

// runEngineStep runs one step with work. reported says that the step
// sent its own last StepChanged event.
func (r *runner) runEngineStep(cp *ClusterPlan, ph Phase, s Step, snap *fleet.Snapshot) (res StepResult, reported bool) {
	switch s.Ref.Kind {
	case StepControlPlane:
		return r.runControlPlane(cp, s, snap), false
	case StepAddon:
		return r.runEngineAddon(cp, s), false
	case StepNodegroup:
		return r.runEngineNodegroup(cp, ph, s)
	}
	res = StepResult{Ref: s.Ref, Status: StepFailed}
	res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, "this build cannot run a "+string(s.Ref.Kind)+" step")
	return res, false
}

// confirmPhase asks the question of a phase of an upgrade with
// ConfirmPhases, and reports whether the run may go on. --yes answers it.
// The question comes before any request of the phase. What was read for
// it is not used after it: each step reads again, and guardSubmit reads
// once more right before each request.
// The phase that the start question named is confirmed already. A no ends
// the run as Declined: nothing of the phase was read or sent.
func (r *runner) confirmPhase(cp *ClusterPlan, ph Phase) bool {
	up, ok := r.req.(UpgradeRequest)
	if !ok || !up.ConfirmPhases || r.common.Policy.Yes {
		return true
	}
	if r.confirmedPhase != nil && *r.confirmedPhase == ph.Ref {
		return true
	}
	// The question names what the phase does now, and not what the plan
	// said: the steps are derived again from a read of the cluster. A
	// phase with nothing left asks nothing. When the read fails, the
	// question is the one of the plan: consent still comes first.
	r.newRead(r.common.Limits.Read)
	if snap, err := r.phaseRead(cp); err == nil {
		ph.Steps = rederive(r.req.kind(), ph, snap)
		if !slices.ContainsFunc(ph.Steps, Step.work) {
			return true
		}
	}
	if r.engineEnded(cp, phaseTitle(ph)) {
		return false
	}
	q := Question{Kind: ConfirmPhase, Step: StepRef{Phase: ph.Ref}, Text: phaseQuestion(cp, ph)}
	if r.hooks.Ask == nil {
		r.stop = &stop{status: Failed, err: fmt.Errorf("%s needs an answer and nobody can be asked; pass --yes. Nothing more was changed", phaseTitle(ph))}
		return false
	}
	switch a, _ := r.askDuringRun(q); a {
	case askInterrupted:
		r.recordInterrupt()
		return false
	case askFailed:
		return false
	case answeredNo:
		// The message names the phase that was declined.
		r.stop.err = fmt.Errorf("declined before %s; nothing more was changed", phaseTitle(ph))
		return false
	case answeredYes:
	}
	return true
}

// workNames returns the names of the steps with work.
func workNames(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if s.work() {
			out = append(out, s.Ref.Name)
		}
	}
	return out
}

// phaseTitle names a phase in a message: "the control plane to 1.33".
func phaseTitle(ph Phase) string {
	to := ""
	if !ph.Ref.Hop.IsZero() {
		to = " to " + ph.Ref.Hop.String()
	}
	switch ph.Ref.Kind {
	case PhaseControlPlane:
		for _, s := range ph.Steps {
			to = " to " + s.To
		}
		return "the control plane" + to
	case PhaseAddonsBefore:
		return "the add-ons that " + ph.Ref.Hop.String() + " needs before the nodegroup rolls"
	case PhaseNodegroups:
		for _, s := range ph.Steps {
			to = " to " + s.To
		}
		return "the nodegroup rolls" + to
	}
	if ph.Ref.Hop.IsZero() {
		return "the add-on downgrades"
	}
	return "the add-on updates for " + ph.Ref.Hop.String()
}

// phaseQuestion is the question before one phase of an upgrade.
func phaseQuestion(cp *ClusterPlan, ph Phase) string {
	names := workNames(ph.Steps)
	list := strings.Join(names, ", ")
	switch ph.Ref.Kind {
	case PhaseControlPlane:
		// The phase has one step.
		if s := ph.Steps[0]; s.Action == Attach {
			return fmt.Sprintf("Wait for the control-plane update of %s to %s that is running?", cp.Cluster, s.To)
		}
		return fmt.Sprintf("Upgrade the control plane of %s from %s to %s?", cp.Cluster, ph.Steps[0].From, ph.Steps[0].To)
	case PhaseAddonsBefore:
		return fmt.Sprintf("Update %d add-on(s) of %s that Kubernetes %s cannot run as they are (%s)?", len(names), cp.Cluster, ph.Ref.Hop, list)
	case PhaseNodegroups:
		return fmt.Sprintf("Roll %d nodegroup(s) of %s to Kubernetes %s (%s)?", len(names), cp.Cluster, ph.Ref.Hop, list)
	}
	return fmt.Sprintf("Update %d add-on(s) of %s for Kubernetes %s (%s)?", len(names), cp.Cluster, ph.Ref.Hop, list)
}

// engineQuestion is the question before the first change of an upgrade
// or a rollback, for a plan that nobody confirmed yet.
//
// A rollback asks once, with the whole order in the question. An upgrade
// with ConfirmPhases asks about its first phase with work, and each later
// phase asks for itself. When the gate has warnings, the question names
// them, and a yes accepts them.
func (r *runner) engineQuestion(p *Plan) *Question {
	cp := &p.Clusters[0]
	q := &Question{Kind: ConfirmStart}
	var text string
	up, _ := r.req.(UpgradeRequest)
	switch {
	case p.Kind == ClusterRollback:
		text = "Roll back " + cp.Cluster + " from " + cp.From.String() + " to " + cp.To.String() + ": " + strings.Join(orderOf(cp), ", then ")
	case up.ConfirmPhases:
		for _, ph := range cp.Phases {
			if slices.ContainsFunc(ph.Steps, Step.work) {
				q.Kind, q.Step = ConfirmPhase, StepRef{Phase: ph.Ref}
				text = strings.TrimSuffix(phaseQuestion(cp, ph), "?")
				break
			}
		}
	default:
		text = "Upgrade " + cp.Cluster + " from " + cp.From.String() + " to " + cp.To.String() + ": " + strings.Join(orderOf(cp), ", then ")
	}
	if v := cp.Gate; v.Decision == health.Ask {
		q.Reasons = reasonStrings(v.Reasons)
		if q.Kind == ConfirmStart {
			q.Kind = GateWarnings
		}
		text += ", despite the health warnings (" + strings.Join(q.Reasons, "; ") + ")"
	}
	q.Text = text + "?"
	return q
}

// orderOf lists the phases of a plan that have work, in run order, for a
// question: "2 nodegroup roll(s) to 1.32 (ng-a, ng-b)".
func orderOf(cp *ClusterPlan) []string {
	var out []string
	for _, ph := range cp.Phases {
		names := workNames(ph.Steps)
		if len(names) == 0 {
			continue
		}
		switch ph.Ref.Kind {
		case PhaseControlPlane:
			out = append(out, phaseTitle(ph))
		case PhaseNodegroups:
			out = append(out, fmt.Sprintf("%d nodegroup roll(s)%s (%s)", len(names), hopWords(ph), strings.Join(names, ", ")))
		default:
			out = append(out, fmt.Sprintf("%d add-on update(s)%s (%s)", len(names), hopWords(ph), strings.Join(names, ", ")))
		}
	}
	return out
}

func hopWords(ph Phase) string {
	if ph.Ref.Hop.IsZero() {
		return ""
	}
	return " for " + ph.Ref.Hop.String()
}

// resumeHint is the end of the message of an upgrade or a rollback that
// stopped part way: the phases that completed, and the command that goes
// on from there. A run derives its plan from live state, so the same
// command resumes it. It is "" for every other run.
func (r *runner) resumeHint(res Result) string {
	if res.Plan == nil || res.Plan.Kind.independent() || len(res.Clusters) != 1 {
		return ""
	}
	statuses := map[StepRef]StepStatus{}
	for _, s := range res.Clusters[0].Steps {
		statuses[s.Ref] = s.Status
	}
	var done []string
	left := false
	for _, ph := range res.Plan.Clusters[0].Phases {
		worked, complete := false, true
		for _, s := range ph.Steps {
			switch statuses[s.Ref] {
			case StepSucceeded:
				worked = true
			case StepSkipped:
			default:
				complete = false
			}
		}
		if worked && complete {
			done = append(done, phaseTitle(ph))
		}
		left = left || !complete
	}
	if !left {
		return ""
	}
	completed := "no phase completed"
	if len(done) > 0 {
		completed = "completed: " + strings.Join(done, ", ")
	}
	return completed + "; to go on from the live state of the cluster, run: " + engineCommand(r.d.FollowUp, r.req)
}

// clusterSettled is the done function of a wait for a control plane:
// ACTIVE is done, and a cluster that is FAILED or DELETING never becomes
// ACTIVE.
func clusterSettled(s Settled) (bool, error) {
	switch s.Status {
	case string(ekstypes.ClusterStatusActive):
		return true, nil
	case string(ekstypes.ClusterStatusFailed), string(ekstypes.ClusterStatusDeleting):
		return false, fmt.Errorf("the cluster is %s; it will not become ACTIVE", s.Status)
	}
	return false, nil
}

// nodegroupSettled is the done function of a wait for a nodegroup that is
// changing: any status but UPDATING and CREATING ends it, and the caller
// judges the version first and the status second.
func nodegroupSettled(s Settled) (bool, error) {
	return s.Status != string(ekstypes.NodegroupStatusUpdating) && s.Status != string(ekstypes.NodegroupStatusCreating), nil
}
