package change

import (
	"fmt"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
)

// ChangeKind says how a plan differs from the plan that a person saw.
type ChangeKind string //nolint:revive // exported: change.Kind is the kind of a change; this is the kind of a Change

// The kinds of difference, each with its effect.
const (
	// Done: a step with work is now Skip. The run goes on, with a notice.
	Done ChangeKind = "Done"
	// NowInFlight: a Submit step is now Attach, or its target is now
	// changing elsewhere. The run goes on, with a notice.
	NowInFlight ChangeKind = "NowInFlight"
	// KubernetesVersion: the To of a nodegroup step differs. The run
	// refuses.
	KubernetesVersion ChangeKind = "KubernetesVersion"
	// Release: the ToRelease of a step differs. The run asks.
	Release ChangeKind = "Release"
	// Version: the To of any other step differs. The run asks.
	Version ChangeKind = "Version"
	// SizesChanged: the sizes that a scale step starts from, or the sizes
	// that it ends at, differ. The run asks.
	SizesChanged ChangeKind = "Sizes"
	// StepDiffers: a step with work differs in a field that no other kind
	// names: what the run does with it and why, the version that it
	// starts from, or the force that is sent with it. The run asks.
	StepDiffers ChangeKind = "Step"
	// Added and Removed: a step with work appeared, or went away without
	// being done. The run asks.
	Added   ChangeKind = "Added"
	Removed ChangeKind = "Removed"
	// NowBlocked: a new blocker, or a step that became Blocked. The run
	// refuses.
	NowBlocked ChangeKind = "NowBlocked"
	// GateWorse: a health check is worse than it was. The run asks again;
	// --yes accepts, and --require-healthy has blocked the plan already.
	GateWorse ChangeKind = "GateWorse"
)

// Effect is what a difference does to a run.
type Effect int

// The effects, mildest first. A refusal wins over a question.
const (
	// Continue: the run goes on and says what changed.
	Continue Effect = iota
	// AskAgain: a person must accept the new plan. --yes accepts.
	AskAgain
	// Refuse: the run ends before it submits anything.
	Refuse
)

// Effect returns what a difference of kind k does to a run.
func (k ChangeKind) Effect() Effect {
	switch k {
	case Done, NowInFlight:
		return Continue
	case KubernetesVersion, NowBlocked:
		return Refuse
	}
	return AskAgain
}

// Change is one difference between two plans.
type Change struct {
	// Step is the zero value for a difference that is about the cluster.
	Step    StepRef
	Kind    ChangeKind
	Message string
}

// Diff compares the plan that a person saw with the plan computed now and
// returns the differences that matter, in the order of cur and then of
// the steps that went away.
//
// It compares everything that a step submits or acts on. The steps are
// matched by their ref, so a target that is new or gone is Added or
// Removed. For a step with work in both plans, every field of Step is
// compared: the action, the reason, From, FromRelease, To, ToRelease,
// Force, and the sizes before and after. The reason, From, and FromRelease
// are not compared for a step that now attaches to a change in flight:
// they are the ones of that change. InFlight is not compared at all: it names the update of someone
// else on a target that the run leaves alone, so no change of it needs an
// answer or a notice, and the row of the step says what it is now. It
// also compares the blockers and the health
// checks of each cluster. Diff does not compare the requests of the two
// plans: Run checks that with sameRequest before it takes a reviewed plan
// as consent.
func Diff(prev, cur *Plan) []Change {
	var out []Change
	before := map[StepRef]Step{}
	blockers := map[string]bool{}
	checks := map[Target][]health.Result{}
	for i := range prev.Clusters {
		cp := &prev.Clusters[i]
		for _, s := range cp.Steps() {
			before[s.Ref] = s
		}
		for _, b := range cp.Blockers {
			blockers[blockerKey(cp.Target, b)] = true
		}
		checks[cp.Target] = cp.Checks
	}
	seen := map[StepRef]bool{}
	for i := range cur.Clusters {
		cp := &cur.Clusters[i]
		for _, s := range cp.Steps() {
			seen[s.Ref] = true
			old, ok := before[s.Ref]
			out = append(out, diffStep(old, ok, s)...)
		}
		for _, b := range cp.Blockers {
			if blockers[blockerKey(cp.Target, b)] {
				continue
			}
			c := Change{Kind: NowBlocked, Message: b.Message}
			if b.Step != nil {
				c.Step = *b.Step
			}
			out = append(out, c)
		}
		if worse := health.Worse(checks[cp.Target], cp.Checks); len(worse) > 0 {
			parts := make([]string, len(worse))
			for j, r := range worse {
				parts[j] = r.String()
			}
			out = append(out, Change{Kind: GateWorse, Message: strings.Join(parts, "; ")})
		}
	}
	for i := range prev.Clusters {
		for _, s := range prev.Clusters[i].Steps() {
			if s.work() && !seen[s.Ref] {
				out = append(out, Change{Step: s.Ref, Kind: Removed, Message: stepName(s.Ref) + " is no longer part of the plan"})
			}
		}
	}
	return out
}

