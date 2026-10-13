# Phase 4 design note: changes

Status: draft 1, for review. It belongs to `REWRITE.md` and stays inside `CONTRACT.md`, except for the contract edits that section 14 proposes. Phase 3 (`docs/design/phase-3-health.md`) is the source for the gate, the verdict, and the verification checks that this phase calls. Section 15 is empty until code changes the design.

Scope: `internal/change` (plans, waits, the batch, add-on update, scale, the upgrade engine, rollback, the `Operation` handle, claims, the busy rule), `internal/noderoll` and the roll panel in `internal/view`, the `Plan` and `Run` documents, and the commands `addon update`, `nodegroup scale`, `nodegroup update` (with `--all-clusters`), `cluster upgrade`, and `cluster rollback`.

Out of scope: the TUI. `Operation` is specified in full here so that phase 5 needs no change to it.

How the old behaviour was read: the old code under `legacy/`, and the 152 recorded traces in `test/parity/testdata/legacy`. A claim about the old binary names a file and line, or a trace. I could not run the new binary, so every statement about a v2 trace is a prediction from the design. Section 11 marks the predictions that the first paired run must confirm.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | One function builds a plan. `NewPlan` returns it for a dry run. `Start` calls the same function inside the operation. There is no dry-run code path. |
| 2 | `Start` takes the request, not a plan. A plan that a person reviewed earlier is an optional input (`StartOptions.Expect`) that the operation compares against. A CLI run with `--yes` reads the cluster once. This departs from the `Start(ctx, plan)` signature in `REWRITE.md`. |
| 3 | One exported way to run a change: the `Operation` handle. Inside it the executors are plain synchronous functions on one goroutine. The CLI and the TUI use the same handle. |
| 4 | Every change is a list of phases, and every phase is a batch: recheck each step, submit in order, wait together, confirm. `nodegroup update` is one batch of N. `cluster upgrade` is a sequence of batches of one. |
| 5 | Two waits. `WaitUpdate` polls `DescribeUpdate` and nothing else. `WaitSettled` polls the resource. A separate confirm read follows a successful wait. |
| 6 | One busy rule for every phase of every command: nothing else in the cluster is changing. A phase attaches to an in-flight change of its own target. |
| 7 | Each phase starts with one read of the whole cluster. The busy rule, the step recheck, attach detection, and the gate input all come from that read. |
| 8 | Attach follows the resource status (`WaitSettled`), as the old engine does. Only `Observe` looks up an update ID. |
| 9 | A wait deadline exits 4 and an interrupt exits 1. A batch with some steps done and some failed exits 4. A run in which every attempted step failed exits 1. |
| 10 | The operation owns its deadline and pauses it while a question is open or the run is paused. |
| 11 | The node observer and the Auto Scaling watch publish events into the operation log. The CLI draws the panel from events. Nothing below the command layer writes to the terminal. |
| 12 | `StartNodegroupRoll`, the `DecideAMIUpdate` table, `noderoll.Tracker`, the upgrade planner, and the rollback planner are ported with their behaviour and tests frozen. |

## 1. Plans

### Requests

A request says what the user asked for. Each command builds one in `complete`.

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
	NoWait     bool          // --no-wait
	SkipVerify bool          // --skip-verify
}

type AddonUpdateRequest struct {
	Common
	Target
	Addons  []string // exact names; empty with All
	All     bool     // --all
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
	Filters    []fleet.Filter // -f; applied to nodegroup names
	Reroll     bool           // --reroll
	// Fleet is set for --all-clusters. Target is then empty.
	Fleet *FleetScope
}

type FleetScope struct {
	Regions []string       // -r; empty means the default sweep
	Filters []fleet.Filter // -f keys that name clusters
}

type UpgradeRequest struct {
	Common
	Target
	To             fleet.Version // --to, required
	Only           []Part        // --only: ControlPlane, Addons, Nodegroups
	Nodegroups     []string      // -n, exact names
	SkipAddons     []string      // --skip-addon
	SkipNodegroups []string      // --skip-nodegroup, exact names
	SkipInsights   bool          // --skip-insights-check
}

type RollbackRequest struct {
	Common
	Target
	SkipNodegroups  []string
	SkipInsights    bool
	RollbackTimeout time.Duration // --rollback-timeout; 0 leaves the EKS default
}
```

### The plan

```go
// Plan is what a change would do, computed from one read of live state.
type Plan struct {
	Kind     Kind
	Request  Request
	Clusters []ClusterPlan // one entry, except in fleet mode
	// SkippedRegions are the regions a default sweep left out (fleet mode).
	SkippedRegions []string
	Failures       []apidoc.Failure
	At             time.Time
}

type ClusterPlan struct {
	Target
	// From and To are the control-plane versions of an upgrade or a
	// rollback. Both are zero for the other kinds.
	From, To fleet.Version
	Phases   []Phase
	// Gate is the verdict of the health gate, and Checks the results it
	// was computed from. Both are zero when the plan has no step to run.
	Gate   health.Verdict
	Checks []health.Result
	// Blockers are the refusals that do not come from the gate: a busy
	// cluster, a blocked readiness step, a rollback that is not available.
	Blockers []Blocker
	Notices  []string
	Failures []apidoc.Failure
	// Rollback is the rollback window, for a rollback plan.
	Rollback *fleet.Rollback

	snapshot *fleet.Snapshot // what the plan was computed from
}

type BlockerKind string

const (
	Busy        BlockerKind = "Busy"        // another change is in flight
	NotReady    BlockerKind = "NotReady"    // insights, version skew
	Unavailable BlockerKind = "Unavailable" // no rollback window, AL2 past 1.32
	Unread      BlockerKind = "Unread"      // a read the plan needs failed
)

type Blocker struct {
	Kind    BlockerKind
	Step    StepID // zero when the blocker is about the cluster
	Message string
}

// PhaseID names a phase within one cluster plan. It is stable between two
// plans of the same request: "roll", "addons", "scale",
// "1.33/control-plane", "1.33/addons-before", "1.33/nodegroup/ng-a",
// "1.33/addons-after", "rollback/nodegroup/ng-a", "rollback/addons",
// "rollback/control-plane".
type PhaseID string

type PhaseKind string

const (
	ControlPlanePhase PhaseKind = "ControlPlane"
	AddonsPhase       PhaseKind = "Addons"
	RollPhase         PhaseKind = "Roll"
	ScalePhase        PhaseKind = "Scale"
)

type Phase struct {
	ID    PhaseID
	Kind  PhaseKind
	Hop   fleet.Version // the hop target of an upgrade phase; zero otherwise
	Steps []Step
}

// StepID is the phase and the resource: "roll/ng-a",
// "1.33/addons-before/vpc-cni", "1.33/control-plane/prod".
type StepID string

type StepKind string

const (
	ControlPlaneStep StepKind = "ControlPlane"
	AddonStep        StepKind = "Addon"
	RollStep         StepKind = "Nodegroup"
	ScaleStep        StepKind = "Scale"
)

type Action string

const (
	Run     Action = "Run"     // submit an update
	Attach  Action = "Attach"  // the target is already changing; wait for it
	Skip    Action = "Skip"    // nothing to do; Reason says why
	Manual  Action = "Manual"  // left to the operator; Reason says why
	Blocked Action = "Blocked" // cannot run; the plan has a Blocker for it
)

type Reason string

const (
	AMIOutdated     Reason = "AMIOutdated"
	AMIUnknown      Reason = "AMIUnknown"
	Reroll          Reason = "Reroll"
	Forced          Reason = "Forced"
	VersionBehind   Reason = "VersionBehind"
	PreRoll         Reason = "PreRoll"         // kubelet skew catch-up
	Repair          Reason = "Repair"          // DEGRADED or a failed state at the target version
	Unsettled       Reason = "Unsettled"       // at the target, not ACTIVE: wait and verify
	AlreadyLatest   Reason = "AlreadyLatest"
	AlreadyAtTarget Reason = "AlreadyAtTarget"
	AlreadyUpdating Reason = "AlreadyUpdating"
	CustomAMI       Reason = "CustomAMI"
	SkippedByFlag   Reason = "SkippedByFlag"
	OutOfScope      Reason = "OutOfScope" // --only, -n
	NoChange        Reason = "NoChange"   // scale to the current sizes
)

type Step struct {
	ID     StepID
	Kind   StepKind
	Target string // nodegroup or add-on name; the cluster name for the control plane
	Action Action
	Reason Reason
	// From and To are the pinned versions. A roll pins the Kubernetes
	// version in To and records the AMI release it expects in ToRelease.
	// An add-on pins the add-on version. The control plane pins the minor.
	From, To  string
	ToRelease string
	// Sizes are the sizes of a scale step.
	Sizes *ScaleSizes
	// Force is sent to UpdateNodegroupVersion (roll) or UpdateClusterVersion
	// (rollback with --skip-insights-check).
	Force bool
}

type ScaleSizes struct {
	Before, After Sizes // Desired, Min, Max
	// Sent are the fields the request carries: only the ones the user gave.
	Sent struct{ Desired, Min, Max *int32 }
}
```

`Plan.Decision()` folds the parts into what the run would do:

| Condition, first match | Decision | Exit of a dry run |
|---|---|---|
| A `Blocker`, or `Gate.Decision` is `Block` | `Blocked` | 4 when the plan or the gate has a failure, else 3 |
| No step with `Run` or `Attach` | `NothingToDo` | 4 with a failure, else 0 |
| `Gate.Decision` is `Ask`, or the gate went past reasons with `--yes` or `--force` | `Ask` | `Gate.Exit`: 4 or 2 |
| Otherwise | `Proceed` | 4 with a failure, else 0 |

The exit of a dry run is `max(Gate.Exit, 3 if blocked, 4 if failures)`. It ignores `--yes`, as phase 3 requires.

### One function

```go
// Deps is what the package needs from the command. Tests point it at the
// fake world.
type Deps struct {
	AWS    *awsx.Provider
	Fetch  *fleet.Fetcher
	Kube   func(ctx context.Context, t kube.Target) (*kube.Client, error)
	Now    func() time.Time
	After  func(time.Duration) <-chan time.Time
	Claims *Claims // nil in the CLI: one process, one operation
}

// NewPlan reads live state and returns the plan for req. It sends no
// mutating request. A dry run prints the result.
func NewPlan(ctx context.Context, d Deps, req Request) (*Plan, error)
```

`NewPlan` and the `Preparing` state of an operation call the same unexported `plan(ctx, d, req, mode)`. The one difference between the two callers is `mode.refreshInsights`: a real `cluster upgrade` starts an insights refresh and a dry run does not. That is the old behaviour (`insights.go:220-259`) and the one request `CONTRACT.md` lets a blocked run send.

What `plan` does for one cluster, in order:

1. Fetch the snapshot. `Need{Nodegroups: true, Addons: true}` for every kind, because the busy rule reads all three resource types. A roll adds `AMI` and `InstanceIDs`. An add-on update adds the latest version of its target add-ons. An upgrade adds `InstanceIDs`, `Support`, and the add-on versions for the live version and for each hop. A rollback adds `InstanceIDs`, `Updates`, and the add-on versions for the target.
2. Apply the busy rule (section 3). A busy cluster is a `Busy` blocker.
3. Derive the steps (sections 4 to 6).
4. Run the gate with the check set of the kind (section 3) and the targets of the steps with `Run`. A plan with no such step runs no gate.
5. For a nodegroup step with `Run`, read instance-type availability (`DescribeSubnets`, `DescribeInstanceTypeOfferings`). A problem is a notice. A failed read is dropped, as in the old command (`availability.go:36-115`).

Steps 4 and 5 start together.

### Revalidation

`Preparing` computes a plan and compares it with the previous one whenever a previous one exists: the plan in `StartOptions.Expect`, or the plan this operation showed before a person answered a question. A run with `--yes` and no `Expect` has no previous plan and compares nothing.

```go
// Diff compares the plan a person saw with the plan computed now.
func Diff(prev, cur *Plan) []Change

type Change struct {
	Step    StepID
	Kind    ChangeKind
	Message string // "ng-a: AMI release 1.32.0-20260101, was 1.32.0-20251201"
}
```

| `ChangeKind` | What changed, by `StepID` | Effect |
|---|---|---|
| `Done` | `Run` or `Attach` became `Skip` | Go on, with a notice |
| `InFlight` | `Run` became `Attach` | Go on, with a notice |
| `RollTarget` | A roll step's `To` or `ToRelease` differs, or its `Reason` moved between `AMIOutdated` and `Reroll` | Refuse: `Refused`, exit 3 |
| `Version` | An add-on step's `To` differs, or the control-plane target differs | Ask |
| `Added` | A step with `Run` that the previous plan did not have, or a `Skip` that became `Run` | Ask |
| `Removed` | A step that is gone and is not `Done` | Ask |
| `NowBlocked` | A new `Blocker`, or a step became `Blocked` | Refuse: `Refused`, exit 3 or 4 |
| `GateWorse` | `health.Worse(prev.Checks, cur.Checks)` is not empty | The pre-phase table of phase 3, section 4 |

The roll row is the rule in `REWRITE.md`: a changed nodegroup version refuses. The old TUI did the same (`tui/live/roll.go:387-392`). The ask rows are what the old TUI did for upgrade steps (`upgrade.go:220-225`). The old `cluster rollback` refused on any changed step (`rollback.go:698-727`); it now asks, like the upgrade. When several rows apply, a refuse wins over an ask.

How an ask is answered: a person answers through `Answer`. With `--yes` the policy accepts, and the changes go in the run's notices. Without a way to ask, the operation refuses.

After a person answers, `Preparing` plans again, because the answer took time. It stops asking when a plan equals the one before it. After three plans in a row that each need a new question, the operation refuses with "the cluster keeps changing".

### Documents

One `Plan` kind and one `Run` kind for all five commands and for fleet mode.

```go
package apidoc

type Operation string // AddonUpdate, NodegroupScale, NodegroupUpdate, ClusterUpgrade, ClusterRollback

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
	Blockers      []Blocker    `json:"blockers"`
	Notices       []string     `json:"notices"`
	Rollback      *Rollback    `json:"rollback,omitzero"`
}

type Blocker struct {
	Kind    string `json:"kind"` // Busy, NotReady, Unavailable, Unread
	Step    string `json:"step,omitzero"`
	Message string `json:"message"`
}

type PlanStep struct {
	ID        string      `json:"id"`
	Phase     string      `json:"phase"`
	Kind      string      `json:"kind"`   // ControlPlane, Addon, Nodegroup, Scale
	Target    string      `json:"target"`
	Action    string      `json:"action"` // Run, Attach, Skip, Manual, Blocked
	Reason    string      `json:"reason,omitzero"`
	From      string      `json:"from,omitzero"`
	To        string      `json:"to,omitzero"`
	ToRelease string      `json:"toRelease,omitzero"`
	Force     bool        `json:"force,omitzero"`
	Sizes     *ScaleSizes `json:"sizes,omitzero"` // before, after: desired, min, max
	// Nodegroup and Addon are the resource as the plan read it, in the
	// shared shapes.
	Nodegroup *Nodegroup `json:"nodegroup,omitzero"`
	Addon     *Addon     `json:"addon,omitzero"`
}

