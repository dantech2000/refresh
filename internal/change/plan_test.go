package change

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

var (
	prod       = Target{Region: east, Cluster: "prod"}
	addonPhase = PhaseRef{Target: prod, Kind: PhaseAddons}
)

func addonStep(name string, action Action, reason Reason, from, to string) Step {
	return Step{Ref: StepRef{Phase: addonPhase, Kind: StepAddon, Name: name}, Action: action, Reason: reason, From: from, To: to}
}

// planOf returns a plan of one cluster with the steps in one phase.
func planOf(kind Kind, steps ...Step) *Plan {
	return &Plan{Kind: kind, Clusters: []ClusterPlan{{Target: prod, Phases: []Phase{{Ref: addonPhase, Steps: steps}}}}}
}

// TestPlanDecision is the decision table of a plan with the exit code of
// its dry run. The gate verdicts are the ones that health.Gate gives for
// the results and the policy of each row.
func TestPlanDecision(t *testing.T) {
	submit := addonStep("vpc-cni", Submit, VersionBehind, "v1", "v2")
	skip := addonStep("coredns", Skip, AlreadyAtTarget, "v1", "v1")
	busy := addonStep("vpc-cni", Skip, InFlight, "v1", "v2")
	warn := health.Result{Name: "Addons", Outcome: health.Warn, Blocking: true, Message: "coredns is DEGRADED"}
	fail := health.Result{Name: "Addons", Outcome: health.Fail, Blocking: true, Message: "coredns is DELETING"}
	unreadable := health.Result{
		Name: "Quotas", Outcome: health.Unreadable, Message: "could not read the quota",
		Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod"}},
	}
	pass := health.Result{Name: "Addons", Outcome: health.Pass, Blocking: true}
	failure := apidoc.Failure{Kind: apidoc.FailureAddon, Name: "kube-proxy"}
	blocker := Blocker{Kind: BlockerBusy, Message: "prod is busy"}

	tests := []struct {
		name     string
		steps    []Step
		checks   []health.Result
		policy   health.Policy
		blockers []Blocker
		failures []apidoc.Failure
		want     Decision
		exit     int
	}{
		{name: "blocker", steps: []Step{submit}, checks: []health.Result{pass}, blockers: []Blocker{blocker}, want: DecisionBlocked, exit: 3},
		{name: "blocker and a failure", steps: []Step{submit}, blockers: []Blocker{{Kind: BlockerUnread}}, failures: []apidoc.Failure{failure}, want: DecisionBlocked, exit: 4},
		{name: "gate blocks", steps: []Step{submit}, checks: []health.Result{fail}, want: DecisionBlocked, exit: 3},
		{name: "gate blocks, --yes changes nothing", steps: []Step{submit}, checks: []health.Result{fail}, policy: health.Policy{Yes: true}, want: DecisionBlocked, exit: 3},
		{name: "gate blocks and a read failed", steps: []Step{submit}, checks: []health.Result{fail, unreadable}, want: DecisionBlocked, exit: 4},
		{name: "--require-healthy over a warning", steps: []Step{submit}, checks: []health.Result{warn}, policy: health.Policy{RequireHealthy: true}, want: DecisionBlocked, exit: 2},
		{name: "nothing to do", steps: []Step{skip}, want: DecisionNothingToDo, exit: 0},
		{name: "nothing to do, with a failure", steps: []Step{skip}, failures: []apidoc.Failure{failure}, want: DecisionNothingToDo, exit: 4},
		{name: "every target is busy", steps: []Step{busy}, failures: []apidoc.Failure{{Kind: apidoc.FailureAddon, Name: "vpc-cni", Reason: apidoc.ReasonBusy}}, want: DecisionNothingToDo, exit: 4},
		{name: "gate asks", steps: []Step{submit}, checks: []health.Result{warn}, want: Ask, exit: 2},
		{name: "gate went past a warning with --yes", steps: []Step{submit}, checks: []health.Result{warn}, policy: health.Policy{Yes: true}, want: Ask, exit: 2},
		{name: "gate asks about a read that failed", steps: []Step{submit}, checks: []health.Result{unreadable}, want: Ask, exit: 4},
		{name: "gate went past an unreadable check with --yes", steps: []Step{submit}, checks: []health.Result{unreadable}, policy: health.Policy{Yes: true}, want: Ask, exit: 4},
		{name: "proceed", steps: []Step{submit, skip}, checks: []health.Result{pass}, want: Proceed, exit: 0},
		{name: "proceed with --yes", steps: []Step{submit}, checks: []health.Result{pass}, policy: health.Policy{Yes: true}, want: Proceed, exit: 0},
		{name: "proceed, with a failure", steps: []Step{submit}, checks: []health.Result{pass}, failures: []apidoc.Failure{failure}, want: Proceed, exit: 4},
		{name: "proceed, one target busy", steps: []Step{submit, busy}, checks: []health.Result{pass}, failures: []apidoc.Failure{failure}, want: Proceed, exit: 4},
		{name: "the checks were skipped", steps: []Step{submit}, policy: health.Policy{Skip: true}, want: Proceed, exit: 0},
		{name: "attach alone runs no gate", steps: []Step{addonStep("vpc-cni", Attach, InFlight, "v2", "v2")}, want: Proceed, exit: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := planOf(AddonUpdate, tt.steps...)
			cp := &p.Clusters[0]
			cp.Blockers, cp.Failures, cp.Checks = tt.blockers, tt.failures, tt.checks
			if tt.checks != nil || tt.policy.Skip {
				cp.Gate = health.Gate(tt.checks, tt.policy)
			}
			if got := p.Decision(); got != tt.want {
				t.Errorf("Decision() = %s, want %s", got, tt.want)
			}
			if got := p.ExitCode(); got != tt.exit {
				t.Errorf("ExitCode() = %d, want %d", got, tt.exit)
			}
		})
	}
}

