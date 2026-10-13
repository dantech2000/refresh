# Phase 4 design note: changes

Status: draft 2. Draft 1 was reviewed twice against the old code and the parity traces: one reject with five blockers, one approve with changes and three blockers. This draft applies the findings and the owner's decisions on draft 1 (section 13). It belongs to `REWRITE.md` and stays inside `CONTRACT.md`. Phase 3 (`docs/design/phase-3-health.md`) is the source for the gate, the verdict, and the verification checks. Section 14 is empty until code changes the design.

Scope: `internal/change` (plans, waits, the batch, add-on update, scale, the upgrade engine, rollback, the busy rule), `internal/noderoll` and the roll panel in `internal/view`, the `Plan` and `Run` documents, and the commands `addon update`, `nodegroup scale`, `nodegroup update` (with `--all-clusters`), `cluster upgrade`, and `cluster rollback`.

Out of scope: the TUI and everything only the TUI needs. The executor is synchronous. The handle with an event log, question IDs, pause and stop intent, claims, and the watching of a change started elsewhere is phase 5, built as an adapter over the hooks of section 7.

A claim about the old binary names a file and line, or a trace in `test/parity/testdata/legacy`. The new binary does not exist yet, so a statement about a v2 trace is a prediction.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | One function builds a plan. `NewPlan` returns it for a dry run, and `Run` calls the same function. There is no dry-run code path. |
| 2 | `Run(ctx, deps, request, hooks)` executes on the caller's goroutine and returns a `Result`. It starts no goroutine of its own beyond the polls of one wait and the best-effort observers, which it joins. |
| 3 | The hooks are three functions: an event, a question, a checkpoint. The CLI implements them in a few lines. |
| 4 | Every change is a list of phases. A phase runs its steps as one batch (`nodegroup update`) or one after another (add-ons, the nodegroups of an upgrade). |
| 5 | Two waits. `WaitUpdate` polls `DescribeUpdate` only. `WaitSettled` polls the resource and returns what it settled at; the caller decides what that means. |
| 6 | The steps are derived first. The busy rule then treats every step of the plan as "ours". |
| 7 | After every question and every pause the run reads the cluster again and derives the plan again before it submits anything. |
| 8 | A step has one code. Among independent targets, a failed step beside a step that changed something is a partial failure, exit 4. The highest code wins, and an interrupt is always 1. |
| 9 | A wait that outlasts `--wait-timeout`, and a change that was accepted and could not be followed, exit 4. |
| 10 | The node observer and the Auto Scaling watch report through the event hook. Nothing below the command layer writes to the terminal. |
| 11 | `StartNodegroupRoll`, the `DecideAMIUpdate` table less its `--force` row, `noderoll.Tracker`, the upgrade planner, and the rollback planner are ported with their behaviour and tests frozen. |

## 1. Plans

### Requests

```go
package change

type Kind string

const (
	AddonUpdate     Kind = "AddonUpdate"
	NodegroupScale  Kind = "NodegroupScale"
	NodegroupUpdate Kind = "NodegroupUpdate"
	ClusterUpgrade  Kind = "ClusterUpgrade"
	ClusterRollback Kind = "ClusterRollback"
)

// Request is one of the five request types below.
type Request interface{ kind() Kind }

// Target names one cluster.
type Target struct {
	Region  string
	Cluster string
}

// Common holds what every request carries.
type Common struct {
	Policy     health.Policy // --yes, --require-healthy, --force, --skip-health-check
	NoWait     bool
	SkipVerify bool
	Limits     Limits // section 8
}

type AddonUpdateRequest struct {
	Common
	Target
	Addons  []string // exact names; empty with All
	All     bool
	Version string   // --version; only with exactly one add-on
	Skip    []string // --skip-addon; only with All
}

type ScaleRequest struct {
	Common
	Target
	Nodegroup         string
	Desired, Min, Max *int32 // nil when the flag was not given
}

type RollRequest struct {
	Common
	Target
	Nodegroups []string       // exact names; empty means every nodegroup
	Filters    []fleet.Filter // -f
	Reroll     bool
	Fleet      *FleetScope // --all-clusters; Target is then empty
}

type FleetScope struct {
	Regions []string // -r; empty means the default sweep
}

type UpgradeRequest struct {
	Common
	Target
	To             fleet.Version // --to, required
	Only           []Part        // --only: ControlPlane, Addons, Nodegroups
	Nodegroups     []string      // -n
	SkipAddons     []string
	SkipNodegroups []string
	SkipInsights   bool
	ConfirmPhases  bool // ask before each phase; the CLI sets it without --yes
}

type RollbackRequest struct {
	Common
	Target
	SkipNodegroups  []string
	SkipInsights    bool
	RollbackTimeout time.Duration // 0 leaves the EKS default
}
```

### The plan

Identity is typed. A string form exists for display and for the documents, and no code parses it.

```go
type PhaseKind string // ControlPlane, AddonsBefore, Nodegroups, AddonsAfter, Addons, Roll, Scale

// PhaseRef identifies a phase in a run. Two clusters of a fleet have
// different refs for their Roll phase, because Target differs.
type PhaseRef struct {
	Target Target
	Kind   PhaseKind
	Hop    fleet.Version // the hop target of an upgrade phase; zero otherwise
}

type StepKind string // ControlPlane, Addon, Nodegroup, Scale

// StepRef identifies a step in a run.
type StepRef struct {
	Phase PhaseRef
	Kind  StepKind
	Name  string // nodegroup or add-on; empty for the control plane
}

func (r PhaseRef) String() string // "prod/1.33/nodegroups"
func (r StepRef) String() string  // "prod/1.33/nodegroups/ng-a"

type Plan struct {
	Kind           Kind
	Request        Request
	Clusters       []ClusterPlan // one, except in fleet mode
	SkippedRegions []string      // a default sweep left these out
	Failures       []apidoc.Failure
	At             time.Time
}

type ClusterPlan struct {
	Target
	From, To fleet.Version // upgrade and rollback only
	Phases   []Phase
	Gate     health.Verdict
	Checks   []health.Result
	Blockers []Blocker
	Notices  []string
	Failures []apidoc.Failure
	// Rollback is the upgrade this rollback undoes: UpgradedAt and
	// AvailableUntil. It is set whenever the upgrade was found, also when
	// the window has closed.
	Rollback *fleet.Rollback

	snapshot *fleet.Snapshot
}

type BlockerKind string // Busy, NotReady, Unavailable, Unread

type Blocker struct {
	Kind    BlockerKind
	Step    *StepRef // nil when the blocker is about the cluster
	Message string
}

type Phase struct {
	Ref PhaseRef
	// Serial runs the steps one after another, each with its own recheck,
	// gate, wait, and verification. Otherwise the phase is one batch.
	Serial bool
	// StopOnFailure ends the phase and the run at the first step that does
	// not succeed. The engines set it. addon update --all does not.
	StopOnFailure bool
	Steps         []Step
}

type Action string

const (
	Submit  Action = "Submit"  // send an update
	Attach  Action = "Attach"  // the target is already changing towards the goal; wait for it
	Verify  Action = "Verify"  // at the goal but not settled or not ACTIVE: wait if needed, then verify
	Skip    Action = "Skip"    // nothing to do
	Manual  Action = "Manual"  // left to the operator
	Blocked Action = "Blocked" // cannot run; the plan has a Blocker
)

type Reason string

const (
	AMIOutdated     Reason = "AMIOutdated"
	AMIUnknown      Reason = "AMIUnknown"
	Reroll          Reason = "Reroll"
	VersionBehind   Reason = "VersionBehind"
	PreRoll         Reason = "PreRoll" // kubelet skew catch-up
	Repair          Reason = "Repair"  // not healthy at the target version
	Unsettled       Reason = "Unsettled"
	InFlight        Reason = "InFlight"
	AlreadyLatest   Reason = "AlreadyLatest"
	AlreadyAtTarget Reason = "AlreadyAtTarget"
	NewerThanLatest Reason = "NewerThanLatest"
	CustomAMI       Reason = "CustomAMI"
	SkippedByFlag   Reason = "SkippedByFlag"
	OutOfScope      Reason = "OutOfScope"
	NoChange        Reason = "NoChange"
)

type Step struct {
	Ref    StepRef
	Action Action
	Reason Reason
	// From and To are the pinned versions: the Kubernetes version for a
	// roll and the control plane, the add-on version for an add-on.
	// ToRelease is the AMI release a roll expects.
	From, To  string
	ToRelease string
	Sizes     *ScaleSizes // Before, After, and the fields the request sends
	// Force is sent to UpdateNodegroupVersion (--force) or to
	// UpdateClusterVersion (a rollback with --skip-insights-check).
	Force bool
}
```

`Plan.Decision()`:

| Condition, first match | Decision | Exit of a dry run |
|---|---|---|
| A `Blocker`, or the gate decides `Block` | `Blocked` | 4 when the plan or the gate has a failure, else 3, else `Gate.Exit` for `--require-healthy` over warnings (2) |
| No step with `Submit`, `Attach`, or `Verify` | `NothingToDo` | 4 with a failure, else 0 |
| The gate decides `Ask`, or went past reasons with `--yes` or `--force` | `Ask` | `Gate.Exit`: 4 or 2 |
| Otherwise | `Proceed` | 4 with a failure, else 0 |

The code ignores `--yes`, as phase 3 requires.

### One function

```go
// Deps is what the package needs from the command.
type Deps struct {
	AWS   *awsx.Provider
	Fetch *fleet.Fetcher
	Kube  func(ctx context.Context, t kube.Target) (*kube.Client, error)
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// NewPlan reads live state and returns the plan for req. It sends no
// mutating request.
func NewPlan(ctx context.Context, d Deps, req Request) (*Plan, error)
```

`NewPlan` and `Run` call the same unexported `plan(ctx, d, req, refreshInsights)`. A real `cluster upgrade` starts an insights refresh and a dry run does not (`insights.go:220-259`). That is the only difference.

`plan` for one cluster, in order:

1. **Read.** One snapshot with `Need{Nodegroups: true, Addons: true}`. A roll adds `AMI` and `InstanceIDs`. An add-on update adds the latest version of its targets. An upgrade adds `InstanceIDs`, `Support`, and the add-on catalogues of the live version and of each hop. A rollback adds `InstanceIDs`, `Updates`, and the catalogue of the target.
2. **Eligibility.** The rollback window, the scale bounds, the version catalogue of `--version`.
3. **Steps.** Sections 4 to 6.
4. **Busy rule**, with every `Submit`, `Attach`, and `Verify` step of the plan as "ours" (section 3).
5. **Gate**, with the check set of the kind and the targets of the `Submit` steps. A plan with no such step runs no gate.
6. Beside the gate: instance-type availability for each roll or scale-up target (`availability.go:36-115`). A problem is a notice; a failed read is dropped.

### When the plan is computed again

Time passes at a prompt and at a pause. The old code re-read after both: the scale re-checked its bounds and its direction after the prompt (`nodegroup_scale.go:81,419`), and the rollback rebuilt its plan before it started (`rollback.go:653`). The rule here is one sentence: **after every question that a person answered, and after every checkpoint that waited, `Run` calls `plan` again and compares before it submits anything.**

A run with `--yes` asks nothing, so it computes the plan once, immediately before its first submit, and has nothing older to compare. A later phase of an upgrade or a rollback always re-reads (section 4), whatever the flags.

```go
// Diff compares the plan a person saw with the plan computed now.
func Diff(prev, cur *Plan) []Change

type Change struct {
	Step    StepRef
	Kind    ChangeKind
	Message string
}
```

| `ChangeKind` | What changed | Effect |
|---|---|---|
| `Done` | A `Submit`, `Attach`, or `Verify` step is now `Skip` | Go on, with a notice |
| `NowInFlight` | A `Submit` step is now `Attach` | Go on, with a notice |
| `KubernetesVersion` | A roll step's `To` differs | Refuse, exit 3 |
| `RollbackTarget` | The rollback's `To` differs | Refuse, exit 3. No answer and no `--yes` accepts it. |
| `Release` | A roll step's `ToRelease` differs: AWS published a new AMI | Ask |
| `Version` | An add-on step's `To` differs, or the upgrade's next hop differs | Ask |
| `Sizes` | A scale step's `Before` differs | Ask, after the gate of the new direction has run |
| `Added`, `Removed` | A step with work appeared or went away without being done | Ask |
| `NowBlocked` | A new `Blocker`, or a step became `Blocked` | Refuse, exit 3 or 4 |
| `GateWorse` | `health.Worse(prev.Checks, cur.Checks)` is not empty | The pre-phase table of phase 3, section 4 |

The `KubernetesVersion` row is the rule of `REWRITE.md` and of the old TUI, which compared the version only (`tui/live/roll.go:387-392`). A refuse wins over an ask.

How an ask is answered: the question hook, or `--yes`, which accepts and records the changes as notices. After a person answers, the plan is computed once more. When three plans in a row each need a new question the run refuses: "the cluster keeps changing".

The scale case from the review, as a check of the rule: the plan says 2 to 3, a scale-up, and the gate ran `Quotas`. Someone raises the desired size to 5 while the prompt waits. The new plan says 5 to 3, a scale-down. Its gate runs `ScaleDownBlockers`, `Diff` reports `Sizes`, and the person is asked about the new plan or the gate blocks.

### Documents

