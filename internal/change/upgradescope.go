package change

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/fleet"
)

// includes reports whether the request covers a part: every part when
// Only is empty.
func (r UpgradeRequest) includes(p Part) bool {
	return len(r.Only) == 0 || slices.Contains(r.Only, p)
}

// partOf returns the part that a step belongs to.
func partOf(k StepKind) Part {
	switch k {
	case StepAddon:
		return PartAddons
	case StepNodegroup:
		return PartNodegroups
	}
	return PartControlPlane
}

// FollowUp holds the words around a command that a notice or a blocker
// names, so that the command reaches the same account, region, and
// Kubernetes context as the run.
type FollowUp struct {
	// Prefix starts the command: "refresh" with the --profile and the
	// --region of the run. Empty means "refresh".
	Prefix string
	// Suffix ends it: the --kubeconfig and the --kube-context of the run.
	Suffix string
	// Resume holds the flags that only the command of a stopped run
	// repeats, and that the request cannot tell from a default: the
	// --wait-timeout that the user chose.
	Resume string
	// Profile is the AWS profile that refresh gives the SDK, from
	// --profile or the active context, for a command of the AWS CLI that a
	// notice names. It is empty when the environment names the profile:
	// the AWS CLI reads the environment too.
	Profile string
}

// checkScope refuses a scope that the plan cannot keep safe. Without the
// control plane, the add-ons and the nodegroups can only catch up to the
// version that it runs. Without the add-ons, the control plane moves one
// minor version: an add-on that stays behind may not run two versions on.
func checkScope(req UpgradeRequest, cluster string, live fleet.Version, hops int) error {
	if len(req.Only) == 0 {
		return nil
	}
	if !req.includes(PartControlPlane) && live != req.To {
		return &RefusalError{Reason: RefusalNeedsControlPlane, Message: fmt.Sprintf(
			"the control plane of %s runs %s: --only %s catches up to the control plane's version, so use --to %s, or add control-plane to --only",
			cluster, live, joinParts(req.Only), live)}
	}
	if !req.includes(PartAddons) && hops > 1 {
		return &RefusalError{Reason: RefusalNeedsAddons, Message: fmt.Sprintf(
			"without add-ons, %s upgrades one minor version at a time: an add-on left on the old version may not run two versions on; use --to %s, or add addons to --only",
			cluster, nextMinor(live))}
	}
	return nil
}

// checkExists returns a NotFoundError for the first name that the cluster
// does not have. A name is exact, and one that matches nothing never
// selects nothing in silence.
func checkExists(t Target, kind StepKind, names, have []string) error {
	for _, n := range names {
		if !slices.Contains(have, n) {
			return &NotFoundError{Target: t, Kind: kind, Name: n, Names: slices.Clone(have)}
		}
	}
	return nil
}

// applyScope turns the steps of the parts that --only leaves out, and of
// the nodegroups that -n leaves out, into Manual steps, and adds a notice
// for each part that still has work: what to run next.
//
// One step keeps its place: an add-on step that is Blocked and goes
// before the rolls. The new control plane cannot run the installed
// version and no compatible one was found, so leaving the add-ons out
// cannot make that control-plane move safe. Any other Blocked step that
// turns Manual loses its blocker, and a notice keeps the reason.
func (p *upgradePlanner) applyScope() {
	req := p.req
	if len(req.Only) == 0 && len(req.Nodegroups) == 0 {
		return
	}
	var incompatible, addonsLeft, nodegroupsLeft []string
	for pi := range p.cp.Phases {
		ph := &p.cp.Phases[pi]
		for si := range ph.Steps {
			st := &ph.Steps[si]
			part := partOf(st.Ref.Kind)
			unselected := part == PartNodegroups && len(req.Nodegroups) > 0 && !slices.Contains(req.Nodegroups, st.Ref.Name)
			if (!st.work() && st.Action != Blocked) || (req.includes(part) && !unselected) {
				continue
			}
			before := ph.Ref.Kind == PhaseAddonsBefore
			if st.Action == Blocked {
				if before {
					continue
				}
				p.unblock(st.Ref)
			}
			st.Action, st.Reason, st.Force = Manual, OutOfScope, false
			if part == PartNodegroups {
				nodegroupsLeft = appendNew(nodegroupsLeft, st.Ref.Name)
				continue
			}
			addonsLeft = appendNew(addonsLeft, st.Ref.Name)
			if before {
				incompatible = appendNew(incompatible, st.Ref.Name)
			}
		}
	}
	switch {
	case len(incompatible) > 0:
		p.notice("add-on(s) %s may not run on Kubernetes %s: update them right after the control plane: %s",
			strings.Join(incompatible, ", "), req.To, p.followUp(PartAddons, req.To))
	case len(addonsLeft) > 0:
		p.notice("add-on(s) %s stay where they are; update them later: %s", strings.Join(addonsLeft, ", "), p.followUp(PartAddons, req.To))
	}
	if len(nodegroupsLeft) > 0 {
		p.notice("nodegroup(s) %s stay on their version; roll them later: %s", strings.Join(nodegroupsLeft, ", "), p.followUp(PartNodegroups, req.To))
	}
}

