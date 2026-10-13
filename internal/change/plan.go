package change

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// PhaseKind names a phase. The values are the ones of the documents.
type PhaseKind = apidoc.PhaseKind

// StepKind names what a step changes. The values are the ones of the
// documents.
type StepKind = apidoc.StepKind

// The phase kinds.
const (
	PhaseControlPlane = apidoc.PhaseControlPlane
	PhaseAddonsBefore = apidoc.PhaseAddonsBefore
	PhaseNodegroups   = apidoc.PhaseNodegroups
	PhaseAddonsAfter  = apidoc.PhaseAddonsAfter
	PhaseAddons       = apidoc.PhaseAddons
	PhaseRoll         = apidoc.PhaseRoll
	PhaseScale        = apidoc.PhaseScale
)

// The step kinds.
const (
	StepControlPlane = apidoc.StepControlPlane
	StepAddon        = apidoc.StepAddon
	StepNodegroup    = apidoc.StepNodegroup
	StepScale        = apidoc.StepScale
)

// PhaseRef identifies a phase in a run. Two clusters of a fleet have
// different refs for their Roll phase, because Target differs.
type PhaseRef struct {
	Target Target
	Kind   PhaseKind
	// Hop is the hop target of an upgrade phase, and zero otherwise.
	Hop fleet.Version
}

// StepRef identifies a step in a run.
type StepRef struct {
	Phase PhaseRef
	Kind  StepKind
	// Name is the nodegroup or the add-on. It is empty for the control
	// plane.
	Name string
}

// String returns the ref for a message, such as "prod/1.33/Nodegroups".
// No code parses it.
func (r PhaseRef) String() string {
	parts := []string{r.Target.Cluster}
	if !r.Hop.IsZero() {
		parts = append(parts, r.Hop.String())
	}
	return strings.Join(append(parts, string(r.Kind)), "/")
}

// String returns the ref for a message, such as "prod/Addons/vpc-cni".
// No code parses it.
func (r StepRef) String() string {
	if r.Name == "" {
		return r.Phase.String()
	}
	return r.Phase.String() + "/" + r.Name
}

// Plan is what a change would do, from one read of live state.
type Plan struct {
	Kind    Kind
	Request Request
	// Clusters has one plan, except in fleet mode.
	Clusters []ClusterPlan
	// SkippedRegions are the regions that a default sweep left out.
	SkippedRegions []string
	// Failures are the reads that failed and belong to no cluster.
	Failures []apidoc.Failure
	At       time.Time
}

// ClusterPlan is the plan for one cluster.
type ClusterPlan struct {
	Target
	// From and To are the Kubernetes versions of an upgrade or a
	// rollback, and zero otherwise.
	From, To fleet.Version
	Phases   []Phase
	// Gate is the verdict of the health gate, and Checks its results.
	// Gate.Decision is empty when no gate ran: the plan submits nothing.
	Gate     health.Verdict
	Checks   []health.Result
	Blockers []Blocker
	Notices  []string
	// Failures are the reads that failed, and one failure with the reason
	// Busy for each target that is already changing.
	Failures []apidoc.Failure
	// Rollback is the upgrade that a rollback undoes. It is set whenever
	// the upgrade was found, also when its window has closed.
	Rollback *fleet.Rollback

	// snapshot is the read that the plan was built on.
	snapshot *fleet.Snapshot
	// addonTargets has the version that each add-on step moves to, for
	// the fresh read before its submit.
	addonTargets map[string]addonTarget
}

// BlockerKind is why a plan cannot run. The values are the ones of the
// documents.
type BlockerKind = apidoc.BlockerKind

// The blocker kinds.
const (
	BlockerBusy        = apidoc.BlockerBusy
	BlockerNotReady    = apidoc.BlockerNotReady
	BlockerUnavailable = apidoc.BlockerUnavailable
	BlockerUnread      = apidoc.BlockerUnread
)

// Blocker is one reason a plan cannot run.
type Blocker struct {
	Kind BlockerKind
	// Step is nil when the blocker is about the cluster.
	Step    *StepRef
	Message string
}

// Phase is a group of steps. They run one after another, each with its
// own fresh read, wait, and confirmation, and a step that does not
// succeed does not stop the next one.
type Phase struct {
	Ref   PhaseRef
	Steps []Step
}

// Action is what a run does with a step. The values are the ones of the
// documents.
type Action = apidoc.StepAction

// The actions.
const (
	Submit  = apidoc.ActionSubmit
	Attach  = apidoc.ActionAttach
	Verify  = apidoc.ActionVerify
	Skip    = apidoc.ActionSkip
	Manual  = apidoc.ActionManual
	Blocked = apidoc.ActionBlocked
)

// Reason is why a step has its action. The values are the ones of the
// documents.
type Reason = apidoc.StepReason

// The reasons.
const (
	AMIOutdated     = apidoc.ReasonAMIOutdated
	AMIUnknown      = apidoc.ReasonAMIUnknown
	Reroll          = apidoc.ReasonReroll
	VersionBehind   = apidoc.ReasonVersionBehind
	PreRoll         = apidoc.ReasonPreRoll
	Repair          = apidoc.ReasonRepair
	Unsettled       = apidoc.ReasonUnsettled
	InFlight        = apidoc.ReasonInFlight
	AlreadyLatest   = apidoc.ReasonAlreadyLatest
	AlreadyAtTarget = apidoc.ReasonAlreadyAtTarget
	NewerThanLatest = apidoc.ReasonNewerThanLatest
	CustomAMI       = apidoc.ReasonCustomAMI
	SkippedByFlag   = apidoc.ReasonSkippedByFlag
	OutOfScope      = apidoc.ReasonOutOfScope
	NoChange        = apidoc.ReasonNoChange
)

