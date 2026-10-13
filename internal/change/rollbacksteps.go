package change

import (
	"slices"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// workSteps adds the Nodegroups phase and the Addons phase for the move
// to the target of the plan, and the notice about EKS Auto Mode.
func (p *rollbackPlanner) workSteps() {
	if p.snap.Cluster.AutoMode {
		p.notice("EKS Auto Mode: EKS rolls back the Auto Mode nodes itself before the control plane, so the rollback takes longer")
	}
	if steps := p.nodegroupSteps(); len(steps) > 0 {
		p.cp.Phases = append(p.cp.Phases, Phase{Ref: steps[0].Ref.Phase, Steps: steps})
	}
	if steps := p.addonSteps(); len(steps) > 0 {
		p.cp.Phases = append(p.cp.Phases, Phase{Ref: steps[0].Ref.Phase, Steps: steps})
	}
}

// nodegroupSteps derives the step of each nodegroup, in listing order.
// The first row that matches decides:
//
//  1. Not read: Blocked.
//  2. At the target or below it, not settled, and not skipped: Verify.
//  3. At the target or below it: Skip.
//  4. Skipped by flag: Manual.
//  5. Custom AMI: Manual.
//  6. UPDATING: Attach.
//  7. Otherwise: Submit.
//
// The control plane does not roll back while a nodegroup of row 4 or 5
// is above the target: see RollbackControlPlaneBlockers.
func (p *rollbackPlanner) nodegroupSteps() []Step {
	target := p.cp.To
	phase := PhaseRef{Target: p.cp.Target, Kind: PhaseNodegroups}
	steps := make([]Step, 0, len(p.snap.Nodegroups))
	for _, n := range p.snap.Nodegroups {
		step := Step{Ref: StepRef{Phase: phase, Kind: StepNodegroup, Name: n.Name}, From: n.Version.String(), To: target.String()}
		done, skipped := atLeast(target, n.Version), slices.Contains(p.req.SkipNodegroups, n.Name)
		switch {
		case n.State != fleet.Collected:
			step.Action = Blocked
			p.block(BlockerUnread, &step.Ref, "could not read nodegroup %s", n.Name)
		case done && unsettled(n) && !skipped:
			step.Action, step.Reason = Verify, Unsettled
		case done:
			step.Action, step.Reason = Skip, AlreadyAtTarget
		case skipped:
			step.Action, step.Reason = Manual, SkippedByFlag
			p.notice("nodegroup %s is skipped: move it to %s yourself; the control plane does not roll back while it is at %s", n.Name, target, n.Version)
		case n.AMIType == customAMIType:
			step.Action, step.Reason = Manual, CustomAMI
			p.notice("nodegroup %s has a custom AMI: roll it to a %s AMI yourself; the control plane does not roll back while it is at %s", n.Name, target, n.Version)
		case n.Status == nodegroupUpdating:
			step.Action, step.Reason = Attach, InFlight
		default:
			step.Action, step.Force = Submit, p.req.Policy.Force
		}
		steps = append(steps, step)
	}
	return steps
}

// addonSteps derives the step of each add-on, in dependency order. The
// first row that matches decides:
//
//  1. Not read, or its catalogue could not be read: Blocked.
//  2. EKS publishes no version for the target: Blocked.
//  3. The installed version is in the catalogue of the target: Skip.
//  4. Otherwise: Submit, to the newest version of that catalogue.
func (p *rollbackPlanner) addonSteps() []Step {
	target := p.cp.To
	phase := PhaseRef{Target: p.cp.Target, Kind: PhaseAddons}
	names := make([]string, len(p.snap.Addons))
	byName := make(map[string]fleet.Addon, len(p.snap.Addons))
	for i, a := range p.snap.Addons {
		names[i], byName[a.Name] = a.Name, a
	}
	steps := make([]Step, 0, len(names))
	for _, name := range sortByDependency(names) {
		a := byName[name]
		step := Step{Ref: StepRef{Phase: phase, Kind: StepAddon, Name: name}, From: a.Version, Action: Blocked}
		if a.State != fleet.Collected {
			p.block(BlockerUnread, &step.Ref, "could not read add-on %s", name)
			steps = append(steps, step)
			continue
		}
		versions, err := p.in.AddonVersions(name, target)
		switch {
		case err != nil:
			p.fail(apidoc.FailureAddon, name, err)
			p.block(BlockerUnread, &step.Ref, "could not look up versions of %s compatible with %s (rerun to retry)", name, target)
		case len(versions) == 0:
			p.block(BlockerUnavailable, &step.Ref, "no version of %s is compatible with %s", name, target)
		case addonVersionListed(a.Version, versions):
			step.Action, step.Reason, step.To = Skip, AlreadyAtTarget, a.Version
		default:
			step.Action, step.To = Submit, versions[0]
		}
		steps = append(steps, step)
	}
	return steps
}
