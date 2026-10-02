package upgrade

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/common"
)

// Part is one part of a cluster upgrade that --only selects.
type Part string

const (
	// PartControlPlane is the control-plane version step.
	PartControlPlane Part = "control-plane"
	// PartAddons is every EKS add-on step.
	PartAddons Part = "addons"
	// PartNodegroups is every managed nodegroup roll.
	PartNodegroups Part = "nodegroups"
)

// Parts lists every Part in run order.
func Parts() []Part { return []Part{PartControlPlane, PartAddons, PartNodegroups} }

// ParseParts reads --only values. No values means every part; values that
// name no part (--only "" or --only ,) are an error, so an empty variable
// never widens a run to every part.
func ParseParts(values []string) ([]Part, error) {
	var out []Part
	for _, v := range values {
		for _, f := range strings.Split(v, ",") {
			p := Part(strings.ToLower(strings.TrimSpace(f)))
			if p == "" {
				continue
			}
			if !slices.Contains(Parts(), p) {
				return nil, fmt.Errorf("--only %q: want %s", f, joinParts(Parts()))
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if len(values) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("--only needs at least one of %s", joinParts(Parts()))
	}
	return out, nil
}

func joinParts(parts []Part) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

// includes reports whether the plan covers part: every part when Only is
// empty.
func (o PlanOptions) includes(p Part) bool { return len(o.Only) == 0 || slices.Contains(o.Only, p) }

// partOf is the part a step belongs to; "" for the readiness gate, which
// every scope keeps.
func partOf(t StepType) Part {
	switch t {
	case StepControlPlane:
		return PartControlPlane
	case StepAddon:
		return PartAddons
	case StepNodegroup:
		return PartNodegroups
	}
	return ""
}

// checkScope refuses a scope the plan cannot keep safe. Without the control
// plane, add-ons and nodegroups can only catch up to the version it already
// runs. Without add-ons, the control plane moves one minor version at a
// time: an add-on left behind may not run two versions on.
func checkScope(o PlanOptions, cluster, live, target string, hops int) error {
	if len(o.Only) == 0 {
		return nil
	}
	if !o.includes(PartControlPlane) && live != target {
		return fmt.Errorf("the control plane of %s runs %s: --only %s catches up to the control plane's version, so use --to %s, or add control-plane to --only", cluster, live, joinParts(o.Only), live)
	}
	if !o.includes(PartAddons) && hops > 1 {
		return fmt.Errorf("without add-ons, %s upgrades one minor version at a time: an add-on left on the old version may not run two versions on; use --to %s, or add addons to --only", cluster, nextMinor(live))
	}
	return nil
}

// applyScope turns the pending and blocked steps of the parts outside
// o.Only into manual steps, and adds a notice for each part left out that
// still has work: what to run next. An add-on step that is blocked and must
// go before the rolls (the new control plane cannot run the installed
// version, and no compatible one could be found) stays blocked: leaving
// the add-ons out cannot make that control-plane move safe. A blocked step
// that turns manual keeps its reason.
func applyScope(plan *Plan, o PlanOptions) {
	if len(o.Only) == 0 && len(o.Nodegroups) == 0 {
		return
	}
	var incompatible, addonsLeft, nodegroupsLeft []string
	for h := range plan.Hops {
		for i := range plan.Hops[h].Steps {
			st := &plan.Hops[h].Steps[i]
			p := partOf(st.Type)
			if p == "" || (st.Status != StatusPending && st.Status != StatusBlocked) {
				continue
			}
			var left string
			switch {
			case !o.includes(p):
				left = "left out by --only " + joinParts(o.Only)
			case p == PartNodegroups && len(o.Nodegroups) > 0 && !slices.Contains(o.Nodegroups, st.Target):
				left = "left out by --nodegroup " + strings.Join(o.Nodegroups, ",")
			default:
				continue
			}
			if st.Status == StatusBlocked && p == PartAddons && st.BeforeNodegroups {
				continue
			}
			if st.Status == StatusBlocked && st.Reason != "" {
				left += "; blocked: " + st.Reason
			}
			st.Status, st.Reason = StatusManual, left
			switch p {
			case PartAddons:
				addonsLeft = appendNew(addonsLeft, st.Target)
				if st.BeforeNodegroups {
					incompatible = appendNew(incompatible, st.Target)
				}
			case PartNodegroups:
				nodegroupsLeft = appendNew(nodegroupsLeft, st.Target)
			}
		}
	}
	target := plan.TargetVersion
	if len(incompatible) > 0 {
		plan.Notices = append(plan.Notices, fmt.Sprintf("add-on(s) %s may not run on Kubernetes %s: update them right after the control plane: %s", strings.Join(incompatible, ", "), target, followUp(plan, o, PartAddons, target)))
	} else if len(addonsLeft) > 0 {
		plan.Notices = append(plan.Notices, fmt.Sprintf("add-on(s) %s stay where they are; update them later: %s", strings.Join(addonsLeft, ", "), followUp(plan, o, PartAddons, target)))
	}
	if len(nodegroupsLeft) > 0 {
		plan.Notices = append(plan.Notices, fmt.Sprintf("nodegroup(s) %s stay on their version; roll them later: %s", strings.Join(nodegroupsLeft, ", "), followUp(plan, o, PartNodegroups, target)))
	}
}

// followUp is the command that runs part later, to version: the same
// account and region (o.CommandPrefix), and the same --skip or
// --skip-nodegroup exclusions, so following it never changes what the user
// left out.
func followUp(plan *Plan, o PlanOptions, part Part, version string) string {
	prefix := o.CommandPrefix
	if prefix == "" {
		prefix = "refresh"
	}
	parts := []string{prefix, "cluster", "upgrade", "-c", common.ShellQuote(plan.ClusterName), "--to", common.ShellQuote(version), "--only", string(part)}
	switch part {
	case PartAddons:
		for _, s := range o.SkipAddons {
			parts = append(parts, "--skip", common.ShellQuote(s))
		}
	case PartNodegroups:
		for _, s := range o.SkipNodegroups {
			parts = append(parts, "--skip-nodegroup", common.ShellQuote(s))
		}
	}
	return strings.Join(parts, " ")
}

// blockOnLaggingAddons blocks the plan's first control-plane move when
// add-ons left out of it already cannot run on the live control plane (an
// earlier --only run left them): moving the control plane again would leave
// them two versions behind. It fails closed in every scope: an add-on whose
// versions for the live control plane could not be read (unread) blocks
// too, and its failure is on the plan.
func blockOnLaggingAddons(plan *Plan, o PlanOptions, lagging, unread []string, live string) {
	if len(lagging) == 0 && len(unread) == 0 {
		return
	}
	var why []string
	if len(lagging) > 0 {
		why = append(why, fmt.Sprintf("add-on(s) %s do not run on the live control plane %s", strings.Join(lagging, ", "), live))
	}
	if len(unread) > 0 {
		why = append(why, fmt.Sprintf("the versions of add-on(s) %s for %s could not be read", strings.Join(unread, ", "), live))
	}
	for h := range plan.Hops {
		for i := range plan.Hops[h].Steps {
			st := &plan.Hops[h].Steps[i]
			if st.Type == StepControlPlane && st.Status == StatusPending {
				st.Status = StatusBlocked
				st.Reason = fmt.Sprintf("%s: update the add-ons first (%s), or add addons to --only", strings.Join(why, "; "), followUp(plan, o, PartAddons, live))
				return
			}
		}
	}
}

// checkNodegroups refuses a --nodegroup name the cluster does not have.
func checkNodegroups(names []string, nodegroups []nodegroupState, cluster string) error {
	for _, n := range names {
		if !slices.ContainsFunc(nodegroups, func(ng nodegroupState) bool { return ng.Name == n }) {
			have := make([]string, len(nodegroups))
			for i, ng := range nodegroups {
				have[i] = ng.Name
			}
			return fmt.Errorf("--nodegroup %q: cluster %s has no such nodegroup (it has: %s)", n, cluster, strings.Join(have, ", "))
		}
	}
	return nil
}

// selectedNodegroups is the nodegroups named in names, or all of them when
// names is empty.
func selectedNodegroups(nodegroups []nodegroupState, names []string) []nodegroupState {
	if len(names) == 0 {
		return nodegroups
	}
	var out []nodegroupState
	for _, ng := range nodegroups {
		if slices.Contains(names, ng.Name) {
			out = append(out, ng)
		}
	}
	return out
}

func appendNew(list []string, s string) []string {
	if s == "" || slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}
