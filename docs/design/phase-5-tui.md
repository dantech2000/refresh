# Phase 5 design note: the TUI

Status: draft 1. It belongs to `REWRITE.md` and stays inside `CONTRACT.md`. Phase 4 (`docs/design/phase-4-changes.md`) is the source for the executor, its hooks, plans, `Expect`, the consent rule, and the busy rule. Phase 3 is the source for the gate and the readiness checks. Phase 2 is the source for `Snapshot`, `Fetcher`, and `Sweep`. Where `internal/change` in this checkout and the phase 4 note differ, the note is the reference: the roll batch and the engines are mid-build.

Scope: the command `refresh ui`, the TUI model and its screens, the backend that sweeps the fleet and runs changes, the handle that adapts the synchronous executor to an interactive front end, the watching of changes started elsewhere, and the simulator switch.

A claim about the old tree names a file and line under `legacy/internal/`.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | The handle is `change.Start(ctx, d, req) *Handle`: `Run` on a goroutine of its own, the three hooks turned into a log, a question with an ID, and a checkpoint that pauses or stops. It lives in `internal/change`, beside `Run`, because it is the asynchronous form of `Run` and knows nothing of a terminal. |
| 2 | A change started elsewhere is `change.Watch(ctx, d, resource) *Handle`: the same handle, with no controls, over `WaitUpdate` or `WaitSettled`, the node observer, and the post-roll verification. |
| 3 | The TUI gives its consent through `Common.Expect`: the reviewed plan of the dialog. It never sets `Policy.Yes`. Every question that `Run` asks after that is shown as a question with `y` and `n`. |
| 4 | `--allow-changes` is read once, at start, and enforced in one function: `backend.Start`. There is no runtime unlock. |
| 5 | "Busy" in the TUI means "this UI runs or watches a change on the cluster". Whether AWS is changing something is `change.Busy`'s question, answered in the plan and in the fresh read before each submit. The TUI reads no status to decide it. |
| 6 | The model draws a `state.State` that the backend publishes with a version. The model waits for the next version in a `tea.Cmd` and never polls. A clock tick runs only while an elapsed time is on screen. |
| 7 | One sweep loop with `fleet.Fetcher.Sweep`, every `--interval`, on `ctrl+r`, and for one cluster after a change on it ends. |
| 8 | Bubble Tea v2 and Lip Gloss v2, already in `go.mod`. The `Seg`, `Line`, `Block` layout is ported as it is. Three screens: Fleet, Cluster, Change. Key bindings are one table that drives the handler, the key bar, and the help. |
| 9 | The simulator is the `ui` command with a factory that points at an in-process `awsxtest` and `kubetest` over a generated `fleettest.World` whose clock follows the wall clock. No package under `internal/tui` knows about it. |
| 10 | Dropped from the old TUI: the runtime unlock (`ctrl+u`), the session export (`e`), the AWS call pane and its log handler, the kubectl-cluster cursor, and the separate Rolls and Upgrade screens. Section 15 lists each for the owner. |

## 1. Packages

```
internal/cmd/ui/        the cobra command: flags, validate, run; the stdio detach; the simulator switch
internal/tui/           the Bubble Tea model, the screens, the key table, the layout
internal/tui/state/     State and its parts: what the model draws. Imports apidoc, fleet, change, health, noderoll, view for their types.
internal/tui/backend/   the live backend: the sweep loop, readiness, plans, handles, adoption, the publisher
internal/change/        gains handle.go and watch.go (section 2)
```

Dependency direction, added to the `imports_test.go` rule of `REWRITE.md`: `tui` imports `state`, `view`, and `change` for types; it never imports `awsx`, `fleet`'s fetcher, `kube`, or `backend`. `backend` imports `change`, `fleet`, `health`, `kube`, `awsx`, `noderoll`, `state`. `state` imports no package that makes a call. The consumer-defined interface that the model needs is in `tui`:

```go
package tui

// Backend is what the model needs. backend.Live implements it, and the
// tests of the model use a scripted one.
type Backend interface {
	// Next returns the first state whose version is above after. It blocks
	// until there is one, or ctx ends.
	Next(ctx context.Context, after uint64) (state.State, error)
	// Refresh asks for a sweep now.
	Refresh()
	// Readiness starts the readiness checks of a cluster. The result lands
	// in a later state.
	Readiness(ctx context.Context, c state.Key) error
	// Plan is the dry run of an action. It changes nothing.
	Plan(ctx context.Context, a state.Action) (state.Proposal, error)
	// Start runs the proposal. It is refused without --allow-changes, when
	// the proposal is not the newest, and when the cluster has a change.
	Start(ctx context.Context, p state.ProposalID) (state.ChangeID, error)
	Answer(ctx context.Context, c state.ChangeID, q change.QuestionID, yes bool) error
	Pause(ctx context.Context, c state.ChangeID) error
	Resume(ctx context.Context, c state.ChangeID) error
	Stop(ctx context.Context, c state.ChangeID) error
}
```

The model calls every method inside a `tea.Cmd`, so a slow backend never holds the UI loop (`legacy/internal/tui/model.go:5-7` had the same rule, and `TestUpdateNeverCallsTheBackend` guards it).

## 2. The handle

### What the old `live` package needed

Each row is something `tui/live` built by hand around the engine. The piece on the right serves it. Nothing else is built.

| Need | Old code | Served by |
|---|---|---|
| A feed of what happened, per change, that the UI reads at its own pace | `b.feed`, `b.log`, copied every 100 ms (`live.go:721-765`) | `Handle.Events(after)` over an append-only log with sequence numbers |
| The state of each step and phase | `upgradeSnapshot`, phases matched by label prefix (`upgrade.go:462-487`) | `Handle.View().Steps`, keyed by `StepRef`; the phase state is derived (section 2, "Phase states") |
| The nodes of a roll, the last poll of an update | `rollSnapshot` (`roll.go:775-806`) | `View().Progress[StepRef]`: the latest `UpdatePolled` and `NodeProgress`, coalesced |
| A question that exactly one answer closes, not a stale key | `Answer` matched by cluster only (`live.go:1136-1158`) | `QuestionID`; `Answer(id, yes)` returns `ErrStaleQuestion` for any other |
| Pause before the next phase, stop after the current one | `waitToGoOn` (`upgrade.go:333-350`) | `Pause`, `Resume`, `Stop` as intents that the checkpoint hook reads |
| A stop that is not a failure | `finishRun` (`upgrade.go:302-329`) | `Result.Status` is `Stopped` only from a checkpoint |
| The dry run first, then "the plan changed" | five acceptance maps (`live.go:165-184`) | `Common.Expect`; `Run` asks `PlanChanged` |
| The same pipeline as the CLI | a third copy of the roll (`roll.go:331-448`) | `Run` with the same request as the command |
| One change per cluster | `b.claimed` (`live.go:162`) | The backend's map, section 4. Not in the handle. |
| A change started elsewhere | `adoptRoll`, `adoptUpgrade` (`observed.go:167-308`) | `change.Watch` |
| Memory that does not grow with the session | `b.rolls`, `b.upgrades` never trimmed | The backend keeps the last 20 finished handles; the log of a handle is bounded by its plan |
| The deadline paused while asking | the pausable deadline | Phase 4, section 8: the budgets do not run inside `Ask` and `Checkpoint` |

Cut: a journal of every event (`journal.go`), the AWS call log handler (`live.go:232-266`, the latent deadlock), optional interfaces found by type assertion (`keys.go:570-577`), the simulator's own state machine.

### Types