type RunStatus string // see section 7

type Run struct {
	Operation Operation    `json:"operation"`
	Status    RunStatus    `json:"status"`
	Clusters  []ClusterRun `json:"clusters"`
	Failures  Failures     `json:"failures"`
}

type ClusterRun struct {
	ClusterPlan
	Status       RunStatus      `json:"status"`
	Results      []StepResult   `json:"results"`
	Verification []HealthResult `json:"verification"`
	// Resume is the command line that goes on from here, for a run that
	// stopped early.
	Resume string `json:"resume,omitzero"`
}

type StepStatus string // NotAttempted, Started, Succeeded, Failed, Cancelled, InProgress, Skipped, Blocked

type StepResult struct {
	Step       string     `json:"step"` // PlanStep.ID
	Status     StepStatus `json:"status"`
	Attached   bool       `json:"attached,omitzero"`
	UpdateID   string     `json:"updateId,omitzero"`
	StartedAt  time.Time  `json:"startedAt,omitzero"`
	FinishedAt time.Time  `json:"finishedAt,omitzero"`
	Failure    *Failure   `json:"failure,omitzero"`
	// Nodegroup and Addon are the resource as the confirm read saw it.
	Nodegroup *Nodegroup `json:"nodegroup,omitzero"`
	Addon     *Addon     `json:"addon,omitzero"`
}
```

Rules:

- A dry run prints `Plan`. A run that was refused before any update was submitted prints `Plan` too, with the blockers. Every other run prints `Run`. `CONTRACT.md` says the same.
- `ClusterRun.Results` has one entry for each step of the plan, in plan order. A step that was never reached is `NotAttempted`.
- `Gate.Accepted` and `Gate.Forced` record what `--yes` and `--force` went past, as phase 3 defines them.
- `Run.Failures` is the union of the plan failures, the gate failures, and the step failures.
- The mappers allocate every list, so an empty one encodes as `[]`.

The human view of both documents is a `view.Description`, so `-o plain` is `FIELD`/`VALUE` rows from the same sections. Field names are `steps.<id>.action`, `results.<id>.status`, `gate.decision`, and so on.

## 2. Waits

Two functions. Neither prints. Both take `awsx.PollClients`, so each call is one attempt and the loop owns the retry.

```go
type WaitOptions struct {
	Interval time.Duration                        // --poll-interval
	After    func(time.Duration) <-chan time.Time // Deps.After
	// Progress receives one call per poll per resource. It may be nil.
	Progress func(UpdateProgress)
}

type Resource struct {
	Target
	Kind StepKind // ControlPlaneStep, AddonStep, RollStep, ScaleStep
	Name string   // nodegroup or add-on; empty for the control plane
}

type UpdateProgress struct {
	Resource  Resource
	UpdateID  string
	Status    string // the EKS update status, or the resource status in WaitSettled
	Polls     int
	PollError string // the last transient poll error, or ""
}

// WaitUpdate polls DescribeUpdate for one update until it ends.
func WaitUpdate(ctx context.Context, c awsx.PollClients, r Resource, updateID string, o WaitOptions) error

// Settled is what WaitSettled found.
type Settled struct {
	Status  string
	Version string // the Kubernetes version, or the add-on version
	// Met says that the postcondition holds: the resource is at the wanted
	// version and healthy.
	Met bool
}

// WaitSettled polls the resource until it no longer changes.
func WaitSettled(ctx context.Context, c awsx.PollClients, r Resource, want string, o WaitOptions) (Settled, error)
```

### Rules for both

| Point | Rule |
|---|---|
| First poll | At once. Then one poll after each `Interval`, timed by `After`. |
| A poll error that `awsx.IsPermanentAPIError` reports (access denied, not found, validation) | Stop. Return `*UnobservedError` wrapping it. The update may still be running. |
| Any other poll error (throttling, 5xx, network, an attempt timeout) | Keep polling. Call `Progress` with `PollError` set. |
| A response with no update or no resource in it | The same as a transient error (`monitor.go:273-276`). |
| The context ends | Return `ctx.Err()` wrapped with the last status seen. The function does not decide between deadline and interrupt. The operation does (section 8). |
| Events | The functions emit none. The batch turns `Progress` calls into `UpdateProgress` events, which the log coalesces by resource. |
| Reads | `WaitUpdate` calls `DescribeUpdate` only. `WaitSettled` calls the describe of its resource only. |

The last row is a parity rule. The suite compares the distinct answers a binary saw after its last mutation. The old waits for an update ID never describe the resource while the update is in flight (trace `nodegroup-update-yes`: two `DescribeUpdate` answers, then one `DescribeNodegroup`). A wait that also polled the resource would add an `UPDATING` answer to the trace.

### `WaitUpdate`

| Update status | Result |
|---|---|
| `InProgress` | Keep polling |
| `Successful` | `nil` |
| `Failed` | `*UpdateError{Status, Errors}` with the error codes, messages, and resource IDs of the update |
| `Cancelled` | `*UpdateError` with `Status: Cancelled` |
| A status this build does not know | Keep polling, and report it through `Progress` |

### `WaitSettled`

| Resource | Keep polling | Settled, `Met` true | Settled, `Met` false | Terminal error |
|---|---|---|---|---|
| Control plane | `UPDATING`, `CREATING`, `PENDING` | `ACTIVE` at `want` | `ACTIVE` at another version | `FAILED`, `DELETING`, not found |
| Add-on | `UPDATING`, `CREATING` | `ACTIVE` at `want` | `ACTIVE` at another version | `DEGRADED`, `UPDATE_FAILED`, `CREATE_FAILED`, `DELETE_FAILED`, `DELETING`, not found |
| Nodegroup (roll) | `UPDATING`, `CREATING` | `ACTIVE` at `want` or newer | `ACTIVE` at an older version, `DEGRADED`, `CREATE_FAILED`, `DELETE_FAILED` | `DELETING`, not found |
| Nodegroup (scale) | `UPDATING`, `CREATING` | `ACTIVE` | `DEGRADED`, `CREATE_FAILED`, `DELETE_FAILED` | `DELETING`, not found |

Sources: `controlplane.go` `waitForClusterActive`, `version_analyzer.go:222-256` `WaitUntilActive`, and the nodegroup wait in `nodegroups.go` (`waitForNodegroupSettled`).

A nodegroup that settles in `DEGRADED` is not an error of the wait. The caller gets `Met: false`, and the gate before the roll judges the nodegroup: the `Nodes` check fails a target that is not `ACTIVE` (phase 3). Trace `cluster-upgrade-nodegroup-degrades-mid-run` pins this.

A terminal error is `*SettleError{Status, Issues}`.

### After a wait

The step's confirm read follows a wait that succeeded. It is one describe of the resource through the read clients.

| Step | Confirm read | Postcondition | Not met |
|---|---|---|---|
| Add-on | `DescribeAddon` | Version equals `To` | The step fails: "reported Successful, but the add-on is at X" (`version_analyzer.go:318`) |
| | | Status `ACTIVE` and no health issue | The step succeeds and the run has a verification issue: exit 5 (old `CompletedWithIssues`) |
| | The read fails | | The step succeeds with a failure: exit 4 (old `Unverified`) |
| Control plane | `WaitSettled`, which polls `DescribeCluster` | `ACTIVE` at `To` | The step fails: "control plane reports version X" (`controlplane.go`) |
| Scale | `DescribeNodegroup` | Each size that was sent equals the request | The step fails (`nodegroup_scale.go:229-231`) |
| | | Status `ACTIVE` when a desired size was sent | `WaitSettled` until it settles |
| Roll | None in the wait. The `NodegroupActive` check of the verification reads the nodegroup. | | |

No confirm read follows a wait that failed, timed out, or was interrupted. The old traces show none (`nodegroup-scale-failed`, `nodegroup-update-deadline`).

### A missing update ID

A submit that EKS accepts without an update ID in the answer goes to `WaitSettled` with the step's `To`. The old code had three answers: an error for add-ons and scale (`version_analyzer.go:264`, `nodegroup_scale.go:156`), a failed row in `nodegroup update` (`actions_update.go:970-973`), and "treat as done" in the upgrade engine (`nodegroups.go`, `updateID == ""`). The fake world always returns an ID, so no trace decides this.

## 3. The busy rule, claims, and the gate set

### Busy

One rule, applied to the phase read before every phase of every command.

```go
// InFlight is one change the cluster has in progress.
type InFlight struct {
	Resource Resource
	Status   string
}

// Busy returns the changes in flight that are not on a target of the
// phase. An empty result means the phase may go on.
func Busy(snap *fleet.Snapshot, targets []Resource) []InFlight
```

| Resource | In flight when its status is | It is a target of the phase | It is not a target |
|---|---|---|---|
| Cluster | Anything but `ACTIVE` | Control-plane phase and `UPDATING`: attach. Other statuses: refuse. | Refuse |
| Nodegroup | Anything but `ACTIVE` and `DEGRADED` | `UPDATING` or `CREATING`: the step becomes `Attach` (upgrade, rollback) or `Skip` with `AlreadyUpdating` (`nodegroup update`). `DELETING`: refuse. | Refuse |
| Add-on | `CREATING`, `UPDATING`, `DELETING` | `UPDATING` or `CREATING`: attach. `DELETING`: refuse. | Refuse |

A `DEGRADED` nodegroup is not busy. The gate judges it.

The statuses are the old `CheckBusy` (`services/cluster/busy.go:64-121`). `nodegroup scale` has no attach: its target counts as busy, as before (`actions_scale.go:104-112`, trace `nodegroup-scale-busy`).

A snapshot whose cluster, nodegroup, or add-on group is not `Collected` cannot prove that nothing is changing. It is an `Unread` blocker, exit 4.

What a refusal is, by where it happens:

| Where | Result | Exit |
|---|---|---|
| In `Preparing`, before any update | `Refused` with a `Busy` blocker; the document is a `Plan` | 3 |
| Before a later phase of an upgrade or a rollback | The phase is `Blocked`, the run stops with status `Blocked`; the document is a `Run` | 3 |

The message keeps the old wording: "prod is busy (nodegroup ng-a UPDATING); nothing was started. Run it again once that finishes".

This closes two rows of `CONTRACT.md`: "`cluster upgrade` has no busy check", and the differing exit codes of the same refusal.

### Claims

```go
// Claims records which clusters this process is changing. The TUI shares
// one value among its operations. The CLI passes nil.
type Claims struct { /* a mutex and a map */ }

func NewClaims() *Claims

// Holder returns the kind of the operation that holds the cluster.
func (c *Claims) Holder(t Target) (Kind, bool)
```

An operation claims each cluster as it starts work on it, at the top of `Preparing`, before the first read. A second operation on a claimed cluster finishes at once with `Refused` and a `Busy` blocker that names the holder. The operation releases its claims at `Finished`. A claim is process-local. Two processes are kept apart by the busy rule and by EKS (`ResourceInUseException`), as today.

### The check set of each kind

| Kind | Checks | Targets |
|---|---|---|
| `NodegroupUpdate`, and each nodegroup phase of an upgrade or a rollback | `health.Preflight()` | The nodegroups with `Run` |
| `ClusterUpgrade`, `ClusterRollback`, in the plan | `health.Preflight()`, plus `Insights` and `VersionSkew` as blockers of the readiness step (section 6) | Every nodegroup the plan would roll |
| `NodegroupScale`, desired size goes down | `Nodes`, `health.ScaleDownBlockers(nodegroup, remove)` | The nodegroup |
| `NodegroupScale`, otherwise | `Quotas` | The nodegroup |
| `AddonUpdate` | `health.Addons(targets)`, new in this phase | The add-ons with `Run` |

`health.Addons` is one more check value in `internal/health`, blocking, not fail-closed, with no call of its own. It reads the add-ons in the snapshot.

| Target add-on | Outcome |
|---|---|
| `ACTIVE` | `Pass` |
| `DEGRADED`, `UPDATE_FAILED`, `CREATE_FAILED` | `Warn`: "vpc-cni is DEGRADED (InsufficientNumberOfReplicas); the update will try to repair it" |
| `DELETING`, `DELETE_FAILED` | `Fail` |
| Not `Collected` | `Unreadable` |

This replaces the raw log line of the old command (`health_check.go:43`, trace `addon-update-degraded`). `--yes` accepts the warning and the result records it.

Two changes that phase 3 has to take:

1. `health.Run` appends the `Kubernetes` access result only when a check in the set has `Needs.Kube`. A scale-up and an add-on update then ask nothing about Kubernetes access.
2. `health.Addons` joins the package.

## 4. The batch

### One phase

Every phase runs through one unexported function. A phase of an upgrade is a batch of one.

```go
// runBatch runs the steps of one phase and returns one result per step.
func (r *run) runBatch(ctx context.Context, cp *ClusterPlan, ph Phase) []StepResult
```

The order, with the phase state of section 7 in brackets:

1. **Read** [`Checking`]. One snapshot of the whole cluster. The first phase of an operation uses the snapshot that `Preparing` holds, unless `Preparing` waited for an insights refresh after it.
2. **Busy rule** [`Checking`]. A refusal ends the phase as `Blocked`.
3. **Recheck** [`Checking`]. Derive each step again from the snapshot. A step that is now done becomes `Skip`. A step whose target is now changing becomes `Attach`.
4. **Gate** [`Checking`]. For a phase that rolls nodes or scales down: the check set, with the targets of the phase. It is skipped in the first phase when `Preparing` ran it and nothing came between (phase 3, section 4, rule 3). The pre-phase table of phase 3 decides.
5. **Checkpoint**. Pause, stop, and the per-phase confirmation of an interactive upgrade happen here (section 7).
6. **Baseline** [`Submitting`]. For a roll phase with verification: `health.TakeBaseline`, once, right before the first submit.
7. **Submit** [`Submitting`]. For each step with `Run`, in order: one fresh describe of the target, then the update request. For each step with `Attach`: nothing.
8. **Wait** [`Waiting`]. All accepted and attached steps together.
9. **Confirm** [`Waiting`]. The confirm read of each step whose wait succeeded.
10. **Verify** [`Verifying`]. For a roll phase.

### Submit

The fresh describe in step 7 is the read the request is built from. It is the rule "a mutation re-reads what it is about to change".

| The fresh describe shows | Result |
|---|---|
| The status the step was planned on | Submit |
| A nodegroup that is now `UPDATING` | No request. `nodegroup update`: `Skipped` with `AlreadyUpdating`. Upgrade: the step becomes `Attach`. |
| A nodegroup whose Kubernetes version differs from the step's `From` | No request. `Failed`: "ng-a changed to 1.33 since the plan". |
| The read fails | `Failed` with the read failure |

The old command read the nodegroup as `UPDATING` in this describe and sent the roll anyway. EKS refused it (trace `nodegroup-update-changed-under-the-gate`, exit 4). The new command acts on its last read. Contract edit 4.

`StartNodegroupRoll` is ported frozen from `version_update.go:86-105`:

```go
// StartNodegroupRoll asks EKS to roll the nodegroup onto the latest AMI of
// version. version is always sent, so a patch roll can never become a
// minor upgrade. An empty version is refused before any request.
func StartNodegroupRoll(ctx context.Context, c *awsx.Clients, t Target, nodegroup, version string, force bool) (updateID string, err error)
```

The request carries `clusterName`, `nodegroupName`, `version`, and `force` only when true. It carries no `releaseVersion` and no launch template, as the traces show: `{"clientRequestToken":"token-1","version":"1.32"}`. The SDK sets the idempotency token once per call, and the `Mutate` retry policy reuses it. Trace `nodegroup-update-lost-response` shows two requests with one token and one update.

| Submit outcome | Result | Next step |
|---|---|---|
| Accepted with an update ID | `Started` | Submit the next step |
| Accepted with no update ID | `Started`; the wait is `WaitSettled` | Submit the next step |
| Refused or failed | `Failed` with the failure | Submit the next step |
| The context ended before the request | `NotAttempted` | Stop: every later step is `NotAttempted` |

A batch goes on after a submit failure. The old command did (`actions_update.go:965-973`), and so did `addon update --all` (`service.go:549-562`). A batch of one makes the question moot for the upgrade engine: its next phase does not start after a failed step.

`Accepted(phase)` returns one row per step as soon as step 7 ends: the target with its update ID or its error. With `--no-wait` the batch ends there. Each accepted step is `Started`, the run status is `Started`, and nothing is waited for or verified.

### Wait together

One ticker for the phase. On each tick every unsettled step polls at once (`fanout.ForEach`, limit the number of steps). A step with an update ID uses `WaitUpdate`. An attached step, and a step with no ID, uses `WaitSettled`.

| Wait outcome | Step status | Failure reason |
|---|---|---|
| `nil` and the confirm holds | `Succeeded` | |
| `*UpdateError`, status `Failed` | `Failed` | `UpdateFailed` |
| `*UpdateError`, status `Cancelled` | `Cancelled` | `UpdateCancelled` |
| `*SettleError` | `Failed` | `UpdateFailed` |
| `*UnobservedError` | `InProgress` | `NotMonitored` |
| The deadline passed | `InProgress` | `Timeout` |
| Interrupted | `InProgress` | `Interrupted` |

One step that ends does not stop the wait for the others. A permanent poll error settles only its own step (`monitor.go:212-218`).

### Verification

For a roll phase, after the wait.

| Condition | Verification |
|---|---|
| `--skip-verify`, `--no-wait`, or no step was started or attached | None |
| Any started step is not `Succeeded` | None. The old command verified only when the monitor returned no error (`actions_update.go:428`). A failed roll leaves Pending pods by design, and reporting them as a second problem hides the first. |
| The context ended | None |
| Otherwise | `health.Verify(nodegroups, baseline, wait)` over every step that succeeded, against the one baseline of the batch |

An attached step has no baseline for its own roll, because the roll began before this run. In a batch of one that was attached, `TakeBaseline` is not called, `Baseline.Taken` is false, and `NodesReady` and `NewPendingPods` are `Skipped`. `NodegroupActive` still runs. This is the old `skipAttachedPreroll` (`nodegroups.go`).

`health.VerifyExit` gives the code: 5 for an issue, 4 for a verification that could not read.

### The roll plan

`DecideAMIUpdate` is ported frozen from `ami_update.go:46-76`, as a pure function over the snapshot:

```go
type RollDecision struct {
	Action Action
	Reason Reason
}