// blockerKey identifies a blocker for the comparison of two plans.
func blockerKey(t Target, b Blocker) string {
	step := ""
	if b.Step != nil {
		step = b.Step.String()
	}
	return t.Region + "\x00" + t.Cluster + "\x00" + string(b.Kind) + "\x00" + step + "\x00" + b.Message
}

// diffStep compares one step of the new plan with the step of the same
// ref in the plan before, when there was one. For two steps with work it
// reads every field of Step but Ref, which matched already, and InFlight,
// which a step with work does not have: a field that is added to Step or
// to ScaleSizes needs a comparison here, and TestDiff_EveryStepField
// fails without one.
func diffStep(old Step, had bool, cur Step) []Change {
	name := stepName(cur.Ref)
	change := func(k ChangeKind, format string, args ...any) []Change {
		return []Change{{Step: cur.Ref, Kind: k, Message: fmt.Sprintf(format, args...)}}
	}
	if !had || !old.work() {
		switch {
		case cur.work():
			return change(Added, "%s is now part of the plan", name)
		case cur.Action == Blocked && (!had || old.Action != Blocked):
			return change(NowBlocked, "%s can no longer be changed", name)
		}
		return nil
	}
	switch {
	case cur.Action == Blocked:
		return change(NowBlocked, "%s can no longer be changed", name)
	case cur.busy():
		return change(NowInFlight, "%s is now changing elsewhere", name)
	case cur.Action == Skip:
		return change(Done, "%s needs no change any more", name)
	case !cur.work():
		return change(Removed, "%s is no longer part of the plan", name)
	}
	var out []Change
	switch {
	case old.Action == cur.Action:
	case old.Action == Submit && cur.Action == Attach:
		out = append(out, change(NowInFlight, "%s is already changing to %s; waiting for it", name, cur.To)...)
	default:
		// A step that waited for the change of someone else and now sends
		// its own does more than the person saw.
		out = append(out, change(StepDiffers, "%s is now %s, not %s", name, actionWords(cur.Action), actionWords(old.Action))...)
	}
	// A step that now waits for the change of someone else has the reason
	// and the starting version of that change: NowInFlight says it all.
	if old.Action == cur.Action {
		if old.Reason != cur.Reason {
			out = append(out, change(StepDiffers, "%s is now changed for the reason %s, not %s", name, reasonWords(cur.Reason), reasonWords(old.Reason))...)
		}
		if old.From != cur.From {
			out = append(out, change(StepDiffers, "%s is now at %s, not %s", name, cur.From, old.From)...)
		}
		if old.FromRelease != cur.FromRelease {
			out = append(out, change(StepDiffers, "%s now runs the AMI release %s, not %s", name, cur.FromRelease, old.FromRelease)...)
		}
	}
	if old.To != cur.To {
		k := Version
		if cur.Ref.Kind == StepNodegroup {
			k = KubernetesVersion
		}
		out = append(out, change(k, "%s now goes to %s, not %s", name, cur.To, old.To)...)
	}
	if old.ToRelease != cur.ToRelease {
		out = append(out, change(Release, "%s now gets the AMI release %s, not %s", name, cur.ToRelease, old.ToRelease)...)
	}
	if old.Force != cur.Force {
		with := map[bool]string{true: "with", false: "without"}
		out = append(out, change(StepDiffers, "%s is now changed %s --force, not %s it", name, with[cur.Force], with[old.Force])...)
	}
	switch {
	case old.Sizes == nil && cur.Sizes == nil:
	case old.Sizes == nil || cur.Sizes == nil:
		out = append(out, change(SizesChanged, "the sizes of %s are no longer the ones of the plan", name)...)
	default:
		if old.Sizes.Before != cur.Sizes.Before {
			out = append(out, change(SizesChanged, "%s now has %s", name, cur.Sizes.Before)...)
		}
		if old.Sizes.After != cur.Sizes.After {
			out = append(out, change(SizesChanged, "%s now goes to %s, not %s", name, cur.Sizes.After, old.Sizes.After)...)
		}
	}
	return out
}

// actionWords says what a run does for a step with the action.
func actionWords(a Action) string {
	switch a {
	case Submit:
		return "changed by this run"
	case Attach:
		return "waited for"
	case Verify:
		return "verified"
	}
	return string(a)
}

// reasonWords names a reason in a message. A step can have none.
func reasonWords(r Reason) string {
	if r == "" {
		return "(none)"
	}
	return string(r)
}

// stepName returns the words for a step in a message: "add-on vpc-cni".
func stepName(r StepRef) string {
	switch r.Kind {
	case StepAddon:
		return "add-on " + r.Name
	case StepNodegroup, StepScale:
		return "nodegroup " + r.Name
	}
	return "the control plane of " + r.Phase.Target.Cluster
}

// worstEffect returns the strongest effect among changes.
func worstEffect(changes []Change) Effect {
	worst := Continue
	for _, c := range changes {
		worst = max(worst, c.Effect())
	}
	return worst
}

// Effect returns what the difference does to a run.
func (c Change) Effect() Effect { return c.Kind.Effect() }