```go
package apidoc

type Operation string    // AddonUpdate, NodegroupScale, NodegroupUpdate, ClusterUpgrade, ClusterRollback
type PlanDecision string // Proceed, Ask, Blocked, NothingToDo

type Plan struct {
	Operation      Operation     `json:"operation"`
	Clusters       []ClusterPlan `json:"clusters"`
	SkippedRegions []string      `json:"skippedRegions,omitzero"`
	Failures       Failures      `json:"failures"`
}

type ClusterPlan struct {
	Cluster       string       `json:"cluster"`
	Region        string       `json:"region"`
	Version       string       `json:"version,omitzero"`
	TargetVersion string       `json:"targetVersion,omitzero"`
	Decision      PlanDecision `json:"decision"`
	Steps         []PlanStep   `json:"steps"`
	Gate          *Gate        `json:"gate,omitzero"`
	Blockers      []Blocker    `json:"blockers"` // kind, step, message
	Notices       []string     `json:"notices"`
	Rollback      *Rollback    `json:"rollback,omitzero"`
}

type PlanStep struct {
	Phase     string      `json:"phase"`  // ControlPlane, AddonsBefore, ...
	Hop       string      `json:"hop,omitzero"`
	Kind      string      `json:"kind"`   // ControlPlane, Addon, Nodegroup, Scale
	Name      string      `json:"name,omitzero"`
	Action    string      `json:"action"` // Submit, Attach, Verify, Skip, Manual, Blocked
	Reason    string      `json:"reason,omitzero"`
	From      string      `json:"from,omitzero"`
	To        string      `json:"to,omitzero"`
	ToRelease string      `json:"toRelease,omitzero"`
	Force     bool        `json:"force,omitzero"`
	Sizes     *ScaleSizes `json:"sizes,omitzero"` // before, after: desired, min, max
}

type RunStatus string // section 7

type Run struct {
	Operation Operation    `json:"operation"`
	Status    RunStatus    `json:"status"`
	Clusters  []ClusterRun `json:"clusters"`
	Failures  Failures     `json:"failures"`
}

type ClusterRun struct {
	ClusterPlan
	Status       RunStatus      `json:"status"`
	Results      []StepResult   `json:"results"` // one per step, in plan order
	Verification []HealthResult `json:"verification"`
}

type StepStatus string // NotAttempted, Started, Succeeded, Failed, Cancelled, InProgress, Busy, Skipped, Blocked

type StepResult struct {
	Phase      string     `json:"phase"`
	Hop        string     `json:"hop,omitzero"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name,omitzero"`
	Status     StepStatus `json:"status"`
	Attached   bool       `json:"attached,omitzero"`
	UpdateID   string     `json:"updateId,omitzero"`
	// InFlight is the type of the update that made a Busy step busy:
	// VersionUpdate, ConfigUpdate, AddonUpdate, or "" when it was not found.
	InFlight   string    `json:"inFlight,omitzero"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Failure    *Failure  `json:"failure,omitzero"`
}
```

A step is identified in a document by `cluster`, `region`, `phase`, `hop`, `kind`, and `name` together. The documents do not embed the `Nodegroup` and `Addon` shapes; a reader who wants the resource reads it with the describe command. `CONTRACT.md` lists both shapes under "plans and runs", and contract edit 10 removes that.

Rules: a dry run prints `Plan`. A run that was refused before any update was submitted prints `Plan` with the blockers. Every other run prints `Run`. A step that was never reached is `NotAttempted`. There is no "resume" field: running the same command again derives the plan from live state, and the summary line says so. The human view of both documents is a `view.Description`, so `-o plain` is `FIELD`/`VALUE` rows from the same sections.

## 2. Waits

Both functions take `awsx.PollClients`, so each call is one attempt and the loop owns the retry. Neither prints.

```go
type WaitOptions struct {
	Interval time.Duration                        // --poll-interval
	After    func(time.Duration) <-chan time.Time // Deps.After
	Progress func(UpdateProgress)                 // may be nil
}

type Resource struct {
	Target
	Kind StepKind
	Name string
}

type UpdateProgress struct {
	Resource  Resource
	UpdateID  string
	Status    string
	PollError string // the last transient poll error, or ""
}

// WaitUpdate polls DescribeUpdate for one update until it ends.
func WaitUpdate(ctx context.Context, c awsx.PollClients, r Resource, updateID string, o WaitOptions) error

// Settled is the state a resource stopped changing in.
type Settled struct {
	Status  string
	Version string // the Kubernetes version, or the add-on version
	Sizes   Sizes  // nodegroups only
}

// WaitSettled polls the resource until done reports true. done sees every
// answer. It returns an error to end the wait as failed.
func WaitSettled(ctx context.Context, c awsx.PollClients, r Resource, done func(Settled) (bool, error), o WaitOptions) (Settled, error)
```

### Rules for both

| Point | Rule |
|---|---|
| First poll | After one `Interval`, as the old monitor (`monitor.go:78-96`). A fresh update ID can answer not-found for a moment. |
| `ResourceNotFoundException` from `DescribeUpdate` before the update was seen once | Transient |
| A poll error that `awsx.IsPermanentAPIError` reports | Stop with `*UnobservedError`. The change may still be running. |
| Any other poll error, or a response with no update or resource in it (`monitor.go:273-276`) | Keep polling; report it through `Progress` |
| The context ends | Return `ctx.Err()` wrapped with the last status. The caller tells a deadline from an interrupt (section 8). |
| Reads | `WaitUpdate` calls `DescribeUpdate` only. `WaitSettled` calls the describe of its resource only. |

### `WaitUpdate`

| Update status | Result |
|---|---|
| `InProgress`, or a status this build does not know | Keep polling |
| `Successful` | `nil` |
| `Failed`, `Cancelled` | `*UpdateError{Status, Errors}` with the codes, messages, and resource IDs |

### `WaitSettled` and its callers

`WaitSettled` has no table of its own. It keeps polling while the status is one that changes (`UPDATING`, `CREATING`, and `PENDING` for a cluster) and hands every other answer to `done`. The old nodegroup wait did exactly that and returned the version (`nodegroups.go:443-470`): `DELETING` and `DEGRADED` end it like `ACTIVE`, and the caller judges. The callers:

| Caller | Wants | `done` says |
|---|---|---|
| Control plane, after `WaitUpdate` or on attach | `ACTIVE` | `FAILED` or `DELETING`: error (`controlplane.go`, `waitForClusterActive`). `ACTIVE`: done. The engine then compares the version by direction. |
| Add-on, on attach | `ACTIVE` | `DEGRADED`, `CREATE_FAILED`, `UPDATE_FAILED`, `DELETE_FAILED`: error "addon update failed: status X" (`version_analyzer.go:222-256`). `ACTIVE`: done. |
| Nodegroup, on attach or for a `Verify` step | Settled | Any status but `UPDATING` and `CREATING`: done |
| Scale, after `WaitUpdate` | The sizes and `ACTIVE` | Below |

**Direction.** The engine compares the settled version, never the wait. An upgrade step is at its goal when the version is at or above `To`. A rollback step is at its goal when the version is at or below `To` (`rollback.go:749`, `atOrBelow`; `controlplane.go`, the `done` argument of `moveControlPlane`). Draft 1 said "at want or newer" for both, which would have marked a rollback done on a nodegroup that was never rolled back.

**Version first.** After a nodegroup settles, the engine branches on the version, then on the status:

| Settled version | Status | Next |
|---|---|---|
| At the goal | Any | Verification with no baseline. A nodegroup that is not `ACTIVE` is a verification issue, exit 5 (`nodegroups.go:127-141`). |
| Not at the goal | `ACTIVE` | A notice, then the gate and a roll |
| Not at the goal | Not `ACTIVE` | The gate refuses: the `Nodes` check fails a target that is not `ACTIVE`. Exit 3. This covers `DELETING` too, as the old built-in gate did ("is DELETING, not ACTIVE", `nodegroups.go`, `defaultNodegroupGate`). |

### After `WaitUpdate` succeeds

| Step | Confirm | Not met |
|---|---|---|
| Add-on | One `DescribeAddon`: the version equals `To` | The step fails: "reported Successful, but the add-on is at X" (`version_analyzer.go:318`) |
| | The status is `ACTIVE` with no health issue | The step succeeds with a verification issue, exit 5 (old `CompletedWithIssues`) |
| | The read fails | The step succeeds with a failure, exit 4 (old `Unverified`) |
| Control plane | `WaitSettled` to `ACTIVE`, then the version by direction | The step fails: "control plane reports version X" |
| Scale | `WaitSettled` with a `done` that checks every answer: a size that was sent and does not match is an error at once; with no desired size sent, the first answer is done; otherwise `ACTIVE` is done and `DEGRADED` or a failed status is an error | The step fails (`nodegroup_scale.go:213-235`; `TestScaleWait_SuccessfulButConfigMismatchErrors`, `TestScaleWait_DesiredWaitsForActive`). The old code polled a `DEGRADED` nodegroup until the timeout; failing at once is the same exit code sooner. |
| Roll | None in the wait. The `NodegroupActive` check of the verification reads the nodegroup. | |

No confirm follows a wait that failed, timed out, or was interrupted.

### A submit with no update ID

A request that EKS accepts with no update ID in the answer cannot be followed. The step is `InProgress` with the failure reason `NotMonitored`, and the run exits 4 (owner decision D3). The same holds for a wait that ends with `*UnobservedError`.

Draft 1 sent such a step to `WaitSettled`. That is wrong for a roll: its goal version equals its starting version, and a first read can still show the old `ACTIVE` before EKS flips to `UPDATING`, so a roll that never ran would be marked done. I propose no reconciliation in this run. A bounded one would need a signal that the change began, and without the update ID there is none that a stale read cannot fake. The next run derives its plan from live state: it finds the nodegroup `UPDATING`, or finds the AMI still outdated. The old `scale` and `addon update` treated a missing ID as an error too (`TestScaleWait_MissingUpdateIDErrors`, `version_analyzer.go:264`).

## 3. The busy rule and the gate set

### Busy

The steps are derived before the rule runs. Draft 1 ran it first, which made a resumed upgrade refuse its own work: after an interrupt during the roll of `ng-a`, the cluster is `ACTIVE` at 1.33 with `ng-a` `UPDATING`, and the old engine attaches to `ng-a` and then rolls `ng-b` (`nodegroups.go:157-171`).

```go
// Busy returns the refusals and the notices for a set of steps: the steps
// of the whole plan in the plan step, the steps of the phase before a phase.
func Busy(snap *fleet.Snapshot, kind Kind, ours []Step) (refuse []Blocker, notices []string)
```

In flight means: a cluster that is not `ACTIVE`; a nodegroup that is neither `ACTIVE` nor `DEGRADED`; an add-on that is `CREATING`, `UPDATING`, or `DELETING` (`services/cluster/busy.go:64-121`).

| Resource in flight | `cluster upgrade`, `cluster rollback` | `nodegroup update`, `nodegroup scale`, `addon update` |
|---|---|---|
| The cluster | Refuse every phase. One exception: `UPDATING` with a control-plane step that is `Attach`. | Refuse |
| One of ours, in a status the step can attach to or verify (`UPDATING`, `CREATING`) | The step is `Attach` or `Verify`; no refusal | An add-on that is `UPDATING` or `CREATING` at the step's own target version: `Attach`. Anything else: the step is `Busy` (below). |
| One of ours, `DELETING` | Refuse the phase | The step is `Busy` |
| Not one of ours | A notice. The run goes on. | Refuse |

The right column keeps the old `CheckBusy`, which refused when anything else in the cluster was changing (traces `addon-update-cluster-updating`, exit 3). The left column is the old engine: it had no check, attached to its own targets, and let EKS decide the rest (`addons.go:92-104`, `nodegroups.go:157`). The contract's new busy check for the upgrade is therefore the first row: a control plane that someone else is changing stops the run before it starts a phase. `cluster rollback` uses the same column. Its old planner already refused a cluster that is not `ACTIVE` and any in-flight version update, except a rollback to the same target, which it attached to (`rollback.go:397-431`); its nodegroup phase shared the upgrade's attach code (`rollNodegroupsTo`).

A snapshot whose cluster, nodegroups, or add-ons are not `Collected` cannot show that nothing is changing. That is an `Unread` blocker, exit 4.

A refusal before any update is the result `Refused` with a `Busy` blocker and a `Plan` document, exit 3. Before a later phase it is `Blocked`, exit 3, with a `Run` document. The message keeps the old words: "prod is busy (cluster UPDATING); nothing was started. Run it again once that finishes".

### A busy target in a standalone command (owner decision D4)

When the plan, or the last read before a submit, shows a target of `nodegroup update`, `nodegroup scale`, or `addon update` in flight, that target is neither changed nor attached to. The update in flight may be of another kind: a scale, where this run wants a roll.

- The step's result is `Busy`. `InFlight` names the type of the update, found with `ListUpdates` and `DescribeUpdate` for that resource; these two reads happen only in this case.
- The run's `failures` gets a failure with reason `Busy` for the resource.
- The other targets go on.
- The run exits 4.

One case is not busy (owner decision D9): an add-on whose in-flight update is already to the version this run wants, that is, `UPDATING` or `CREATING` and reporting that version. It is the change the user asked for, so the command attaches to it, as the old one did (`service.go:415`, `TestUpdate_UpdatingAtTargetWaits`; traces `addon-update-attach`, `addon-update-busy`, `addon-update-attach-failed`). Every other in-flight state of an add-on target is `Busy`.

Old behaviour this replaces: `nodegroup update` skipped an `UPDATING` target with exit 0 (trace `nodegroup-update-busy`), and sent the roll anyway when only its last read showed `UPDATING` (trace `nodegroup-update-changed-under-the-gate`, where EKS refused it). `nodegroup scale` refused with exit 3 (trace `nodegroup-scale-busy`).

### The check set of each kind

| Kind | Checks | Targets |
|---|---|---|
| `NodegroupUpdate`; each nodegroup step of an upgrade or a rollback | `health.Preflight()` | The nodegroups with `Submit` |
| `ClusterUpgrade`, `ClusterRollback`, in the plan | `health.Preflight()`; `Insights` and `VersionSkew` feed the readiness blockers (section 6) | Every nodegroup the plan would roll |
| `NodegroupScale`, the desired size goes down | `Nodes`, `health.ScaleDownBlockers(nodegroup, remove)` | The nodegroup |
| `NodegroupScale`, otherwise | `Quotas` | The nodegroup |
| `AddonUpdate` | `health.Addons(targets)`, new in this phase | The add-ons with `Submit` |

`health.Addons` is one more check value in `internal/health`: blocking, no call of its own, it reads the add-ons of the snapshot.

| Target add-on | Outcome |
|---|---|
| `ACTIVE` | `Pass` |
| `DEGRADED`, `UPDATE_FAILED`, `CREATE_FAILED`, `DELETE_FAILED` | `Warn`: "vpc-cni is DEGRADED (InsufficientNumberOfReplicas); the update will try to repair it" |
| `DELETING` | `Fail` |

The old gate let all four failed states through so that a broken add-on can be repaired, and refused `DELETING` (`health_check.go:34-45`). The warning replaces its raw log line (trace `addon-update-degraded`). An add-on that the snapshot could not read never reaches the check: the plan has an `Unread` blocker for it.

Phase 3's `health.Run` adds the `Kubernetes` access result only when a check of the set needs Kubernetes. A scale-up and an add-on update therefore ask nothing about Kubernetes access. That is a line for phase 3's change log, not a request.

## 4. Phases, the batch, and rolls

### One phase