// DecideAMIUpdate says what a patch roll does with one nodegroup.
func DecideAMIUpdate(ng fleet.Nodegroup, force, reroll bool) RollDecision
```

| # | Condition, first match | Action | Reason |
|---|---|---|---|
| 1 | AMI type `CUSTOM`, with `--force` too | `Manual` | `CustomAMI` |
| 2 | Status `UPDATING` | `Skip` | `AlreadyUpdating` |
| 3 | `--force` | `Run` | `Forced` |
| 4 | The current or the latest AMI is unknown | `Run` | `AMIUnknown` |
| 5 | Current equals latest, with `--reroll` | `Run` | `Reroll` |
| 6 | Current equals latest | `Skip` | `AlreadyLatest` |
| 7 | Otherwise | `Run` | `AMIOutdated` |

Rows 3 and 4 keep two old behaviours that deserve a second look, and section 14 asks about the first. `--force` rolls a nodegroup that is already on the latest AMI. A nodegroup whose AMI could not be read is rolled.

One change in how the table is fed: the old real run with `--reroll` or `--force` skipped the AMI lookup and always landed on row 3 or 4 (`ami_update.go:57`). The snapshot now always has the AMI, so `--reroll` lands on row 5 or 7 and the plan can say which. The action is `Run` either way.

What each flag does to a roll:

| Flag | Effect |
|---|---|
| `--reroll` | Row 5: roll a nodegroup that is on the latest AMI. PDBs are honoured. |
| `--force` | Row 3. The `DrainBlockers` check becomes a warning (phase 3). The request carries `force: true`, so EKS evicts past PDBs. |
| `--no-wait` | Stop after the submits. |
| `--skip-verify` | No baseline and no verification. |
| `--require-healthy` | Any gate warning blocks. |
| `--skip-health-check` | No gate. The busy rule still applies. |

Targets: no positional and no `-f` means every nodegroup. Positionals are exact names, and one that does not exist is an error that lists the close matches. `-f name=web-*` selects by pattern; the command then lists the matches and asks, or needs `--yes` (`CONTRACT.md`, "Targets").

### One exit code for mixed outcomes

Each step result has a code. The run takes the highest, except that an interrupt is always 1.

| Step result | Code |
|---|---|
| `Succeeded`, `Skipped`, `Started` | 0 |
| `Failed`, `Cancelled`, and no step of the run succeeded | 1 |
| `Failed`, `Cancelled`, and at least one step of the run succeeded | 4 |
| `InProgress` with `Timeout` or `NotMonitored` | 4 |
| `InProgress` with `Interrupted`, or `NotAttempted` after an interrupt | 1, and it wins |
| `Blocked` | 3, or 4 when the gate could not read |
| Verification issue | 5 |
| Verification unreadable, or any failure in the run | 4 |

The second and third rows are one rule for every command: all failed is an error, some failed is a partial failure. The old commands disagreed. `addon update --all` exited 4 for a partial failure, `nodegroup update` exited 1 (`updateExit`, `actions_update.go:475-494`), and a submit that EKS refused exited 4 in `nodegroup update` and 1 everywhere else. Contract edit 5.

### Fleet mode

`nodegroup update --all-clusters` is one operation over many clusters.

1. **Discover.** A region sweep of `ListClusters` with the sweep clients, under `--timeout`. A default sweep skips the regions it cannot reach (`Plan.SkippedRegions`). A region that fails is a failure, exit 4. No listable region is an error, exit 1.
2. **Plan.** One `ClusterPlan` per cluster, built with bounded fan-out. The clusters stay in sweep order: regions in input order, clusters in `ListClusters` order. A dry run stops here.
3. **Confirm.** One question for the fleet, with the per-cluster decisions above it. The old prompt came before any cluster was read (`fleet.go:296-304`).
4. **Run.** One cluster after another. Each cluster gets its own wait deadline (section 8). Each cluster is planned again when its turn comes, because time passed, and compared with the plan that was confirmed. The revalidation table applies: `--yes` accepts an ask, a person is asked again, a refuse marks that cluster `Refused`.

| Cluster outcome | Effect on the rest |
|---|---|
| `Refused` (busy, gate, changed roll target), `Failed`, `Partial`, `TimedOut`, `VerifyFailed`, `Incomplete` | None. The next cluster runs. |
| `Interrupted`, or the parent context ended | Stop. Every later cluster is `NotAttempted`. |
| `Stopped` (`Stop()` was called) | Stop after the current cluster. Later clusters are `NotAttempted`. |

The exit code is the highest cluster code, and an interrupt is 1. The old `fleetExit` took the numeric maximum over 5, 4, 3, 2, 1 (`fleet.go:858-905`), which is the same order. The drift table in `REWRITE.md` asks for a test of each mixed pair.

Fleet mode ignores `REFRESH_CLUSTER` and the active context. `-c`, positionals, and `--kube-context` are usage errors with `--all-clusters`. A kubeconfig context is matched for each cluster by its endpoint.

### A batch of one in the upgrade

Each nodegroup of an upgrade or a rollback is its own phase, so the list above runs once per nodegroup: a fresh read, the busy rule, the gate with that nodegroup as the target, a baseline, one submit, one wait, one verification. The old engine did the same in one loop (`nodegroups.go`, `UpgradeNodegroups`). A nodegroup that fails stops the run, and the later nodegroups are `NotAttempted`.

## 5. Add-on update and scale

Both are one phase on the machinery of section 4.

### `addon update`

The plan:

| Input | Steps |
|---|---|
| One or more names | One step per name. A name that is not installed is an error that lists the close matches. |
| `--all` | One step per installed add-on, less `--skip-addon`, in dependency order |
| `--version` | Only with exactly one name. The step's `To` is that version. |

Dependency order is always on for `--all` (contract change 7). The table and the sort are ported from `addon_dependencies.go:13-101`: Kahn's algorithm, stable by list position, an edge to an add-on that is not installed or is skipped is dropped. `kube-proxy`, `coredns`, and `aws-guardduty-agent` follow `vpc-cni`. The three CSI drivers follow `vpc-cni` and `coredns`. `amazon-cloudwatch-observability` and `adot` follow `coredns`.

The step of one add-on, from the snapshot with `Need.LatestAddons`. This is the old `versionGuard` (`service.go:403-434`):

| Condition, first match | Action | Reason |
|---|---|---|
| `--version` names a version that is not compatible with the cluster version | The plan fails with a usage-style error, exit 1, that lists the supported versions (`health_check.go:151`) | |
| The latest compatible version could not be read | `Blocked`, with an `Unread` blocker | |
| `ACTIVE` at the target, or newer than the latest with no `--version` | `Skip` | `AlreadyAtTarget` |
| `UPDATING` or `CREATING` | `Attach` | `AlreadyUpdating` |
| `DEGRADED` or a failed status | `Run` | `Repair` |
| Otherwise | `Run` | `VersionBehind` |

A `--version` older than the installed one runs, with the notice "downgrading vpc-cni from X to Y" (`service.go:430`).

The request is `UpdateAddon` with `addonVersion` and the token. It sets no `resolveConflicts`, no configuration values, and no service account role, as the old one (`service.go:353-363`).

The phase submits in dependency order and waits for each add-on before it submits the next. That is a batch per add-on inside one phase: `runBatch` takes a `serial` flag that turns steps 7 to 9 into a loop. A failed add-on does not stop the rest (`service.go:549-562`). Its dependents are still attempted.

With `--no-wait` the phase submits every add-on in dependency order and returns. The old default did the same (trace `addon-update-all`).

`--wait-timeout` is the limit for each add-on (`CONTRACT.md`: "5m for each add-on"). Section 8.

### `nodegroup scale`

`validate`, before any AWS call:

| Input | Error |
|---|---|
| None of `--desired`, `--min`, `--max` | "give at least one of --desired, --min, --max" |
| A negative size | "--min must be 0 or more" |
| `--min` above `--max` | "--min 5 is above --max 2" |
| `--desired` outside `--min` and `--max`, when both sides of the comparison were given | "--desired 9 is above --max 3" |

The plan, after the read, fills the sizes that were not given from the nodegroup and checks again:

| Check against the nodegroup | Result |
|---|---|
| The resulting min is above the resulting max | The plan fails, exit 1, no request |
| The resulting desired is outside the resulting min and max | The plan fails, exit 1, no request |
| Every resulting size equals the current one | The step is `Skip` with `NoChange`; nothing is sent |

The old command checked none of this and let EKS refuse (traces `nodegroup-scale-above-max` and `nodegroup-scale-min-above-max`: `InvalidParameterException`). It also sent a request that changed nothing. Contract edit 3.

The request is `UpdateNodegroupConfig` with a `scalingConfig` that holds only the sizes the user gave. Trace `nodegroup-scale-yes` shows `{"scalingConfig":{"desiredSize":3}}`. The filled values are for validation and for the plan's `after`, not for the request.

The gate follows section 3: a scale-down runs `Nodes` and `ScaleDownBlockers(nodegroup, current - desired)`. `--force` goes past a blocker with a warning. Any other scale runs `Quotas`. The instance-type availability notice stays for a scale-up.

The wait is `WaitUpdate`, then the confirm read of section 2.

## 6. The upgrade engine and rollback

### What is ported as it is

| Part | Source | Note |
|---|---|---|
| Hop expansion, the `maxHops` cap, the same-version catch-up hop | `types.go:184-211`, `plan.go:101-107,163-187` | Frozen |
| Step derivation for the control plane, add-ons, and nodegroups | `plan.go:542-679` | Frozen; the output is `Step` values with typed actions and reasons |
| The kubelet skew rule (3 minors, every nodegroup counts) | `plan.go:393-417` | It becomes the `VersionSkew` check of phase 3 with the hop target |
| "Before" and "after" add-ons by compatibility with the hop target | `plan.go:571-629` | Frozen |
| `--only`, `-n`, `--skip-addon`, `--skip-nodegroup` scope rules and errors | `scope.go` | Frozen, except that `--skip-nodegroup` is an exact name (contract change 3) |
| The insights refresh: reuse within 10 minutes, a 5 minute wait, the start-failure cases | `insights.go:24-150` | Frozen; it polls through `WaitOptions` |
| The insights verdict | `insights.go:174-213` | It becomes the `Insights` check of phase 3 with `ForVersion` |
| The rollback planner: the window, the 2 hour slack, eligibility, the step order | `rollback.go:359-636` | Frozen; the window comes from `fleet.RollbackWindow` |
| The rollback request: `force` from `--skip-insights-check`, `rollbackConfig.timeoutMinutes` | `rollback.go:789-795` | Frozen (trace `cluster-rollback-force-and-timeout`) |
| Resume by planning again from live state | `engine.go` | Frozen. No state file. |
| Attach to an in-flight change of the phase's own target | `controlplane.go`, `nodegroups.go`, `addons.go` | Frozen, through `WaitSettled` |

### What changes

| Old | New |
|---|---|
| A phase is a label string. The TUI matched phases by label prefix (`tui/live/upgrade.go:468-502`). | `PhaseID` and `StepID`. Labels are made in `view`. |
| `ExecuteOptions` with seven hooks | No hooks. The engine calls the operation's `run` for events, questions, and checkpoints. |
| One nodegroup phase per hop with a loop inside | One phase per nodegroup |
| The health gate ran when the run reached each nodegroup, and nowhere before | The plan runs the gate once for every nodegroup it would roll. Each nodegroup phase runs it again. |
| No busy check | The busy rule before every phase |
| An attached update that failed was followed by a new one in silence | A notice first (below) |
| A gate refusal was a plain error, exit 1 | `Blocked`, exit 3, or 4 when the gate could not read |
| Verification through an optional hook | Always, unless `--skip-verify`; issues exit 5, an unreadable one exits 4 |
| `cluster rollback` refused any changed step | It asks, by the revalidation table |
| `cluster rollback` interrupted before it starts reports `Failed` | `Interrupted` (`CONTRACT.md`) |

### Phases of an upgrade

For each hop, in order:

| Phase | ID | Steps | Before the submit |
|---|---|---|---|
| Control plane | `1.33/control-plane` | One | The busy rule. The readiness of the hop: insights refresh (unless `--skip-insights-check`), the `Insights` and `VersionSkew` checks. |
| Add-ons the new control plane cannot run | `1.33/addons-before` | One per add-on, dependency order, serial | The busy rule |
| Each nodegroup | `1.33/nodegroup/<name>` | One | The busy rule, the gate, the baseline |
| The other add-ons | `1.33/addons-after` | One per add-on, dependency order, serial | The busy rule |

A catch-up hop has no control-plane phase to run. A phase with no step that has `Run` or `Attach` is `Skipped`.

The readiness of a hop is judged twice, as before: in the plan, and live before that hop's control plane moves (`checkHopReadiness`, `plan.go:477-518`). For the hop right after the live version the live check refreshes the insights first. Trace `cluster-upgrade-two-hops` shows the second `StartInsightsRefresh` before the second `UpdateClusterVersion`.

A readiness check that blocks before a later hop ends the run as `Blocked`, exit 3, with the completed phases listed.

### Phases of a rollback

| Phase | ID | Steps | Before the submit |
|---|---|---|---|
| Each nodegroup | `rollback/nodegroup/<name>` | One | The busy rule, the gate, the baseline |
| Add-ons | `rollback/addons` | One per add-on that the target version cannot run, dependency order, serial | The busy rule |
| Control plane | `rollback/control-plane` | One | The busy rule. Every managed nodegroup is at or below the target (`rollback.go:803`). |

The plan has the eligibility blockers of `rollback.go:397-431` and the `ROLLBACK_READINESS` insights of `:507-555`. `--skip-insights-check` turns an insight blocker into a notice and sets `force` on the request. No refresh is started for a rollback.

### The readiness step in the plan

The upgrade plan runs `health.Insights(InsightsOptions{ForVersion: hop})` and `VersionSkew` for the first hop above the live version. A `Fail` or an `Unreadable` of either is a `NotReady` or `Unread` blocker on the control-plane step. A `Warn` is a notice.

| Run | Insights |
|---|---|
| Dry run | No refresh. Read what EKS has. An `ERROR` blocks. A missing or `UNKNOWN` insight is a notice. A failed read is a failure, exit 4, and does not block (`insights.go:220-259`). |
| Real run | `StartInsightsRefresh`, wait up to 5 minutes, `ListInsights`. An empty list, `ERROR`, or `UNKNOWN` blocks. A refresh or read that fails blocks, with a failure. |
| `--skip-insights-check` | No refresh, no read, a notice |

The refresh comes before the block decision, so a blocked real run has sent one request (`CONTRACT.md`, last paragraph; trace `cluster-upgrade-blocked`).

### Attach, and the notice when it fails

| Target of the phase | State in the phase read | What the phase does |
|---|---|---|
| Control plane | `UPDATING` | `WaitSettled(want: hop)`. `Met`: the step is `Succeeded`, `Attached`. Not met: notice, then submit. |
| Add-on | `UPDATING` or `CREATING` | `WaitSettled(want: To)`. `Met`: done. A terminal error: notice, then submit (a repair). Settled at another version: notice, then submit. |
| Nodegroup | `UPDATING` or `CREATING` | `WaitSettled(want: hop)`. `Met`: verify with no baseline, then done. Not met and `ACTIVE`: notice, then the gate and a roll. Not met and not `ACTIVE`: the gate blocks. |

The notice is a lifecycle event of kind `Notice`, never dropped, and the CLI prints it on stderr:

```text
warning: the update of the control plane that was already running did not reach 1.33 (cluster is ACTIVE at 1.32); starting a new one
```

The old engine printed "attaching to the in-flight update" and then "upgrade started" with nothing between (trace `cluster-upgrade-attach-failed`).

`WaitSettled` cannot name the error of the foreign update, because it has no update ID. Looking the ID up costs `ListUpdates` and one `DescribeUpdate` per update. The engine does not do it. `Observe` does (section 7).

### Flags

| Flag | Effect on the plan |
|---|---|
| `--to` | Required. One hop per minor from the live version to it. Equal to the live version: a catch-up plan for laggards. Older: an error. |
| `--only` | `control-plane`, `addons`, `nodegroups`, repeatable or comma-separated. A step of a part that is left out is `Manual` with `OutOfScope`. Without `control-plane` the target must equal the live version. Without `addons` the plan may span one hop only. |
| `-n` | Exact nodegroup names. The others are `Manual` with `OutOfScope`. The skew rule still counts every nodegroup. |
| `--skip-addon` | Exact add-on names. `Manual` with `SkippedByFlag`. An add-on that the new control plane cannot run stays a blocker. |
| `--skip-nodegroup` | Exact nodegroup names. `Manual` with `SkippedByFlag`. |
| `--skip-insights-check` | As above |
| `--rollback-timeout` | `0`, or whole minutes from 2h to 168h (`rollback.go:52-60`). Validated before any call. |

A run that ends with `Manual` steps is `Succeeded` and says in a notice what is left, with the command that does it.

## 7. The operation handle

### Shape

`REWRITE.md` describes a handle that `Start` returns at once. The assignment for this note asked whether a simpler shape would serve both front ends: a synchronous `Execute(ctx, plan, hooks)` for the CLI, wrapped by a small handle for the TUI. I looked at the evidence for each and chose a layered form of the handle.

What the evidence says:

- The executors want to be synchronous. The old engine is a straight loop over phases (`engine.go`, `runPhases`), and the old TUI ran it unchanged on one goroutine (`tui/live/upgrade.go:231`). Nothing in the 152 scenarios needs two executor steps at once, apart from the polls of one wait.
- Hooks were not enough for the TUI. Around the seven hooks of `ExecuteOptions` the old TUI kept a mutex taken by every hook, a capped event log, an `answers` channel with clear-before-send, a `wake` channel with `Paused` and `StopAfter` flags, `Confirm` reused as the pause point, two sentinel errors to tell a stop from a failure, and phase matching by label prefix (`upgrade.go:36-48,177-196,333-350,468-502`). Rolls and add-on updates had no engine at all and were written a second time by hand (`roll.go:550-626`, `addons.go:182-231`). Adoption of a change started elsewhere has no plan to execute, so no hook fits it (`observed.go`).
- A second entry point for the CLI would bring back two forks. A CLI on a synchronous `Execute` answers questions inside a callback on the executor's goroutine. The TUI answers them from another goroutine. That is two implementations of the question, of the deadline pause, and of cancel waking a prompt. The scenarios `*-prompt-interrupt` and `cluster-rollback-interrupt-before-start` show that cancel must wake a pending question in the CLI too.
- The CLI's cost of using the handle is small: about 25 lines (below).

So: one exported API, the handle. Inside it, each executor is a synchronous function that receives a `*run` and calls three methods on it: `emit`, `ask`, `checkpoint`. A unit test of an executor passes a `run` that records the calls and needs no goroutine. `Start` runs the executor on one goroutine. The handle itself is the log, the question table, and the control flags: about 300 lines.

### API

```go
// Start begins a change and returns at once. The context owns the run:
// cancelling it is an interrupt.
func Start(ctx context.Context, d Deps, req Request, opts StartOptions) *Operation

