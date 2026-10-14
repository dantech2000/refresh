package apidoc

import "time"

// Operation is the change that a plan or a run is about.
type Operation string

// The operations.
const (
	OperationAddonUpdate     Operation = "AddonUpdate"
	OperationNodegroupScale  Operation = "NodegroupScale"
	OperationNodegroupUpdate Operation = "NodegroupUpdate"
	OperationClusterUpgrade  Operation = "ClusterUpgrade"
	OperationClusterRollback Operation = "ClusterRollback"
)

// Values implements Enum.
func (Operation) Values() []string {
	return []string{
		string(OperationAddonUpdate), string(OperationNodegroupScale), string(OperationNodegroupUpdate),
		string(OperationClusterUpgrade), string(OperationClusterRollback),
	}
}

// PlanDecision is what a real run of a plan would do first.
type PlanDecision string

// The plan decisions.
const (
	// PlanProceed: the run would start.
	PlanProceed PlanDecision = "Proceed"
	// PlanAsk: the run would ask about health warnings, or went past them
	// with --yes or --force.
	PlanAsk PlanDecision = "Ask"
	// PlanBlocked: a blocker or the health gate stops the run.
	PlanBlocked PlanDecision = "Blocked"
	// PlanNothingToDo: no step would change, attach to, or verify anything.
	PlanNothingToDo PlanDecision = "NothingToDo"
)

// Values implements Enum.
func (PlanDecision) Values() []string {
	return []string{string(PlanProceed), string(PlanAsk), string(PlanBlocked), string(PlanNothingToDo)}
}

// PhaseKind names a phase of a change.
type PhaseKind string

// The phase kinds.
const (
	PhaseControlPlane PhaseKind = "ControlPlane"
	PhaseAddonsBefore PhaseKind = "AddonsBefore"
	PhaseNodegroups   PhaseKind = "Nodegroups"
	PhaseAddonsAfter  PhaseKind = "AddonsAfter"
	PhaseAddons       PhaseKind = "Addons"
	PhaseRoll         PhaseKind = "Roll"
	PhaseScale        PhaseKind = "Scale"
)

// Values implements Enum.
func (PhaseKind) Values() []string {
	return []string{
		string(PhaseControlPlane), string(PhaseAddonsBefore), string(PhaseNodegroups), string(PhaseAddonsAfter),
		string(PhaseAddons), string(PhaseRoll), string(PhaseScale),
	}
}

// StepKind names what a step changes.
type StepKind string

// The step kinds.
const (
	StepControlPlane StepKind = "ControlPlane"
	StepAddon        StepKind = "Addon"
	StepNodegroup    StepKind = "Nodegroup"
	StepScale        StepKind = "Scale"
)

// Values implements Enum.
func (StepKind) Values() []string {
	return []string{string(StepControlPlane), string(StepAddon), string(StepNodegroup), string(StepScale)}
}

// StepAction is what a run does with a step.
type StepAction string

// The step actions.
const (
	// ActionSubmit: send an update.
	ActionSubmit StepAction = "Submit"
	// ActionAttach: the target is already changing towards the goal; wait
	// for it.
	ActionAttach StepAction = "Attach"
	// ActionVerify: at the goal but not settled; wait if needed, then
	// verify.
	ActionVerify StepAction = "Verify"
	// ActionSkip: nothing to do.
	ActionSkip StepAction = "Skip"
	// ActionManual: left to the operator.
	ActionManual StepAction = "Manual"
	// ActionBlocked: the step cannot run; a blocker of the plan says why.
	ActionBlocked StepAction = "Blocked"
)

// Values implements Enum.
func (StepAction) Values() []string {
	return []string{
		string(ActionSubmit), string(ActionAttach), string(ActionVerify),
		string(ActionSkip), string(ActionManual), string(ActionBlocked),
	}
}

// StepReason is why a step has its action.
type StepReason string