```go
// runPhase runs the steps of one phase and returns one result per step.
func (r *runner) runPhase(ctx context.Context, cp *ClusterPlan, ph Phase) ([]StepResult, error)
```

The order:

1. **Read.** One snapshot of the whole cluster. The first phase uses the snapshot of the plan when nothing waited since: no person answered, no checkpoint waited, no insights refresh ran.
2. **Recheck.** Derive the phase's steps again from the snapshot.
3. **Nothing to do.** No step with `Submit`, `Attach`, or `Verify`: the phase is skipped. The busy rule does not run.
4. **Busy rule**, with the phase's steps as "ours".
5. **Gate**, for a phase that rolls nodes or scales down, with the phase's `Submit` steps as targets, by the pre-phase table of phase 3. In a serial phase it runs before each step. In the first phase it is the gate of the plan when nothing waited since.
6. **Checkpoint** (section 7). When it waited, go back to step 1.
7. **Run the steps**: as one batch, or one after another.

A batch:

1. **Baseline**, for a roll with verification: `health.TakeBaseline`, once. It is taken after the health checks, as the old command did (`actions_update.go:320-327`); here that is after the whole gate, right before the first submit.
2. **Submit** each `Submit` step in order: one fresh describe of the target, then the request.
3. **Wait together** for every accepted and attached step.
4. **Confirm** each step whose wait succeeded.
5. **Verify**, for a roll.

A serial phase runs the same five for each step alone: the step's read is its fresh describe, and for a nodegroup the gate (step 5 above) and the baseline come before its submit. With `StopOnFailure`, the first step that does not end `Succeeded` ends the phase, and the later steps are `NotAttempted`.

"Fresh" means since the gate's read. Two submits of one batch need no cluster read between them; each has its own describe of its own target.

### Submit

| The fresh describe shows | Result |
|---|---|
| The state the step was planned on | Send the request |
| The target in flight, in a standalone command | `Busy` (section 3). No request. |
| The target in flight, in an engine | The step becomes `Attach` |
| A nodegroup whose Kubernetes version is not the step's `From` | `Failed`: "ng-a changed to 1.33 since the plan". No request. |
| The read fails | `Failed` with the read failure |

`StartNodegroupRoll` is ported frozen from `version_update.go:86-105`:

```go
// StartNodegroupRoll asks EKS to roll the nodegroup onto the latest AMI of
// version. version is always sent, so a patch roll can never become a
// minor upgrade. An empty version is refused before any request.
func StartNodegroupRoll(ctx context.Context, c *awsx.Clients, t Target, nodegroup, version string, force bool) (updateID string, err error)
```

The request carries the version, and `force` only when true. It carries no release version and no launch template (trace `nodegroup-update-yes`: `{"clientRequestToken":"token-1","version":"1.32"}`). The SDK sets the token once per call and the `Mutate` policy reuses it for the retries (`TestStartNodegroupRoll_RetriesKeepOneToken`, `TestStartNodegroupRoll_NewTokenPerCall`; trace `nodegroup-update-lost-response`).

| Submit outcome | Step | Then |
|---|---|---|
| Accepted with an update ID | `Started` | The next step |
| Accepted with no update ID | `InProgress`, `NotMonitored` | The next step |
| Refused or failed | `Failed` | The next step, unless the phase has `StopOnFailure` |
| The context ended before the request | `NotAttempted` | Stop; the later steps are `NotAttempted` |

A batch goes on after a submit failure, as the old command did (`actions_update.go:965-973`). With `--no-wait` the phase ends after the submits: each accepted step is `Started`, an `Attach` step is `InProgress` with no failure, and nothing is waited for or verified.

### Wait together

One ticker for the batch. On each tick every unsettled step polls at once (`fanout.ForEach`).

| Wait outcome | Step status | Failure reason |
|---|---|---|
| `nil`, and the confirm holds | `Succeeded` | |
| `*UpdateError`, `Failed` | `Failed` | `UpdateFailed` |
| `*UpdateError`, `Cancelled` | `Cancelled` | `UpdateCancelled` |
| An error from `done` | `Failed` | `UpdateFailed` |
| `*UnobservedError` | `InProgress` | `NotMonitored` |
| The wait deadline passed | `InProgress` | `Timeout` |
| Interrupted | `InProgress` | `Interrupted` |

One step that ends does not stop the wait for the others, and a permanent poll error settles only its own step (`monitor.go:212-218`).

### Verification

| Condition | Verification |
|---|---|
| `--skip-verify`, `--no-wait`, or nothing was started or attached | None |
| Any started step of the batch is not `Succeeded` | None. The old command verified only on a clean monitor (`actions_update.go:428`). |
| The context ended | None |
| Otherwise | `health.Verify(nodegroups, baseline, wait)` over the steps that succeeded |

An attached roll and a `Verify` step have no baseline of their own roll, since it began before this run. `Baseline.Taken` is false, so `NodesReady` and `NewPendingPods` are `Skipped` and `NodegroupActive` runs (`TestUpgradeNodegroups_VerifiesAttachedRoll`). `health.VerifyExit` gives 5 for an issue and 4 for a verification that could not read.

### The `Verify` action

A nodegroup that is already at the goal version but is `UPDATING`, `CREATING`, or not `ACTIVE` gets a `Verify` step with the reason `Unsettled`. The step sends nothing and runs no gate. It waits with `WaitSettled` when the nodegroup is changing, then runs the verification with no baseline.

| Outcome | Step | Exit |
|---|---|---|
| `ACTIVE` | `Succeeded` | 0 |
| Settled and not `ACTIVE` | `Failed`, with the `NodegroupActive` issue | 5 |
| The nodegroup cannot be read | `Failed`, with the failure | 4 |

`Skip` would hide a degraded nodegroup, and `Submit` would gate it or roll it. The old engine had this case in both engines and in the catch-up hop (`nodegroups.go:127-141`, `plan.go:260-268`; `TestResume_DegradedNodegroupAtTargetIsVerifiedAgain`, `TestResume_UpdatingNodegroupAtTargetIsAttachedAndVerified`, `TestResume_MultiHopVerifiesDegradedNodegroupBeforeTheControlPlane`, `TestRollbackResume_DegradedNodegroupAtTargetStopsTheControlPlane`).

### The roll plan

`DecideAMIUpdate` is ported from `ami_update.go:46-76` as a pure function over the snapshot, less one row.

```go
type RollDecision struct {
	Action Action
	Reason Reason
}

func DecideAMIUpdate(ng fleet.Nodegroup, reroll bool) RollDecision
```

| # | Condition, first match | Action | Reason |
|---|---|---|---|
| 1 | AMI type `CUSTOM` | `Manual` | `CustomAMI` |
| 2 | Status `UPDATING` | `Skip` in the plan, with a `Busy` failure, so a dry run exits 4; the result row is `Busy` (section 3) | `InFlight` |
| 3 | The current or the latest AMI is unknown | `Submit` | `AMIUnknown` |
| 4 | Current equals latest, with `--reroll` | `Submit` | `Reroll` |
| 5 | Current equals latest | `Skip` | `AlreadyLatest` |
| 6 | Otherwise | `Submit` | `AMIOutdated` |

The old row "`--force`: roll, whatever the AMI" is gone (owner decision D5). `--force` means what the contract says: go past drain blockers. The frozen test's row for it changes. The old real run with `--reroll` skipped the AMI lookup (`ami_update.go:57`); the snapshot now always has the AMI, so the plan can say `Reroll` or `AMIOutdated`.

| Flag | Effect on a roll |
|---|---|
| `--reroll` | Row 4. PDBs are honoured. |
| `--force` | The `DrainBlockers` check becomes a warning (phase 3). The request carries `force: true`. |
| `--no-wait` | Stop after the submits |
| `--skip-verify` | No baseline and no verification |
| `--require-healthy` | Any gate warning blocks |
| `--skip-health-check` | No gate. The busy rule still applies. |

Targets: no positional and no `-f` means every nodegroup. A positional is an exact name. `-f name=web-*` selects by pattern, and the confirmation question lists the matches.

### One exit code

One rule for every command (owner decision D6). Fleet mode pools the steps of all its clusters. In three moves:

1. Give each step its code from the first table, and each failure that belongs to no step its code from the second.
2. Partial failure, for independent targets only: the clusters of a fleet, the targets of a multi-target `nodegroup update`, and the add-ons of `addon update --all`. When at least one step has code 1 and at least one step is `Succeeded` or `Started`, the steps with code 1 count as 4. The move does not apply to `cluster upgrade` and `cluster rollback`: their phases are a sequence, and a phase that fails has failed the operation, whatever the earlier phases did.
3. The run exits with the highest code. An interrupt is always 1.

| Step result | Code |
|---|---|
| `Succeeded`, `Skipped`, `Started`; `InProgress` from an attach under `--no-wait` | 0 |
| `Failed`, `Cancelled`: the update ended badly, EKS refused the request, or the fresh read failed | 1 |
| `Blocked`: the busy rule, the gate, or a plan blocker stopped it | 3; 4 when the gate or the plan could not read |
| `InProgress` with `Timeout` or `NotMonitored` | 4 |
| `Busy` | 4 |
| `Failed` from a verification issue; an add-on that is updated and not healthy | 5 |
| `NotAttempted` | None. The step that stopped the run has the code. |

| Not a step | Code |
|---|---|
| A failure in the plan, the gate (`Verdict.Failures`), a region of the sweep, or the verification reads | 4 |
| A question answered no; the hook error of section 7 | 1 |
| The read budget ran out before anything was submitted | 1 when nothing was read, else 4 |
| Interrupt | 1, whatever else happened |

A failed step's own failure does not also count under "a failure in the plan". Move 2 turns on a change that was made or is running: a `Skipped` step changed nothing and does not make a failure partial.

The table a test copies:

| Steps of the run | Exit |
|---|---|
| All `Succeeded` | 0 |
| All `Started` (`--no-wait`) | 0 |
| All attempted steps `Failed` | 1 |
| Independent targets: one `Failed`, one `Succeeded` | 4 |
| Independent targets: one `Failed`, one `Started` | 4 |
| `cluster upgrade`: the control plane `Succeeded`, an add-on `Failed` | 1 |
| `cluster upgrade`: the control plane and the add-ons `Succeeded`, a nodegroup `Failed` | 1 |
| `cluster rollback`: the nodegroups and the add-ons `Succeeded`, the control plane `Failed` | 1 |
| One `Failed`, one `Skipped` as already current | 1 |
| One `Failed`, one `Blocked` | 3 |
| Independent targets: one `Failed`, one `Blocked`, one `Succeeded` | 4 |
| One verification issue, one `Failed` | 5 |
| One verification issue, the rest `Succeeded` | 5 |
| One `Busy`, the rest `Succeeded` | 4 |
| One `InProgress` with `Timeout`, the rest `Succeeded` | 4 |
| One `Blocked`, the rest `Succeeded` | 3 |
| All `Succeeded`, and a gate read that `--yes` went past | 4 |
| Any of the rows above, interrupted | 1 |

A failed upgrade or rollback exits 1, as the old binary does (traces `cluster-upgrade-addon-failed`, `cluster-upgrade-nodegroup-failed`, `cluster-rollback-failed`). Its result still lists the phases that completed, and the summary names the command that goes on from there. Against the old binary the rule changes two codes. A multi-target `nodegroup update` with one failed roll exited 1 and exits 4, as `addon update --all` and the fleet already did (`fleet.go:887-888`). A roll that EKS refuses, with nothing else done, exited 4 in `nodegroup update` and exits 1, as in the other commands.

### Fleet mode

1. **Discover.** A region sweep of `ListClusters` under `--timeout`. A default sweep skips the regions it cannot reach. A region that fails is a failure. No listable region is an error, exit 1.
2. **Plan.** One `ClusterPlan` per cluster, with bounded fan-out, in sweep order. A dry run stops here.
3. **Confirm.** One question for the fleet, with every cluster's decision above it. The old prompt came before any cluster was read (`fleet.go:296-304`).
4. **Run.** One cluster after another. Each has its own budgets (section 8). Before each cluster there is a checkpoint, and each cluster's plan is computed again when its turn comes, because the clusters before it took time. `Diff` against the confirmed plan decides: `--yes` accepts an ask, a person is asked again, a refuse marks that cluster's steps `Blocked`.

A cluster that is refused, fails, times out, or fails verification does not stop the next one. An interrupt, or a stop from the checkpoint, does: the steps of every later cluster are `NotAttempted`.

Fleet mode ignores `REFRESH_CLUSTER` and the active context. A kubeconfig context is matched for each cluster by its endpoint.

## 5. Add-on update and scale

### `addon update`

| Input | Steps |
|---|---|
| One or more names | One step per name. A name that is not installed is an error that lists the close matches. |
| `--all` | One step per installed add-on, less `--skip-addon`, in dependency order |
| `--version` | Only with exactly one name |

The phase is `Serial`, without `StopOnFailure`: a failed add-on does not stop the rest, and its dependents are still attempted (`service.go:549-562`; trace `addon-update-all-partial-failure`, both updates sent).

Dependency order is always on for `--all`. The table and the sort are ported from `addon_dependencies.go:13-101`.

The step of one add-on is the old `versionGuard` (`service.go:403-434`, `TestUpdate_VersionGuardHonorsStatus`). "At target" means the installed version equals the target, or is newer than it when the target was resolved as the latest.

| # | Condition, first match | Action | Reason |
|---|---|---|---|
| 1 | `--version` names a version the cluster's Kubernetes version cannot run | The plan fails, exit 1, with the supported versions (`health_check.go:151`) | |
| 2 | The latest compatible version could not be read | `Blocked`, an `Unread` blocker | |
| 3 | At target and `ACTIVE` | `Skip` | `AlreadyAtTarget` |
| 4 | At target and `UPDATING` or `CREATING` | `Attach` | `InFlight` |
| 5 | Newer than the latest, no `--version`, any other status | `Skip`, with the notice "vpc-cni is DEGRADED at X, newer than the latest catalogue version Y; not re-applied" | `NewerThanLatest` |
| 6 | At target, in any other status (`DEGRADED`, a failed state) | `Submit` at the same version | `Repair` |
| 7 | `UPDATING`, `CREATING`, or `DELETING`, not at target | `Skip` in the plan, with a `Busy` failure; the result row is `Busy` (section 3) | `InFlight` |
| 8 | Otherwise | `Submit` | `VersionBehind` |