// Observe returns an operation that watches a change started elsewhere.
// It has no controls and claims nothing.
func Observe(ctx context.Context, d Deps, r Resource, opts ObserveOptions) *Operation

type StartOptions struct {
	// Expect is a plan that a person reviewed before this call, such as
	// the TUI's dry run. Preparing compares its own plan with it.
	Expect *Plan
	// CanAsk says that a caller will answer questions. When it is false a
	// question that the policy cannot answer refuses the run.
	CanAsk bool
	// ConfirmStart asks "proceed?" once before the first submit.
	// ConfirmPhases asks before each later phase too.
	ConfirmStart  bool
	ConfirmPhases bool
	Limits        Limits // section 8
	// Nodes starts a node observer for each roll and publishes NodeProgress
	// events.
	Nodes bool
}

type ObserveOptions struct {
	Limits Limits
	Nodes  bool
}

type Operation struct{ /* unexported */ }

// Status returns the current state. It makes no call and is cheap enough
// for every TUI frame.
func (o *Operation) Status() Status

// Next returns the events after the cursor and the cursor to pass next.
// It blocks until there is one. It returns io.EOF once the log is closed
// and read to the end, and ctx.Err() when ctx ends.
func (o *Operation) Next(ctx context.Context, after Cursor) ([]Event, Cursor, error)

// Answer answers the open question with that ID.
func (o *Operation) Answer(id QuestionID, yes bool) error

func (o *Operation) Pause() error
func (o *Operation) Resume() error

// Stop asks the operation to stop after the current phase.
func (o *Operation) Stop() error

// Accepted returns one row per step of the phase once its submits are
// done, and nil before that.
func (o *Operation) Accepted(phase PhaseID) []Acceptance

// Done is closed at Finished.
func (o *Operation) Done() <-chan struct{}

// Result blocks until Finished. It can be called again.
func (o *Operation) Result() Result

// Controls says what the caller may do now.
func (o *Operation) Controls() Controls

type Controls struct {
	Answer, Pause, Resume, Stop bool
}

type Acceptance struct {
	Step     StepID
	Resource Resource
	UpdateID string
	Err      error
}

var (
	ErrStaleQuestion = errors.New("change: the question is closed")
	ErrNoControl     = errors.New("change: the operation does not accept this control")
)
```

There is no `Cancel` method. The caller cancels the context it gave to `Start`.

```go
type State string

const (
	Preparing State = "Preparing"
	Running   State = "Running"
	Paused    State = "Paused"
	Asking    State = "Asking"
	Finished  State = "Finished"
)

type PhaseState string

const (
	PhasePending    PhaseState = "Pending"
	PhaseChecking   PhaseState = "Checking"
	PhaseSubmitting PhaseState = "Submitting"
	PhaseWaiting    PhaseState = "Waiting"
	PhaseVerifying  PhaseState = "Verifying"
	PhaseDone       PhaseState = "Done"
	PhaseFailed     PhaseState = "Failed"
	PhaseBlocked    PhaseState = "Blocked"
	PhaseSkipped    PhaseState = "Skipped"
)

type Status struct {
	State State
	// Plan is nil until Preparing has computed one.
	Plan     *Plan
	Phases   []PhaseStatus // plan order, across clusters
	Question *Question     // the open question, or nil
	// PauseRequested and StopRequested are the stored intents.
	PauseRequested, StopRequested bool
	StartedElsewhere               bool // an Observe operation
	Cursor                         Cursor
}

type PhaseStatus struct {
	Target
	ID    PhaseID
	State PhaseState
	Steps []StepResult
}

type QuestionID uint64

type QuestionKind string

const (
	ConfirmStart QuestionKind = "ConfirmStart" // proceed with the plan?
	ConfirmPhase QuestionKind = "ConfirmPhase" // proceed with this phase?
	GateWarnings QuestionKind = "GateWarnings" // proceed despite the warnings?
	PlanChanged  QuestionKind = "PlanChanged"  // the plan changed; proceed?
)

type Question struct {
	ID      QuestionID
	Kind    QuestionKind
	Phase   PhaseID
	Text    string   // "Proceed despite 2 warnings (Nodes: 5/6 ready; ...)?"
	Reasons []string // the gate reasons or the plan changes behind it
}
```

`PhaseChecking` and `PhaseBlocked` are not in the list in `REWRITE.md`. `Checking` covers the phase read, the busy rule, and the gate, which the TUI has to show and which take seconds. `Blocked` keeps a gate refusal apart from a failure, because the two have different exit codes.

### Operation states

| From | Event | To |
|---|---|---|
| (start) | `Start` returns | `Preparing` |
| `Preparing` | The plan is ready, nothing needs a question, the first phase begins | `Running` |
| `Preparing` | A question is needed (`ConfirmStart`, `GateWarnings`, `PlanChanged`) | `Asking` |
| `Preparing` | The plan is blocked, has nothing to do, or failed to build; a claim is held by another operation | `Finished` |
| `Preparing` | Pause is requested and the plan is ready | `Paused` |
| `Asking` | Answered yes while in preparation, and the plan must be computed again | `Preparing` |
| `Asking` | Answered yes, no pause requested | `Running` |
| `Asking` | Answered yes, pause requested | `Paused` |
| `Asking` | Answered no | `Finished` (`Declined`) |
| `Running` | A checkpoint, pause requested | `Paused` |
| `Running` | A checkpoint, a question is needed (`ConfirmPhase`, a pre-phase `GateWarnings`) | `Asking` |
| `Running` | The last phase ended, a phase failed or was blocked, or a checkpoint found stop requested | `Finished` |
| `Paused` | `Resume` | `Running` |
| `Paused` | `Stop` | `Finished` (`Stopped`) |
| Any but `Finished` | The context is cancelled | `Finished` (`Interrupted`) |
| Any but `Finished` | The operation deadline passes | `Finished` (`TimedOut`) |
| `Asking`, `Paused` | `Stop` | `Finished` (`Stopped`) |
| `Finished` | | none |

A checkpoint is the one place where pause and stop take effect: after `Preparing` and before the first phase, and between two phases. In fleet mode the boundary between two clusters is a checkpoint too. There is no checkpoint inside a batch. A change that EKS has accepted is never abandoned by a pause.

### Phase states

| From | Event | To |
|---|---|---|
| `Pending` | The phase's turn comes | `Checking` |
| `Pending` | The run ended before its turn | stays `Pending`; its steps are `NotAttempted` |
| `Checking` | The recheck leaves no step with `Run` or `Attach` | `Skipped` |
| `Checking` | The busy rule or the gate refuses, or the phase question is declined | `Blocked` |
| `Checking` | The phase read fails | `Failed` |
| `Checking` | Everything passes, and the checkpoint lets the run go on | `Submitting` |
| `Submitting` | The submits are done, at least one step is accepted or attached, the run waits | `Waiting` |
| `Submitting` | The submits are done, `--no-wait` | `Done` |
| `Submitting` | No step was accepted or attached | `Failed` |
| `Waiting` | Every step succeeded, the phase verifies | `Verifying` |
| `Waiting` | Every step succeeded, no verification | `Done` |
| `Waiting` | A step failed, timed out, or was interrupted | `Failed` |
| `Verifying` | The verification passed | `Done` |
| `Verifying` | An issue, or the verification could not read | `Failed` |
| `Done`, `Failed`, `Blocked`, `Skipped` | | none |

An upgrade enters `Submitting` once per phase, after the phase before it is `Done` or `Skipped`. A batch whose steps partly failed is `Failed` as a phase. Its step results carry the detail, and the exit code table of section 4 reads the steps.

### Preparing, in order

1. Claim the cluster (the clusters, in fleet mode: each as its turn comes).
2. `plan(ctx, d, req, mode{refreshInsights: true})`: the snapshot, the busy rule, the steps, the insights refresh for an upgrade, the gate.
3. If `opts.Expect` or an earlier plan of this operation exists: `Diff`, and the effect from the revalidation table.
4. Publish `PlanReady` with the plan.
5. If the plan is blocked: `Finished`, `Refused`. If nothing is to do: `Finished`, `NothingToDo`.
6. Decide the question. At most one is open before the first submit:

| Gate decision | Policy | Question |
|---|---|---|
| `Go` | `ConfirmStart` set | `ConfirmStart` |
| `Go` | `ConfirmStart` not set | none |
| `Ask` | `--yes` | none; phase 3 turned it into `Go` with `Accepted` |
| `Ask` | `CanAsk` | `GateWarnings`; a yes also confirms the start |
| `Ask` | cannot ask | `Finished`, `Refused`: "health warnings need an answer; pass --yes or --require-healthy" |

7. After a yes from a person, go to step 2. The loop ends when the new plan needs no new question.

The refusal boundary is "before any update is submitted". Every `Finished` that leaves `Preparing` has no accepted row. One request can come before the boundary: `StartInsightsRefresh`, which is not an update.

### The event log

```go
type Cursor uint64