// The step reasons.
const (
	ReasonAMIOutdated     StepReason = "AMIOutdated"
	ReasonAMIUnknown      StepReason = "AMIUnknown"
	ReasonReroll          StepReason = "Reroll"
	ReasonVersionBehind   StepReason = "VersionBehind"
	ReasonPreRoll         StepReason = "PreRoll"
	ReasonRepair          StepReason = "Repair"
	ReasonUnsettled       StepReason = "Unsettled"
	ReasonInFlight        StepReason = "InFlight"
	ReasonAlreadyLatest   StepReason = "AlreadyLatest"
	ReasonAlreadyAtTarget StepReason = "AlreadyAtTarget"
	ReasonNewerThanLatest StepReason = "NewerThanLatest"
	ReasonCustomAMI       StepReason = "CustomAMI"
	ReasonSkippedByFlag   StepReason = "SkippedByFlag"
	ReasonOutOfScope      StepReason = "OutOfScope"
	ReasonNoChange        StepReason = "NoChange"
	// ReasonVersionAhead: the resource runs a version above the one that a
	// rollback returns to, or one that the older version cannot run.
	ReasonVersionAhead StepReason = "VersionAhead"
)

// Values implements Enum.
func (StepReason) Values() []string {
	return []string{
		string(ReasonAMIOutdated), string(ReasonAMIUnknown), string(ReasonReroll), string(ReasonVersionBehind),
		string(ReasonPreRoll), string(ReasonRepair), string(ReasonUnsettled), string(ReasonInFlight),
		string(ReasonAlreadyLatest), string(ReasonAlreadyAtTarget), string(ReasonNewerThanLatest),
		string(ReasonCustomAMI), string(ReasonSkippedByFlag), string(ReasonOutOfScope), string(ReasonNoChange),
		string(ReasonVersionAhead),
	}
}

// BlockerKind is why a plan cannot run.
type BlockerKind string

// The blocker kinds.
const (
	// BlockerBusy: something in the cluster is already changing.
	BlockerBusy BlockerKind = "Busy"
	// BlockerNotReady: a readiness rule of the change does not hold.
	BlockerNotReady BlockerKind = "NotReady"
	// BlockerUnavailable: the change is not offered, such as a rollback
	// after its window.
	BlockerUnavailable BlockerKind = "Unavailable"
	// BlockerUnread: something the plan needs could not be read.
	BlockerUnread BlockerKind = "Unread"
)

// Values implements Enum.
func (BlockerKind) Values() []string {
	return []string{string(BlockerBusy), string(BlockerNotReady), string(BlockerUnavailable), string(BlockerUnread)}
}

// Plan is the document of every --dry-run, and of a run that was refused
// before it submitted anything.
type Plan struct {
	Operation Operation     `json:"operation"`
	Clusters  []ClusterPlan `json:"clusters"`
	// SkippedRegions are the regions that a default sweep left out.
	SkippedRegions []string `json:"skippedRegions,omitzero"`
	Failures       Failures `json:"failures"`
}

// Kind implements Document.
func (Plan) Kind() Kind { return KindPlan }

// ClusterPlan is the plan for one cluster.
type ClusterPlan struct {
	Cluster string `json:"cluster"`
	Region  string `json:"region"`
	// Version is the Kubernetes version of the control plane, and
	// TargetVersion the version that an upgrade or a rollback moves to.
	Version       string       `json:"version,omitzero"`
	TargetVersion string       `json:"targetVersion,omitzero"`
	Decision      PlanDecision `json:"decision"`
	Steps         []PlanStep   `json:"steps"`
	// Gate is the verdict of the health gate. It is left out when no gate
	// ran: nothing would be submitted, or --skip-health-check.
	Gate     *Gate     `json:"gate,omitzero"`
	Blockers []Blocker `json:"blockers"`
	Notices  []string  `json:"notices"`
	// Rollback is the upgrade that a rollback undoes.
	Rollback *Rollback `json:"rollback,omitzero"`
}