Row 5 comes before row 6 so that a degraded add-on that is ahead of the catalogue is never downgraded by a repair (`service.go:418-424`). A `--version` older than the installed one is row 8, with the notice "downgrading vpc-cni from X to Y".

An attached add-on (row 4) waits with `WaitSettled`. A failed wait fails the step. A settled version other than the target fails the step: "addon vpc-cni settled at X, not Y" (`service.go:450-458`). In neither case does the command send an update of its own (trace `addon-update-attach-failed`: no mutation, exit 1).

The request is `UpdateAddon` with the version and the token, and nothing else (`service.go:353-363`).

With `--no-wait` every add-on is submitted in dependency order with no wait between (trace `addon-update-all`). `--wait-timeout` bounds each add-on on its own.

### `nodegroup scale`

`validate`, before any AWS call:

| Input | Error |
|---|---|
| None of `--desired`, `--min`, `--max` | "give at least one of --desired, --min, --max" |
| A negative size | "--min must be 0 or more" (the old command checked this, `actions_scale.go:632-642`) |
| `--min` above `--max`; `--desired` outside a bound that was given | "--min 5 is above --max 2" |

The plan fills the sizes that were not given from the nodegroup and checks the result:

| Check | Result |
|---|---|
| `--min` or `--max` given without `--desired`, and the current desired size falls outside | The plan fails, exit 1: "--max 2 is below the current desired size 3; pass --desired too". The old command had this check and this hint (`CheckScaleBounds`, `nodegroup_scale.go:454-469`; `TestScale_MaxOnlyBelowCurrentFails`). |
| The resulting min is above the resulting max, or the resulting desired is outside them | The plan fails, exit 1, no request. New: the old command sent these and EKS refused (traces `nodegroup-scale-above-max`, `nodegroup-scale-min-above-max`). |
| Every resulting size equals the current one | `Skip` with `NoChange`. New: the old command sent the request. |

The request is `UpdateNodegroupConfig` with a `scalingConfig` of only the sizes the user gave (trace `nodegroup-scale-yes`: `{"scalingConfig":{"desiredSize":3}}`). The gate follows section 3, and `--force` goes past a scale-down blocker with a warning. The wait is `WaitUpdate`, then the scale confirm of section 2.

## 6. The upgrade engine and rollback

### Ported as it is

| Part | Source |
|---|---|
| Hop expansion, the `maxHops` cap, the same-version catch-up hop | `types.go:184-211`, `plan.go:101-107,163-187` |
| Step derivation for the control plane, add-ons, and nodegroups; the output is `Step` values | `plan.go:542-679` |
| The kubelet skew rule: 3 minors, every nodegroup counts, also skipped ones | `plan.go:393-417`; it becomes the `VersionSkew` check with the hop target |
| "Before" and "after" add-ons by compatibility with the hop target | `plan.go:571-629` |
| `--only` and `-n` rules and errors | `scope.go` |
| The insights refresh: reuse within 10 minutes, a 5 minute wait, the start-failure cases | `insights.go:24-150` |
| The insights verdict | `insights.go:174-213`; it becomes the `Insights` check with `ForVersion` |
| The rollback planner: eligibility, the step order, the request | `rollback.go:359-636,789-795` |
| Resume by planning again from live state; no state file | `engine.go` |
| Add-on steps stop the run at the first failure | `addons.go:123` |

### What changes

| Old | New |
|---|---|
| A phase is a label string, and the TUI matched phases by label prefix | `PhaseRef` and `StepRef`. Labels are made in `view`. |
| `ExecuteOptions` with seven hooks | The three hooks of section 7 |
| The health gate ran when the run reached each nodegroup | The plan runs the gate once for every nodegroup it would roll. Each nodegroup step runs it again. |
| No busy check | The cluster row of the busy rule |
| A control-plane update that was attached to and did not reach the target was followed by a new one in silence | A notice first |
| A gate refusal was a plain error, exit 1 | `Blocked`, exit 3, or 4 when the gate could not read |
| `cluster rollback` refused any changed step | It asks, by the table of section 1; a changed target still refuses |
| `cluster rollback` interrupted before it starts reports `Failed` | `Interrupted` |

### Phases

An upgrade, for each hop:

| Phase | Steps | Before the submit |
|---|---|---|
| `ControlPlane` | One | The readiness of the hop: the insights refresh (unless `--skip-insights-check`), the `Insights` and `VersionSkew` checks |
| `AddonsBefore`: the add-ons the new control plane cannot run | Serial, dependency order, `StopOnFailure` | |
| `Nodegroups` | Serial, listing order, `StopOnFailure` | Per step: the gate and the baseline |
| `AddonsAfter` | Serial, dependency order, `StopOnFailure` | |

A rollback:

| Phase | Steps | Before the submit |
|---|---|---|
| `Nodegroups` | Serial, `StopOnFailure` | Per step: the gate and the baseline |
| `Addons`: the add-ons the target version cannot run | Serial, dependency order, `StopOnFailure` | |
| `ControlPlane` | One | Every managed nodegroup in the phase read is at or below the target (`rollback.go:803`). A nodegroup above it blocks the phase. A nodegroup group that could not be read is an `Unread` blocker, exit 4. |

Every phase begins with the read, the recheck, and the busy rule of section 4. A catch-up hop has no control-plane step to run.

The nodegroups of a hop are one phase, as in the old engine, and not one phase each. An interactive `cluster upgrade` therefore asks three times in trace `cluster-upgrade-prompt-yes`, as the old one: control plane, add-ons, nodegroups.

The readiness of a hop is judged twice, as before: in the plan, and live before that hop's control plane moves (`plan.go:477-518`; `TestExecute_PerHopReadinessBlocksLaterHop`). For the hop right after the live version the live check refreshes the insights first (trace `cluster-upgrade-two-hops`: a second `StartInsightsRefresh` before the second `UpdateClusterVersion`). A readiness check that blocks a later hop ends the run as `Blocked`, exit 3.

### Insights in the plan

| Run | Insights |
|---|---|
| Dry run | No refresh. An `ERROR` blocks. A missing or `UNKNOWN` insight is a notice. A failed read is a failure, exit 4, and does not block. |
| Real run | `StartInsightsRefresh`, a wait of up to 5 minutes, `ListInsights`. An empty list, `ERROR`, or `UNKNOWN` blocks. A refresh or read that fails blocks, with a failure. |
| `--skip-insights-check` | No refresh, no read, a notice |

The refresh comes before the block decision, so a blocked real run has sent that one request (trace `cluster-upgrade-blocked`). A rollback reads the `ROLLBACK_READINESS` insights and starts no refresh. `--skip-insights-check` turns its insight blocker into a notice and sets `force` on the request.

### The rollback window

The planner keeps the old three cases (`rollback.go:411-419`):

| Now | Result |
|---|---|
| Before `UpgradedAt` + 7 days | Eligible |
| Up to 2 hours after that | Eligible, with the notice "the 7-day rollback window may have just closed; EKS decides" |
| Later | An `Unavailable` blocker, exit 3 (trace `cluster-rollback-expired`) |

`fleet.RollbackWindow` reports an open window only. The planner needs `UpgradedAt` in the second and third case too, so phase 4 adds `fleet.LastUpgrade(snap) (Rollback, bool)`, which finds the upgrade whatever the clock says, and `RollbackWindow` is built on it.

### Attach in the engines

| Step | State in the phase read | What happens |
|---|---|---|
| Control plane | `UPDATING` | `WaitSettled` to `ACTIVE`. At the goal: `Succeeded`, `Attached`. Not at the goal: a notice, then the step submits its own update (`controlplane.go`, `moveControlPlane`; trace `cluster-upgrade-attach-failed`). |
| Add-on | `UPDATING` or `CREATING`, at any version | `WaitSettled`. A failed wait fails the step and the run; no update is sent (`addons.go:92-100`). Settled `ACTIVE` and compatible with the hop: done. Settled `ACTIVE` and not compatible: the step submits (`addons.go:102-107`, where `keep` is false). |
| Nodegroup | `UPDATING` or `CREATING` | `WaitSettled`, then the version-first table of section 2 |

The notice of the first row, on stderr:

```text
warning: the control-plane update that was already running did not reach 1.33 (the cluster is ACTIVE at 1.32); starting a new one
```

The contract's row "it says that the attached update failed before it continues" covers the control plane only. Draft 1 extended it to add-ons, which the old engine never followed with an update of its own after a failure.

The engine waits by resource status and looks up no update ID, as the old engine. The notice cannot quote the error of the foreign update.

### Flags

| Flag | Effect on the plan |
|---|---|
| `--to` | Required. One hop per minor. Equal to the live version: a catch-up plan. Older: an error. |
| `--only` | A step of a part that is left out is `Manual` with `OutOfScope`. Without `control-plane` the target must equal the live version. Without `addons` the plan spans one hop, and an add-on that the new control plane cannot run blocks the control-plane step (`scope.go:122-123,181-210`). |
| `-n` | Exact nodegroup names. The others are `Manual`. The skew rule still counts every nodegroup. |
| `--skip-addon` | Exact names. The step is `Manual` with `SkippedByFlag`, decided before the catalogue is read (`plan.go:580-584`). A skipped add-on is never a blocker, compatible or not (trace `cluster-upgrade-skip-addon`). |
| `--skip-nodegroup` | Exact names. `Manual`. |
| `--rollback-timeout` | `0`, or whole minutes from 2h to 168h (`rollback.go:52-60`), validated before any call |

A run that ends with `Manual` steps succeeded, and a notice says what is left and the command that does it.

## 7. `Run` and its hooks

### The entry point

```go
// Run executes the change on the caller's goroutine and returns when it
// has ended. Cancelling ctx is an interrupt. The error is non-nil only
// when Run could not do its job as a program: a hook returned an error, or
// the request is invalid. A change that failed is a Result, not an error.
func Run(ctx context.Context, d Deps, req Request, h Hooks) (Result, error)

type Hooks struct {
	// Event receives every event, one call at a time. It must not block
	// for long: the polls of a wait go through it.
	Event func(Event)
	// Ask asks a person and returns the answer. It returns ctx.Err() when
	// ctx ends first, and any other error when the question could not be
	// asked or read.
	Ask func(ctx context.Context, q Question) (yes bool, err error)
	// Checkpoint is called before each phase after the first and before
	// each cluster of a fleet after the first. It may block: that is a
	// pause. It returns when the run may go on or must stop.
	Checkpoint func(ctx context.Context, next PhaseRef) (Checkpoint, error)
}

type Checkpoint struct {
	Stop   bool // end the run here; nothing more is submitted
	Waited bool // time passed in the hook; the run reads the cluster again
}
```

A nil `Event` drops events. A nil `Checkpoint` means go on.

**The consent rule.** `Run` submits only when `Policy.Yes` is set, or `Common.Expect` is a reviewed plan of the same request that the current plan matches, or `Ask` returned yes for the plan being executed.

A nil `Ask` means nobody can be asked. It is never a yes. A run with a nil `Ask`, no `Policy.Yes`, and no matching `Expect` is refused before any change: status `Refused`, exit 1, "nobody confirmed the change and nobody can be asked; pass --yes". The same holds for a question about health warnings or about a changed plan: "health warnings need an answer and nobody can be asked; pass --yes or --require-healthy".

`Expect` is consent to one request. Before `Run` reads anything, it compares `Expect.Request` with the request, field by field: the kind, the cluster, the region, the targets, the parameters, and the whole policy (`Yes`, `RequireHealthy`, `Force`, `Skip`). It does not compare `Limits`, `NoWait`, and `Expect`, which cannot change what is submitted. A difference is the error `ErrOtherRequest`, "the reviewed plan is for another request", and nothing is read or changed. `Run` then compares its first plan with `Expect` through `Diff`.

The consent covers the health that the plan showed. The fresh read before each submit applies the policy to the health that it finds, by the pre-phase table of phase 3: a block blocks the step, nothing worse goes on, something worse asks again, `--yes` goes on with a notice, and `--require-healthy` blocks. When nobody can be asked, the step fails and nothing is sent for it.

`Common` gains two fields that a front end sets:

```go
	// Expect is a plan that a person reviewed before this call. Run
	// compares its first plan with it, by the table of section 1.
	Expect *Plan
	// Nodes starts a node observer for each roll (section 9).
	Nodes bool
```

`Run` starts goroutines in three places and joins each before it goes on: the checks of one gate (`health.Run`), the polls of one tick (`fanout.ForEach`), and the observers beside a wait (`fanout.RunAlongside`). It calls `Event` under a mutex, so the front end needs no lock even while observers report. `internal/change` gets a `goleak.VerifyTestMain`.

### What `Run` does

1. `plan`. With `Expect`: `Diff`, and its effect.
2. `Event(PlanReady)`. A blocked plan ends here as `Refused`; a plan with nothing to do as `NothingToDo`.
3. At most one question before the first submit:

| Gate decision | Policy | Question |
|---|---|---|
| `Go` | `--yes` | None |
| `Go` | no `--yes` | `ConfirmStart`: "Roll 2 nodegroups in prod?" Every changing command asks it once (owner decision D7). |
| `Ask` | `--yes` | None. Phase 3 turned it into `Go` with the reasons in `Accepted`. |
| `Ask` | no `--yes` | `GateWarnings`. A yes also confirms the start. |

4. After a yes from a person: back to step 1, with the plan just shown as the one to compare with.
5. The phases in order, each by section 4. Between two phases: the `Checkpoint` hook, and with `ConfirmPhases` the question `ConfirmPhase`.
6. The result.

Nothing is submitted before step 5, and step 5 needs the consent of the rule above. A refusal, a declined question, and an interrupt before then leave the cluster untouched, with one exception: the `StartInsightsRefresh` of an upgrade plan.

### Events and questions

```go
type EventKind string

const (
	PlanReady      EventKind = "PlanReady"      // Plan
	GateReport     EventKind = "GateReport"     // Checks, Verdict
	PhaseStarted   EventKind = "PhaseStarted"   // Phase
	PhaseEnded     EventKind = "PhaseEnded"     // Phase, Skipped
	StepChanged    EventKind = "StepChanged"    // Step, Result
	Notice         EventKind = "Notice"         // Text
	VerifyReport   EventKind = "VerifyReport"   // Checks
	UpdateProgress EventKind = "UpdateProgress" // Step, Update; one per poll
	NodeProgress   EventKind = "NodeProgress"   // Step, Nodes
)

type Event struct {
	At    time.Time
	Kind  EventKind
	Phase PhaseRef
	Step  StepRef

	Plan    *Plan
	Verdict *health.Verdict
	Checks  []health.Result
	Result  *StepResult
	Skipped bool
	Text    string
	Update  *UpdateProgress
	Nodes   *NodeProgress
}

type QuestionKind string // ConfirmStart, ConfirmPhase, GateWarnings, PlanChanged

type Question struct {
	Kind    QuestionKind
	Phase   PhaseRef
	Text    string
	Reasons []string // the gate reasons or the plan changes behind it
}
```