type EventKind string

const (
	StateChanged   EventKind = "StateChanged"   // lifecycle
	PlanReady      EventKind = "PlanReady"      // lifecycle; Plan is set
	GateReport     EventKind = "GateReport"     // lifecycle; Checks and Verdict
	QuestionAsked  EventKind = "QuestionAsked"  // lifecycle; Question
	QuestionClosed EventKind = "QuestionClosed" // lifecycle; Question and Outcome
	PhaseChanged   EventKind = "PhaseChanged"   // lifecycle; Phase and PhaseState
	StepChanged    EventKind = "StepChanged"    // lifecycle; Result
	Notice         EventKind = "Notice"         // lifecycle; Text
	VerifyReport   EventKind = "VerifyReport"   // lifecycle; Checks
	UpdateProgress EventKind = "UpdateProgress" // progress; coalesced by step
	NodeProgress   EventKind = "NodeProgress"   // progress; coalesced by step
)

type Event struct {
	Seq    Cursor
	At     time.Time
	Kind   EventKind
	Target Target
	Phase  PhaseID
	Step   StepID

	State      State
	PhaseState PhaseState
	Plan       *Plan
	Verdict    *health.Verdict
	Checks     []health.Result
	Question   *Question
	Outcome    QuestionOutcome // Yes, No, Stopped, Cancelled, TimedOut
	Result     *StepResult
	Text       string
	Update     *UpdateProgress
	Nodes      *NodeProgress // section 9
}
```

Rules:

- The log is a slice of lifecycle events and a map from `StepID` and kind to the latest progress event. Both are guarded by the operation's mutex. Every event gets the next `Seq`.
- `emit` appends, or replaces the map entry, and wakes the readers. It never waits for a reader. A slow reader, or none, costs the executor nothing.
- `Next(after)` returns the lifecycle events with `Seq > after` and the progress entries with `Seq > after`, merged in `Seq` order. A reader that falls behind sees the latest progress of each step and every lifecycle event.
- Lifecycle events and questions are never dropped. Their number is bounded by the plan: a few per step, plus notices. A scaling-failure notice is published once per activity ID.
- The log closes at `Finished`. The last event is `StateChanged` to `Finished`. `Next` then returns the rest and `io.EOF`.
- Any number of readers may call `Next`, each with its own cursor.

### Questions

- `ask` registers the question in the operation, sets the state to `Asking`, and then publishes `QuestionAsked`. A reader that sees the event can answer at once.
- At most one question is open.
- `Answer(id, yes)` takes the mutex, checks that `id` is the open question, records the answer, and clears it. One caller wins. Any other call, and any call with an old ID, returns `ErrStaleQuestion`.
- An unattended run answers before the question is ever published: the gate policy turns `Ask` into `Go`, and `Diff` asks are accepted under `--yes`. Those go in the log as `Notice` events with the reasons.
- While a question is open the operation deadline is paused (section 8).

### Pause, stop, cancel

Pause intent and stop intent are two booleans in the operation, apart from the question table.

| Call | Effect |
|---|---|
| `Pause()` | Sets the pause intent. The run holds at the next checkpoint. |
| `Resume()` | Clears the pause intent and wakes a held checkpoint. |
| `Stop()` | Sets the stop intent. Wakes a held checkpoint and an open question. The run finishes at the next checkpoint with `Stopped`. A phase that is submitting or waiting ends first. |
| Cancel of the context | Wakes everything. A wait returns. The run finishes with `Interrupted`. |

Precedence when several hold at a checkpoint or while a question is open: cancel, then stop, then pause, then the answer.

| Situation | Outcome |
|---|---|
| A question is open and the context is cancelled | The question closes with `Cancelled`. `Finished`, `Interrupted`. |
| A question is open and `Stop()` is called | The question closes with `Stopped`. `Finished`, `Stopped`. |
| A question is open and `Pause()` is called | The question stays open. After a yes the run is `Paused`. |
| Paused, a question was answered yes earlier | Stays `Paused` until `Resume`. |
| `Answer` and `Stop` race | Whichever takes the mutex first. If the answer wins, the stop intent holds at the next checkpoint, which comes before any submit. Either way nothing is submitted. |
| `Answer` and cancel race | The same, and the run is `Interrupted`. |
| `Stop()` during `Waiting` | The phase ends, then `Finished`. The status is `Stopped` when the phase succeeded, and the phase's own failure status otherwise. |

Only a stop intent or a declined question gives `Stopped` or `Declined`. A failure after a stop request is still a failure, as the old TUI held (`upgrade.go:305-318`).

`Controls()`:

| Operation | `Answer` | `Pause`, `Resume` | `Stop` |
|---|---|---|---|
| From `Start`, a question is open | yes | by state | yes |
| From `Start`, more than one phase or cluster left | no | yes | yes |
| From `Start`, in its last phase | no | no | no |
| From `Observe` | no | no | no |
| `Finished` | no | no | no |

A call that `Controls` does not allow returns `ErrNoControl`.

### Result

```go
type RunStatus string

const (
	Succeeded    RunStatus = "Succeeded"
	NothingToDo  RunStatus = "NothingToDo"
	Started      RunStatus = "Started"      // --no-wait: accepted, not followed
	Refused      RunStatus = "Refused"      // nothing was submitted; the plan says why
	Declined     RunStatus = "Declined"     // a question was answered no
	Stopped      RunStatus = "Stopped"      // Stop() between phases
	Blocked      RunStatus = "Blocked"      // a later phase was refused
	Failed       RunStatus = "Failed"       // every attempted step failed, or a sequence stopped on a failure
	Partial      RunStatus = "Partial"      // a batch or a fleet with some done and some not
	VerifyFailed RunStatus = "VerifyFailed" // the change was applied; verification found issues
	Incomplete   RunStatus = "Incomplete"   // done, but something could not be read
	TimedOut     RunStatus = "TimedOut"
	Interrupted  RunStatus = "Interrupted"
)

type Result struct {
	Status   RunStatus
	Plan     *Plan // the last plan Preparing computed; nil if none
	Clusters []ClusterResult
	Failures []apidoc.Failure
	// Err is the one-line error for the user, or nil.
	Err error
}

type ClusterResult struct {
	Target
	Status       RunStatus
	Phases       []PhaseStatus
	Verification []health.Result
}

// ExitCode maps the result to the exit code of CONTRACT.md.
func (r Result) ExitCode() int

// Document returns the Plan document for a Refused or NothingToDo result
// and the Run document for every other one.
func (r Result) Document() apidoc.Document
```

| `RunStatus` | Exit |
|---|---|
| `Succeeded`, `NothingToDo`, `Started` | 0, or 4 when `Failures` is not empty |
| `Refused` | The plan's dry-run code: 3, or 4 when unread, or 2 for `--require-healthy` over warnings; 1 for "cannot ask" |
| `Declined`, `Stopped` | 1 |
| `Blocked` | 3, or 4 when the gate could not read |
| `Failed` | 1 |
| `Partial` | 4, or the highest cluster code in fleet mode |
| `VerifyFailed` | 5 |
| `Incomplete` | 4 |
| `TimedOut` | 4 |
| `Interrupted` | 1, whatever else happened |

`ExitCode` lives in `change`, so the CLI and the TUI report the same outcome for the same run.

### Finished

At `Finished`, in this order, on the executor goroutine: join the observers and the waiters of the last phase (they are already joined, because every phase joins its own), close any open question, release the claims, publish the last event, close the log, close `Done`. After `Done` is closed the operation owns no goroutine.

### Goroutines

| Goroutine | Started by | Ends | Joined by |
|---|---|---|---|
| The executor, one per operation | `Start`, `Observe` | When the run is `Finished` | Nobody waits for it; `Done` reports it |
| The deadline timer | `Start` | At `Finished` | The executor (`timer.Stop`) |
| The polls of one tick | `fanout.ForEach` in the batch wait | When each poll returns | `ForEach`, before the next tick |
| The node observer and the scaling watch of a roll | `fanout.RunAlongside` in the batch wait | When the wait returns and their context is cancelled | `RunAlongside`, before the phase ends |
| The informers of a node observer | `noderoll.Attach` | `Observer.Close` | The node observer goroutine |
| The checks of one gate run | `health.Run` | When each check returns | `health.Run` |

`Next` starts no goroutine. A reader blocks on a channel that `emit` closes and replaces.

`internal/change` gets a `goleak.VerifyTestMain`.

### The CLI front end

One function in `cmdutil` drives every changing command. It needs no knowledge of the kind.

```go
// RunChange starts the change, renders its events, asks its questions,
// and returns the exit error.
func RunChange(ctx context.Context, ios *IOStreams, format Format, d change.Deps, req change.Request, o change.StartOptions, r Renderer) error {
	op := change.Start(ctx, d, req, o)
	var cur change.Cursor
	for {
		events, next, err := op.Next(ctx, cur)
		cur = next
		for _, ev := range events {
			r.Render(ev) // stderr for a machine format; the roll panel on NodeProgress
			if ev.Kind != change.QuestionAsked {
				continue
			}
			yes, cerr := Confirm(ctx, ios, ev.Question.Text)
			if cerr != nil {
				continue // ctx ended: the operation closes the question itself
			}
			if aerr := op.Answer(ev.Question.ID, yes); aerr != nil && !errors.Is(aerr, change.ErrStaleQuestion) {
				return aerr
			}
		}
		if err != nil {
			break // io.EOF at Finished, or ctx ended
		}
	}
	res := op.Result() // returns soon after ctx ends: the executor sees the same ctx
	r.Close()
	if perr := Print(ios, format, res.Document(), r.View(res)); perr != nil {
		return perr
	}
	ReportFailures(ios, res.Failures)
	if code := res.ExitCode(); code != 0 {
		return &ExitError{Code: code, Err: res.Err}
	}
	return nil
}
```

`Confirm` is `cmdutil.Confirm(ctx, ios, question) (bool, error)` from `REWRITE.md`. It writes "question [y/N] " on stderr, reads one line through the shared reader, and returns `ctx.Err()` when the context ends. `RequireYes` ran before `Start`, so `Confirm` is only reached where a prompt can show. `Renderer` is an interface of three methods in `cmdutil`, implemented in `view`: `Render(change.Event)`, `Close()`, `View(change.Result) HumanView`.

A human run and a machine run are the same loop. They differ in the renderer's writer and in whether the panel is on.

A dry run does not call `RunChange`:

```go
plan, err := change.NewPlan(ctx, d, req)
// print the Plan document or its view, report failures, exit with plan.ExitCode()
```

### The TUI front end (phase 5)

- The backend keeps a map from cluster to `*Operation` and one `Claims`.
- `State()` calls `op.Status()` for each. It copies a small struct under the operation's mutex and makes no AWS call.
- One `tea.Cmd` per operation blocks in `op.Next` and returns the events as a message. The model appends lifecycle events to its feed and replaces progress by step.
- Keys map to `op.Pause`, `op.Resume`, `op.Stop`, and `op.Answer(status.Question.ID, yes)`. `op.Controls()` says which keys are live.
- A dry run is `change.NewPlan`. A start passes that plan as `StartOptions.Expect` with `CanAsk: true`. Read-only mode never calls `Start`.
- An `UPDATING` resource that no operation of this process holds becomes `change.Observe`.
- Quitting cancels the backend context. Every operation finishes as `Interrupted`, and the backend waits on each `Done`.

### `Observe`

`Observe` serves a change that this process did not start.

1. No claim. `Status.StartedElsewhere` is true. `Controls()` is all false.
2. Find the update: `ListUpdates` for the resource, `DescribeUpdate` for each, the newest `InProgress` one (`observed.go:32-64`).
3. The plan has one phase with one `Attach` step. `From` and `To` come from the update's parameters when it was found.
4. With an update ID: `WaitUpdate`. Without one: `WaitSettled` with an empty `want`, so `Met` only means settled.
5. For a nodegroup with `Nodes` set: a node observer with `Identity.StartedAt` from the update's creation time.
6. For a nodegroup whose update succeeded: `health.Verify` with no baseline. `NodegroupActive` runs and the pod checks are `Skipped`.
7. `Finished` with `Succeeded`, `Failed`, `VerifyFailed`, `TimedOut`, or `Interrupted`.

A cluster's own update is observed as one control-plane step. The rolls and add-on updates that the foreign run starts later show up as separate observed operations, as today (`observed.go:251-308`).

## 8. Deadlines

The old tree had three calculators (`scaleSetupTimeout`, `fleetClusterContext`, `monitorContext`), and more timeouts beside them: the root deadline of `nodegroup update`, one inside `waitForScaleCompletion`, one in the live panel. One model replaces them.

```go
type Limits struct {
	// Prepare bounds the reads of one cluster before its first submit.
	// It is --timeout. Zero means no limit.
	Prepare time.Duration
	// Wait bounds one cluster's run from its first submit to its end:
	// the waits, the reads of later phases, and verification. It is
	// --wait-timeout. Zero means no limit.
	Wait time.Duration
	// StepWait bounds the wait of each step on its own. Only addon update
	// sets it, to --wait-timeout, and leaves Wait at zero.
	StepWait time.Duration
	Poll     time.Duration // --poll-interval
}