// Blocker is one reason a plan cannot run.
type Blocker struct {
	Kind BlockerKind `json:"kind"`
	// Step is the step that the blocker is about. It is left out when the
	// blocker is about the cluster.
	Step    *StepID `json:"step,omitzero"`
	Message string  `json:"message"`
}

// StepID identifies a step within its cluster: phase, hop, kind, and name
// together. No field is a composite ID to parse.
type StepID struct {
	Phase PhaseKind `json:"phase"`
	// Hop is the Kubernetes version that the phase of an upgrade moves to.
	Hop  string   `json:"hop,omitzero"`
	Kind StepKind `json:"kind"`
	// Name is the nodegroup or the add-on. It is left out for the control
	// plane.
	Name string `json:"name,omitzero"`
}

// PlanStep is one step of a plan.
type PlanStep struct {
	Phase PhaseKind `json:"phase"`
	// Hop is the Kubernetes version that the phase of an upgrade moves to.
	Hop  string   `json:"hop,omitzero"`
	Kind StepKind `json:"kind"`
	// Name is the nodegroup or the add-on. It is left out for the control
	// plane.
	Name   string     `json:"name,omitzero"`
	Action StepAction `json:"action"`
	Reason StepReason `json:"reason,omitzero"`
	// From and To are the versions: the Kubernetes version for a roll and
	// the control plane, the add-on version for an add-on.
	From string `json:"from,omitzero"`
	To   string `json:"to,omitzero"`
	// FromRelease is the AMI release version that the nodegroup of a roll
	// ran when the plan read it, such as 1.32.0-20250101. A run does not
	// roll a nodegroup that is on another release by the time of its
	// request: it was rolled elsewhere.
	FromRelease string `json:"fromRelease,omitzero"`
	// FromImage is the ID of the AMI that the nodes of a roll ran when the
	// plan read them, such as ami-0abc. It is an image ID, and not a
	// release version.
	FromImage string `json:"fromImage,omitzero"`
	// ToImage is the ID of the latest recommended AMI, which a roll moves
	// the nodegroup to. It is an image ID, and not a release version.
	ToImage string `json:"toImage,omitzero"`
	// Force is true when the request carries the force flag.
	Force bool        `json:"force,omitzero"`
	Sizes *ScaleSizes `json:"sizes,omitzero"`
	// InFlight is the type of the update that makes a target busy: a step
	// with the action Skip and the reason InFlight. It is VersionUpdate,
	// ConfigUpdate, AddonUpdate, or another type of EKS, and it is left out
	// when the update was not found.
	InFlight string `json:"inFlight,omitzero"`
}

// ScaleSizes are the sizes of a nodegroup before and after a scale.
type ScaleSizes struct {
	Before Sizes `json:"before"`
	After  Sizes `json:"after"`
}

// Sizes are the scaling sizes of a nodegroup.
type Sizes struct {
	Desired int32 `json:"desired"`
	Min     int32 `json:"min"`
	Max     int32 `json:"max"`
}

// RunStatus is how a run, or the part of it for one cluster, ended.
type RunStatus string

// The run statuses.
const (
	RunSucceeded   RunStatus = "Succeeded"
	RunNothingToDo RunStatus = "NothingToDo"
	// RunStarted: every change was accepted and --no-wait did not wait.
	RunStarted RunStatus = "Started"
	// RunRefused: nothing was submitted; the plan says why.
	RunRefused RunStatus = "Refused"
	// RunDeclined: a question was answered no.
	RunDeclined RunStatus = "Declined"
	// RunStopped: the run was stopped between two phases.
	RunStopped RunStatus = "Stopped"
	// RunBlocked: a later phase was refused.
	RunBlocked RunStatus = "Blocked"
	RunFailed  RunStatus = "Failed"
	// RunVerifyFailed: the change was applied and the check after it
	// found issues.
	RunVerifyFailed RunStatus = "VerifyFailed"
	// RunIncomplete: something is unread, not followed, busy, or timed
	// out.
	RunIncomplete  RunStatus = "Incomplete"
	RunInterrupted RunStatus = "Interrupted"
)