// A plan of several clusters has the worst decision and the highest code.
func TestPlanDecision_Fleet(t *testing.T) {
	a := planOf(NodegroupScale, addonStep("a", Skip, AlreadyAtTarget, "v1", "v1")).Clusters[0]
	b := planOf(NodegroupScale, addonStep("b", Submit, VersionBehind, "v1", "v2")).Clusters[0]
	b.Cluster = "dev"
	c := b
	c.Cluster, c.Blockers = "test", []Blocker{{Kind: BlockerBusy}}
	p := &Plan{Kind: NodegroupScale, Clusters: []ClusterPlan{a, b}}
	if got := p.Decision(); got != Proceed {
		t.Errorf("Decision() = %s, want Proceed", got)
	}
	p.Clusters = append(p.Clusters, c)
	if got, code := p.Decision(), p.ExitCode(); got != DecisionBlocked || code != 3 {
		t.Errorf("Decision() = %s, exit %d; want Blocked, 3", got, code)
	}
	p.Failures = []apidoc.Failure{{Kind: apidoc.FailureRegion, Name: "eu-west-1"}}
	if code := p.ExitCode(); code != 4 {
		t.Errorf("ExitCode() with a region failure = %d, want 4", code)
	}
}

func TestRefStrings(t *testing.T) {
	hop := PhaseRef{Target: prod, Kind: PhaseNodegroups, Hop: fleet.Version{Major: 1, Minor: 33}}
	tests := []struct{ got, want string }{
		{addonPhase.String(), "prod/Addons"},
		{StepRef{Phase: addonPhase, Kind: StepAddon, Name: "vpc-cni"}.String(), "prod/Addons/vpc-cni"},
		{hop.String(), "prod/1.33/Nodegroups"},
		{StepRef{Phase: hop, Kind: StepNodegroup, Name: "web"}.String(), "prod/1.33/Nodegroups/web"},
		{StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseControlPlane}, Kind: StepControlPlane}.String(), "prod/ControlPlane"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("String() = %q, want %q", tt.got, tt.want)
		}
	}
}