// Sizes are the scaling sizes of a nodegroup. All three were read: a
// size that EKS did not return never becomes a 0 here.
type Sizes struct {
	Desired, Min, Max int32
}

// String returns the sizes for a message: "desired 3, min 1, max 5".
func (s Sizes) String() string {
	return fmt.Sprintf("desired %d, min %d, max %d", s.Desired, s.Min, s.Max)
}

// ScaleSizes are the sizes of a nodegroup before and after a scale.
type ScaleSizes struct {
	Before, After Sizes
}

// Step is one thing that a plan does, or leaves alone and says why.
type Step struct {
	Ref    StepRef
	Action Action
	Reason Reason
	// From and To are the pinned versions: the Kubernetes version for a
	// roll and the control plane, the add-on version for an add-on.
	From, To string
	// ToRelease is the AMI release that a roll expects.
	ToRelease string
	Sizes     *ScaleSizes
	// Force is sent with the request.
	Force bool
}

// work reports whether a run does something for the step: it submits,
// attaches, or verifies.
func (s Step) work() bool {
	return s.Action == Submit || s.Action == Attach || s.Action == Verify
}

// busy reports whether the step is a target that is already changing: the
// plan leaves it alone and lists it as a failure.
func (s Step) busy() bool {
	return s.Action == Skip && s.Reason == InFlight
}

// Steps returns the steps of every phase, in plan order.
func (cp *ClusterPlan) Steps() []Step {
	var out []Step
	for _, ph := range cp.Phases {
		out = append(out, ph.Steps...)
	}
	return out
}

// Decision is what a real run of a plan would do first. The values are
// the ones of the documents.
type Decision = apidoc.PlanDecision

// The decisions.
const (
	Proceed             = apidoc.PlanProceed
	Ask                 = apidoc.PlanAsk
	DecisionBlocked     = apidoc.PlanBlocked
	DecisionNothingToDo = apidoc.PlanNothingToDo
)

// Decision returns what a real run of the cluster plan would do first.
// The first row that matches decides:
//
//  1. A blocker, or a gate that blocks: Blocked.
//  2. No step that submits, attaches, or verifies: NothingToDo.
//  3. A gate that asks, or that went past reasons with --yes or --force:
//     Ask.
//  4. Otherwise: Proceed.
func (cp *ClusterPlan) Decision() Decision {
	switch {
	case len(cp.Blockers) > 0 || cp.Gate.Decision == health.Block:
		return DecisionBlocked
	case !slices.ContainsFunc(cp.Steps(), Step.work):
		return DecisionNothingToDo
	case cp.Gate.Decision == health.Ask || len(cp.Gate.Accepted) > 0 || len(cp.Gate.Forced) > 0:
		return Ask
	}
	return Proceed
}

// ExitCode returns the exit code of a dry run of the cluster plan. --yes
// does not change it: the gate's code comes from the findings alone.
//
// A plan with a failure exits 4, whatever its decision. Otherwise a
// blocked plan exits 3, or with the code of the gate when the gate blocks
// over warnings alone (2, with --require-healthy); a plan that asks exits
// with the code of the gate; and the other two exit 0.
func (cp *ClusterPlan) ExitCode() int {
	if len(cp.Failures) > 0 || len(cp.Gate.Failures) > 0 {
		return exitcode.Incomplete
	}
	switch cp.Decision() {
	case DecisionBlocked:
		if len(cp.Blockers) > 0 || len(cp.Gate.Blockers) > 0 {
			return exitcode.Blocked
		}
		return cp.Gate.Exit
	case Ask:
		return cp.Gate.Exit
	}
	return exitcode.OK
}

// decisionRank orders the decisions for a plan of several clusters.
func decisionRank(d Decision) int {
	switch d {
	case DecisionBlocked:
		return 3
	case Ask:
		return 2
	case Proceed:
		return 1
	}
	return 0
}

// Decision returns the decision of the plan: the one of its cluster, and
// in fleet mode the worst of them, in the order Blocked, Ask, Proceed,
// NothingToDo.
func (p *Plan) Decision() Decision {
	worst := DecisionNothingToDo
	for i := range p.Clusters {
		if d := p.Clusters[i].Decision(); decisionRank(d) > decisionRank(worst) {
			worst = d
		}
	}
	return worst
}

// ExitCode returns the exit code of a dry run: the highest code of the
// clusters, and 4 when a read that belongs to no cluster failed.
func (p *Plan) ExitCode() int {
	code := exitcode.OK
	if len(p.Failures) > 0 {
		code = exitcode.Incomplete
	}
	for i := range p.Clusters {
		code = max(code, p.Clusters[i].ExitCode())
	}
	return code
}

// allFailures returns the failures of the plan, of its clusters, and of
// their gates.
func (p *Plan) allFailures() []apidoc.Failure {
	out := slices.Clone(p.Failures)
	for i := range p.Clusters {
		out = append(out, p.Clusters[i].Failures...)
		out = append(out, p.Clusters[i].Gate.Failures...)
	}
	return out
}