// Values implements Enum.
func (RunStatus) Values() []string {
	return []string{
		string(RunSucceeded), string(RunNothingToDo), string(RunStarted), string(RunRefused),
		string(RunDeclined), string(RunStopped), string(RunBlocked), string(RunFailed),
		string(RunVerifyFailed), string(RunIncomplete), string(RunInterrupted),
	}
}

// Run is the document of every change that got past its plan.
type Run struct {
	Operation Operation    `json:"operation"`
	Status    RunStatus    `json:"status"`
	Clusters  []ClusterRun `json:"clusters"`
	Failures  Failures     `json:"failures"`
}

// Kind implements Document.
func (Run) Kind() Kind { return KindRun }

// ClusterRun is the plan for one cluster and what the run did with it.
type ClusterRun struct {
	ClusterPlan
	Status RunStatus `json:"status"`
	// Results has one entry for each step of the plan, in plan order.
	Results []StepResult `json:"results"`
	// Verification are the checks that ran after the change.
	Verification []HealthResult `json:"verification"`
}

// StepStatus is what a run did with one step.
type StepStatus string

// The step statuses.
const (
	// StepNotAttempted: the run ended before it reached the step.
	StepNotAttempted StepStatus = "NotAttempted"
	// StepStarted: the update was accepted and not waited for.
	StepStarted   StepStatus = "Started"
	StepSucceeded StepStatus = "Succeeded"
	StepFailed    StepStatus = "Failed"
	// StepCancelled: the EKS update ended Cancelled.
	StepCancelled StepStatus = "Cancelled"
	// StepInProgress: the change may still be running: the wait timed
	// out, was interrupted, or could not follow it.
	StepInProgress StepStatus = "InProgress"
	// StepBusy: the target was already changing, and was left alone.
	StepBusy    StepStatus = "Busy"
	StepSkipped StepStatus = "Skipped"
	// StepBlocked: the busy rule, the gate, or a blocker stopped the step.
	StepBlocked StepStatus = "Blocked"
)

// Values implements Enum.
func (StepStatus) Values() []string {
	return []string{
		string(StepNotAttempted), string(StepStarted), string(StepSucceeded), string(StepFailed),
		string(StepCancelled), string(StepInProgress), string(StepBusy), string(StepSkipped), string(StepBlocked),
	}
}

// StepResult is what a run did with one step of the plan.
type StepResult struct {
	Phase PhaseKind `json:"phase"`
	// Hop is the Kubernetes version that the phase of an upgrade moves to.
	Hop  string   `json:"hop,omitzero"`
	Kind StepKind `json:"kind"`
	// Name is the nodegroup or the add-on. It is left out for the control
	// plane.
	Name   string     `json:"name,omitzero"`
	Status StepStatus `json:"status"`
	// Attached is true when the run waited for a change that it did not
	// start.
	Attached bool   `json:"attached,omitzero"`
	UpdateID string `json:"updateId,omitzero"`
	// InFlight is the type of the update that made a Busy step busy:
	// VersionUpdate, ConfigUpdate, or AddonUpdate. It is left out when the
	// update was not found.
	InFlight   string    `json:"inFlight,omitzero"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	// Failure is why the step did not succeed. It is also in the failures
	// list of the document.
	Failure *Failure `json:"failure,omitzero"`
	// Note says what the run found right before its request, when it then
	// did something else than the step of the plan says: a nodegroup that
	// was rolled elsewhere since the plan is Skipped, and its step of the
	// plan still says Submit. The plan in a Run document is the plan that
	// was confirmed.
	Note string `json:"note,omitzero"`
}
