package change

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// catchUp plans the work at the live version that the next control-plane
// step depends on, as a hop of its own before the regular hops, and
// returns what the add-ons block that step with. The hop has:
//
//   - the add-ons, when one of them cannot run on the live control plane:
//     a run that was stopped after the control plane moved left it behind
//   - the nodegroups that the next step would push past the kubelet skew
//   - the nodegroups at the live version that are not settled: no regular
//     hop would look at one before the control plane moves, because its
//     next roll comes after that step
//
// Everything else rolls with the regular hops. The control plane does not
// move in the hop, so the hop has no readiness gate. A part that --only
// leaves out does not catch up: the readiness of the next hop then blocks
// on a nodegroup beyond the skew, and blockOnLaggingAddons on the
// add-ons.
func (p *upgradePlanner) catchUp(next fleet.Version) addonLag {
	var preRoll []fleet.Nodegroup
	if p.req.includes(PartNodegroups) {
		rollable := p.rollable()
		preRoll = append(p.preRoll(rollable, next), p.unsettledAtLive(rollable)...)
	}
	lag := p.liveAddonLag()
	withAddons := p.req.includes(PartAddons) && len(lag.incompatible) > 0
	if withAddons || len(preRoll) > 0 {
		p.phase(PhaseControlPlane, p.live, []Step{p.controlPlaneStep(p.live)})
		var before, after []Step
		if withAddons {
			before, after = p.addonSteps(p.live)
		}
		p.phase(PhaseAddonsBefore, p.live, before)
		p.phase(PhaseNodegroups, p.live, p.nodegroupSteps(preRoll, p.live, PreRoll))
		p.phase(PhaseAddonsAfter, p.live, after)
		p.advance(preRoll, p.live)
	}
	if p.req.includes(PartAddons) {
		// The plan updates them itself: only an unread catalogue blocks.
		return addonLag{unread: lag.unread}
	}
	return lag
}

// preRoll returns the nodegroups that must roll to the live version
// before the control plane moves to next. A nodegroup qualifies on its
// own version alone, never on its siblings: the plan rolls it, next would
// put it beyond the kubelet skew, and it is within the skew of the live
// control plane, so the roll to that version is supported. A nodegroup
// that is further behind is not rolled early: the readiness of the next
// hop blocks on it.
//
// A nodegroup within the skew of next is not rolled early, even when it
// trails the control plane: the regular hop rolls it once, straight to
// the hop target.
func (p *upgradePlanner) preRoll(nodegroups []fleet.Nodegroup, next fleet.Version) []fleet.Nodegroup {
	var out []fleet.Nodegroup
	for _, n := range nodegroups {
		if n.AMIType == customAMIType || p.skipsNodegroup(n.Name) || atLeast(n.Version, p.live) {
			continue
		}
		if beyondKubeletSkew(n.Version, next) && !beyondKubeletSkew(n.Version, p.live) {
			out = append(out, n)
		}
	}
	return out
}

// unsettledAtLive returns the nodegroups at the live version, or above
// it, that are not settled and not skipped.
func (p *upgradePlanner) unsettledAtLive(nodegroups []fleet.Nodegroup) []fleet.Nodegroup {
	var out []fleet.Nodegroup
	for _, n := range nodegroups {
		if atLeast(n.Version, p.live) && unsettled(n) && !p.skipsNodegroup(n.Name) {
			out = append(out, n)
		}
	}
	return out
}

// liveAddonLag sorts the installed add-ons that the request does not
// skip by what the catalogue of the live version says about them. An
// add-on that is only not at the newest compatible version is in no
// list: the add-on phase of the next hop moves it anyway.
func (p *upgradePlanner) liveAddonLag() addonLag {
	var lag addonLag
	for _, a := range p.addons {
		if p.skipsAddon(a.Name) {
			continue
		}
		if a.State != fleet.Collected {
			lag.unread = append(lag.unread, a.Name)
			continue
		}
		versions, err := p.in.AddonVersions(a.Name, p.live)
		switch {
		case err != nil:
			lag.unread = append(lag.unread, a.Name)
			p.fail(apidoc.FailureAddon, a.Name, err)
		case len(versions) == 0:
			lag.empty = append(lag.empty, a.Name)
		case !addonVersionListed(a.Version, versions):
			lag.incompatible = append(lag.incompatible, a.Name)
		}
	}
	return lag
}