// TestDiff has one row for each kind of difference, and the rows that say
// which effect wins.
func TestDiff(t *testing.T) {
	submit := addonStep("vpc-cni", Submit, VersionBehind, "v1", "v2")
	ref := submit.Ref
	rollPhase := PhaseRef{Target: prod, Kind: PhaseRoll}
	roll := Step{Ref: StepRef{Phase: rollPhase, Kind: StepNodegroup, Name: "web"}, Action: Submit, Reason: AMIOutdated, From: "1.33", To: "1.33", ToRelease: "1.33.0-20260101"}
	scale := Step{
		Ref: StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseScale}, Kind: StepScale, Name: "web"}, Action: Submit,
		Sizes: &ScaleSizes{Before: Sizes{Desired: 2, Min: 1, Max: 3}, After: Sizes{Desired: 3, Min: 1, Max: 3}},
	}
	with := func(s Step, edit func(*Step)) Step {
		edit(&s)
		return s
	}
	warn := []health.Result{{Name: "Addons", Outcome: health.Warn, Message: "coredns is DEGRADED"}}
	pass := []health.Result{{Name: "Addons", Outcome: health.Pass}}

	tests := []struct {
		name      string
		kind      Kind
		prev, cur []Step
		edit      func(prev, cur *ClusterPlan)
		want      []Change
		effect    Effect
	}{
		{name: "the same plan", kind: AddonUpdate, prev: []Step{submit}, cur: []Step{submit}},
		{
			name: "Done", kind: AddonUpdate, prev: []Step{submit},
			cur:  []Step{with(submit, func(s *Step) { s.Action, s.Reason = Skip, AlreadyAtTarget })},
			want: []Change{{Step: ref, Kind: Done, Message: "add-on vpc-cni needs no change any more"}},
		},
		{
			name: "NowInFlight: attach", kind: AddonUpdate, prev: []Step{submit},
			cur:  []Step{with(submit, func(s *Step) { s.Action, s.Reason = Attach, InFlight })},
			want: []Change{{Step: ref, Kind: NowInFlight, Message: "add-on vpc-cni is already changing to v2; waiting for it"}},
		},
		{
			name: "NowInFlight: busy", kind: AddonUpdate, prev: []Step{submit},
			cur:  []Step{with(submit, func(s *Step) { s.Action, s.Reason = Skip, InFlight })},
			want: []Change{{Step: ref, Kind: NowInFlight, Message: "add-on vpc-cni is now changing elsewhere"}},
		},
		{
			name: "KubernetesVersion", kind: NodegroupScale, prev: []Step{roll},
			cur:    []Step{with(roll, func(s *Step) { s.To = "1.34" })},
			want:   []Change{{Step: roll.Ref, Kind: KubernetesVersion, Message: "nodegroup web now goes to 1.34, not 1.33"}},
			effect: Refuse,
		},
		{
			name: "Release", kind: NodegroupScale, prev: []Step{roll},
			cur:    []Step{with(roll, func(s *Step) { s.ToRelease = "1.33.0-20260201" })},
			want:   []Change{{Step: roll.Ref, Kind: Release, Message: "nodegroup web now gets the AMI release 1.33.0-20260201, not 1.33.0-20260101"}},
			effect: AskAgain,
		},
		{
			name: "Version", kind: AddonUpdate, prev: []Step{submit},
			cur:    []Step{with(submit, func(s *Step) { s.To = "v3" })},
			want:   []Change{{Step: ref, Kind: Version, Message: "add-on vpc-cni now goes to v3, not v2"}},
			effect: AskAgain,
		},
		{
			name: "Sizes", kind: NodegroupScale, prev: []Step{scale},
			cur: []Step{with(scale, func(s *Step) {
				s.Sizes = &ScaleSizes{Before: Sizes{Desired: 5, Min: 1, Max: 5}, After: Sizes{Desired: 3, Min: 1, Max: 5}}
			})},
			want: []Change{
				{Step: scale.Ref, Kind: SizesChanged, Message: "nodegroup web now has desired 5, min 1, max 5"},
				{Step: scale.Ref, Kind: SizesChanged, Message: "nodegroup web now goes to desired 3, min 1, max 5, not desired 3, min 1, max 3"},
			},
			effect: AskAgain,
		},
		{
			name: "Added: a new step", kind: AddonUpdate, prev: []Step{submit},
			cur:    []Step{submit, addonStep("coredns", Submit, VersionBehind, "v1", "v2")},
			want:   []Change{{Step: StepRef{Phase: addonPhase, Kind: StepAddon, Name: "coredns"}, Kind: Added, Message: "add-on coredns is now part of the plan"}},
			effect: AskAgain,
		},
		{
			name: "Added: a skipped step has work now", kind: AddonUpdate,
			prev:   []Step{with(submit, func(s *Step) { s.Action, s.Reason = Skip, AlreadyAtTarget })},
			cur:    []Step{submit},
			want:   []Change{{Step: ref, Kind: Added, Message: "add-on vpc-cni is now part of the plan"}},
			effect: AskAgain,
		},
		{
			name: "Removed: the step is gone", kind: AddonUpdate,
			prev:   []Step{submit, addonStep("coredns", Submit, VersionBehind, "v1", "v2")},
			cur:    []Step{submit},
			want:   []Change{{Step: StepRef{Phase: addonPhase, Kind: StepAddon, Name: "coredns"}, Kind: Removed, Message: "add-on coredns is no longer part of the plan"}},
			effect: AskAgain,
		},
		{
			name: "Removed: the step is left to the operator", kind: AddonUpdate, prev: []Step{submit},
			cur:    []Step{with(submit, func(s *Step) { s.Action, s.Reason = Manual, "" })},
			want:   []Change{{Step: ref, Kind: Removed, Message: "add-on vpc-cni is no longer part of the plan"}},
			effect: AskAgain,
		},
		{
			name: "NowBlocked: the step", kind: AddonUpdate, prev: []Step{submit},
			cur:    []Step{with(submit, func(s *Step) { s.Action, s.Reason = Blocked, "" })},
			want:   []Change{{Step: ref, Kind: NowBlocked, Message: "add-on vpc-cni can no longer be changed"}},
			effect: Refuse,
		},
		{
			name: "NowBlocked: a new blocker", kind: AddonUpdate, prev: []Step{submit}, cur: []Step{submit},
			edit: func(prev, cur *ClusterPlan) {
				prev.Blockers = []Blocker{{Kind: BlockerUnread, Message: "old"}}
				cur.Blockers = []Blocker{{Kind: BlockerUnread, Message: "old"}, {Kind: BlockerBusy, Message: "prod is busy (cluster UPDATING)"}}
			},
			want:   []Change{{Kind: NowBlocked, Message: "prod is busy (cluster UPDATING)"}},
			effect: Refuse,
		},
		{
			name: "GateWorse", kind: AddonUpdate, prev: []Step{submit}, cur: []Step{submit},
			edit:   func(prev, cur *ClusterPlan) { prev.Checks, cur.Checks = pass, warn },
			want:   []Change{{Kind: GateWorse, Message: "Addons: coredns is DEGRADED"}},
			effect: AskAgain,
		},
		{
			name: "a gate that is no worse", kind: AddonUpdate, prev: []Step{submit}, cur: []Step{submit},
			edit: func(prev, cur *ClusterPlan) { prev.Checks, cur.Checks = warn, warn },
		},
		{
			name: "a refusal wins over a question", kind: NodegroupScale, prev: []Step{roll},
			cur: []Step{with(roll, func(s *Step) { s.To, s.ToRelease = "1.34", "1.34.0-20260201" })},
			want: []Change{
				{Step: roll.Ref, Kind: KubernetesVersion, Message: "nodegroup web now goes to 1.34, not 1.33"},
				{Step: roll.Ref, Kind: Release, Message: "nodegroup web now gets the AMI release 1.34.0-20260201, not 1.33.0-20260101"},
			},
			effect: Refuse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev, cur := planOf(tt.kind, tt.prev...), planOf(tt.kind, tt.cur...)
			if tt.edit != nil {
				tt.edit(&prev.Clusters[0], &cur.Clusters[0])
			}
			got := Diff(prev, cur)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Diff (-want +got):\n%s", diff)
			}
			if effect := worstEffect(got); effect != tt.effect {
				t.Errorf("effect = %d, want %d", effect, tt.effect)
			}
		})
	}
}

// Every kind of difference has the effect of the design note.
func TestChangeKindEffect(t *testing.T) {
	want := map[ChangeKind]Effect{
		Done: Continue, NowInFlight: Continue,
		KubernetesVersion: Refuse, NowBlocked: Refuse,
		Release: AskAgain, Version: AskAgain, SizesChanged: AskAgain, Added: AskAgain, Removed: AskAgain, GateWorse: AskAgain, StepDiffers: AskAgain,
	}
	for kind, effect := range want {
		if got := kind.Effect(); got != effect {
			t.Errorf("%s.Effect() = %d, want %d", kind, got, effect)
		}
	}
}