`StepResult` here is the typed form of the document's: a `StepRef`, the status, the update ID, the times, the failure.

How a question ends:

| `Ask` returns | The run |
|---|---|
| yes | Goes on, after the plan is computed again |
| no | Ends as `Declined`, exit 1. Nothing more is submitted. |
| `ctx.Err()` | Ends as `Interrupted`, or as timed out when a budget ran out |
| Any other error, such as a read error on stdin (`cmdutil/prompt.go`) | Ends at once. Nothing more is submitted. `Run` returns the error and a `Result` with status `Failed` that lists what was done: before the first submit every step is `NotAttempted`; at a later phase the finished steps keep their results and the rest are `NotAttempted`. Exit 1. |

The budgets of section 8 do not run while `Run` is inside `Ask` or `Checkpoint`.

### Result

```go
type RunStatus string

const (
	Succeeded    RunStatus = "Succeeded"
	NothingToDo  RunStatus = "NothingToDo"
	Started      RunStatus = "Started"      // --no-wait
	Refused      RunStatus = "Refused"      // nothing was submitted; the plan says why
	Declined     RunStatus = "Declined"     // a question was answered no
	Stopped      RunStatus = "Stopped"      // a checkpoint returned Stop
	Blocked      RunStatus = "Blocked"      // a later phase was refused
	Failed       RunStatus = "Failed"
	VerifyFailed RunStatus = "VerifyFailed"
	Incomplete   RunStatus = "Incomplete"   // something is unread, unfollowed, busy, or timed out
	Interrupted  RunStatus = "Interrupted"
)

type Result struct {
	Status   RunStatus
	Plan     *Plan // the last plan computed; nil if none
	Clusters []ClusterResult // Target, Status, Steps []StepResult, Verification []health.Result
	Failures []apidoc.Failure
	Err      error // the one-line message for the user, or nil
}

// ExitCode applies the tables of section 4.
func (r Result) ExitCode() int

// Document returns the Plan document for Refused and NothingToDo, and the
// Run document otherwise.
func (r Result) Document() apidoc.Document
```

`Status` is a label for people: the name of the outcome with the highest code. `ExitCode` does not read it; it reads the steps and the failures. `Stopped` comes only from a checkpoint and `Declined` only from an answer, so a failure after a stop request is still `Failed`, as the old TUI held (`tui/live/upgrade.go:305-318`).

### The CLI front end

```go
// RunChange runs the change with the terminal as its front end.
func RunChange(ctx context.Context, ios *IOStreams, format Format, d change.Deps, req change.Request, r Renderer) error {
	res, err := change.Run(ctx, d, req, change.Hooks{
		Event: r.Render, // stderr for a machine format; the roll panel on NodeProgress
		Ask: func(ctx context.Context, q change.Question) (bool, error) {
			r.Pause() // take the panel down while the prompt shows
			defer r.Resume()
			return Confirm(ctx, ios, q.Text)
		},
	})
	r.Close()
	if perr := Print(ios, format, res.Document(), r.View(res)); perr != nil {
		return perr
	}
	ReportFailures(ios, res.Failures)
	if err != nil {
		return err // exit 1
	}
	if code := res.ExitCode(); code != 0 {
		return &ExitError{Code: code, Err: res.Err}
	}
	return nil
}
```

`Confirm` runs under the context of the run, so Ctrl+C wakes the prompt (traces `*-prompt-interrupt`). `RequireYes` ran before `RunChange`, so `Ask` is only reached where a prompt can show, and with `--yes` no question arises. `Renderer` is a small interface in `cmdutil` that `view` implements: `Render`, `Pause`, `Resume`, `Close`, `View`. A human run and a machine run are the same call with another renderer.

A dry run is `change.NewPlan`, `Print`, and the plan's exit code.

### What phase 5 will need

The TUI's handle is an adapter that calls `Run` on a goroutine of its own. This table is the check that three hooks are enough. Each row is something the old TUI built by hand around the engine (`tui/live/upgrade.go`, `roll.go`, `addons.go`, `observed.go`).

| Old TUI need | Served by |
|---|---|
| A feed of what happened, and the state of each phase and step | `Event`. The adapter appends to its own log, keeps the latest progress per `StepRef`, and gives readers a cursor. |
| Phases matched by label prefix | `PhaseRef` and `StepRef` on every event |
| The outcome of each roll inside an upgrade, which the hooks never reported | `StepChanged` with the result |
| A question that exactly one answer closes | `Ask`. The adapter registers the question under an ID and blocks on a channel and on the context. |
| Pause, and stop after the current phase | `Checkpoint`. The adapter blocks while paused and returns `Stop`. |
| A stop that is not a failure | `Result.Status` |
| The dry run first, then "the plan changed" | `NewPlan`, then `Common.Expect` |
| Standalone rolls and add-on updates, written a second time | `Run` with the same requests as the CLI |
| The node view | `Common.Nodes` and `NodeProgress` |
| The deadline not running while paused or asking | Section 8 |
| One change per cluster in the UI | The adapter's own map, taken before it calls `Run`. Phase 4 has no claim type. |
| Watching a change that was started elsewhere | Not `Run`: there is no plan to execute. Phase 5 builds it from the exported `WaitUpdate`, `WaitSettled`, `noderoll.Attach`, and `health.Verify`. |

## 8. Deadlines

The old tree had three calculators (`scaleSetupTimeout`, `fleetClusterContext`, `monitorContext`) and more timeouts beside them. One model replaces them.

```go
type Limits struct {
	Read     time.Duration // --timeout; zero means no limit
	Wait     time.Duration // --wait-timeout; zero means no limit
	StepWait bool          // Wait applies to each step alone (addon update)
	Poll     time.Duration // --poll-interval
}
```

| Budget | Starts | Covers | Does not run |
|---|---|---|---|
| `Read` | At each `plan` of a cluster | The reads up to the first submit or attachment. With `--no-wait`: every read and submit of the run. | Inside `Ask` and `Checkpoint`; while an insights refresh is waited for, which has its own 5 minute limit |
| `Wait` | At the first submit or the first attachment of a cluster, whichever comes first | Everything after it for that cluster: the waits, the reads and submits of later phases, verification | Inside `Ask` and `Checkpoint` |
| `Wait` with `StepWait` | At each step's submit or attachment | That step's wait and confirm | |
| A dry run | In the command: `cmdutil.WithTimeout(ctx, --timeout)` | `NewPlan` | |

An attach starts the wait budget. Trace `addon-update-attach` follows an update that it did not start for four minutes; under the read budget alone it would time out after one.

Defaults, from `CONTRACT.md`: `--timeout` 1m; `--wait-timeout` 40m for `nodegroup update`, 5m for a scale, 5m for each add-on, 4h for an upgrade or a rollback; `--poll-interval` 15s. In fleet mode every cluster has its own budgets and discovery runs under `--timeout`.

`Wait` starts at the first submit and not at the start of the command. The old `nodegroup update` put one deadline over the reads and the wait (`actions_update.go:164`), so a short `--wait-timeout` could expire during the reads.

`Run` derives one context per cluster and cancels it with a cause when a budget runs out. `context.Cause` tells a deadline from an interrupt (`TestExecute_DeadlineSaysTimedOut`, `TestExecute_CancelSaysInterrupted`).

| Ending | Steps | Exit |
|---|---|---|
| `Read` ran out and nothing had been read | All `NotAttempted`; "timed out reading prod (increase --timeout); nothing was changed" | 1 |
| `Read` ran out after something was read | All `NotAttempted`, or `Started` under `--no-wait`; the document has what was read and a `Timeout` failure | 4, as in the read commands |
| `Wait` ran out | Each step that was accepted or attached and has not ended is `InProgress` with a `Timeout` failure: "--wait-timeout passed before the update finished; it may still be running" | 4 |
| Interrupt | The same rows with an `Interrupted` failure | 1 |

**The last poll.** A wait never pauses past its deadline. It cuts the last pause to the time that is left, so the last poll is due at the deadline and no poll comes after it. With a 10 s budget and a 15 s interval, the one poll is at 10 s. The answer of the last poll counts: a change that has in fact finished is reported as finished, as a success or as a failure, because the result is then known and nothing is left running. Only an answer that shows the change still running ends the wait as timed out, exit 4. The wait checks the budget after each answer. The timer that ends a hung call fires 10 s after the deadline of a wait, so the last poll can get its answer.

A deadline or an interrupt cancels nothing in EKS. The next run derives its plan from live state. The old binary exits 1 for a wait deadline in all five commands (traces `*-deadline`); the 4 is owner decision D2.

## 9. The node observer and the roll panel

`internal/noderoll` imports client-go and nothing of refresh. It is a port of the old package: `Tracker` frozen, the 638-line observer split by subject into the observer, the classifier, the pod counts of draining nodes, the warning events, and the informers. `scripted.go` is not ported; it served the hidden `--simulate` flag, which `CONTRACT.md` does not have.

```go
// Identity says how to tell the old nodes of a roll from the new ones.
type Identity struct {
	TargetVersion string    // the Kubernetes minor of an upgrade or rollback roll; "" for an AMI roll
	TargetAMI     string    // "" when it is not known
	StartedAt     time.Time // zero means now
}

// Attach returns an observer for the nodes of one nodegroup. It starts
// the informers and falls back to a list per snapshot when they do not
// sync in 10 seconds. It fails only when the first node list fails.
func Attach(ctx context.Context, kube kubernetes.Interface, nodegroup string, id Identity) (*Observer, error)

func (o *Observer) Snapshot(ctx context.Context) (Snapshot, error)
func (o *Observer) Watching() bool
func (o *Observer) Close()
```

`Attach` picks the mode once. The old code had three setters and a precedence the caller had to know (`observer.go:226-238,562-565`).

| Condition, first match | A node is new when |
|---|---|
| `TargetVersion` is set and a node has another kubelet minor | Its kubelet minor equals `TargetVersion` |
| Otherwise | It is not among the nodes created before `StartedAt`; with a zero `StartedAt`, not among the nodes present at `Attach` |

The image-label rule stays in the classifier for its frozen tests; `Attach` never selects it. Everything else is unchanged: nodes by the nodegroup label, `Draining` by cordon or drain taint, `Joining` when not Ready, pods listed only for draining nodes with a `spec.nodeName` selector. One goroutine drives an observer.

### Beside the wait

```go
err := fanout.RunAlongside(ctx, observe, func(ctx context.Context) error {
	return r.waitTogether(ctx, steps)
})
```

With `Common.Nodes`, `observe` runs one goroutine per accepted roll. Each calls `Attach`, takes a snapshot every second when the observer watches and every `min(Poll, 3s)` when it lists, feeds a `Tracker`, and sends `NodeProgress{Nodegroup, Snapshot, Recent, Watching}` through `Event`. `observe` also runs the Auto Scaling watch (`scaling.go`, `WatchScalingFailures`): `DescribeScalingActivities` every `Poll`, and a `Notice` for each failed launch since the roll began, once per activity.

Neither can change the result. `RunAlongside` cancels and joins both when the wait returns. A failed `Attach`, or five snapshot errors in a row, ends the node observer with one `Notice`.

### The panel

`view.RollPanel(th, progress, desired, elapsed) []string` is a port of `rollview.go:166-286`: the header with old and new version, the bar of replaced nodes, the node table with drain progress, the last tracker events, the warnings. The renderer draws it through `view.Live` on each `NodeProgress` and holds the wait's progress lines while it is up. The old engine needed a locked buffer for that (`heldProgress`); the renderer gets its events one at a time.

| Condition | Output during the wait |
|---|---|
| Table output, stderr is a terminal, no `--quiet`, exactly one roll in flight, Kubernetes access | The panel, redrawn in place |
| `--live`, stderr is not a terminal | Panel frames appended, at most one each 15 s, only when the frame changed |
| `--live`, no Kubernetes access | One warning, then the lines of the next row |
| Several rolls in flight, or no Kubernetes access | A line per status change of each update, and one per minute while nothing changes |
| `--quiet` | Nothing. Questions, warnings, and the summary still print. |
| `-o json` or `-o yaml` | The progress lines on stderr. `--live` is a usage error. |

The command sets `Common.Nodes` for the first two rows. `cluster upgrade` and `cluster rollback` roll one nodegroup at a time and get the panel on a terminal without a flag, as today. `--live` exists on `nodegroup update` only.

## 10. Commands

### Shared

```go
package cmdutil

type ChangeFlags struct {
	DryRun, Yes, NoWait, Quiet           bool
	SkipHealthCheck, RequireHealthy      bool
	Force                                bool
	WaitTimeout, PollInterval            time.Duration
}

// AddChangeFlags registers the shared flags. noWait is false for cluster
// upgrade and cluster rollback. force is false for addon update.
func AddChangeFlags(cmd *cobra.Command, f *ChangeFlags, waitDefault time.Duration, noWait, force bool)

func (f *ChangeFlags) Validate() error

// RequireYes returns the --yes error when the run would need a prompt it
// cannot show. It reads nothing.
func RequireYes(ios *IOStreams, format Format, yes, dryRun bool) error

// Confirm asks on stderr and reads one line.
func Confirm(ctx context.Context, ios *IOStreams, question string) (bool, error)
```

`Validate` rejects `--dry-run` with `--no-wait`, a `--poll-interval` of zero or less, a negative `--wait-timeout`, and `--skip-health-check` with `--require-healthy`.

Every changing command has this `RunE` and this `run`:

```go
RunE: func(cmd *cobra.Command, args []string) error {
	if err := o.complete(cmd, args); err != nil {
		return err
	}
	if err := o.validate(); err != nil {
		return err
	}
	if err := cmdutil.RequireYes(o.io, o.format, o.change.Yes, o.change.DryRun); err != nil {
		return err
	}
	return o.run(cmd.Context())
},

func (o *updateOptions) run(ctx context.Context) error {
	d, err := o.deps(ctx) // the first use of the network
	if err != nil {
		return err
	}
	req := o.request()
	if o.change.DryRun {
		return cmdutil.PrintPlan(ctx, o.io, o.format, d, req, o.timeout)
	}
	return cmdutil.RunChange(ctx, o.io, o.format, d, req, view.NewChangeRenderer(o.io, o.format, o.change.Quiet))
}
```