// LimitsFor returns the limits of a command from its flags.
func LimitsFor(kind Kind, timeout, waitTimeout, poll time.Duration, noWait bool) Limits
```

| Budget | Starts | Covers | Pauses |
|---|---|---|---|
| `Prepare` | When a cluster's `Preparing` starts, and again for each new plan after a person's answer | The snapshot, the gate, the baseline | While a question is open; while the insights refresh is waited for, which has its own 5 minute limit |
| `Wait` | At the first submit of a cluster | Everything after it, for that cluster | While a question is open; while the run is `Paused` |
| `StepWait` | At each step's submit | That step's wait and confirm read | never |
| The plan of a dry run | In the command, `cmdutil.WithTimeout(ctx, --timeout)` | `NewPlan` | A dry run never asks |

Defaults, from `CONTRACT.md`: `--timeout` 1m; `--wait-timeout` 40m for `nodegroup update`, 5m for a scale, 5m for each add-on, 4h for an upgrade or a rollback; `--poll-interval` 15s. With `--no-wait`, `Wait` and `StepWait` are not used. In fleet mode every cluster has its own `Prepare` and its own `Wait`, and discovery runs under `--timeout`.

`Wait` starts at the first submit and not at the start of the command. The old `nodegroup update` put one deadline over the reads and the wait (`actions_update.go:164`). A short `--wait-timeout` on a slow day could then expire during the reads and look like a timeout of a roll that never started.

The operation owns the timers. It derives one context per cluster from the context given to `Start`, and cancels it with a cause when a budget runs out. The executor tells the two ends apart with `context.Cause`: the operation's own cause is a deadline, and anything else is an interrupt. The pause is the same technique as `cmdutil`'s pausable deadline (`deadline.go`), applied inside the operation, because the operation is what knows that a question is open or that the run is paused.

| Ending | Status | Exit | What the result says |
|---|---|---|---|
| `Prepare` ran out | `Failed`, with a `Timeout` failure | 1 | "timed out reading prod before anything was started (increase --timeout); nothing was changed" |
| `Wait` or `StepWait` ran out | `TimedOut` | 4 | Each step that was accepted and has not ended is `InProgress` with its update ID and a `Timeout` failure: "--wait-timeout passed before the update finished; it is still running in EKS" |
| The context was cancelled before any submit | `Interrupted` | 1 | "interrupted before anything was started; nothing was changed" |
| The context was cancelled after a submit | `Interrupted` | 1 | The same rows with an `Interrupted` failure, and `Resume`: the command line that attaches and goes on |

A deadline or an interrupt cancels nothing in EKS. The next run finds the resource `UPDATING` and attaches (upgrade, rollback, add-on update), or skips it as `AlreadyUpdating` (`nodegroup update`), or refuses it as busy (`nodegroup scale`). Those are the old behaviours (traces `cluster-upgrade-attach`, `nodegroup-update-busy`, `nodegroup-scale-busy`).

The old binary exits 1 for a wait deadline in all five commands (traces `*-deadline`). Exit 4 is contract edit 2.

## 9. The node observer and the roll panel

### `internal/noderoll`

The package imports client-go and nothing of refresh. It is a port of `legacy/internal/noderoll`.

| New file | From `observer.go` lines | Content |
|---|---|---|
| `noderoll.go` | 24-97 | `Phase`, `NodeView`, `PodView`, `Snapshot`, `WarnEvent`, the label constants |
| `observer.go` | 99-267 | The observer struct, the node list, `Snapshot` |
| `classify.go` | 549-638 | `classify`, the kubelet minor, pressure, readiness, drain taints |
| `pods.go` | 368-458, 531-547 | Pod counts of draining nodes, the evictable rule |
| `warnings.go` | 269-366, 460-529 | Warning events, their scope and dedupe |
| `informer.go` | `informer.go` | The node and event informers, the 10 s sync limit |
| `tracker.go` | `tracker.go` | Frozen: `Tracker`, `Event`, `EventKind` |
| `attach.go` | new | `Attach`, `Identity` |

`scripted.go` and `DemoTimeline` are not ported. They served the hidden `--simulate` flag, which `CONTRACT.md` does not have. The fake world rolls nodes.

```go
// Identity says how to tell the old nodes of a roll from the new ones.
type Identity struct {
	// TargetVersion is the Kubernetes minor the roll moves to. It is set
	// for an upgrade or a rollback roll and empty for an AMI roll.
	TargetVersion string
	// TargetAMI is the AMI the roll moves to, or "" when it is not known.
	TargetAMI string
	// StartedAt is when the roll began. The zero value means now.
	StartedAt time.Time
}

// Attach returns an observer for the nodes of one nodegroup. It starts
// the informers, and falls back to a list per snapshot when they do not
// sync in 10 seconds. It fails only when the first node list fails.
func Attach(ctx context.Context, kube kubernetes.Interface, nodegroup string, id Identity) (*Observer, error)

func (o *Observer) Snapshot(ctx context.Context) (Snapshot, error)
func (o *Observer) Watching() bool // informer-backed
func (o *Observer) Close()
```

`Attach` picks the mode once, where the old code had three setters and a precedence that the caller had to know (`observer.go:226-238,562-565`):

| Condition, first match | Mode | A node is new when |
|---|---|---|
| `TargetVersion` is set and a node of the nodegroup has another kubelet minor | Version | Its kubelet minor equals `TargetVersion` |
| The first node list succeeded | Baseline | It is not among the nodes created before `StartedAt`; with a zero `StartedAt`, not among the nodes present at `Attach` |
| `TargetAMI` is set | Image label | Its `eks.amazonaws.com/nodegroup-image` label equals `TargetAMI` |

The third row cannot be reached today, since `Attach` fails without the first list. It stays in `classify` because the frozen tests cover it.

The rest is unchanged: nodes are selected by the `eks.amazonaws.com/nodegroup` label, a node is `Draining` when it is cordoned or carries a drain taint, `Joining` when it is not Ready, else `Ready`. Pods are listed only for draining nodes, with a `spec.nodeName` field selector. An `Observer` is driven from one goroutine.

### Running it

A roll phase with `StartOptions.Nodes` set runs two best-effort observers beside the authoritative wait:

```go
err := fanout.RunAlongside(ctx, observe, func(ctx context.Context) error {
	return r.waitTogether(ctx, steps)
})
```

`observe` starts one goroutine per accepted roll step. Each calls `Attach`, then takes a snapshot every second when the observer is watching and every `min(Poll, 3s)` when it lists, feeds the snapshot to a `Tracker`, and emits a `NodeProgress` event:

```go
type NodeProgress struct {
	Nodegroup string
	Snapshot  noderoll.Snapshot
	Recent    []noderoll.Event // the tracker's last six
	Watching  bool
}
```

The second observer is the Auto Scaling watch (`scaling.go`, `WatchScalingFailures`): `DescribeScalingActivities` for the nodegroup's groups every `Poll`, and a `Notice` for each failed launch since the roll began. It runs whenever the phase waits.

Neither observer can change the result. `RunAlongside` cancels and joins both when the wait returns. A failed `Attach`, or five snapshot errors in a row, ends the node observer with one `Notice`, and the wait goes on.

The baseline is taken in `Attach`, after the submit, as in the old code (`rollview.go:394`). A node that EKS launches between the submit and the first list counts as old until it is replaced in the view. That affects the panel only.

### The panel

```go
package view

// RollPanel draws the live view of one roll: a header with the old and
// new version, a bar of replaced nodes, the node table with the drain
// progress of each node, the last tracker events, and the warnings.
func RollPanel(th Theme, p change.NodeProgress, desired int, elapsed time.Duration) []string
```

It is a port of `rollview.go:166-286`. The CLI renderer draws it through `view.Live` on each `NodeProgress` event. Progress lines of the wait are held while the panel is up and printed when it closes, which the old engine did with `heldProgress` (`nodegroups.go`). The renderer is the one consumer of the event stream, so it orders the two without a lock.

| Condition | Output during the wait |
|---|---|
| Table output, stderr is a terminal, not `--quiet`, exactly one roll is in flight, Kubernetes access | The panel, redrawn in place |
| `--live` and stderr is not a terminal | Panel frames appended, at most one each 15 s, and only when the frame changed |
| `--live` and no Kubernetes access | One warning line, then the lines of the next row |
| More than one roll in flight, or no Kubernetes access | One line per status change of each update, and a line per minute while nothing changes |
| `--quiet` | Nothing. Questions, warnings, and the final summary still print. |
| `-o json` or `-o yaml` | The progress lines on stderr. `--live` is a usage error. |

`StartOptions.Nodes` is set when the panel can show: the first two rows. `cluster upgrade` and `cluster rollback` roll one nodegroup at a time, so they get the panel on a terminal without a flag, as today (`commands/cluster/upgrade.go:304`). `--live` exists on `nodegroup update` only.

## 10. Commands

### Shared flags

```go
package cmdutil

// ChangeFlags are the flags every changing command has.
type ChangeFlags struct {
	DryRun, Yes, NoWait, Quiet               bool
	SkipHealthCheck, RequireHealthy, Force   bool
	WaitTimeout, PollInterval                time.Duration
}

// AddChangeFlags registers them. waitDefault is the command's default for
// --wait-timeout. noWait is false for cluster upgrade and cluster rollback,
// which then have no --no-wait flag.
func AddChangeFlags(cmd *cobra.Command, f *ChangeFlags, waitDefault time.Duration, noWait bool)

// Validate rejects the combinations that are wrong for every command.
func (f *ChangeFlags) Validate(format Format) error

// RequireYes returns the --yes error when the run would need a prompt it
// cannot show: a machine format or no terminal, without --yes or --dry-run.
// It reads nothing.
func RequireYes(ios *IOStreams, format Format, yes, dryRun bool) error

// Confirm asks on stderr and reads one line.
func Confirm(ctx context.Context, ios *IOStreams, question string) (bool, error)
```

`Validate`:

| Combination | Error |
|---|---|
| `--dry-run` with `--no-wait` | "--no-wait has no effect on a dry run" |
| `--poll-interval` of zero or less | "--poll-interval must be above 0" |
| A negative `--wait-timeout` | "--wait-timeout must be 0 or more" |
| `--skip-health-check` with `--require-healthy` | "they contradict each other" |

Every changing command has the same `RunE`:

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
```

and the same `run`:

```go
func (o *updateOptions) run(ctx context.Context) error {
	d, err := o.deps(ctx) // AWS provider, fetcher, kube, clock: the first network use
	if err != nil {
		return err
	}
	req := o.request()
	if o.change.DryRun {
		return cmdutil.PrintPlan(ctx, o.io, o.format, d, req, o.timeout)
	}
	return cmdutil.RunChange(ctx, o.io, o.format, d, req, o.startOptions(), view.NewChangeRenderer(o.io, o.format, o.change.Quiet))
}
```

`complete` resolves the cluster the mutation-safe way: the explicit value, `REFRESH_CLUSTER`, the active context, and never the kubeconfig. `startOptions` sets `CanAsk` and `ConfirmStart` to "no `--yes`", and `Limits` from `change.LimitsFor`.

One event stream gives both outputs. For a human run the renderer writes progress to stderr and the final view to stdout. For a machine run it writes the same progress lines to stderr, and `Print` writes the one document to stdout.

### The five commands

| | `addon update` | `nodegroup scale` |
|---|---|---|
| `Use` | `update [addon...]` | `scale <nodegroup>` |
| Args | Any number; none needs `--all` | Exactly 1 |
| Own flags | `-c`, `--version`, `--all`, `--skip-addon` | `-c`, `--desired`, `--min`, `--max` |
| `--wait-timeout` default | 5m, for each add-on | 5m |
| Output | `-o table\|json\|yaml\|plain` | the same |
| `validate` | Names and `--all` exclude each other; one of them is needed. `--version` needs exactly one name. `--skip-addon` needs `--all`. `--force` is a usage error: an add-on update drains nothing. | The size rules of section 5 |
| Request | `AddonUpdateRequest` | `ScaleRequest` |

| | `nodegroup update` | `cluster upgrade` | `cluster rollback` |
|---|---|---|---|
| `Use` | `update [nodegroup...]` | `upgrade [cluster]` | `rollback [cluster]` |
| Args | Any number; none means all | At most 1 | At most 1 |
| Own flags | `-c`, `-f`, `--all-clusters`, `--reroll`, `--skip-verify`, `--changelog`, `--live` | `--to`, `--only`, `-n`, `--skip-addon`, `--skip-nodegroup`, `--skip-insights-check`, `--skip-verify` | `--skip-nodegroup`, `--skip-insights-check`, `--rollback-timeout`, `--skip-verify` |
| `--wait-timeout` default | 40m | 4h | 4h |
| `--no-wait` | yes | no | no |
| `validate` | `--all-clusters` excludes `-c`, positionals, and `--kube-context`. `--changelog` needs `--dry-run`. `--live` needs `-o table` and excludes `--quiet`. `--reroll` with `--force` is allowed. | `--to` is required and parses as `1.<minor>`. `--only` values are known. `-n` excludes `--skip-nodegroup` of the same name. | `--rollback-timeout` is 0 or whole minutes from 2h to 168h |
| Request | `RollRequest` | `UpgradeRequest` | `RollbackRequest` |
| Extra in `startOptions` | `Nodes` by the panel rule | `ConfirmPhases` when there is no `--yes`; `Nodes` | `Nodes` |

Notes:

- `--require-healthy` is in the shared set (contract edit 1).
- `nodegroup update -f` selects by pattern. The plan lists the matches, and the `ConfirmStart` question names them. With `--yes` the notice lists them.
- `--changelog` stays in the command layer. It fetches the AMI release notes for the plan's roll steps and prints them under a dry-run plan. A failed fetch is a warning (trace `nodegroup-update-dry-run-changelog`).
- `cluster upgrade` on a terminal asks before each phase, as today (`upgrade.go:547`). `cluster rollback` asks once, with the whole order in the question (`rollback.go:278-305`).
- `cluster upgrade` and `cluster rollback` gain `-o plain` for the plan and the run through the shared view. The old rollback had none.

The options struct of each command holds the streams, the factory functions it needs, `cmdutil.ChangeFlags`, the output format, and its own flags. `nodegroup update` as the example:

```go
type updateOptions struct {
	io   *cmdutil.IOStreams
	deps func(ctx context.Context) (change.Deps, error)

	format  cmdutil.Format
	timeout time.Duration
	change  cmdutil.ChangeFlags

	target      cmdutil.Target // region and cluster, from complete
	nodegroups  []string
	filters     []string
	allClusters bool
	regions     []string
	reroll      bool
	skipVerify  bool
	changelog   bool
	live        bool
}
```

### Exit codes

Every command uses `Result.ExitCode()` (section 7) for a run and the plan's code (section 1) for a dry run. Before either:

| Case | Exit |
|---|---|
| A usage error, a name that does not exist, a `--version` the cluster cannot run, sizes that cannot hold | 1, stdout empty |
| The `--yes` error | 1, stdout empty |
| No cluster resolves | 1, stdout empty |