// unblock removes the blockers of a step that the run leaves alone, and
// keeps their reasons in a notice.
func (p *upgradePlanner) unblock(ref StepRef) {
	kept := p.cp.Blockers[:0]
	for _, b := range p.cp.Blockers {
		if b.Step == nil || *b.Step != ref {
			kept = append(kept, b)
			continue
		}
		p.notice("%s is left alone, and could not be changed: %s", stepName(ref), b.Message)
	}
	p.cp.Blockers = kept
}

// followUp returns the command that runs one part later, to a version,
// with the exclusions of this run: following it never changes what the
// user left out. Both lists go on the command whatever the part, because
// a skipped add-on with no usable version would block a later nodegroup
// run.
func (p *upgradePlanner) followUp(part Part, version fleet.Version) string {
	prefix := p.in.Command.Prefix
	if prefix == "" {
		prefix = "refresh"
	}
	words := []string{prefix, "cluster", "upgrade", shellQuote(p.cp.Cluster), "--to", version.String(), "--only", string(part)}
	for _, s := range p.req.SkipAddons {
		words = append(words, "--skip-addon", shellQuote(s))
	}
	for _, s := range p.req.SkipNodegroups {
		words = append(words, "--skip-nodegroup", shellQuote(s))
	}
	if s := strings.TrimSpace(p.in.Command.Suffix); s != "" {
		words = append(words, s)
	}
	return strings.Join(words, " ")
}

// addonLag is what blocks a control-plane move on the add-ons.
type addonLag struct {
	// incompatible are installed versions that the live control plane
	// cannot run, and empty are add-ons with no version listed for it.
	// Both block when the plan leaves the add-ons out: nothing in it
	// would update them.
	incompatible, empty []string
	// unread are add-ons whose versions for the live control plane could
	// not be read. They block in every scope.
	unread []string
}

// blockOnLaggingAddons blocks the first control-plane move of the plan
// when add-ons already cannot run on the live control plane and the plan
// leaves them out: an earlier --only run left them, and one more move
// would put them two versions behind. It fails closed in every scope for
// an add-on whose versions for the live control plane could not be read.
// Each cause names its own remedy, because updating the add-ons fixes the
// first one only.
func (p *upgradePlanner) blockOnLaggingAddons(lag addonLag) {
	if len(lag.incompatible) == 0 && len(lag.empty) == 0 && len(lag.unread) == 0 {
		return
	}
	step := p.firstControlPlaneMove()
	if step == nil {
		return
	}
	step.Action, step.Reason = Blocked, ""
	if len(lag.incompatible) > 0 {
		p.block(BlockerNotReady, &step.Ref, "add-on(s) %s do not run on the live control plane %s: update them first (%s), or add addons to --only",
			strings.Join(lag.incompatible, ", "), p.live, p.followUp(PartAddons, p.live))
	}
	if len(lag.empty) > 0 {
		flags := make([]string, len(lag.empty))
		for i, n := range lag.empty {
			flags[i] = "--skip-addon " + shellQuote(n)
		}
		p.block(BlockerNotReady, &step.Ref, "EKS lists no version of add-on(s) %s for %s: if they are managed outside EKS add-ons, leave them out with %s",
			strings.Join(lag.empty, ", "), p.live, strings.Join(flags, " "))
	}
	if len(lag.unread) > 0 {
		p.block(BlockerUnread, &step.Ref, "the versions of add-on(s) %s for %s could not be read: rerun to retry (the failures name the cause)",
			strings.Join(lag.unread, ", "), p.live)
	}
}

// firstControlPlaneMove returns the first control-plane step of the plan
// that a run would act on, or nil.
func (p *upgradePlanner) firstControlPlaneMove() *Step {
	for pi := range p.cp.Phases {
		for si := range p.cp.Phases[pi].Steps {
			if st := &p.cp.Phases[pi].Steps[si]; st.Ref.Kind == StepControlPlane && st.work() {
				return st
			}
		}
	}
	return nil
}

func appendNew(list []string, s string) []string {
	if s == "" || slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// shellQuote returns s as one POSIX shell word: unchanged when it holds
// only safe characters, and in single quotes otherwise. A command that a
// notice prints for the user to run quotes its values with it.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_./:=,@%+", r)
	}
	if strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