`complete` resolves the cluster the mutation-safe way: the explicit value, `REFRESH_CLUSTER`, the active context, never the kubeconfig. The options struct of a command holds the streams, the `deps` function, the format, `ChangeFlags`, and its own flags.

### The five

| | `addon update` | `nodegroup scale` |
|---|---|---|
| `Use` | `update [addon...]` | `scale <nodegroup>` |
| Args | Any number; none needs `--all` | Exactly 1 |
| Own flags | `-c`, `--version`, `--all`, `--skip-addon` | `-c`, `--desired`, `--min`, `--max` |
| `--wait-timeout` | 5m, for each add-on | 5m |
| `validate` | Names and `--all` exclude each other, and one is needed. `--version` needs exactly one name. `--skip-addon` needs `--all`. There is no `--force`. | The size rules of section 5 |

| | `nodegroup update` | `cluster upgrade` | `cluster rollback` |
|---|---|---|---|
| `Use` | `update [nodegroup...]` | `upgrade [cluster]` | `rollback [cluster]` |
| Args | Any number; none means all | At most 1 | At most 1 |
| Own flags | `-c`, `-f`, `--all-clusters`, `--reroll`, `--skip-verify`, `--changelog`, `--live` | `--to`, `--only`, `-n`, `--skip-addon`, `--skip-nodegroup`, `--skip-insights-check`, `--skip-verify` | `--skip-nodegroup`, `--skip-insights-check`, `--rollback-timeout`, `--skip-verify` |
| `--wait-timeout` | 40m | 4h | 4h |
| `--no-wait` | yes | no | no |
| `validate` | `--all-clusters` excludes `-c`, positionals, and `--kube-context`. `--changelog` needs `--dry-run`. `--live` needs `-o table` and excludes `--quiet`. | `--to` is required and parses as `1.<minor>`. `--only` values are known. | `--rollback-timeout` is 0, or whole minutes from 2h to 168h |
| Asks, without `--yes` | Once | Before each phase | Once, with the whole order in the question (`rollback.go:278-305`) |

All five print `-o table|json|yaml|plain`. `--require-healthy` is a shared flag. `--changelog` stays in the command layer: it fetches the AMI release notes for the roll steps of a dry-run plan, and a failed fetch is a warning (trace `nodegroup-update-dry-run-changelog`).

Exit codes: `Result.ExitCode()` for a run and the plan's code for a dry run. Before either, a usage error, a name that does not exist, a `--version` the cluster cannot run, sizes that cannot hold, the `--yes` error, and an unresolved cluster all exit 1 with stdout empty. Each command needs a line in the exit-code help.

## 11. Calls, order, and parity

### The command lines

| Old | New |
|---|---|
| `-n ng-a`, `-a vpc-cni`; `-c prod` on a cluster command | The positional |
| `addon update` and `nodegroup scale` without `--wait` | `--no-wait` |
| `--wait` | Nothing |
| `--skip coredns` | `--skip-addon coredns` |
| `--dependency-order`, `--health-check`, `--check-pdbs` | Nothing; `--require-healthy` where the scenario means "the check must run" |

### The main path of each command

Requests in bold are mutations. Calls on one line start together.

**`addon update`** (old: trace `addon-update-wait`)

| # | New | Old |
|---|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons` | `ListClusters`, `ListAddons` (name lookup), then the busy check in series |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` per target | The same, then `DescribeCluster`, `DescribeAddonVersions`, `DescribeAddon` again in the service |
| 3 | `DescribeAddon` (fresh), **`UpdateAddon`** `{addonVersion}` | **`UpdateAddon`**, the same payload |
| 4 | `DescribeUpdate` until it ends, `DescribeAddon` | The same |

**`nodegroup scale`** (old: traces `nodegroup-scale-wait`, `nodegroup-scale-pdb-safe`)

| # | New | Old |
|---|---|---|
| 1 | The same two waves as above, without the add-on versions | The availability reads, then the busy check in series |
| 2 | The gate. Up: `GetServiceQuota`, `GetMetricData`, the availability reads. Down: `list nodes`, `list poddisruptionbudgets`, then `list pods` of the namespaces at risk. | Only with `--check-pdbs`: a namespace probe, the PDB list, a node list by label, pod lists per namespace |
| 3 | `DescribeNodegroup` (fresh), **`UpdateNodegroupConfig`** `{scalingConfig: the given sizes}` | `DescribeNodegroup` twice, the same request |
| 4 | `DescribeUpdate` until it ends, `DescribeNodegroup` until the sizes hold and it is `ACTIVE` | The same |

**`nodegroup update`** (old: trace `nodegroup-update-kube-yes`)

| # | New | Old |
|---|---|---|
| 1 | The two waves, plus `DescribeAutoScalingGroups`, `GetParameter` per AMI type, `DescribeInstances` | The busy check in series; the AMI reads in the health check and again in the decide step |
| 2 | The gate: `GetMetricData` × 3, `GetServiceQuota`, `list nodes`, `list poddisruptionbudgets`, `list deployments`, node metrics, `list pods` × 3, then `list pods` of the namespaces at risk; beside it the availability reads | A namespace probe, the health checks, the Pending-pod baseline, `DescribeNodegroup` per target, the drain gate |
| 3 | The baseline: `list nodes`, `list pods` (Pending) | Taken between the health checks and the drain gate |
| 4 | Per target: `DescribeNodegroup` (fresh), **`UpdateNodegroupVersion`** `{version}`, with `force` under `--force` | The same pair |
| 5 | `DescribeUpdate` for every update each tick | The same |
| 6 | Verification: `DescribeNodegroup` per rolled nodegroup, `list nodes`, `list pods` (Pending) | The same, without the node readiness rule |

Fleet (trace `nodegroup-update-all-clusters`): `ListClusters` per region, rows 1 and 2 for every cluster in parallel, then rows 1 to 6 for each cluster in turn.

**`cluster upgrade`** (old: trace `cluster-upgrade-yes`)

| # | New | Old |
|---|---|---|
| 1 | The plan read with `DescribeClusterVersions` and the add-on catalogues of 1.32 and 1.33 | The same reads in series |
| 2 | **`StartInsightsRefresh`**, `DescribeInsightsRefresh` until done, `ListInsights` | The same |
| 3 | The gate, for both nodegroups | Not here |
| 4 | The phase read, `DescribeCluster` (fresh), **`UpdateClusterVersion`** `{version}`, `DescribeUpdate` until done, `DescribeCluster` until `ACTIVE` | `DescribeCluster`, the nodegroups (readiness), the same request and waits |
| 5 | The phase read; per add-on: `DescribeAddon`, **`UpdateAddon`**, `DescribeUpdate` until done, `DescribeAddon`. The first failure ends the run. | The same per add-on |
| 6 | The phase read; per nodegroup: `DescribeNodegroup`, the gate, the baseline, **`UpdateNodegroupVersion`** `{version: 1.33}`, `DescribeUpdate` until done, verification | `ListNodegroups`, `DescribeNodegroup`, the built-in gate, the command's gate with the PDB check, the roll, verification |

**`cluster rollback`** (old: trace `cluster-rollback-yes`): the plan read with `ListUpdates`, `DescribeUpdate` per update, and `ListInsights` (`ROLLBACK_READINESS`); the gate; then the nodegroups as row 6, the add-ons as row 5, and the control plane as row 4 with **`UpdateClusterVersion`** `{version: 1.32}`, plus `force` and `rollbackConfig` when asked (trace `cluster-rollback-force-and-timeout`). The old command built its plan a second time before it started; the new one does that only after a prompt.

Mutations, payloads, and their order are the same as the old binary's on every main path.

### Intended differences and their allowlist entries

The owner decided that the comparator checks safety obligations and no longer compares preconditions for equality (D8). Draft 1's two largest classes, 55 entries for reads that differ, are gone with that. The comparator in this checkout is still the old one; the new one is on a branch and lands before phase 4 code starts. The count below is an estimate for it, over `mutations`, `world`, `refusedBeforeMutation`, and `exitCode`, and the first paired run of each command settles it.

| Class | Scenarios | Fields and `expect` | Contract row | Entries |
|---|---|---|---|---|
| `--yes` is required | `nodegroup-update-no-yes` | `mutations` `[]`, `world`, `refusedBeforeMutation` true, `exitCode` 1 | "`nodegroup update -o json` without `--yes`" | 4, exist |
| No Kubernetes access | `nodegroup-scale-check-pdbs-no-kube`, run with `--yes --require-healthy` | `exitCode` 2 (the entry says 4 today) | "Without Kubernetes access" | 1 |
| | `nodegroup-update-dry-run`, `nodegroup-update-dry-run-changelog`, `cluster-upgrade-dry-run`, `cluster-rollback-dry-run` | `exitCode` 2 | the same | 4 |
| Verification | `nodegroup-update-verify-node-not-ready` | `exitCode` 5 | "Post-roll verification does not read node readiness" | 1 |
| | `nodegroup-update-verify-pods-unreadable` | `exitCode` 4 | "If the pod list fails after a roll" | 1 |
| The gate runs before any change | `cluster-upgrade-pdb-blocked`, `cluster-upgrade-pdb-blocks-second-nodegroup`, `cluster-upgrade-pdbs-unreadable` | `mutations`: `StartInsightsRefresh` only; `world`: nothing upgraded; `exitCode` 3, 3, 4 | "`cluster upgrade` reads PDBs only when it reaches the nodegroup phase" | 9 |
| | `cluster-rollback-pdb-blocks-second-nodegroup` | `mutations` `[]`, `world`, `refusedBeforeMutation` true, `exitCode` 3 | the same | 4 |
| A gate refusal exits 3 | `cluster-rollback-pdb-blocked` (3), `cluster-rollback-pdbs-unreadable` (4), `cluster-upgrade-nodegroup-degrades-mid-run` (3) | `exitCode` | "A drain-gate refusal exits 3" | 3 |
| An unreadable gate exits 4 | `nodegroup-update-pdbs-unreadable` (was 3), `nodegroup-scale-pdbs-unreadable` (was 1) | `exitCode` 4 | "A check that cannot read its data"; "`nodegroup scale -o json` prints no document" | 2 |
| Deadline (D2) | The five `*-deadline` scenarios | `exitCode` 4 | The row the owner adds for D2 | 5 |
| A busy target (D4) | `nodegroup-update-busy`, `nodegroup-update-changed-before-the-gate` | `exitCode` 4 (was 0), `refusedBeforeMutation` true | The row for D4 | 4 |
| | `nodegroup-update-changed-under-the-gate` | `mutations` `[]`, `refusedBeforeMutation` true | the same | 2 |
| | `nodegroup-scale-busy` | `exitCode` 4 (was 3) | the same | 1 |
| Sizes | `nodegroup-scale-above-max`, `nodegroup-scale-min-above-max` | `mutations` `[]`, `refusedBeforeMutation` true | Contract edit 3 | 4 |
| One rule for mixed results (D6) | `nodegroup-update-partial-failure`: one roll succeeded and one failed (was 1) | `exitCode` 4 | Contract edit 5 | 1 |
| | `nodegroup-update-cluster-starts-updating`: the one submit was refused (was 4) | `exitCode` 1 | the same | 1 |

47 entries: 5 exist or change, 42 are new. This is an estimate. If the comparator keeps a field for the reads, `nodegroup-update-no-yes` keeps its two entries for them.

No entry is needed where only the output changes: the rollback that is interrupted before it starts reports `Interrupted`; the notice before a second control-plane update; the accepted `Kubernetes` reason of a `Kube: false` run with `--yes`; the removed `--force` row, since `nodegroup-update-force` rolls an outdated nodegroup either way; the confirmation on a terminal, since `nodegroup-update-prompt-*` already prompt for warnings.

## 12. Test plan and build order

### Tests

**Table tests for pure decisions.** `DecideAMIUpdate` (six rows; the frozen test loses its `--force` row); `Busy` (each status of each resource, ours and not, in both columns, and each unread group); `Plan.Decision` with the dry-run code; `Diff` (one row per `ChangeKind`, refuse over ask); `Result.ExitCode` (every row of both tables of section 4, every pair of step classes, fleet pools, interrupt over all); the add-on step table (eight rows) and the dependency sort; scale validation; the direction rule and the version-first table; the upgrade and rollback planners from their frozen tests, with `Step` values in place of labels; `noderoll.classify`, `Tracker`, and the mode choice of `Attach`.

**Waits.** Against `awsxtest` with update scripts and the virtual clock, with `Deps.After` as the handshake: each terminal status; not-found before the first sighting, then success; a throttled poll, then success; access denied in the middle (`*UnobservedError`); a response with no update; a cancelled context; the request log shows `DescribeUpdate` only. The scale confirm: sizes and `ACTIVE` together, a mismatch, `DEGRADED`.

**`Run` with scripted hooks.** `Run` is synchronous, so these are ordinary tests through the fakes: a hooks value that records events and answers from a script.

- The order of events for each kind of change, as a golden list.
- Each row of the question table: yes, no, cancel during the question, a hook error before the first submit and at a later phase. Each asserts the mutations in the request log.
- After a yes, the plan is computed again: the world changes while `Ask` blocks (the scale that turns into a scale-down; a new AMI release; a rollback window that closes; a rollback target that changes), and the test asserts the second question, the block, or the refusal.
- `Checkpoint`: `Stop` before the second phase submits nothing more and gives `Stopped`; `Waited` causes a new phase read; a failure after a stop is `Failed`.
- Budgets: the read budget before the first submit; the wait budget from the first submit and from an attachment; neither runs inside `Ask`; deadline against cancel.
- `goleak` in `TestMain`; `-race`.

**Whole-command tests.** Through `cmdtest.NewFake` and a `kubetest.Server`, with goldens for stdout, stderr, and the exit code in the four formats. For each command: success, `--no-wait`, a dry run that would proceed, ask, and be blocked, the `--yes` error, a declined prompt, a busy refusal, a busy target, a failed update, a deadline, an interrupt, attach where the command has it, a lost response, an update with no ID. For `nodegroup update` also: several targets with one failed, `--reroll`, `--force` on a current nodegroup (not rolled), a custom AMI, `-f`, a fleet with a blocked cluster and a failed one. For `cluster upgrade` also: two hops, each `--only`, `-n`, both skip flags with an incompatible skipped add-on, resume after each phase with a nodegroup still `UPDATING`, a degraded nodegroup at the target (exit 5), an add-on that fails (the run stops), a control-plane attach that does not reach the target (the notice). Every machine document validates against its schema.