```go
package change

// QuestionID names one question of one handle. It is never reused.
type QuestionID uint64

// RunState is where a run is between Start and Finished.
type RunState string

const (
	Preparing RunState = "Preparing" // reading, revalidating, gating; nothing submitted
	Running   RunState = "Running"   // a phase runs
	Paused    RunState = "Paused"    // held at a checkpoint
	Asking    RunState = "Asking"    // a question is open
	Finished  RunState = "Finished"  // Result is set
)

// Controls says what a front end may do with the handle now.
type Controls struct {
	Answer bool // a question is open
	Pause  bool // the kind has checkpoints and the run is not finished
	Stop   bool // the same
}

// PendingQuestion is a question that waits for its answer.
type PendingQuestion struct {
	ID      QuestionID
	AskedAt time.Time
	Question
}

// Progress is the latest poll and the latest node view of one step.
type Progress struct {
	Update *UpdateProgress
	Nodes  *NodeView
}

// LogEntry is one lifecycle event of a run, numbered from 1.
type LogEntry struct {
	Seq uint64
	Event
}

// View is a handle at one moment. It is immutable: every slice and map is
// a copy, or a reference to data that the handle never changes again.
type View struct {
	Version   uint64
	State     RunState
	Kind      Kind
	Target    Target
	Elsewhere bool // from Watch: no controls, no plan of ours
	StartedAt time.Time
	// Plan is the last plan announced, nil before PlanReady.
	Plan *Plan
	// Steps has the latest result of every step of Plan, in plan order.
	Steps []StepResult
	// Phases has one entry per phase of Plan, in order (section "Phase states").
	Phases   []PhaseState
	Question *PendingQuestion
	// PauseRequested and StopRequested are the intents, whether or not a
	// checkpoint has read them yet.
	PauseRequested, StopRequested bool
	Progress map[StepRef]Progress
	Controls Controls
	// Result is set at Finished.
	Result *Result
	// LastSeq is the sequence number of the newest log entry.
	LastSeq uint64
}

// Handle follows and steers one run. Every method is safe for concurrent
// use. Nothing a front end calls blocks the executor.
type Handle struct { /* unexported */ }

// Start runs the change on a goroutine of its own and returns at once.
// ctx owns the run: cancelling it is an interrupt. The handle reports
// through its own log, so Hooks are not a parameter.
func Start(ctx context.Context, d Deps, req Request) *Handle

func (h *Handle) View() View
// Changed returns a channel that is closed at the next change of the
// handle: an event, a state, a question, an answer, the result. A reader
// takes the channel, reads the view, and waits on the channel before it
// reads again. It is the pattern of fleettest.World.Changes.
func (h *Handle) Changed() <-chan struct{}
// Events returns the log entries with Seq above after, oldest first, and
// the Seq of the last one. It copies nothing but the slice header.
func (h *Handle) Events(after uint64) []LogEntry
func (h *Handle) Answer(id QuestionID, yes bool) error // ErrStaleQuestion, ErrNoControls
func (h *Handle) Pause() error                          // ErrNoControls, ErrFinished
func (h *Handle) Resume() error
func (h *Handle) Stop() error
// Cancel interrupts the run. It returns at once; Done tells when the run
// ended.
func (h *Handle) Cancel()
func (h *Handle) Done() <-chan struct{}
// Result blocks until Finished. It can be called again.
func (h *Handle) Result() Result
```

### How the hooks become the handle

`Start` builds `Hooks` over the handle and calls `Run` on a new goroutine:

| Hook | What the handle does | Blocks the executor |
|---|---|---|
| `Event` | Under `h.mu`: `UpdatePolled` and `NodeProgress` replace `Progress[step]`; `StepChanged` replaces `Steps[i]`; `PhaseStarted` and `PhaseEnded` update `Phases`; `PlanReady` replaces `Plan` and resets `Steps` to the plan's steps as `NotAttempted`; every kind but the two progress kinds is appended to the log. Then `version++` and the broadcast. | For the time of a map write. Never on a reader. |
| `Ask` | Registers a `PendingQuestion` with the next ID, sets `Asking`, broadcasts, and waits on the answer channel, `ctx.Done()`, and the stop channel. The question is registered before the broadcast, so a reader never sees a question it cannot answer. | Until the answer. That is the design: `Run` cannot go on. |
| `Checkpoint` | Reads the intents under `h.mu`. Stop: returns `{Stop: true}`. Pause: sets `Paused`, broadcasts, and waits on the resume channel, the stop channel, and `ctx.Done()`; returns `{Waited: true}` after a resume and `{Stop: true, Waited: true}` after a stop. Neither: returns the zero value. | While paused. |

The log is append-only and never dropped. Its size is bounded by the plan: `PlanReady` is sent once per plan computed (at most four per question round), a phase sends two events, a step sends at most three `StepChanged` (accepted, ended, verified), and a `Notice` comes from the plan diff, the busy rule, the gate, or one Auto Scaling activity, each once. A roll of one nodegroup logs under 40 entries; an upgrade of two hops with six nodegroups under 150. A test asserts both numbers against the fakes. The progress kinds, which are unbounded, never reach the log.

### Questions

```go
var (
	ErrStaleQuestion = errors.New("the question is no longer open")
	ErrNoControls    = errors.New("the change was started elsewhere and cannot be steered")
	ErrFinished      = errors.New("the change has finished")
)
```

`Answer(id, yes)`: under `h.mu`, if `h.question == nil || h.question.ID != id`, return `ErrStaleQuestion`. Else clear the question, set the state back (section "States"), send the answer on the question's channel (buffered 1), broadcast. Exactly one answer wins; the second call finds no question. A key pressed from a stale screen carries the old ID and is refused, which the old backend could not do (`live.go:1143`).

A stop while a question is open answers it with no, as the old `n` did ("stop after this step", `docs/commands/ui.md`). `Run` then ends as `Declined`, and `View.StopRequested` says why. A cancel while a question is open ends `Ask` with `ctx.Err()`, and `Run` ends as `Interrupted`.

Precedence, as `REWRITE.md` states it: cancel, then stop, then pause, then answer. Each of the three channels is read in that order in every `select`. Pause intent and stop intent are fields apart from the question, so answering while paused leaves the run paused, and a pause requested while asking takes effect at the next checkpoint.

### States

| From | Trigger | To |
|---|---|---|
| (none) | `Start` | `Preparing` |
| `Preparing` | `Ask` hook | `Asking` |
| `Preparing` | `PhaseStarted` | `Running` |
| `Preparing` | `Run` returns (refused, nothing to do, declined, hook error, interrupt) | `Finished` |
| `Running` | `Checkpoint` with pause intent | `Paused` |
| `Running` | `Ask` hook (a gate that got worse, `ConfirmPhase`, a fleet turn) | `Asking` |
| `Running` | `Run` returns | `Finished` |
| `Paused` | `Resume` | `Running` |
| `Paused` | `Stop` | `Running`, then `Finished` as `Stopped` |
| `Paused` | cancel | `Finished` as `Interrupted` |
| `Asking` | `Answer(id, true)` | `Preparing` when no phase has started, else `Running` |
| `Asking` | `Answer(id, false)`, or `Stop` | `Finished` as `Declined`, after `Run` returns |
| `Asking` | cancel | `Finished` as `Interrupted` |
| `Finished` | anything | `Finished`; `Answer`, `Pause`, `Resume`, `Stop` return `ErrFinished` |

Only `Run` returning sets `Finished`. A `Stop` that `Run` reads at a checkpoint ends as `Stopped`; a step that fails after a stop was requested ends as `Failed`, as phase 4's `Result.Status` already holds (`TestAStopDoesNotHideAFailure` in `legacy/internal/tui/live/upgrade_test.go:434` carries over as a handle test).

### Phase states

The handle derives them. No code matches a label.

```go
type PhaseStatus string // Pending, Running, Done, Skipped, Failed

type PhaseState struct {
	Ref    PhaseRef
	Status PhaseStatus
	StartedAt, EndedAt time.Time
}
```

| Events seen | Status |
|---|---|
| No `PhaseStarted` | `Pending` |
| `PhaseStarted`, no `PhaseEnded` | `Running` |
| `PhaseEnded` with `Skipped` | `Skipped` |
| `PhaseEnded`, every step `Succeeded`, `Skipped`, or `Started` | `Done` |
| `PhaseEnded`, otherwise | `Failed` |