Every command needs a line in the exit-code help, which the ported exit-code-help test checks.

## 11. Calls, order, and parity

### How the suite constrains the design

`test/parity/compare.go` compares six fields for equality: the mutations with their payloads and token names, the preconditions of each mutation, the postconditions of the last one, the final world, whether the run was refused before any mutation, and the exit code. Three points follow.

1. A precondition is every distinct answer the binary saw, since its previous mutation, about the target, about the cluster, and about the previous target. Equality means the new binary may not read more about them, nor less.
2. Only EKS and Kubernetes reads name a target. Reads of EC2, Auto Scaling, SSM, CloudWatch, and Service Quotas are in no precondition.
3. `StartInsightsRefresh` counts as a mutation.

The command lines map as follows. A mapping is not a difference.

| Old | New |
|---|---|
| `-n ng-a`, `-a vpc-cni`, `-c prod` on a cluster command | The positional |
| `addon update` and `nodegroup scale` without `--wait` | `--no-wait` |
| `--wait` | Nothing: the default |
| `--skip coredns` | `--skip-addon coredns` |
| `--dependency-order`, `--health-check` | Nothing |
| `--check-pdbs` | Nothing, or `--require-healthy` where the scenario means "must check" |
| `-o json` | `-o json` |

### `addon update`

| # | New | Old (`addon-update-wait`) |
|---|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons` | `ListClusters`, `ListAddons` (name lookup), then the busy check: `DescribeCluster`, `ListNodegroups`, `ListAddons` |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` for each target | `DescribeNodegroup` × N, `DescribeAddon` × M, then `DescribeCluster`, `DescribeAddonVersions`, `DescribeAddon` in the service |
| 3 | The busy rule and the `Addons` check: no call | |
| 4 | `DescribeAddon` (the fresh read) | |
| 5 | **`UpdateAddon`** `{addonVersion, clientRequestToken}` | the same |
| 6 | `DescribeUpdate` until it ends | the same, every 5 s |
| 7 | `DescribeAddon` (confirm) | the same |

Predicted: the preconditions and postconditions are equal. Round trips before the update: 3, against about 8.

### `nodegroup scale`

| # | New | Old (`nodegroup-scale-wait`, `nodegroup-scale-pdb-safe`) |
|---|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons` | `ListClusters`; `DescribeNodegroup`, `DescribeSubnets`, `DescribeInstanceTypeOfferings` (availability) |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M | The busy check: `DescribeCluster`, `ListNodegroups`, `DescribeNodegroup` × N, `ListAddons`, `DescribeAddon` × M |
| 3 | The gate. Scale-up: `GetServiceQuota`, `GetMetricData`, and the availability reads. Scale-down: `list nodes`, `list poddisruptionbudgets`, then `list pods` of the namespaces at risk. | With `--check-pdbs`: `DescribeNodegroup`, `list namespaces` (probe), `list poddisruptionbudgets`, `list nodes` with a label selector, `list pods` per namespace |
| 4 | `DescribeNodegroup` (the fresh read) | `DescribeNodegroup` for the before sizes, and again in `CheckScaleBounds` |
| 5 | **`UpdateNodegroupConfig`** `{scalingConfig: the given sizes, clientRequestToken}` | the same |
| 6 | `DescribeUpdate` until it ends | the same, every 5 s |
| 7 | `DescribeNodegroup` (confirm) | the same |

Predicted: the EKS preconditions are equal. The Kubernetes ones differ in every `Kube: true` scenario that sends an update (class K below).

### `nodegroup update`

| # | New | Old (`nodegroup-update-kube-yes`) |
|---|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons` | `ListClusters`; the busy check (the same five operations, in series) |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M | |
| 3 | `DescribeAutoScalingGroups`, `GetParameter` × 2 per AMI type, then `DescribeInstances` | In the health check and again in the decide step |
| 4 | The gate: `GetMetricData` × 3, `GetServiceQuota`, `list nodes`, `list poddisruptionbudgets`, `list deployments`, `list nodes` (metrics), `list pods` × 3, then `list pods` of the namespaces at risk; beside it `DescribeSubnets`, `DescribeInstanceTypeOfferings` | `DescribeCluster` (kube target), `list namespaces` (probe), the health checks, the Pending-pod baseline, `ListNodegroups`, `DescribeNodegroup` per target (decide), the drain gate (`list poddisruptionbudgets`, `list nodes` with a selector, `list pods`) |
| 5 | The baseline: `list nodes`, `list pods` (Pending) | Taken before the gate |
| 6 | Per target, in order: `DescribeNodegroup` (the fresh read), **`UpdateNodegroupVersion`** `{version, clientRequestToken}`, plus `force: true` with `--force` | The same pair (`StartVersionUpdate`) |
| 7 | `DescribeUpdate` for every update, each tick | the same |
| 8 | Verification: `DescribeNodegroup` per rolled nodegroup, `list nodes`, `list pods` (Pending) | the same, without the node readiness rule |