**The command-order test** of `REWRITE.md` gets the five commands.

**`TestPaired`.** Every scenario gets its `V2` command line. The allowlist follows section 11.

**Old tests that name a past regression in a changing command.** I opened each of these files and name the tests. Every other old test file is triaged in `legacy/PORTING.md` when its code is ported.

| Old test | Protects |
|---|---|
| `services/nodegroup/start_roll_test.go`: `TestStartNodegroupRoll_RetriesKeepOneToken`, `_RefusesEmptyVersion`, `_NewTokenPerCall`, `TestStartVersionUpdate_NoNodegroupVersionStartsNothing` | One token for the retries of a call, a new one per call; no request without a version |
| `services/upgrade/verify_test.go`: `TestUpgradeNodegroups_VerifiesAttachedRoll`, `TestResume_DegradedNodegroupAtTargetIsVerifiedAgain`, `TestResume_UpdatingNodegroupAtTargetIsAttachedAndVerified`, `TestResume_MultiHopVerifiesDegradedNodegroupBeforeTheControlPlane`, `TestRollbackResume_DegradedNodegroupAtTargetStopsTheControlPlane`, `TestExecute_VerifyIssuesAndUnreadNodegroupKeepBoth`, `TestExecute_UnreadVerificationIsIncomplete` | The `Verify` action; issues before unread (5 before 4) |
| `services/upgrade/resume_test.go`: `TestUpgradeNodegroups_AttachesToInFlightRoll`, `TestExecute_PerHopReadinessBlocksLaterHop`, `TestWaitForUpdate_PermanentErrorFailsFast`, `TestWaitForUpdate_NetworkErrorsKeepPolling`, `TestBuildPlan_AddonVersionLookupErrorIsNotIncompatibility`, `TestBuildPlan_CatchUpAddonSkipIsExact` | Resume and attach; the live readiness of a later hop; the transient rule; an unread catalogue is not "incompatible"; exact skip names |
| `services/upgrade/stopped_test.go`: `TestExecute_DeadlineSaysTimedOut`, `TestExecute_CancelSaysInterrupted`, `TestBuildPlan_DeadlineDuringRefreshSaysTimedOut` | Deadline against interrupt, also during the insights refresh |
| `services/nodegroup/scale_wait_test.go`: `TestScale_MaxOnlyBelowCurrentFails`, `TestScale_MinOnlyAboveCurrentFails`, `TestScaleWait_SuccessfulButConfigMismatchErrors`, `TestScaleWait_DesiredWaitsForActive`, `TestScaleWait_MissingUpdateIDErrors`, `TestScaleWait_CancelIsNotATimeout` | The bounds check with its hint; sizes and status together; no ID is not success |
| `services/addons/update_guard_status_test.go`: `TestUpdate_VersionGuardHonorsStatus`, `TestUpdate_UpdatingAtTargetWaits`, `TestUpdateAll_ReappliesDegradedAtTarget` | The eight rows of the add-on step table |
| `monitoring/monitor_test.go`: `TestMonitorUpdates_ParentCancelReturnsErrCancelled`, `TestMonitorUpdates_CancelledUpdateReturnsError` | A cancelled run is not a cancelled update |

**What the fakes still need.** `fleettest`: an update answer with no update ID; an insights refresh that fails and one that never ends; a nodegroup in `DELETING`; an add-on in `DELETING` and in `DELETE_FAILED`; `DescribeUpdate` that answers not-found once for a new ID. `cmdutiltest`: a scripted terminal for whole-command tests. `test/parity`: the `V2` command lines.

### Build order

Each step ends with `task dev:full` green. Where a step has paired scenarios, `TestPaired` passes for every command built so far.

| # | Step | Proof |
|---|---|---|
| 1 | Types: `Plan`, `Step`, the refs, `Plan.Decision`, `Diff`, `Result`, `ExitCode`; the two documents and their schemas; `ChangeFlags`, `RequireYes`, `Confirm` | No paired scenario: no command exists. Table tests, the schema test, the prompt tests. |
| 2 | `WaitUpdate`, `WaitSettled` | No paired scenario. The wait tests against `awsxtest`. |
| 3 | `addon update`: `Busy`, `health.Addons`, `plan` for add-ons, `Run` with the `Event` and `Ask` hooks, a serial phase, `RunChange`, the renderer without the panel, the views | Paired: the 23 `addon-update-*` scenarios |
| 4 | `nodegroup scale`: validation, the scale gate, the scale confirm | Paired: the `nodegroup-scale-*` scenarios |
| 5 | The batch and `nodegroup update`: `StartNodegroupRoll`, `DecideAMIUpdate`, the baseline, the wait together, verification, the Auto Scaling watch, `-f`, `--changelog` | Paired: the `nodegroup-update-*` scenarios but the two of step 6 and the one of step 7 |
| 6 | `noderoll` and the panel: the port, `Attach`, `NodeProgress`, `view.RollPanel`, `--live` | Paired: `nodegroup-update-live-view`, `nodegroup-update-prompt-yes`. The port's own tests carry most of the proof. |
| 7 | Fleet mode: discovery, the fleet plan, budgets per cluster, and the first use of the `Checkpoint` hook, between clusters | Paired: `nodegroup-update-all-clusters`, one scenario. The mixed-result and stop cases are whole-command and scripted-hook tests. |
| 8 | The upgrade engine and `cluster upgrade`: the planner port, the insights refresh, `StopOnFailure`, attach, the `Verify` action, `ConfirmPhases`, the checkpoint between phases | Paired: the `cluster-upgrade-*` scenarios |
| 9 | `cluster rollback`: the planner port, `fleet.LastUpgrade`, direction, the request fields | Paired: the `cluster-rollback-*` scenarios |
| 10 | Close-out: benchmarks (`RollPanel` allocations, `Diff` on a 50-step plan), the livecheck rows, `gen-docs`, `PORTING.md`, the round-trip measurement for section 14 | No paired scenario |

Steps 3 to 6 build no checkpoint, no pause, and no stop: a single-phase change has no place for one. The hook is first called in step 7. This is the order of `REWRITE.md` with the types first and the node observer as its own step.

## 13. Decisions and contract edits

### Decided by the owner on draft 1

| # | Decision |
|---|---|
| D1 | The executor is synchronous: `Run` with three hooks. Phase 4 builds no handle. |
| D2 | A wait that hits `--wait-timeout` exits 4 in all five commands. The read budget running out before anything was submitted exits 1 only when nothing was read, else 4. |
| D3 | A change that was accepted and could not be followed exits 4. |
| D4 | A busy target of `nodegroup update`, `nodegroup scale`, or `addon update` is not changed and not attached to; the others go on; exit 4. |
| D5 | `--force` no longer rolls a nodegroup that is on the latest AMI. |
| D6 | One rule for mixed results over the steps of the whole run, for independent targets (the clusters of a fleet, the targets of `nodegroup update`, the add-ons of `addon update --all`): a failed step beside a step that changed something is a partial failure, exit 4; a run in which every attempted step failed exits 1. A `cluster upgrade` or `cluster rollback` whose phase fails exits 1. |
| D7 | `nodegroup update` confirms once on a terminal. |
| D8 | The parity comparator checks safety obligations; the allowlist is recounted in section 11. |
| D9 | A standalone `addon update` attaches only when the add-on's in-flight update is already to the requested version. Every other in-flight state of a target is the `Busy` row of D4. The engines keep the old engine's attach rules. |

### Open

Nothing is open.

### Contract edits

Accepted on draft 1 and applied in this draft: `--require-healthy` on every changing command and `--skip-verify` on `cluster upgrade` and `cluster rollback` (1); a busy target, as D4 with the exception of D9 (4); one rule for mixed results, as D6 (5); the confirmation before the first change (7); `addon update` flag rules, with no `--force` (8); `--all-clusters` excludes `-c`, positionals, and `--kube-context` (9); `cluster rollback -o plain` (11); the dry-run exit rule gains "and 4 when the plan itself could not read something" (12). Edit 2 became D2. Edit 6 was dropped.

The text of the two edits still to copy into `CONTRACT.md`:

**Edit 3.** A new row of "Behaviour the new CLI does not copy":

| Old behaviour | New behaviour |
|---|---|
| `nodegroup scale` checks only a lone `--min` or `--max` against the current desired size. It sends every other combination to EKS, which refuses sizes that cannot hold, and it sends a request that changes nothing. | `nodegroup scale` checks the sizes before it sends anything: against each other, and against the nodegroup's current sizes for the ones that were not given. Sizes that cannot hold are an error, exit 1, with no request. A lone `--min` or `--max` that excludes the current desired size says to pass `--desired` too. A scale to the current sizes sends nothing and exits 0. |

**Edit 10.** In "Documents", the `Nodegroup` and `Addon` rows of the shapes table lose "plans and runs" from "Used in". After the kinds table, in place of "A plan has an `operation` field and a list of steps per cluster":

> `Plan` has `operation` (`AddonUpdate`, `NodegroupScale`, `NodegroupUpdate`, `ClusterUpgrade`, `ClusterRollback`), `clusters`, `skippedRegions`, and `failures`.
>
> Each entry of `clusters` has `cluster`, `region`, `version`, `targetVersion`, `decision` (`Proceed`, `Ask`, `Blocked`, `NothingToDo`), `steps`, `gate`, `blockers` (each with `kind`, `step`, `message`), `notices`, and `rollback`.
>
> Each step has `phase` (`ControlPlane`, `AddonsBefore`, `Nodegroups`, `AddonsAfter`, `Addons`, `Roll`, `Scale`), `hop`, `kind` (`ControlPlane`, `Addon`, `Nodegroup`, `Scale`), `name`, `action` (`Submit`, `Attach`, `Verify`, `Skip`, `Manual`, `Blocked`), `reason`, `from`, `to`, `toRelease`, `force`, and `sizes` (`before` and `after`, each with `desired`, `min`, `max`).
>
> A step is identified by `region`, `cluster`, `phase`, `hop`, `kind`, and `name` together. No field is a composite ID to parse.
>
> `Run` has `operation`, `status`, `clusters`, and `failures`. Each entry of `clusters` has the fields of a plan cluster, and `status`, `results`, and `verification` (a list of `HealthResult`). `results` has one entry for each step of the plan, in plan order, with the six identity fields and `status` (`NotAttempted`, `Started`, `Succeeded`, `Failed`, `Cancelled`, `InProgress`, `Busy`, `Skipped`, `Blocked`), `attached`, `updateId`, `inFlight`, `startedAt`, `finishedAt`, and `failure`.
>
> `status` of a run and of a cluster is one of `Succeeded`, `NothingToDo`, `Started`, `Refused`, `Declined`, `Stopped`, `Blocked`, `Failed`, `VerifyFailed`, `Incomplete`, `Interrupted`.

Points of `REWRITE.md` that this note changes, with no contract edit: `Run` with hooks in place of `Start` and an `Operation` (D1); the phase and operation state tables move to phase 5 with the handle; `--no-wait` is also allowed for `addon update --all`, which waits for nothing between its submits; `DecideAMIUpdate` loses its `--force` row (D5).

## 14. Changes made while building

The code is the reference where it and the sections above differ. The first two tables cover build steps 1 to 3: the types, the waits, and `addon update`. The tables after them cover step 4, `nodegroup scale`.

### Where the code departs from the note