`REWRITE.md` named `Submitting`, `Waiting`, and `Verifying`. The step results carry them: a step is `Started` while its update is waited for, and a `VerifyReport` ends a batch. The view does not need a fourth copy.

### Cleanup and goroutines

| Goroutine | Started by | Ends when | Owner |
|---|---|---|---|
| The run: `Run(ctx, d, req, hooks)` | `Start` | `Run` returns; the handle sets `Result`, `Finished`, closes `done`, broadcasts | The handle |
| The run's own: gate checks, poll fan-out, observers | `Run` | Joined by `Run` (phase 4, section 7) | `Run` |
| The notifier: `for { <-h.Changed(); b.bump() }` | `backend.Start` and `backend.adopt` | `h.Done()` | The backend |

`Start` makes a child context with `cancel`; `Cancel` calls it. At `Finished` the handle holds its plan, its steps, and its log until the backend drops it. It holds no client and no observer: `Run` closed them. `internal/change` already has `goleak.VerifyTestMain`.

### `Watch`

```go
// Watch follows a change that something else started: the CLI, the
// console, an automation. It waits for the update in flight on the
// resource, shows the nodes of a nodegroup roll, and verifies a nodegroup
// after it. The handle has no controls. The result has one step, with
// Attached set.
func Watch(ctx context.Context, d Deps, r Resource, o WatchOptions) *Handle

type WatchOptions struct {
	Nodes  bool   // start the node observer of a nodegroup
	Limits Limits // Wait bounds the watch; Poll is its interval
}
```

What it runs, on its goroutine, with the same events as `Run`:

1. `PlanReady` with a plan of one step: `PhaseRef{Target, Kind}` where the kind is `PhaseRoll` for a nodegroup, `PhaseControlPlane` for the cluster, `PhaseAddons` for an add-on; the step is `Attach` with reason `InFlight`. The plan's `Kind` is `NodegroupUpdate`, `AddonUpdate`, or by the direction of the update's version against the cluster's version, `ClusterUpgrade` or `ClusterRollback`. The old TUI got that wrong once (`TestAWatchedRollbackSaysRollback`, `roll_test.go:853`).
2. `inFlightUpdate` (`busy.go:211`) finds the update in progress. With an ID: `WaitUpdate`. Without one, as for a resource that the console changed through a path that lists no update: `WaitSettled` with the nodegroup's `done` of phase 4, section 2.
3. Beside the wait of a nodegroup, with `Nodes`: the node observer of `observe.go` with `Identity{StartedAt: update.CreatedAt, TargetVersion: version of a version update}`. `observeNodes` becomes a function that takes an emit function, so the watcher and the runner share it.
4. After a nodegroup settles: `health.Verify(names, health.Baseline{}, wait)`: `NodegroupActive` runs, the other two are `Skipped` (phase 3, section 5).
5. `StepChanged` with the result; `Result` with one `ClusterResult`.

`Watch` is in `internal/change` because every piece it needs is there and unexported: `inFlightUpdate`, `describe`, the wait options, the observer loop. Phase 4 listed `WaitUpdate`, `WaitSettled`, `noderoll.Attach`, and `health.Verify` as its parts; the list holds.

## 3. Consent in the TUI

The flow, mapped onto phase 4's rule "`Run` submits only with `Policy.Yes`, a reviewed plan that matches, or a yes answer":

| Step | What happens | Where |
|---|---|---|
| The user presses `p`, `a`, `U`, or `B` | `backend.Plan` builds the request and calls `change.NewPlan`: a dry run, the gate included, nothing submitted. The dialog shows the steps, the gate verdict with its reasons, the blockers, and the CLI command. | `backend.Plan` |
| The user presses `y` | `backend.Start(proposalID)`: the four checks of the next table, then `change.Start` with the same request and `Common.Expect` set to the reviewed plan. `Policy` is the zero value. | `backend.Start` |
| `Run` computes its first plan | `Diff(Expect, plan)`. `Done` and `NowInFlight`: notices. `KubernetesVersion`, `RollbackTarget`, `NowBlocked`: `Refused`, exit 3 or 4, and the dialog shows why. `Release`, `Version`, `Sizes`, `Added`, `Removed`, `GateWorse`: the question `PlanChanged` or `GateWarnings`. | `change.Run` |
| The gate of the reviewed plan said `Ask` | No question. The dialog showed the warnings, and `y` on that dialog is the answer; `Run` records them in `Gate.Accepted` (`accept`, `run.go:204`). | `change.Run` |
| A question during the run | `GateWarnings` before a nodegroup step whose gate got worse; `ConfirmPhase` before each phase of an upgrade when `ConfirmPhases` is set; `PlanChanged` at a fleet turn. The Change screen shows it with `y` and `n`. | The handle |
| The user presses `y` on a question | `Run` computes the plan again and compares it with the plan of the question (phase 4, section 1). | `change.Run` |

The TUI sets `ConfirmPhases` on an upgrade and a rollback, as the CLI does without `--yes`, so the run asks before each phase, as the old TUI's `y`/`n` did. The owner may prefer no phase questions in the TUI, where `P` and `S` exist: section 15.

The TUI has no `--force`, no `--require-healthy`, and no `--skip-health-check`, as before. A blocked plan has `y` disabled in the dialog (`canStart`, `keys.go:235`); `Run` would refuse it anyway, and a test proves that a `Start` of a blocked proposal ends `Refused` with nothing submitted.

### `--allow-changes`

```go
package backend

var ErrReadOnly = errors.New("the UI is read-only; start it with --allow-changes")

func (b *Live) Start(ctx context.Context, p state.ProposalID) (state.ChangeID, error) {
	if !b.allowChanges { // set by New, never changed
		return 0, ErrReadOnly
	}
	// the proposal, the claim, then change.Start
}
```

`allowChanges` is a field that `New` sets from the flag and no method writes. `Start` is the only function in the tree that calls `change.Start` with a request of the TUI; `adopt` calls `change.Watch`, which submits nothing. A test builds a backend without the flag, plans every kind of change, calls `Start` on each, and asserts `ErrReadOnly` and an empty mutation log of the fake. `Answer`, `Pause`, `Resume`, and `Stop` do not need the check: without a start there is no handle with controls, and a watched handle refuses them with `ErrNoControls`. The old `TestChangesAreRefused` (`live_test.go:414`) carries over with that difference.

The runtime unlock (`ctrl+u`, `unlocker`, `live.go:1056-1062`) is dropped. It needed a dry run that ran twice (with and without the live gates) and three tests of its own. A dry run now always runs the gate, so a read-only dry run is the same plan.

## 4. One change per cluster, busy, and changes started elsewhere

### Claims

```go
type Live struct {
	mu      sync.Mutex
	handles map[state.ChangeID]*entry // every handle the UI holds, own and watched
	byCluster map[state.Key][]state.ChangeID // the running ones
	// ...
}
```

`Start` refuses with `ErrClusterBusy{Cluster, Kind}` when `byCluster[key]` is not empty: the UI runs or watches something there. The claim is taken under `b.mu` before `change.Start` and released by the notifier goroutine when `Done` closes. `TestStartErrorReleasesTheCluster` (`roll_test.go:335`) and `TestClaimSurvivesAKeyChange` (`roll_test.go:520`) carry over; the second becomes trivial, because the key of a cluster is `{Region, Name}` and never changes.

### Busy

Two meanings, two places, no copy:

| Question | Answered by | Shown as |
|---|---|---|
| Is this UI running or watching a change on the cluster? | `byCluster`, above | The row's `CHANGE` column and the Change screen |
| Is AWS changing the cluster, or one of its resources, now? | `change.Busy` in the plan (a `Busy` blocker, a `Busy` step), and the fresh read before each submit (`Run`) | The dialog's blockers; a refused start |
| Is a resource of the snapshot `UPDATING`? | The snapshot's statuses, read by the sweep | The row's `STATUS` column, and adoption |

