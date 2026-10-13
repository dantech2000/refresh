package change

import (
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// controlPlaneStep derives the control-plane step of a hop: Skip when
// the live control plane is at the hop target or past it, Attach when the
// cluster is updating already, and Submit otherwise.
func (p *upgradePlanner) controlPlaneStep(to fleet.Version) Step {
	step := Step{Ref: StepRef{Kind: StepControlPlane}, Action: Submit, Reason: VersionBehind, From: p.live.String(), To: to.String()}
	switch {
	case atLeast(p.live, to):
		step.Action, step.Reason = Skip, AlreadyAtTarget
	case p.snap.Cluster.Status == clusterUpdating:
		step.Action, step.Reason = Attach, InFlight
	}
	return step
}

// The EKS values that the planners compare with.
const (
	clusterActive     = "ACTIVE"
	clusterUpdating   = "UPDATING"
	nodegroupActive   = "ACTIVE"
	nodegroupUpdating = "UPDATING"
	customAMIType     = "CUSTOM"
	al2AMIPrefix      = "AL2_"
)

// al2LastVersion is the last Kubernetes minor version with EKS-optimized
// Amazon Linux 2 AMIs.
// https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-deprecation-faqs.html
var al2LastVersion = fleet.Version{Major: 1, Minor: 32}

// unsettled reports whether a nodegroup is in a state that its version
// does not vouch for: an update is in flight, or it is not ACTIVE, such
// as DEGRADED after a roll. A nodegroup at the goal version in such a
// state is not done: the run waits for it and verifies it. A status that
// is not known counts as settled.
func unsettled(n fleet.Nodegroup) bool {
	return n.Status != "" && n.Status != nodegroupActive
}

// nodegroupSteps derives the step of each nodegroup for a hop. The first
// row that matches decides:
//
//  1. Not read: Blocked.
//  2. At the hop target or past it, not settled, and not skipped: Verify.
//  3. At the hop target or past it: Skip.
//  4. Skipped by flag: Manual.
//  5. Custom AMI: Manual. The operator owns the AMI.
//  6. Amazon Linux 2 and a hop target that has no such AMI: Blocked. The
//     roll would fail after the control plane moved.
//  7. UPDATING: Attach.
//  8. Otherwise: Submit, with the reason that the caller gives.
func (p *upgradePlanner) nodegroupSteps(nodegroups []fleet.Nodegroup, to fleet.Version, reason Reason) []Step {
	steps := make([]Step, 0, len(nodegroups))
	for _, n := range nodegroups {
		step := Step{Ref: StepRef{Kind: StepNodegroup, Name: n.Name}, From: n.Version.String(), To: to.String()}
		ref := StepRef{Phase: PhaseRef{Target: p.cp.Target, Kind: PhaseNodegroups, Hop: to}, Kind: StepNodegroup, Name: n.Name}
		done, skipped := atLeast(n.Version, to), p.skipsNodegroup(n.Name)
		switch {
		case n.State != fleet.Collected:
			step.Action = Blocked
			p.block(BlockerUnread, &ref, "could not read nodegroup %s", n.Name)
		case done && unsettled(n) && !skipped:
			step.Action, step.Reason = Verify, Unsettled
		case done:
			step.Action, step.Reason = Skip, AlreadyAtTarget
		case skipped:
			step.Action, step.Reason = Manual, SkippedByFlag
		case n.AMIType == customAMIType:
			step.Action, step.Reason = Manual, CustomAMI
		case strings.HasPrefix(n.AMIType, al2AMIPrefix) && !atLeast(al2LastVersion, to):
			step.Action = Blocked
			p.block(BlockerUnavailable, &ref,
				"Amazon Linux 2 AMIs end at Kubernetes %s: replace nodegroup %s with an AL2023 or Bottlerocket nodegroup first, or leave it at %s with --skip-nodegroup %s",
				al2LastVersion, n.Name, n.Version, shellQuote(n.Name))
		case n.Status == nodegroupUpdating:
			step.Action, step.Reason = Attach, InFlight
		default:
			step.Action, step.Reason, step.Force = Submit, reason, p.req.Policy.Force
		}
		steps = append(steps, step)
	}
	return steps
}

// addonSteps derives the step of each add-on for a hop, in two groups,
// each in dependency order. before are the add-ons that the control plane
// of the hop cannot run: the version that an add-on has when the hop
// starts is not in the catalogue of the hop target. They update before
// the nodegroup rolls. The others update after the rolls, the order that
// AWS documents. The first row that matches decides:
//
//  1. Skipped by flag: Manual, after. The catalogue is not read, so a
//     skipped add-on is never a blocker.
//  2. Not read, or its catalogue could not be read: Blocked, before.
//  3. EKS publishes no version for the hop target: Blocked, before.
//  4. The installed version is the newest one for the hop target, or
//     newer: Skip, after.
//  5. The version at the start of the hop is not in the catalogue:
//     Submit, before.
//  6. Otherwise: Submit, after.
func (p *upgradePlanner) addonSteps(to fleet.Version) (before, after []Step) {
	for _, a := range p.addons {
		step := Step{Ref: StepRef{Kind: StepAddon, Name: a.Name}, From: a.Version}
		ref := StepRef{Phase: PhaseRef{Target: p.cp.Target, Kind: PhaseAddonsBefore, Hop: to}, Kind: StepAddon, Name: a.Name}
		if p.skipsAddon(a.Name) {
			step.Action, step.Reason = Manual, SkippedByFlag
			after = append(after, step)
			continue
		}
		if a.State != fleet.Collected {
			step.Action = Blocked
			p.block(BlockerUnread, &ref, "could not read add-on %s", a.Name)
			before = append(before, step)
			continue
		}
		versions, err := p.in.AddonVersions(a.Name, to)
		switch {
		case err != nil:
			// An API failure is not an incompatibility, and says so.
			step.Action = Blocked
			p.fail(apidoc.FailureAddon, a.Name, err)
			p.block(BlockerUnread, &ref, "could not look up versions of %s compatible with %s (rerun to retry)", a.Name, to)
			before = append(before, step)
			continue
		case len(versions) == 0:
			step.Action = Blocked
			p.block(BlockerUnavailable, &ref, "no version of %s is compatible with %s", a.Name, to)
			before = append(before, step)
			continue
		}
		chosen := versions[0]
		step.To = chosen
		if fleet.CompareAddonVersions(a.Version, chosen) >= 0 {
			step.Action, step.Reason = Skip, AlreadyAtTarget
			after = append(after, step)
			continue
		}
		current := p.simAddons[a.Name]
		if fleet.CompareAddonVersions(current, chosen) < 0 {
			p.simAddons[a.Name] = chosen
		}
		step.Action, step.Reason, step.From = Submit, VersionBehind, current
		if addonVersionListed(current, versions) {
			after = append(after, step)
		} else {
			before = append(before, step)
		}
	}
	return before, after
}

// addonVersionListed reports whether a version is one of versions.
func addonVersionListed(version string, versions []string) bool {
	return slices.ContainsFunc(versions, func(v string) bool { return fleet.CompareAddonVersions(version, v) == 0 })
}