| Section | Note | Built |
|---|---|---|
| 1 | `Action`, `Reason`, `PhaseKind`, `StepKind`, `BlockerKind`, `RunStatus`, `StepStatus`, and the plan decision are string types of `change` | They are aliases of the `apidoc` enums, so one list of values serves the plan, the result, and the schema. The documents use these enums where the note wrote `string`. |
| 1 | `Blocker` in a document has `kind`, `step`, `message` | `step` is an object with `phase`, `hop`, `kind`, and `name`, the identity fields of a step. It is left out for a blocker about the cluster. |
| 1 | Five request types | `AddonUpdateRequest` and `ScaleRequest` are declared. The other three arrive with their build steps, because their fields need types that do not exist yet. `Request` has three unexported methods: `kind`, `base`, and `cluster`. |
| 1 | `plan(ctx, d, req, refreshInsights)` | `plan(ctx, d, req)`. The fourth argument arrives with the upgrade. Each plan reads with a fresh fetcher (`fleet.Fetcher.Fresh`), because the fetcher of the command remembers the newest add-on version. |
| 1 | The names `Blocked`, `NothingToDo`, `Sizes` are used twice | Go allows one meaning each. The action is `Blocked`, the decision `DecisionBlocked`, the run status `RunBlocked`. The decision is `DecisionNothingToDo`. The kind of difference is `SizesChanged`, with the value `Sizes`. |
| 1 | `Diff` has ten kinds | The same ten. A `Submit` step whose target is now changing to another version is `NowInFlight` too, with its own message. `ChangeKind.Effect()` returns `Continue`, `AskAgain`, or `Refuse`. |
| 1 | A read adds "the latest version of its targets" | `fleet.Need.LatestFor` names the add-ons, so the catalogue reads stay in the wave of the describe calls. `--version` reads the catalogue of its one add-on with `Fetcher.AddonVersions`, one round trip after the snapshot. |
| 2 | "`WaitSettled` keeps polling while the status changes and hands every other answer to `done`", and "done sees every answer" | `done` sees every answer. The scale confirm needs that. `Settled` has `Issues`, and `UpdateProgress` has `Final` for the poll that ended the wait. |
| 2 | An add-on confirm is one `DescribeAddon` | After `WaitUpdate`, yes. After an attach the settled answer of `WaitSettled` is the confirm, with no second read. |
| 3 | "Ours" are the `Submit`, `Attach`, and `Verify` steps | Also a step that the table marked as a busy target (`Skip` with `InFlight`). Without that, the busy rule would refuse the whole run for a target that D4 says to report and pass by. |
| 3 | Step table rows 5, 6, and 7 | Row 7 takes a `DELETING` add-on at any version, and rows 5 and 6 leave it out. D9 says that every in-flight state but the attach case is busy. `health.Addons` keeps its `DELETING` row for a caller that gets there another way. |
| 3 | The lookup of the update in flight | It runs for a `Busy` row of a run, in one place (`runner.inFlight`), whether the plan or the fresh read before a submit found the target changing. A run in which every target is busy goes through its steps too, with no question: it prints the `Run` document with the `Busy` rows, exits 4, and the row and the failure name the type. When the lookup fails, the failure says "an update of unknown type" and the row has no `inFlight`. When EKS lists no update in progress, as for a resource that it deletes, the failure names the status alone. A dry run makes no lookup: its `Plan` document has the `Busy` failure without the type. |
| 4 | `runPhase` has a batch form | Only the serial form is built. `runStep` and `plan` each have one switch that a new kind adds a case to. |
| 4 | "The context ended before the request: `NotAttempted`" | Built. A request that left and got no answer before the run ended is `InProgress` with the reason `Interrupted` or `Timeout`: EKS may have accepted it. |
| 4 | A partial failure | Its run status is `Incomplete`, the name of exit code 4. The failed step still says `Failed`. |
| 5 | `--all` with an add-on that has no version for the cluster | The step is `Manual` with a notice. A named add-on with no version is an error, exit 1, as before. |
| 5 | `--skip-addon` names | A name that is not installed is an error with the close matches. The old command ignored it. |
| 7 | `UpdateProgress` is an event kind and a type | The event constant is `UpdatePolled`, with the value `UpdateProgress`. `NodeProgress`, `Event.Nodes`, and `Event.Skipped` arrive with their build steps. |
| 7 | `Event(PlanReady)` after each plan | For the first plan, and for a later one that differs from the one before. An equal plan would print twice. |
| 7 | `Common.Expect` | A plan with `Expect` that is a plan of the same request counts as confirmed: the run asks no `ConfirmStart`, and asks `PlanChanged` when the first plan differs. With `--yes` the differences are notices. Section 7 has the consent rule. |
| 7 | `Result.Failures` | The failures that belong to no step. `Result.Document()` adds the failures of the steps. `StepResult.VerifyIssue` marks a step that counts as exit 5. An unexported field of `Result` holds what no step carries: the code of a refusal, an answer of no, an interrupt. |
| 7 | `Result.Document()` always returns a document | It returns nil for a run with no plan: an invalid request, a name that does not exist, a cluster that cannot be read. `RunChange` then prints the error and nothing on stdout. `ClusterReadError` and `UnsupportedError` are the typed errors. |
| 7 | `Deps.Kube(ctx, target)` | `Deps.Kube(target)`, the signature of `Factory.Kube`. |
| 7, 10 | `RunChange`, `PrintPlan`, and `Renderer` are in `cmdutil`, and the renderer is in `view` | They are in `internal/cmd/changecmd`, with `Deps` and `Common`. `changecmd` stays a package of its own after the layering fix below: `cmdutil` may not import the fleet, which `change` imports, and `RunChange` needs `fleetcmd.ClusterError`, whose package imports `cmdutil`. `view` has `PlanView`, `RunView`, `PlanLines`, and `StepLine` over the documents, and imports `apidoc` alone. `cmdutil` has `ChangeFlags`, `AddChangeFlags`, and `Validate`. |
| 8 | The budgets use the injected clock | A budget is a context with a cause. `Run` checks it against `Deps.Now` between two calls, and a wait checks it after each answer of a poll, and no goroutine is needed for that. A timer ends a call that hangs: `Deps.AfterFunc`, or the real clock without one. `Deps.After` is the pause between two polls. Section 8 has the rule for the last poll. |
| 8 | `Read` covers the reads up to the first submit | Each plan starts a new read budget. It stops during the wait of a step and runs again for the read and the submit of the next add-on. |
| 10 | `AddChangeFlags(cmd, f, waitDefault, noWait, force)` | It takes one more argument, the words for what `--wait-timeout` bounds, such as "each add-on". |
| 10 | "Each command needs a line in the exit-code help" | The new tree has no exit-code help yet. The `Long` text of `addon update` lists its codes. There is no mutating-command-order test yet either; `TestUpdate_UsageErrors` checks the order for this command. |
| 12 | The fakes get an update answer with no ID | The fake packages were not edited. A test puts an HTTP front before `awsxtest` that answers `{}` for the one request. `cmdtest.Fake.UseWorldClock` is new: it makes the clock of the world the clock of the command. |
| 12 | No real timers | None. `Deps.AfterFunc` is part of the clock, beside `Now` and `After`. The tests of `change` set it, and `TestMain` makes a real timer a panic. `cmdtest.Fake.UseWorldClock` sets `Factory.AfterFunc` for the command tests. A read that hangs is ended by the timer of the read budget, which the test holds and fires once the fake reports that the read hangs. |

### Where `internal/health` differs from what the note assumed

| Assumed | Real |
|---|---|
| `health.Addons(targets)` has three rows | It has two more rows. A target that is `CREATING` or `UPDATING` fails like `DELETING`. A target that the snapshot did not read is `Unreadable`, and the check fails closed. |
| A check is an interface (`REWRITE.md`) | `health.Check` is a struct with a `Run` function, so `Addons` returns a value. |
| The gate of a skipped check set | `health.Gate(nil, Policy{Skip: true})` returns `Go` with `Skipped` set. The plan keeps no gate in its document then, and adds a notice. |
| `Verdict.Question()` is the prompt | It starts in lower case. `Run` makes the first letter upper case for the question text. |
| `health` is below `cmdutil` | It is, since the layering fix below. `health` took the exit codes from `cmdutil` before it. |
| `Worse` orders outcomes as a summary does | Since `18fc312` it ranks `Unreadable` above `Fail`. `Diff` reports what `Worse` returns. |
| `health.Run` needs a Kubernetes client | A check set with no check that needs Kubernetes adds no `Kubernetes` result, as section 3 says. An add-on update asks nothing about Kubernetes access. |

### Build step 4, `nodegroup scale`: where the code departs from the note

| Section | Note | Built |
|---|---|---|
| 1 | A scale step has an action and a reason | A `Submit` scale step has no reason: none of the reasons fits. `From` and `To` are empty, and `Sizes` has the sizes before and after. The views print the sizes in the `from` and `to` columns. |
| 1 | Instance-type availability "for each roll or scale-up target" | It runs for a scale that raises the desired size and would be submitted, also with `--skip-health-check`: it is beside the gate. It makes one `DescribeSubnets` call and one paged `DescribeInstanceTypeOfferings` call with every type in the filter. The old code made one offerings call per type. `fleet.Nodegroup` has `Subnets` for it. |
| 2 | The scale confirm: "`ACTIVE` is done and `DEGRADED` or a failed status is an error" | `DEGRADED`, `CREATE_FAILED`, `DELETE_FAILED`, and `DELETING` are errors. `UPDATING` and `CREATING` go on. |
| 3 | The gate of a kind uses the Kubernetes client of the command | `change` resolves it from `Deps.Kube` itself, and only for a scale that lowers the desired size. It does not use `fleetcmd.ConnectKube`, which is in the command layer. The `Kubernetes` result of the gate carries the reason, so the hint of a kubeconfig mismatch is not printed. |
| 3 | The busy rule | A target that is already changing is `Busy` before its sizes are checked. Sizes that cannot hold against a nodegroup that is changing are not an error: the sizes are not settled. |
| 4 | The submit table has no row for a scale | Two rows. The fresh read shows the sizes of the plan's goal: `Skipped`, with a notice, and no request. The fresh read shows other sizes than the plan started from: `Failed`, "changed to ... since the plan", and no request, because the gate ran for another direction. |
| 5 | Validation before any AWS call | One more check: `--max` must be 1 or more, the limit of EKS. `change.ValidateSizes` is the one function for the command and for the request. |
| 5 | The messages of the plan checks | A size that does not fit a bound that was not given names the flag to add: "--desired 9 is above the current max size 3; pass --max too". |
| 7 | "`--force` goes past a scale-down blocker with a warning" | The line renderer prints `warning: --force goes past <check>: <message>` for each reason in `Verdict.Forced`, for every command, also with `--quiet`. |
| 7 | The start question | "Scale nodegroup web in prod from desired 2, min 1, max 3 to desired 3, min 1, max 3?" |
| 8 | `Limits.StepWait` | Removed. Every step has its own wait budget, and no code read the field. |
| 10 | Exit-code help, and the command-order test | As for `addon update`: the `Long` text lists the codes, and `TestScale_UsageErrors` checks the order of complete, validate, and the `--yes` check. |
| 11 | "The first paired run of each command settles" the allowlist | Run for the 24 `addon-update-*` and the 24 `nodegroup-scale-*` scenarios. Every `V2` command line also has `--poll-interval 20ms`, so a wait is short. Seven entries were removed: the ones for `addon-update-attach`, `addon-update-attach-failed`, and `addon-update-busy` were written before D9 and expected a `Busy` result where the command now attaches. Four entries were added for `nodegroup-scale-above-max` and `nodegroup-scale-min-above-max` (`mutations` `[]`, `refusedBeforeMutation` true), under the contract row "`nodegroup scale` does no size check of its own". `nodegroup-scale-check-pdbs-no-kube` runs with `--yes --no-wait`, the flags of the old command line less `--check-pdbs`. Four entries pin what the row "Without Kubernetes access" allows: the scale is sent, the nodegroup has the new size, the run is not refused, and the exit code is 0. A `V2Checks` entry reads the accepted reason from the document. The refusal with `--require-healthy` is the command test `down-no-kube-require-healthy`. `nodegroup-scale-pdbs-unreadable-forced` needs no entry: both binaries exit 4. `addon-update-attach-failed` has a `V2Checks` entry for the word "failed" only: the wait follows the add-on, so no update ID is known on that path. |
| 12 | The fakes | No fake package was edited for the scale. |

### The layering fix: `health` and `change` below the command layer

`health` and `change` imported `cmdutil`, and through it `view`, `contexts`, and `build`. They now import no package of the command layer. The import tests (`imports_test.go` in `apidoc`, `awsx`, `fleet`, `health`, and `change`) pin the refresh packages that each may import, and the `...test` packages that only its tests may import.

| What | Was | Is | Why |
|---|---|---|---|
| The six exit codes | Constants of `cmdutil` | `internal/exitcode` (`OK`, `Failure`, `NeedsAttention`, `Blocked`, `Incomplete`, `VerifyFailed`), a package that imports nothing. `cmdutil.ExitOK` and the other five keep their names and take their values from it. | A gate, a verification, a plan, and a run each return a code. That was all that `health` took from `cmdutil`, and most of what `change` took. |
| The error for an add-on or a nodegroup that the cluster does not have | `change` called `cmdutil.NotFoundError`, which writes the close names and the command that lists them | `change.NotFoundError`, a typed error with the names of the cluster. `changecmd` turns it into the same text with `cmdutil.NotFoundError`, as it does for `ClusterReadError`. | The suggestion and the command to run are words of the command line. A front end without one, such as the TUI, gets the names. |
| The error of a later plan | `RunChange` returned it as it was | It goes through the same conversion as the error of the first plan | One place for the words of a plan that could not be computed. |
| `Deadline`, the context that a test expires by hand | `cmdutiltest` | `awsxtest` | A test of `health` used it, and a test below the command layer may not import `cmdutiltest`. |

`internal/cmd/changecmd` is still needed: it is the one place that knows both a change and a terminal, and neither `cmdutil` (no fleet) nor `fleetcmd` (read commands) is that place.

### The review pass: consent, the diff, and fresh state

| What | Was | Is |
|---|---|---|
| Consent | A run with a nil `Ask`, no `--yes`, and no `Expect` started with no answer | It is refused, exit 1. Section 7 has the rule. |
| `Expect` | Any plan counted as confirmed | `sameRequest` compares its request with the request before any read. A mismatch is `ErrOtherRequest`. |
| `Diff` | It compared the action, `To`, `ToRelease`, and the sizes before | It compares every field of a step with work. A changed goal of a scale is `Sizes` and asks. The new kind `Step` asks for a changed action, reason, `From`, or `Force`. A step that now attaches keeps the one notice `NowInFlight`. `TestDiff_EveryStepField` fails for a field of `Step` or `ScaleSizes` with no row. |
| A scale target that is not `ACTIVE` | Only a scale-down ran the `Nodes` check, and `DEGRADED` was sent a request | Row 2 of the scale step table blocks it for every direction, with a `NotReady` blocker, exit 3. The fresh read applies the same row: the step is `Blocked`. No flag goes past it, also not `--skip-health-check`. `health.Nodes` was not used for a scale-up: `--skip-health-check` would skip it while the fresh read still blocks, and its estimate says "no Kubernetes access". |
| In flight, for a scale target | Every status but `ACTIVE` and `DEGRADED` | `CREATING`, `UPDATING`, `DELETING`. `CREATE_FAILED` and `DELETE_FAILED` are not `ACTIVE`, so they block. The busy rule counts a `Blocked` step as one of ours. |
| The fresh read of an add-on | The step table alone | Also the policy on the health that it finds, by `regate`. After a person answers, the add-on is read again. |
| A submit after a wait | No read of the cluster | `recheckAddons` reads the snapshot again before each add-on submit that follows a wait. It applies the busy rule with the steps of the phase as ours, and the gate over the add-ons that remain. An add-on that is changing or unread in that snapshot is left to its own fresh read. A blocked or failed add-on does not stop the next one. |
| A question during the run | None existed | Yes goes on. No ends the run as `Declined`, exit 1, and the later steps are `NotAttempted`. A hook error ends it as `Failed`, and `Run` returns the error. |
| A step blocked during the run | No such result | `Blocked`, exit 3, with a failure that says why. `--require-healthy` over a warning gives 3 here, the code of a blocked step, and not 2. |
| Sizes that EKS did not return | `aws.ToInt32` made them 0 | `describe` returns an error for a scale read with no scaling config or a missing size. A wait polls through it. The fresh read fails, and nothing is sent. `fleet.Nodegroup.SizesKnown` carries the same fact in the snapshot, and the plan has an `Unread` blocker and a failure without it, exit 4. |
| The last poll of a wait | The pause could pass the deadline, and the answer after it still counted | Section 8. |
| Not built yet | `NodegroupUpdate`, `ClusterUpgrade`, `ClusterRollback`, `RollbackTarget`, `Common.Nodes`, `Common.SkipVerify`, the `Checkpoint` hook and type, `ConfirmPhase`, `Limits.StepWait`, `Phase.Serial`, `Phase.StopOnFailure` | Removed. Each returns with the change that uses it. `Question.Phase` is `Question.Step`. |
| Kept | | `Kind.independent`, for the two kinds. The engine column of `Busy`, which a test kind reaches. The aliases of the `apidoc` enums, which are the published values. |