The old `checkNotBusy` (`live.go:1174-1189`) re-read the cluster before a start with the service's busy check. That read is now inside `Run`: the first plan, the diff, and the fresh read before the submit. The TUI makes no read of its own before a start.

### Changes started elsewhere

After each sweep, under `b.mu`, for every snapshot whose cluster, a nodegroup, or an add-on is in flight (the definition of phase 4, section 3) and whose cluster has no running handle:

1. The key is `{Target, StepKind, Name}`. If a watched handle for that key is running, nothing.
2. Else `change.Watch(b.ctx, deps, resource, WatchOptions{Nodes: true, Limits: b.limits})`, recorded with `Elsewhere: true` and claimed by cluster.

A cluster with a change of ours is not adopted: its handle reports what happens there, and a foreign update on it is a notice or a `Busy` row of our run. A watched handle ends when the update ends; a later sweep that still shows the resource in flight is a second update and starts a second watch. The identity of a watch is therefore the key plus the update ID that `Watch` found, and the view names it.

What a watched change shows: the same Change screen, the header marked "started elsewhere, watch only", the step list with one step, the update's status from `Progress`, the node table of a nodegroup, and the verification. The key bar shows no `P`, `S`, `y`, or `n` for it, because `Controls` is empty. An add-on update started elsewhere is watched too, where the old TUI only marked the cluster busy (`docs/commands/ui.md:157`); the watch costs one `DescribeUpdate` per poll and shows when it ends.

`TestChangesStartedElsewhereAreWatched`, `TestAdoptedRollResumesAndKeepsOthersClaims`, `TestAnAdoptedRollCountsFromItsStart`, and `TestAResumedWatchKeepsTheVersionRoll` (`roll_test.go:640-1038`) carry over as backend tests: the first two as they are, the last two as one test of `Watch`'s `Identity`.

## 5. State

```go
package state

type Key struct{ Region, Name string }
type ChangeID uint64
type ProposalID uint64

// State is what the model draws. It is immutable: the backend builds a
// new one for each version and never writes to a published one.
type State struct {
	Version  uint64
	Now      time.Time // the clock of the backend at the publish
	ReadOnly bool
	Profile, Context string
	Sweep    Sweep
	Clusters []Cluster // sorted by region, then name
	Changes  []Change  // running first, then finished, newest last
	Feed     []Line    // the fleet feed, oldest first, at most feedLines
}

type Sweep struct {
	Regions   int
	Answered  []string
	Skipped   []string
	Failed    []apidoc.Failure
	StartedAt time.Time
	EndedAt   time.Time // zero while a sweep runs
	Interval  time.Duration
	// Error is set when no region answered: the fleet is empty for a
	// reason, and the fleet screen says it.
	Error string
}

type Cluster struct {
	Key
	// Snapshot is the read of the last sweep. The backend never changes it.
	Snapshot *fleet.Snapshot
	// Derived once per sweep by backend.row:
	Status       view.Status
	Version      fleet.Version
	Latest       fleet.Version // the newest version EKS offers in the region
	Stale        Stale         // counts of nodegroups and add-ons behind
	InFlight     []string      // resources UPDATING, from the snapshot: "nodegroup ng-a"
	Incomplete   bool
	Change       ChangeID      // the running change of this UI on the cluster, 0 for none
	Readiness    *Readiness
	Rollback     *fleet.Rollback
}

type Stale struct{ Nodegroups, Addons int }

type Readiness struct {
	StartedAt, EndedAt time.Time // EndedAt zero while it runs
	Report             *health.ReadinessReport // section 6
	Err                string
}

// Change is one handle as the backend last read it.
type Change struct {
	ID ChangeID
	change.View
	// Log is the tail of the handle's log that the backend has read, at
	// most logLines entries. The full log stays in the handle.
	Log []change.LogEntry
}

// Line is one line of the fleet feed.
type Line struct {
	At      time.Time
	Cluster Key
	Status  view.Status
	Text    string
}
```

Typed everywhere: `view.Status` for a mark, `fleet.Version`, `apidoc` enums through `change.View`, `change.RunState`, `change.PhaseStatus`, `health.Outcome` in the report. No field is a label that code reads back. The old `Cluster.Busy` string, `Phase.Name`, `Upgrade.Label`, and `Plan.Blocked` (`state.go:124, 339, 287, 406`) have no successor.

### Actions and proposals

```go
type ActionKind string // NodegroupRoll, AddonUpdate, ClusterUpgrade, ClusterRollback

type Action struct {
	Kind      ActionKind
	Cluster   Key
	Nodegroup string        // NodegroupRoll: the one nodegroup
	To        fleet.Version // ClusterUpgrade: the next minor, or the live version for a catch-up
	Only      []change.Part // ClusterUpgrade: as --only
}

// Proposal is a dry run that the dialog shows.
type Proposal struct {
	ID       ProposalID
	Action   Action
	Plan     *change.Plan
	Doc      apidoc.Plan
	Command  string // the CLI command that makes the same change, with --profile and --kube-context
	Blocked  bool   // Plan.Decision() == DecisionBlocked
}
```

The backend keeps one proposal: the newest. `Start` of an older ID returns `ErrStaleProposal`, which carries `TestAnOlderDryRunCannotReplaceTheConfirmedOne` (`upgrade_test.go:547`) and `TestOnlyTheNewestPlanOpens` (`model_test.go:546`) into the backend, where the model cannot get it wrong.

### Publishing

```go
// bump records a change and wakes Next. It never blocks: it is called
// from the sweep, the notifiers, and the readiness goroutines.
func (b *Live) bump() {
	b.mu.Lock()
	b.version++
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// Next builds the state on the caller's goroutine, which is a tea.Cmd.
func (b *Live) Next(ctx context.Context, after uint64) (state.State, error) {
	for {
		b.mu.Lock()
		if b.version > after {
			st := b.build() // under the lock; section "Cost"
			b.mu.Unlock()
			return st, nil
		}
		ch := b.changed
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return state.State{}, ctx.Err()
		}
	}
}
```

The model's `Init` returns `next(b, 0)`; each `stateMsg` returns `next(b, msg.Version)`. One command is in flight at a time, so a burst of bumps while the model draws folds into one build. Nothing is copied on a timer and nothing runs when nothing changed.

What bumps: a region answered (`OnRegion`), a sweep ended, a readiness started or ended, a handle changed (through its notifier), a proposal was made, a cluster was re-read after a change. The clock is not a bump: the model ticks for it (section 7).

### Cost

`build` under the lock does this and no more: copies the slice headers of `b.rows` (rebuilt once per sweep, in `OnRegion` for the regions that answered so far, sorted once at the end), reads the cached view of each handle (the notifier stores `h.View()` and the new log entries when it wakes, so `build` copies two headers per handle), and appends the `Changes` and `Feed` headers. At 200 clusters that is under 300 pointer copies: microseconds.