Predicted: the EKS preconditions are equal, for one target and for several (`nodegroup-update-several`: the second mutation's only precondition is the fresh `DescribeNodegroup` of `ng-b`). The postconditions are equal. Round trips before the first update: 5, against about 14.

Fleet mode (`nodegroup-update-all-clusters`): `ListClusters` per region; rows 1 to 4 for every cluster, in parallel; then rows 1 to 8 for each cluster in turn.

### `cluster upgrade`

`cluster-upgrade-yes`, one hop, two add-ons that the new control plane cannot run, two nodegroups.

| # | New | Old |
|---|---|---|
| 1 | The plan read: `DescribeCluster`, `ListNodegroups`, `ListAddons`, `DescribeClusterVersions`, then `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` per add-on for 1.32 and 1.33, `DescribeAutoScalingGroups` | The same reads in series, without the Auto Scaling one |
| 2 | **`StartInsightsRefresh`**, `DescribeInsightsRefresh` until done, `ListInsights` | the same |
| 3 | The gate, for both nodegroups | Not here. Per nodegroup, at row 8. |
| 4 | Control-plane phase: the phase read (as row 1 without the version catalogues), the busy rule, `DescribeCluster` (the fresh read) | `DescribeCluster`, `ListNodegroups`, `DescribeNodegroup` × N (readiness), `DescribeCluster` |
| 5 | **`UpdateClusterVersion`** `{version}`, `DescribeUpdate` until done, `DescribeCluster` until `ACTIVE` at 1.33 | the same |
| 6 | Add-on phase: the phase read, the busy rule. Per add-on: `DescribeAddon`, **`UpdateAddon`**, `DescribeUpdate` until done, `DescribeAddon` | `ListAddons`, `DescribeAddon` × M, `DescribeAddonVersions`, then the same per add-on |
| 7 | Phase of `ng-a`: the phase read, the busy rule, the gate, the baseline, `DescribeNodegroup` | `DescribeCluster`, `ListNodegroups`, `DescribeNodegroup` × N, the built-in gate (`DescribeNodegroup`), the command's gate with the PDB check |
| 8 | **`UpdateNodegroupVersion`** `{version: 1.33}`, `DescribeUpdate` until done, verification | the same |
| 9 | Phase of `ng-b`: rows 7 and 8 | `ListNodegroups`, `DescribeNodegroup`, the gates, the roll |

Mutations: equal, in order, with the same payloads. Preconditions: not equal. Every phase read adds answers about the cluster that the old phase did not have: `ListAddons` before the control plane and the nodegroups, `ListNodegroups` before the add-ons, `DescribeCluster` before the second nodegroup. That is the busy rule at work, and class P below.

### `cluster rollback`

`cluster-rollback-yes`.

| # | New | Old |
|---|---|---|
| 1 | The plan read: as row 1 of the upgrade, with `ListUpdates`, `DescribeUpdate` per update, `ListInsights` (`ROLLBACK_READINESS`), and the catalogues for 1.32 | the same, in series |
| 2 | The gate, for both nodegroups | Not here |
| 3 | No second plan with `--yes` | A second full plan (`ExecuteRollback`, `rollback.go:643-686`) |
| 4 | Per nodegroup: the phase read, the busy rule, the gate, the baseline, `DescribeNodegroup`, **`UpdateNodegroupVersion`** `{version: 1.32}`, the wait, verification | `ListNodegroups`, `DescribeNodegroup`, the gates, the roll |
| 5 | Add-on phase, as in the upgrade | the same |
| 6 | Control-plane phase: the phase read, the busy rule, the nodegroups-at-or-below check from that read, `DescribeCluster`, **`UpdateClusterVersion`** `{version: 1.32}`, plus `force` and `rollbackConfig` when asked, `DescribeUpdate` until done, `DescribeCluster` | `DescribeCluster`, `ListNodegroups`, `DescribeNodegroup` × N, the same request |

### Intended differences and their allowlist entries

Each entry needs `scenario`, `field`, `reason`, `source`, `ownerApproved`, and `expect`. `source` must quote a row of "Behaviour the new CLI does not copy". The classes marked E1 to E6 need the new row that the contract edit of that number adds. The counts are predictions; the first paired run of each command settles them. The `expect` value of a `preconditions` or `postconditions` entry is recorded from the v2 trace and reviewed once.

| Class | Scenarios | Fields, with `expect` | Source row | Entries |
|---|---|---|---|---|
| Exists today | `nodegroup-update-no-yes` | All six | "`nodegroup update -o json` without `--yes`" | 6, no change |
| Exists today, changes | `nodegroup-scale-check-pdbs-no-kube`, run with `--yes --require-healthy` | `exitCode`: 2 (the entry says 4 now) | "Without Kubernetes access" | 1 |
| V: verification | `nodegroup-update-verify-node-not-ready` | `exitCode`: 5 | "Post-roll verification does not read node readiness" | 1 |
| | `nodegroup-update-verify-pods-unreadable` | `exitCode`: 4 | "If the pod list fails after a roll" | 1 |
| G: the gate runs before any change | `cluster-upgrade-pdb-blocked`, `cluster-upgrade-pdb-blocks-second-nodegroup`, `cluster-upgrade-pdbs-unreadable` | `mutations`: only `StartInsightsRefresh`; `preconditions`; `postconditions`; `world`: nothing upgraded; `exitCode`: 3, 3, 4 | "`cluster upgrade` reads PDBs only when it reaches the nodegroup phase" | 15 |
| | `cluster-rollback-pdb-blocks-second-nodegroup` | `mutations`: `[]`; `preconditions`: `[]`; `postconditions`; `world`; `refusedBeforeMutation`: true; `exitCode`: 3 | the same | 6 |
| | `cluster-rollback-pdb-blocked`, `cluster-rollback-pdbs-unreadable` | `exitCode`: 3, 4 | "A drain-gate refusal exits 3" | 2 |
| | `cluster-upgrade-nodegroup-degrades-mid-run` | `exitCode`: 3 | the same | 1 |
| U: an unreadable gate | `nodegroup-update-pdbs-unreadable` | `exitCode`: 4 (was 3) | "A check that cannot read its data" | 1 |
| | `nodegroup-scale-pdbs-unreadable` | `exitCode`: 4 (was 1) | "`nodegroup scale -o json` prints no document" | 1 |
| D: a dry run without Kubernetes access | `nodegroup-update-dry-run`, `nodegroup-update-dry-run-changelog`, `cluster-upgrade-dry-run`, `cluster-rollback-dry-run` | `exitCode`: 2 | "Without Kubernetes access" | 4 |
| K: the gate's Kubernetes reads | Every `Kube: true` scenario that still sends an update: 12 of `nodegroup update`, 4 of `nodegroup scale` | `preconditions`: no namespace probe, one node list without a selector, one deployment list | E6 | 16 |
| P: one read of the cluster before each phase | Every `cluster upgrade` and `cluster rollback` scenario that still sends an update: 43 less the 4 of class G | `preconditions`: the phase read's answers | "`cluster upgrade` has no busy check" | 39 |
| E2: deadline | `addon-update-deadline`, `nodegroup-scale-deadline`, `nodegroup-update-deadline`, `cluster-upgrade-deadline`, `cluster-rollback-deadline` | `exitCode`: 4 | E2 | 5 |
| E3: sizes | `nodegroup-scale-above-max`, `nodegroup-scale-min-above-max` | `mutations`: `[]`; `preconditions`: `[]`; `refusedBeforeMutation`: true | E3 | 6 |
| E4: the last read | `nodegroup-update-changed-under-the-gate` | `mutations`: `[]`; `preconditions`: `[]`; `exitCode`: 0 | E4 | 3 |
| E5: one rule for mixed results | `nodegroup-update-partial-failure` | `exitCode`: 4 (was 1) | E5 | 1 |
| | `nodegroup-update-cluster-starts-updating` | `exitCode`: 1 (was 4) | E5 | 1 |

About 110 entries, 55 of them in classes K and P. Two rows of `CONTRACT.md` need no entry, because only the output changes: the rollback that is interrupted before it starts now reports `Interrupted`, and the notice before an attached update is replaced. The accepted `Kubernetes` reason of every `Kube: false` run with `--yes` changes no compared field either.

One entry written out:

```json
{
  "scenario": "cluster-upgrade-pdb-blocked",
  "field": "mutations",
  "reason": "The plan runs the drain gate for every nodegroup before the control plane moves. The blocker stops the run after the insights refresh, the one request a blocked run may send.",
  "source": "`cluster upgrade` reads PDBs only when it reaches the nodegroup phase",
  "ownerApproved": true,
  "expect": [{"operation": "eks:StartInsightsRefresh", "region": "us-east-1", "target": "prod", "payload": {}, "outcome": "accepted IN_PROGRESS"}]
}
```

### Questions that the traces raise

These are in the traces, are not in `CONTRACT.md`, and the design does not reproduce them. Each has a contract edit in section 14, and none is settled until the owner answers.

1. **A wait deadline exits 1** in every command. The design exits 4 (E2).
2. **`nodegroup scale` sends sizes that EKS refuses.** The design validates first and sends nothing (E3).
3. **`nodegroup update` sends a roll after its last read showed `UPDATING`.** The design skips the nodegroup, exit 0 (E4).
4. **Mixed results have a different code in each command.** The design has one rule (E5).
5. **The old gate's Kubernetes reads** are part of the compared preconditions, and the new gate reads other things (phase 3, decisions 5 and 9). No row of the contract can be cited for it today (E6).
6. **On a terminal, the old `nodegroup update` starts with no confirmation when health is clean.** The design asks "proceed?" once, as the other four commands do. No scenario covers it. Contract edit 7.

Two more are about the suite and not the product:

7. **`Moves` and `Signal` are placed by the old binary's request count.** `nodegroup-update-changed-before-the-gate` moves the world at the eighth `DescribeNodegroup`, and `cluster-rollback-interrupt-before-start` signals at the second `ListInsights`. The new binary makes other counts: two `DescribeNodegroup` calls for `ng-a` before the roll, and one `ListInsights` with `--yes`. `Scenario` needs `V2Moves` and `V2Signal`, with the same meaning stated in words.
8. **Fifty-five entries for two classes.** The alternative is an allowance that covers a field for a group of scenarios with no pinned value. I recommend the 55 pinned entries (decision 6).

## 12. Test plan

**Table tests for pure decisions.**

| Function | Rows |
|---|---|
| `DecideAMIUpdate` | The seven rows, from the frozen `ami_update_test.go`, plus `--reroll` on an outdated AMI |
| `Busy` | Each status of each resource type, as a target and not; each snapshot group not `Collected` |
| `Plan.Decision` and the dry-run exit code | Every row of the table in section 1, crossed with a failure |
| `Diff` | One row per `ChangeKind`, refuse over ask, a step that vanished because it is done |
| `Result.ExitCode` | Every `RunStatus`; every row of the mixed-outcome table; each pair of cluster results in fleet mode (the drift table asks for this); interrupt over everything |
| The add-on step table, the dependency sort | The frozen tests of `addon_dependencies_test.go` and `service_test.go` |
| Scale validation | Each row of both tables in section 5 |
| The upgrade and rollback planners | The frozen `plan_test.go`, `scope_test.go`, `rollback_test.go`, with `Step` values in place of labels |
| `LimitsFor` | Each kind, `--no-wait`, zero values |
| `noderoll.classify`, `Tracker`, the mode choice of `Attach` | The frozen tests, and one row per mode |

**Waits.** Against `awsxtest` with update scripts and the virtual clock, with `Deps.After` as the handshake: each row of the three tables in section 2; a throttled poll, then success; access denied in the middle of a wait (`*UnobservedError`); a response with no update; a cancelled context; `WaitUpdate` sends `DescribeUpdate` only, checked on the request log.

**Operation lifecycle.** These tests replace the executor with a scripted function (the unexported seam that `Start` uses) and run inside `testing/synctest`, with `Deps.After` set to `time.After`. The fake HTTP server is kept out of them, because network reads are not durably blocked and a `synctest` bubble would not settle. Run with `-race`; `goleak` in `TestMain`.

- Every row of the operation state table and of the phase state table, as a named subtest that asserts the events in order.
- The log: a reader that never reads does not block `emit`; 1,000 progress events for one step leave one entry; lifecycle events are never lost; two readers with their own cursors; `Next` returns `io.EOF` after `Finished`; `Next` returns when its own context ends.
- Questions: two concurrent `Answer` calls give one winner and one `ErrStaleQuestion`; an answer with an old ID; an answer after `Finished`.
- The races, each as a loop over both orders with `synctest.Wait` between: answer and pause, answer and stop, answer and cancel, pause and stop, pause and cancel, stop and cancel, and all four at once. Each asserts the final status by the precedence rule and that nothing was submitted after the stop or the cancel.
- Pause holds between phases and never inside one; `Resume` wakes it; `Stop` while paused; a failure after a stop request is `Failed`.
- Deadlines: `Wait` starts at the first submit; a question pauses `Prepare` and `Wait`; `StepWait` per step; deadline against cancel through `context.Cause`.
- Cleanup: at `Done` the claim is free, no goroutine is left, `Result` returns the same value twice.
- `Observe`: with an update ID, without one, a failed update, no controls.

**Whole-command tests.** Through `cmdtest.NewFake` and a `kubetest.Server`, with goldens for stdout, stderr, and the exit code in the four formats, and the request log asserted. For each command: success, `--no-wait`, a dry run that would proceed, ask, and be blocked, the `--yes` error, a declined prompt, a busy refusal, a failed update, a deadline, an interrupt, attach, a lost response. For `nodegroup update` also: several targets with one failed, a submit that EKS refuses, `--reroll`, `--force`, a custom AMI, `-f`, the fleet with a blocked cluster and with a failed one. For `cluster upgrade` also: two hops, `--only` of each part, `-n`, both skip flags, resume after each phase, an attached update that fails (the notice), a busy refusal before the second phase. Every machine document validates against its schema in `TestDocumentsMatchSchemas`.

**The command-order test** of `REWRITE.md` gets the five commands on its list.

**`TestPaired`.** Every scenario gets its `V2` command line, and `V2Preview` where it has a preview. The allowlist grows by the classes of section 11. `TestAllowlistFile` already fails on an entry whose source is not a contract row, and `Unused` fails on an entry that no difference uses.

**Old tests that name a past regression.** Ported, or covered by a named case above. The files are listed by name. The right column is the behaviour of the code they sit beside, as I read it in the code and the traces; I did not read every test. The `PORTING.md` triage of each file confirms or corrects the row.

| Old test file | What it protects |
|---|---|
| `services/nodegroup/version_update_test.go`, `start_roll_test.go` | The version is always sent; an empty version is refused before any request; one token for the retries |
| `services/nodegroup/ami_update_test.go` | The decision table; a custom AMI is never rolled, with `--force` too |
| `services/nodegroup/scale_request_test.go`, `scale_wait_test.go`, `scale_pdb_gate_test.go` | A size that does not match after the update fails; a transient poll error does not end the wait |
| `monitoring/monitor_test.go` | A permanent poll error settles one update and not the others; a response with no update is transient; a cancelled monitor is not a timeout |
| `commands/nodegroup/update_document_test.go`, `machine_output_test.go`, `verify_test.go`, `timeout_scope_test.go` | Row statuses; not-attempted rows after an interrupt; verification is not run after a failed roll |
| `commands/nodegroup/fleet_test.go`, `fleet_regions_test.go` | One cluster's failure does not stop the rest; the exit order; skipped regions are not failures |
| `commands/nodegroup/liveroll_test.go`, `rollview/resilience_test.go`, `panel_test.go`, `live_test.go` | The panel never changes the result; a panel that ends early does not end the wait |
| `services/addons/service_test.go`, `update_wait_test.go`, `update_guard_status_test.go`, `update_errors_test.go`, `update_all_timeout_test.go` | The version guard; `Successful` with the wrong version fails; attach with no update ID; failure reasons |
| `services/upgrade/engine_test.go`, `nodegroups_test.go`, `controlplane_test.go`, `addons_test.go`, `resume_test.go`, `order_test.go`, `verify_test.go` | Phase order; resume skips what is done; attach; an at-version nodegroup that is not `ACTIVE` is verified, not skipped; "before" add-ons precede the rolls |
| `services/upgrade/insights_test.go`, `plan_test.go`, `scope_test.go`, `catchup_test.go` | Refresh reuse; the scheduled-refresh retry; an empty insight list blocks a real run; skew counts skipped nodegroups; `--only` errors |
| `services/upgrade/rollback_test.go`, `stopped_test.go` | The window and its slack; an in-flight rollback to the same target attaches; a stop is not a failure and a failure after a stop is one |
| `services/cluster/busy_test.go`, `commands/nodegroup/busy_test.go` | The busy statuses; `DEGRADED` is not busy; an unreadable busy check refuses |
| `noderoll/observer_test.go`, `informer_test.go`, `tracker_test.go`, `podeviction_test.go` | The drain taints; pods only for draining nodes; the informer fallback; the first snapshot emits nothing |
| `commands/runner/mutate_test.go`, `deadline_test.go` | The `--yes` error before any call; a prompt pauses the deadline |
| `tui/live/upgrade_test.go`, `roll_test.go`, `rollback_test.go`, `live_test.go` | The cases that concern the handle move to the lifecycle tests: a second answer is refused; a plan change asks; a changed roll version refuses; an adopted change has no controls |

`legacy/PORTING.md` records each file.

**What the fakes still need.**

| Where | Addition |
|---|---|
| `test/parity` | `Scenario.V2Moves` and `V2Signal`; the `V2` command lines |
| `fleettest` | An update answer with no update ID (`Fault{OmitUpdateID: true}`); an insights refresh that fails and one that never ends; a nodegroup in `CREATE_FAILED` and in `DELETING`; an add-on in `DELETING` |
| `awsxtest` | `ListUpdates` with paging; a `Gate` wave helper that accepts "this wave, in any order, plus these mutations in this order" |
| `kubetest` | Nothing beyond what phase 3 lists |
| `cmdutiltest` | A scripted terminal for whole-command tests: answer the nth question, close stdin, send the cancel |

## 13. Build order

The order in `REWRITE.md` holds, with three additions: the documents and the shared command code come first, the node observer is its own step after `nodegroup update` works without it, and `Observe` comes last because only the TUI calls it. Each step ends with `task dev:full` green and `TestPaired` passing for the scenarios of the commands built so far.

1. **Types and the shared command code.** `Plan`, `Step`, the IDs, `Plan.Decision`, `Diff`, `Result`, `ExitCode`; the `Plan` and `Run` documents and their schemas; `cmdutil.ChangeFlags`, `RequireYes`, `Confirm`; the command-order test with an empty list. Table tests only.
2. **The waits and the handle.** `WaitUpdate`, `WaitSettled`, `Operation`, the log, questions, pause, stop, limits, claims, with the scripted executor. All lifecycle tests.
3. **`addon update`.** `Busy`, `health.Addons`, the conditional `Kubernetes` result in `health.Run`, `plan` for add-ons, `runBatch` with its serial form, `cmdutil.RunChange`, the renderer without the panel, the plan and run views. Paired: the 23 `addon-update-*` scenarios.
4. **`nodegroup scale`.** Validation, the scale gate, the confirm read. Paired: the `nodegroup-scale-*` scenarios. The contract edits E2 and E3 must be decided before this step ends, because both first show here.
5. **The roll batch and `nodegroup update`.** `StartNodegroupRoll`, `DecideAMIUpdate`, the roll plan, the baseline, the wait together, verification, the Auto Scaling watch, `-f`, `--changelog`. Paired: the `nodegroup-update-*` scenarios but two.
6. **`noderoll` and the panel.** The port, `Attach`, `NodeProgress`, `view.RollPanel`, `--live`. Paired: `nodegroup-update-live-view`, `nodegroup-update-prompt-yes`.
7. **Fleet mode.** Discovery, the fleet plan, per-cluster limits, the checkpoint between clusters. Paired: `nodegroup-update-all-clusters`.
8. **The upgrade engine and `cluster upgrade`.** The planner port, the insights refresh, the phases, attach and its notice, per-phase confirmation. Paired: the `cluster-upgrade-*` scenarios.
9. **`cluster rollback`.** The planner port, the three phases, the request fields. Paired: the `cluster-rollback-*` scenarios.
10. **`Observe` and close-out.** `Observe`, the benchmarks (`Next` with 10,000 events, `RollPanel` allocations), the livecheck rows, `gen-docs`, the `PORTING.md` rows, and the measurement of round trips for section 15.

Phase 4 ports the hardest command of each kind first within its group, as the risk table of `REWRITE.md` asks: the single add-on before `--all`, one cluster before the fleet, the upgrade before the rollback.

## 14. Decisions for the owner and contract edits

### Decisions

| # | Question | Recommendation |
|---|---|---|
| 1 | `Start` takes the request, with the reviewed plan as an option, and not `Start(ctx, plan)`. | Yes. A `--yes` run then reads the cluster once, and the comparison with a reviewed plan is one code path for the TUI and for the CLI after a prompt. |
| 2 | Exit 4 for a wait deadline (old: 1). | Yes. The change was accepted and its end was not seen. A script can tell that from an error and from Ctrl+C. |
| 3 | One rule for mixed results: every attempted step failed is 1, some failed is 4 (old: differs by command). | Yes. It is what the words of exit code 4 say. |
| 4 | Keep row 3 of the roll table: `--force` also rolls a nodegroup that is on the latest AMI. | Keep it for this phase, since the port is frozen. I would remove it later: `--reroll` exists for that, and a flag about PDBs should not start a roll. |
| 5 | The gate of `addon update` is the `Addons` check alone, and a scale that removes no node runs `Quotas` alone. | Yes. Neither drains a node, so neither should ask about Kubernetes access. |
| 6 | 55 pinned `preconditions` entries for classes K and P, or a grouped allowance with no pinned value. | The pinned entries. They are recorded by the suite and reviewed in one sitting, and they keep the field checked. |
| 7 | Attach by resource status inside the engine, with no update ID lookup. | Yes. It is the old behaviour and costs no call. The notice cannot quote the foreign update's error; `refresh cluster describe` can. |
| 8 | `cluster rollback` asks when its steps changed, where the old command refused. | Yes, one table for both engines. With `--yes` the fresh plan is the plan. |
| 9 | A `Prepare` timeout exits 1 and a `Wait` timeout exits 4. | Yes. Nothing was changed in the first case. |

### Proposed contract edits

1. **Shared flags.** Move `--require-healthy` from `nodegroup update` to the flags of every changing command. Two rows of the contract already assume it. Add `--skip-verify` to `cluster upgrade` and `cluster rollback`, which have it today.
2. **New row, exit codes.** "A wait that outlasts `--wait-timeout` exits 1, like an error. | It exits 4. The result names each update that is still running. An interrupt stays 1."
3. **New row, scale.** "`nodegroup scale` sends sizes that EKS will refuse, and sends a request that changes nothing. | It checks the sizes against each other and against the nodegroup first. Sizes that cannot hold are an error, exit 1, with no request. A scale to the current sizes does nothing, exit 0."
4. **New row, the last read.** "`nodegroup update` sends the roll when its last read shows the nodegroup `UPDATING`, and EKS refuses it. | Every change acts on its last read. The nodegroup is skipped as already updating."
5. **New row, mixed results.** "A partial failure exits 1 in `nodegroup update` and 4 in `addon update --all`. A submit that EKS refuses exits 4 in `nodegroup update` and 1 elsewhere. | One rule: a run in which every attempted step failed exits 1, and a run with some steps done and some failed exits 4."
6. **New row, reads.** "The gate probes the Kubernetes API with a namespace list, lists the nodes of each target with a label selector, and lists deployments per namespace. | The gate makes one node list, one PDB list, and one deployment list for a run, and no probe." Phase 3 decided the behaviour; the row lets the allowlist cite it.
7. **New row, confirmation.** "On a terminal, `nodegroup update` starts without a question when the health checks pass. | Every changing command asks once before its first change, unless `--yes`. `cluster upgrade` asks before each phase."
8. **`addon update`.** `--version` needs exactly one add-on name. `--skip-addon` needs `--all`. `--force` is not accepted. `--no-wait` with `--all` submits every add-on in dependency order.
9. **`nodegroup update --all-clusters`** is a usage error with `-c`, a positional, or `--kube-context`.
10. **Documents.** `Plan` and `Run` have the fields of section 1. `Run.status` and `StepResult.status` have the values of section 7.
11. **`cluster rollback -o plain`** exists, through the shared view.
12. **The dry-run exit rule** already in the contract gains one clause: "and 4 when the plan itself could not read something".

Three points of `REWRITE.md` change in this note and need no contract edit: `Start` takes a request (decision 1); the phase states gain `Checking` and `Blocked`; and `--no-wait` is also allowed for `addon update --all`, which waits for nothing between its submits.

## 15. Changes made while building

Empty until code changes the design.