Memory at 200 clusters: the snapshots of one sweep, which `status -A` already holds at its end (about 10 MB of heap for the fleet of `tools/fakefleet`, measured in phase 2, section 16, after the sweep's goroutines ended); the rows, 200 structs; the feed, at most 500 lines; the handles, at most 20 finished plus the running ones, each with a plan and a log bounded by that plan. The sweep in progress holds a second set of snapshots until it ends; the old set is dropped when the rows are replaced. Under 40 MB, measured by the benchmark of section 12.

The feed: `feedLines = 500`, `logLines = 500` per change. The old backend capped at 500 and 400 (`live.go:738-739`) and the model froze a copy (`TestFrozenFeedSurvivesEviction`); with the full log in the handle, freezing needs no copy: a frozen pane keeps a `Seq` and reads `Events(after)` up to it on the next version.

## 6. The backend and the sweep

```go
package backend

type Options struct {
	Regions      fleet.Regions
	Interval     time.Duration // --interval, at least 10 s
	AllowChanges bool
	Limits       change.Limits // Read from --timeout, Wait from --wait-timeout, Poll 15 s
	Profile, Context string   // for the top bar and the copied command
}

func New(ctx context.Context, f *cmdutil.Factory, o Options) (*Live, error)
func (b *Live) Run(ctx context.Context) error // the sweep loop; returns when ctx ends and every goroutine is joined
```

The sweep loop, one goroutine:

1. `fetcher.Sweep(ctx, provider, regions, SweepOptions{Need: need, OnRegion: b.regionDone})` under `cmdutil.WithTimeout(ctx, Limits.Read)`, with `need = fleet.Need{Nodegroups: true, AMI: true, Addons: true, LatestAddons: true, Support: true, Updates: true}`: what `status` reads, plus the update history for the rollback window. `OnRegion` replaces the rows of that region and bumps, so the fleet fills as regions answer, as `status` on a TTY does.
2. At the end: the sweep summary into `Sweep`, a feed line per region failure, one feed line per skipped-region list, adoption (section 4), bump.
3. Wait on `time.After(Interval)` from the factory's `After`, `b.refresh` (a buffered channel of 1 that `Refresh` and the end of a change send to), or `ctx.Done()`.

A fresh `fleet.Fetcher` per sweep, so the memos of the latest versions do not outlive one.

After a change ends, the notifier sends the cluster's key on `b.refetch`; the loop, between sweeps, runs `fetcher.Fetch` for that cluster alone with the same `need`, replaces its row, and bumps. The whole fleet is not swept for one cluster.

Readiness on demand runs on a goroutine of its own per request, one per cluster at a time (a second request while one runs is refused with `ErrReadinessRunning`). It calls `health.CheckReadiness`, a function that phase 5 extracts from `cluster upgrade-check` (`internal/cmd/cluster/upgradecheck.go:150-186` and `readinessOf`), so the command and the TUI run the same waves:

```go
package health

type ReadinessReport struct {
	Readiness apidoc.Readiness
	Reasons   []string
	Checks    []Result
	Insights  []apidoc.Insight
	Rollback  *fleet.Rollback
	Failures  []apidoc.Failure
}

func CheckReadiness(ctx context.Context, c *awsx.Clients, f *fleet.Fetcher, cluster string, category string) (ReadinessReport, error)
```

The command's `document` and `view` stay in the command; its tests are the proof of the extraction.

Failures and skipped regions on screen: the top bar shows `regions 7/8` and `1 skipped` in the caution color; a failed region is a feed line with the fail mark and the row is absent; an incomplete cluster row carries the warn mark in `STATUS` and its card lists the failure messages; a sweep in which no region answered shows `Sweep.Error` on an empty fleet screen (`TestNoRegionAnsweredSaysWhyNotEmpty`, `model_test.go:918`, carries over; the message comes from `fleetcmd.SweepError`, which runs the STS check once).

## 7. Model and views

### Libraries

Bubble Tea v2 (`charm.land/bubbletea/v2 v2.1.0`) and Lip Gloss v2 (`charm.land/lipgloss/v2 v2.0.6`), both in `go.mod`, plus `github.com/charmbracelet/x/ansi` and `github.com/rivo/uniseg` for width math, as the old tree. No Bubbles components: the lists, the dialogs, and the bars are a few lines each over `Block`, and a component would bring its own key handling and styles.

### Layout

`Seg`, `Line`, `Block`, `hjoin`, `box`, `overlay`, `Fit`, `Cut`, `wrap`, `wrapExact`, `bar`, `joinRight` are ported from `legacy/internal/tui/draw.go` with their tests (`draw_test.go`, the `wrapExact` tests of `model_test.go:1375-1452`). They render styles last, so width math never sees an escape code, and `Block.fit(w, h)` is what makes every frame exactly `w` by `h`. Lip Gloss's `JoinHorizontal` and `Place` measure styled strings and cannot give that guarantee as cheaply; Lip Gloss is used for one thing: `Line.Render`, which paints a `Seg`.

One change: a `Seg` names a role, not a color, so the theme decides.

```go
package tui

type Seg struct {
	Text string
	Role view.Role // Text, Subtext, Dim, Healthy, Warn, Fail, Progress, Accent, Rule, Base, Mantle, Crust, Surface
	Bold bool
	BG   view.Role // zero means the pane's background
}
```

`view` gains the roles and their colors, which it already holds as unexported `rgb` values (`theme.go:51-62`), plus the four background layers of the old TUI (`legacy/internal/tui/theme.go:28-32`):

```go
package view

type Role int

// Color returns the color of a role at the depth of the theme, and false
// with NoColor.
func (t Theme) Color(r Role) (color.Color, bool)
// Border returns the box-drawing set of the theme: Unicode, or +-| without it.
func (t Theme) Border() Border
```

### Screens

| Key | Screen | Left | Right |
|---|---|---|---|
| `1` | Fleet | The cluster table: a mark, `CLUSTER`, `REGION`, `VERSION`, `NODEGROUPS`, `ADD-ONS`, `STATUS`, `CHANGE`; the card of the selected cluster | The fleet feed |
| `2` | Cluster | The nodegroups and the add-ons of the selected cluster, as `cluster describe` shows them | Readiness: the verdict, the checks with the selected check's details, the insights |
| `3` | Change | For a roll: the node table and the pods of the draining nodes. For an upgrade or a rollback: the phase list with the steps, and the node table of the step that rolls. The header: the kind, the target, the versions, the state, elapsed, the question when one is open. | The change's log (the timeline) |

The Rolls and Upgrade screens of the old TUI are one screen. A change is a change; its kind decides the left pane. The screen shows the change of the selected cluster, newest first, and `[` `]` page through the changes of the UI when there are several.

Dialogs over the screen: the proposal (the plan, the gate, the blockers, the command; `y`, `n`, `c`), the picker (which nodegroup; which part of an upgrade), the help (`?`). Each is a `dialogParts` with a scrolling body, ported from `dialogs.go`.

### Keys

The `binding` table of `keys.go:15-28` is ported as it is: `keys`, `label`, `desc`, `short`, `bar`, `primary`, `when`, `do`. One table per screen plus the global one; the handler walks it, the key bar lists the active entries with `bar`, and the help lists every active entry. The key bar's narrowing (`view.go:174-222`) is ported with its tests (`TestKeyBarKeepsEveryKeyAtMinimumWidth`, `TestKeyBarKeepsWordsOnLetterKeys`).

| Key | Where | Action |
|---|---|---|
| `↑` `↓` `g` `G` `j` `k` | lists | move |
| `enter` | Fleet | open: the Change screen when the cluster has a change, else the Cluster screen |
| `esc` | any screen | back to the fleet; in a dialog, close it (not while a start is in flight) |
| `1` `2` `3` | any | switch screen |
| `r` | Fleet, Cluster | run readiness |
| `p` | Fleet, Cluster | propose a roll: the picker when more than one nodegroup is stale |
| `a` | Fleet, Cluster | propose the add-on updates |
| `U` | Fleet, Cluster | propose an upgrade: the picker of parts, as `--only` |
| `B` | Fleet, Cluster | propose a rollback, only with a rollback window |
| `y` | proposal | start; `n` close; `c` copy the command |
| `y` `n` | Change, question open | answer |
| `P` | Change, `Controls.Pause` | pause before the next checkpoint, or resume |
| `S` | Change, `Controls.Stop` | stop at the next checkpoint |
| `[` `]` | Change | page through changes |
| `space` | any | freeze the feeds at the current `Seq`, or follow |
| `w` | any | warnings and errors only |
| `f` | Fleet | feed: all clusters or the selected one |
| `ctrl+r` | any | sweep now |
| `ctrl+l` | any | redraw |
| `?` | any | help |
| `q` `ctrl+c` | any | quit; changes in flight keep running in EKS |

Dropped: `e` (export), `ctrl+u` (unlock), `tab` (log source: there is one log per change), `4`.

### One state struct per screen

```go
type Model struct {
	b        Backend
	ctx      context.Context
	st       state.State
	now      time.Time // the clock tick; st.Now until the first tick
	w, h     int
	screen   screen
	fleet    fleetScreen   // cursor, feed scope
	cluster  clusterScreen // check cursor
	change   changeScreen  // change index, frozen Seq
	dialog   dialog        // nil, or the proposal, the picker, the help; with its scroll
	planning *planning     // a dry run on its way: its action and a cancel
	starting bool
	warnOnly bool
	frozen   uint64 // 0 follows
	notice   notice // the log strip's one-line message and its deadline
}
```

The messages: `stateMsg{State}`, `proposalMsg{id, Proposal, err}`, `startedMsg{ChangeID, err}`, `doneMsg{err}` for a call with no result, `tickMsg`, `tea.WindowSizeMsg`, `tea.KeyPressMsg`. The ordering guards of the old model carry over where they still apply: a proposal reply for an action that is no longer the pending one is dropped (`TestOnlyTheNewestPlanOpens`, `TestEscCancelsAPendingPlan`, `TestMovingTheSelectionCancelsThePlanAndTheFocus`, `TestNoPlanOpensBehindThePicker`); a second `y` while starting sends nothing (`TestStartIsSentOnce`); only `y` starts (`TestEnterNeverConfirmsAChange`). The fetch-ID guards (`TestOlderStateIsDropped`, `TestFetchIDsAreNeverReused`, `TestOnlyPollingRepliesScheduleATick`) have no successor: `Next` returns versions in order, one at a time, and a test asserts that `Update` on a `stateMsg` returns exactly one command.

### The clock

The model ticks once a second with `tea.Tick` while `st.Changes` has a running change, a readiness runs, or a sweep runs. Otherwise no tick is scheduled, and the next `stateMsg` schedules one when needed. The top bar shows the time of the last sweep as a clock time, not as "ago", so it needs no tick. `TestBeforeTheFirstSweep` (`fleet_scale_test.go:95`) carries over: before the first sweep the bar says "sweeping".

### Terminal

| Condition | Behaviour |
|---|---|
| Below 100 by 24 | One line: "refresh ui needs a 100×24 terminal (now 80×20)", exactly filled. The model keeps its state, so a resize back shows the screen. |
| Resize | `tea.WindowSizeMsg` sets `w`, `h`; cursors and scrolls are clamped; the picker keeps its selection visible (`TestResizeKeepsThePickerSelectionVisible`). |
| `--no-color`, `NO_COLOR`, `TERM=dumb` | `tea.WithColorProfile(colorprofile.ASCII)`; `Theme.Color` returns false, so no background is set and the selection is a `>` marker, not a highlight (`TestNoColorLeavesTheBackgroundAlone`). Marks and labels carry every status. |
| Non-UTF-8 locale | `Theme.Unicode` is false: ASCII borders, ASCII marks (`[OK]` is sized into the columns), `#` and `.` for bars, `...` for a cut. The old TUI refused to adapt (`theme.go:36-40`); the cost here is one border set and the glyph table that `view` already has. |
| Another program writes over the screen | `ctrl+l`, and a full redraw after each sweep at most every 30 s (`TestSweepRedrawsTheScreen`, `TestCtrlLRedraws`). |

## 8. The live node view

The node view of a roll is the `NodeProgress` event, coalesced into `View.Progress[step].Nodes`: a `noderoll.Snapshot`, the recent tracker events, and `Watching`. The Change screen draws it with its own node table (sorted draining, joining, old, new, as `rolls.go:91-102`), the drain progress of each draining node with its pod names, the pressure conditions, and the warnings. The CLI's `view.RollPanel` is not reused: it is a column of lines for a terminal of unknown width, and the TUI's pane has a width and a background. The data is shared; the table is not, and it holds no logic.

The view is best effort beside the authoritative wait, as phase 4, section 9, has it: `Run` starts the observer with `Common.Nodes`, which the backend sets for every roll it starts and `Watch` sets for every nodegroup it follows. A failed `Attach` is a `Notice` in the log and the pane says "no node view: <reason>". The EKS update decides the result; the nodes never do.

The kube client comes from `Deps.Kube`, which is `Factory.Kube`: `kube.Connect` with the global `--kubeconfig` and `--kube-context`, matched by endpoint for each cluster. The old TUI resolved it with `health.ConnectKubeClientForCluster` to avoid the stderr write of the runner; `kube.Connect` writes nothing.

## 9. Kubernetes exec plugins and stdio

`kube.Connect` sets `ExecProvider.StdinUnavailable = true` and `InteractiveMode = Never` (`internal/kube/client.go:347-348`), so `aws eks get-token` never reads the terminal and never blocks on it. That covers stdin: the old `detachStdio` pointed `os.Stdin` at `/dev/null` for this reason (`legacy/internal/commands/ui.go:163-180`), and that part is gone.

Stderr is not covered. client-go hands a plugin `os.Stderr` and has no field for it, so a plugin's warning lines, and anything that writes to file descriptor 2 inside the process, would draw over the screen. The `ui` command therefore does, for the time the program runs:

1. `syscall.Dup(2)` to keep the terminal's stderr, `syscall.Dup2(devnull, 2)`, and `os.Stderr = os.NewFile(...)` of `/dev/null`. Restore both at the end, before the final error prints. On Windows the step is skipped (`//go:build !windows`), as the old code did.
2. `klog.SetLogger(logr.Discard())` with `defer klog.ClearLogger()`, so client-go's informer and watch messages cost nothing. With fd 2 redirected they would be silent anyway; the discard avoids the formatting.
3. The `slog` handler of the root (`--log-level`) writes to `IOStreams.ErrOut`, which is the terminal. The command replaces it with `slog.DiscardHandler` for the run. A log file is a later feature if someone asks for one.

Bubble Tea gets `tea.WithInput(ios.In)` and `tea.WithOutput(ios.Out)`: the real terminal, which fd 1 still is. The final error of the command, if any, prints after the restore.

## 10. The simulator

`REFRESH_DEV_SIMULATE=1 refresh ui` starts the real backend against the fake world, in process:

1. `fleettest.Generate(seed, clusters, regions)`: the fleet generator of `tools/fakefleet/main.go:332` moves into `fleettest`, so the tool and the command share it. It gets the scripted failures the TUI needs to show: one nodegroup whose next roll fails with `PodEvictionFailure` and a stuck drain, one whose roll leaves a node `NotReady` (exit 5 after the roll), one add-on whose update ends `DEGRADED`, one cluster with an `ERROR` insight, one in the rollback window, one with a roll already in flight at start (adoption), a slow region, and a throttled operation through `SetScopedFault`. `REFRESH_DEV_SIM_SEED` picks the seed; the fleet size is 24 clusters over 4 regions, enough for every state to appear and small enough to read.
2. `awsxtest.NewServer(world)` and, for each cluster, `kubetest.NewServer(world, region, name)`, which sets the cluster's endpoint in the world. A kubeconfig with one context per cluster is written to a temp dir, and `Factory.Global.Kubeconfig` points at it, so `kube.Connect` matches by endpoint as it does against AWS.
3. `Factory.AWS` returns a provider over `srv.Config(region)` (the code of `cmdutiltest.UseAWS`, which moves into `awsxtest` as `awsxtest.Provider(srv, region)` so that no test package is imported by the binary). `Factory.Now` is `world.Now`. `After`, `AfterFunc`, and `Tick` stay real time, so a 15 s poll is 15 s.
4. `world.Follow(ctx, speed)`: `followClock` of the tool (`main.go:212`) moves into `fleettest`: the world's clock advances by the wall time times `REFRESH_DEV_SIM_SPEED` (default 10), every 100 ms, so an update scripted to take 20 minutes ends in 2.

The three env variables are read in `internal/cmd/ui/sim.go` and nowhere else. `tui`, `state`, `backend`, and `change` do not know that the world is fake: the only difference they can observe is a clock that moves fast. The build has no tag: the fakes are linked as `internal/sim` was, and the binary-size gate measures the result. The import rule of `REWRITE.md` ("a `...test` package is imported only by tests, `test/`, and `tools/`") gets one named exception, `internal/cmd/ui/sim.go`, and the `imports_test.go` of `cmdutil` and `change` are unchanged because neither imports it.

What the fakes still need, beyond the move: `kubetest` serving several clusters from one world at once (today a server is per cluster, which is enough with one server per cluster); a `RollScript` for a roll that fails after some nodes were replaced, so the node table shows a half-rolled nodegroup; the scheduled fault "throttle every third `DescribeNodegroup` in region X", which `Fault` can express if it gains a count.

`task run:tui:sim` becomes `REFRESH_DEV_SIMULATE=1 go run ./cmd/refresh ui --allow-changes`.

## 11. Tests

### Model (`internal/tui`)

- **Exact fill.** A port of `TestEveryScreenFillsTheTerminalExactly` (`model_test.go:133`): sizes 100×24, 120×40, 160×42, 231×64, for Unicode and ASCII, with and without color. The harness feeds `stateMsg` values built from a scripted `Backend` (a struct of states and replies, no goroutine), runs the returned commands inline as the old `send` did, and after every step asserts `len(frame) == h`, every `Line.Width() == w`, and `strings.Count(render, "\n")+1 == h`. The script: the fleet, `p` with the picker, the proposal, `G`, `y`, each screen, `?`, a question, `P`, `S`, `space`, `w`, a resize to 80×20 and back.
- **The guards that carry over**, each as a model test over the scripted backend: `TestTooSmallTerminal`, `TestLoadingFrameBeforeTheFirstState`, `TestEnterNeverConfirmsAChange`, `TestStartIsSentOnce`, `TestCancelIsOffWhileAChangeStarts`, `TestOnlyTheNewestPlanOpens`, `TestEscCancelsAPendingPlan`, `TestNoPlanOpensBehindThePicker`, `TestMovingTheSelectionCancelsThePlanAndTheFocus`, `TestPendingPlanSurvivesKeysThatKeepTheTarget`, `TestSelectionFollowsTheClusterNotTheRow`, `TestPausedFeedHoldsStill`, `TestFrozenFeedSurvivesEviction`, `TestKeyBarShowsOnlyKeysThatWork`, `TestKeyBarKeepsEveryKeyAtMinimumWidth`, `TestKeyBarKeepsWordsOnLetterKeys`, `TestHelpListsThisScreensKeys`, `TestHelpOmitsKeysThatDoNothingHere`, `TestQuitKeys`, `TestUpdateNeverCallsTheBackend`, `TestDialogScrollsOnAShortTerminal`, `TestLongErrorKeepsTheDialogKeysOnScreen`, `TestResizeKeepsThePickerSelectionVisible`, `TestPickerKeepsItsSelectionOnScreen`, `TestFleetTableKeepsTheSelectionInView`, `TestFleetCellsKeepAGap`, `TestFleetNodegroupsSayUnknown`, `TestBeforeTheFirstSweep`, `TestNoColorLeavesTheBackgroundAlone`, `TestAnEmptyFleetSaysSoAndSurvivesEveryKey`, `TestNoRegionAnsweredSaysWhyNotEmpty`, `TestChangeKeysOnABusyClusterSayWhy`, `TestRollbackKeyOnlyWhenAvailable`, `TestUpgradeKeyOffersTheParts`, `TestUpgradeKeyOffersCatchUpMidway`, `TestPatchKeyRollsALaggingNodegroupToTheControlPlane`, `TestDryRunDialogWrapsLongLines`, `TestUpgradeHeaderCutsALongLabel`, `TestUpgradeHeaderKeepsTheStatus`, `TestUpgradeShowsLongReasonsInFull`, `TestCtrlLRedraws`, `TestSweepRedrawsTheScreen`, `TestKubeEventColumnsKeepAGapAtAnyWidth`, `TestPodCardKeepsAGapAfterLongNames`, `TestPodCardKeepsNamesReadableBesideLongReasons`, `TestUpgradeQuestionIsAnsweredWithYOrN`, `TestUpgradeScreenStopAndPause`, `TestPendingHintFollowsThePhaseName` (as "the hint of a phase comes from its `PhaseKind`"). The `draw_test.go` and `wrapExact` tests come with the layout.
- Not carried: the unlock tests, the journal and export tests, the clipboard test (the `c` key sends OSC 52 and says "sent to the terminal"; a system tool is a later feature), the fetch-ID tests, the kubectl-cursor tests, the log-source tests, the simulator-refuses-rollback test.
- A no-real-time rule: no test of `tui` calls `time.Sleep` or `time.After`; `tickMsg` is sent by the test.

### Handle (`internal/change`)

Against `awsxtest` with the world clock, inside `testing/synctest.Test` so that every goroutine of a run is at a known point when the test asserts, and with `goleak` and `-race`:

- Each row of the state table, by a scripted world: a roll (add-on update, roll, upgrade of two phases once the engine is built) through `Preparing`, `Running`, `Finished`; a question answered yes, no, twice, with a stale ID, after Finished; pause then resume; pause then stop; stop then a failing step (`Failed`, not `Stopped`); cancel in every state.
- Races: in one `synctest` bubble, `Answer` from two goroutines (one wins, one `ErrStaleQuestion`); `Stop` and `Answer` at once (stop wins: `Declined` with `StopRequested`); `Pause` then `Cancel` (Interrupted); `Cancel` while the executor is inside `Event` (no deadlock, `Done` closes).
- The log: never blocks the executor (an `Event` from a goroutine while no reader exists returns at once); `Events(after)` is monotonic; the progress kinds are not logged; a roll of one nodegroup logs under 40 entries.
- `View` is immutable: a reader that writes to a slice of a view changes nothing in the handle (the old `TestStateSharesNoMemory`).
- `Watch`: a nodegroup `UPDATING` with an update (wait, nodes, verify), without one (settled), with a failed update, with a `NotReady` new node (exit 5 in the result), a cluster update by direction (upgrade, rollback), an add-on; `ErrNoControls` from each control.

### Backend (`internal/tui/backend`)

Against `cmdtest.NewFake`-style setup (a world, a server, a factory, `UseWorldClock`) and `kubetest.NewServer` for the kube side, each test a scripted world:

- A sweep: rows, a failed region, skipped regions, an empty fleet, no region answered (the STS check), incomplete rows, `OnRegion` versions, `Refresh`, `--interval`.
- Readiness: a run, an error, two requests at once.
- A roll from a proposal: the dialog's plan, `Start`, the node view, the verification, the re-read of the cluster at the end.
- An add-on update, with one add-on that fails and the next that goes on.
- An upgrade: `ConfirmPhase` questions, pause, stop, a gate that got worse, a changed plan.
- A rollback inside the window and one outside (blocked in the proposal).
- An adopted roll and an adopted control-plane update: watched, no controls, ends when EKS says so, a second update after the first.
- Refusals: without `--allow-changes` (every kind), a stale proposal, a blocked proposal, a cluster with a running change, a declined question.
- `Next` honours cancel and returns versions in order; `build` runs under the lock for less than a bound of allocations (section 12).
- Shutdown: `Run` returns with every goroutine joined when ctx ends during a sweep, a plan, a change, and a watch.

### End to end

One scripted run through `tea.NewProgram` with `tea.WithInput` on a pipe, `tea.WithOutput` on a buffer, `tea.WithoutSignals`, and `tea.WithWindowSize(120, 40)`: the simulator's world, the key script of the exact-fill test, and an assertion on the last frame and on the mutations in the fake. `teatest` is not in `go.mod`; `github.com/charmbracelet/x/exp/teatest/v2` is tried first, and if it does not build against `charm.land/bubbletea/v2` the test drives the program directly. The old tree had no such test.

## 12. Performance budgets

| Measure | Budget | How it is measured |
|---|---|---|
| A frame of the Fleet screen at 200 clusters, 231×64 | 8 ms (`REWRITE.md`), and under 4,000 allocations | `BenchmarkFleetFrame` over a state of 200 rows; `testing.AllocsPerRun` in `TestFrameAllocations` gates the PR, `task bench` and the nightly compare the time with `benchstat` |
| A frame of the Change screen during a roll of 40 nodes | 8 ms, under 3,000 allocations | The same, with a `NodeProgress` of 40 nodes and a log of 200 entries |
| `backend.build` at 200 clusters and 5 handles | 50 µs, under 50 allocations | `BenchmarkBuild`; a test asserts the allocation bound |
| The handle's `Event` for `NodeProgress` | 2 µs, 1 allocation (the map value) | `BenchmarkHandleEvent` |
| Idle CPU, fleet loaded, nothing in flight | No work between sweeps: no tick, no build, no `Next` wake | `TestIdleSchedulesNoTick` asserts that `Update` on the last `stateMsg` of an idle fleet returns only `next`; a manual `pprof` over 60 s of the simulator at speed 0 shows no samples outside the sweep |
| Memory at 200 clusters, after two sweeps | Under 40 MB of heap after GC | `TestMemoryAfterTwoSweeps` reads `runtime.ReadMemStats` after `runtime.GC()` against the fake fleet; logged, with a gate at 60 MB |

The viewport rule makes the first two budgets hold: a screen renders the rows that fit, never the fleet, so a frame is O(rows on screen).

## 13. Build order

Each step ends with `task dev:full` green and is reviewable alone.

| # | Step | Proof |
|---|---|---|
| 1 | `internal/cmd/ui` with the five flags, `validate` (`-A` with `-r`, the interval minimum, a terminal), the stdio detach; `internal/tui` with `Block`, the key table, the top bar, the key bar, the help, and the Fleet screen; `internal/tui/state` and `internal/tui/backend` with the sweep loop, `Next`, `Refresh`. Read-only. | The exact-fill test for the Fleet screen; the draw tests; the sweep tests; the shutdown test |
| 2 | The Cluster screen; `health.CheckReadiness` extracted and used by `cluster upgrade-check`; readiness in the backend | The command's tests unchanged; the readiness backend tests; the exact-fill test for the screen |
| 3 | The handle in `change` (`Start`, the log, questions, cancel, result); the proposal dialog; `backend.Plan` and `Start` with `Expect`, claims, `ErrReadOnly`; an add-on update end to end; the Change screen with the step list and the log | The handle tests but pause and stop; the add-on backend tests; the refusal tests |
| 4 | A roll: `Common.Nodes`, the node table, the picker, the post-roll verification shown, the cluster re-read | The roll backend tests; the node-view exact-fill rows |
| 5 | Pause, stop, `ConfirmPhase`, the phase list; an upgrade and a rollback end to end. Needs phase 4's checkpoint per phase and per step (section 14). | The remaining handle tests; the upgrade and rollback backend tests |
| 6 | `change.Watch` and adoption | The `Watch` tests; the adoption backend tests |
| 7 | The simulator: `fleettest.Generate`, `World.Follow`, `awsxtest.Provider`, `sim.go`, `task run:tui:sim`; the end-to-end run | The end-to-end test; a manual session |
| 8 | Close-out: the benchmarks and gates, `docs/commands/ui.md` rewritten, `legacy/PORTING.md` rows for `tui`, `tui/live`, `tui/state`, `sim`, `rollview` | The gates in CI |

## 14. What phase 4 must provide

The phase 4 agent is building the roll batch and the engines now. Each item is small and fits where it is.

| # | Item | Why |
|---|---|---|
| 1 | The `Checkpoint` hook is called before each phase after the first, and before each step of a serial phase after the first step of the run, in the upgrade engine and the rollback engine, as it is called before each cluster of a fleet (`fleet.go:193`). The signature stays; `next` is the `PhaseRef` of the phase, and the hook gets a second argument `step StepRef` that is zero for a phase: `Checkpoint func(ctx, next PhaseRef, step StepRef) (Checkpoint, error)`. | `S` means "stop before the next phase or the next nodegroup roll" (`docs/commands/ui.md`, `S`). Without the per-step call, a stop during the nodegroups phase of a six-nodegroup upgrade waits for all six. |
| 2 | `ConfirmPhase` as a `QuestionKind`, asked before each phase when `UpgradeRequest.ConfirmPhases` is set, with `Question.Step.Phase` set. | The TUI's `y`/`n` before a phase, and the CLI's prompt without `--yes`. The phase 4 note has it; the code does not yet. |
| 3 | Every event carries `Phase.Target` at least, `Notice` included. Today `notice` emits an event with no phase (`run.go:120`). | The feed attributes a line to a cluster; a fleet run would be unreadable otherwise. |
| 4 | `PhaseEnded` carries `Skipped` (the note has it; the code lists it as "arrives with its build step"). | The phase list shows a skipped phase as skipped. |
| 5 | `observeNodes` takes an emit function instead of being a method of `runner`, and `inFlightUpdate` returns the update's `CreatedAt` and type. | `Watch` reuses both without a runner. |
| 6 | `Kind` gains `ClusterUpgrade` and `ClusterRollback`, and `Request` gains `UpgradeRequest` and `RollbackRequest`, as the note has them. | The TUI builds them from an `Action`. |
| 7 | `Part` as a type for `--only` values. | `Action.Only` is typed. |

Nothing else. The handle needs no new hook: an event, a question, and a checkpoint are enough, as phase 4's table predicted.

## 15. Decisions for the owner and contract edits

### Decisions

| # | Question | Recommendation |
|---|---|---|
| 1 | Keep the "experimental" label on `refresh ui` at cutover? | Yes, for the cutover release. The screens and keys change (three screens, no `tab`, no `e`), and the simulator is the only place anyone has run the new one at fleet scale. Drop the label in the release after the first upgrade someone runs through it. |
| 2 | Drop the runtime unlock (`ctrl+u`)? | Yes. One flag, read once, enforced in one function. |
| 3 | Drop the session export (`e`) and the AWS call pane? | Yes. The export needed a journal of every event; the pane needed a log handler that took the backend's lock. Neither is in the contract. |
| 4 | Drop the kubectl-cluster cursor placement? | Yes. It needed a section of its own in the user doc, five tests, and a read of the kubeconfig at start. The user moves the cursor. |
| 5 | One Change screen in place of Rolls and Upgrade? | Yes. One cursor, one layout decided by `View.Kind`. |
| 6 | Should an upgrade from the TUI ask before each phase (`ConfirmPhases`), given `P` and `S`? | Yes, as the old TUI did and as the CLI does on a terminal. A person who wants no questions runs `cluster upgrade --yes`. |
| 7 | The simulator linked into the release binary, or behind a build tag? | Linked, as the old `internal/sim` was. One binary, one code path, one named exception in the import rule. The binary-size gate measures it. |
| 8 | Watch add-on updates started elsewhere, where the old TUI only marked the cluster busy? | Yes. The same `Watch`, one poll per interval. |

### Contract edits

1. In the command flags table, the `ui` row says what `--wait-timeout` bounds: "`--wait-timeout`: how long the UI follows a change it starts or watches. `0` means the default of the command that makes the change: 40m for a roll, 5m for each add-on, 4h for an upgrade or a rollback. The EKS update continues either way." Default `0`.
2. A row for `--interval`: "time between fleet sweeps; default `1m`, at least `10s`. `ctrl+r` sweeps now."
3. Under "Exit codes", one sentence: "`refresh ui` exits `0` when you quit and `1` when it cannot start: no terminal, no credentials, or no region. The result of a change shows in the UI, not in the exit code."
4. Under "Other env vars", nothing: `REFRESH_DEV_SIMULATE`, `REFRESH_DEV_SIM_SPEED`, and `REFRESH_DEV_SIM_SEED` stay undocumented, as `REWRITE.md` says.

## 16. Changes made while building

Empty until code changes the design.
