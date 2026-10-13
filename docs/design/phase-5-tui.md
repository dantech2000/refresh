# Phase 5 design note: the TUI

Status: draft 2. Draft 1 was reviewed against the code: approve with changes, one blocker and ten majors. This draft applies them and the owner's decisions on draft 1 (section 15). It belongs to `REWRITE.md` and stays inside `CONTRACT.md`. Phase 4 (`docs/design/phase-4-changes.md`) is the source for the executor, its hooks, plans, `Expect`, the consent rule, and the busy rule. Phase 3 is the source for the gate and the readiness checks. Phase 2 is the source for `Snapshot`, `Fetcher`, and `Sweep`. Where `internal/change` in this checkout and the phase 4 note differ, the note is the reference: the roll batch and the engines are mid-build.

Scope: the command `refresh ui`, the TUI model and its screens, the backend that sweeps the fleet and runs changes, the handle that adapts the synchronous executor to an interactive front end, the watching of changes started elsewhere, and what `tools/fakefleet` needs so that the unchanged binary runs against it.

A claim about the old tree names a file and line under `legacy/internal/`.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | The handle is `change.Start(ctx, d, req) *Handle`: `Run` on a goroutine of its own, the three hooks turned into a log, a question with an ID, and a checkpoint that pauses or stops. It lives in `internal/change`, beside `Run`, because it is the asynchronous form of `Run` and knows nothing of a terminal. |
| 2 | A nodegroup roll or a control-plane update started elsewhere is `change.Watch(ctx, d, resource) *Handle`: the same handle, with no controls, over `WaitUpdate` or `WaitSettled`, the node observer, and the post-roll verification. An add-on update started elsewhere marks the cluster busy and is not watched. |
| 3 | The TUI gives its consent through `Common.Expect`: the reviewed plan of the dialog. It never sets `Policy.Yes` and never sets `ConfirmPhases`. Every question that `Run` asks after that is shown with `y` and `n`. |
| 4 | `--allow-changes` is read once, at start, and enforced in one function: `backend.Start`. There is no runtime unlock. |
| 5 | "Busy" in the TUI means "this UI runs or watches a change on the cluster". Whether AWS is changing something is `change.Busy`'s question, answered in the plan and in the fresh read before each submit. The TUI reads no status to decide it. |
| 6 | The model draws a `state.State` that the backend publishes with a version. The model waits for the next version in a `tea.Cmd` and never polls. A clock tick runs only while an elapsed time is on screen. |
| 7 | One sweep loop with `fleet.Fetcher.Sweep`, every `--interval`, on `ctrl+r`, and for one cluster after a change on it ends. The sweep reads what `status` reads and no update history. |
| 8 | Bubble Tea v2 and Lip Gloss v2, already in `go.mod`. The `Seg`, `Line`, `Block` layout is ported as it is. Three screens: Fleet, Cluster, Change. Key bindings are one table that drives the handler, the key bar, and the help. |
| 9 | The simulator is `tools/fakefleet` and the unchanged binary. No fake is linked into the product, and no product code path exists for it. |
| 10 | Dropped from the old TUI: the runtime unlock (`ctrl+u`), the session export (`e`), the AWS call pane and its log handler, the kubectl-cluster cursor, and the separate Rolls and Upgrade screens. The export is the one real loss: a change run from the TUI leaves no record on disk. |

## 1. Packages

```
internal/cmd/ui/        the cobra command: flags, validate, run; the stdio detach
internal/tui/           the Bubble Tea model, the screens, the key table, the layout
internal/tui/state/     State and its parts: what the model draws. Imports apidoc, fleet, change, health, noderoll, view for their types.
internal/tui/backend/   the live backend: the sweep loop, readiness, proposals, handles, adoption, the publisher
internal/change/        gains handle.go and watch.go (section 2)
```

Dependency direction, added to the `imports_test.go` rule of `REWRITE.md`: `tui` imports `state`, `view`, and `change` for types; it never imports `awsx`, `fleet`'s fetcher, `kube`, or `backend`. `backend` imports `change`, `fleet`, `health`, `kube`, `awsx`, `noderoll`, `state`. `state` imports no package that makes a call. Nothing in the product imports a `...test` package. The consumer-defined interface that the model needs is in `tui`:

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
	// Plan is the dry run of an action, under the read budget. It changes
	// nothing.
	Plan(ctx context.Context, a state.Action) (state.Proposal, error)
	// Start runs the proposal. It is refused without --allow-changes, when
	// the proposal is not the newest, and when the cluster has a change.
	// A proposal can be started once.
	Start(ctx context.Context, p state.ProposalID) (state.ChangeID, error)
	Answer(ctx context.Context, c state.ChangeID, q change.QuestionID, yes bool) error
	Pause(ctx context.Context, c state.ChangeID) error
	Resume(ctx context.Context, c state.ChangeID) error
	Stop(ctx context.Context, c state.ChangeID) error
}
```

The model calls every method inside a `tea.Cmd`, so a slow backend never holds the UI loop (`tui/model.go:5-7` had the same rule, and `TestUpdateNeverCallsTheBackend` guards it). The context of such a call is the command's, which the model may cancel; nothing that must outlive the call runs under it (section 3, `Start`).

## 2. The handle

### What the old `live` package needed

Each row is something `tui/live` built by hand around the engine. The piece on the right serves it. Nothing else is built.

| Need | Old code | Served by |
|---|---|---|
| A feed of what happened, per change, that the UI reads at its own pace | `b.feed`, `b.log`, copied every 100 ms (`live.go:721-765`) | The handle's log with sequence numbers, published in `state.Change.Log` |
| The state of each step and phase | `upgradeSnapshot`, phases matched by label prefix (`upgrade.go:462-487`) | `Handle.View().Steps`, keyed by `StepRef`; the phase state is derived ("Phase states" below) |
| The nodes of a roll, the last poll of an update, the node lifecycle feed | `rollSnapshot` (`roll.go:775-806`), `Roll.Events` | `View().Progress[StepRef]`: the latest `UpdatePolled` and `NodeProgress`; the node view carries every kept tracker event (section 8) |
| A question that exactly one answer closes, not a stale key | `Answer` matched by cluster only (`live.go:1136-1158`) | `QuestionID`; `Answer(id, yes)` returns `ErrStaleQuestion` for any other |
| Pause before the next phase, stop after the current one, also before the first | `waitToGoOn` (`upgrade.go:333-350`) | `Pause`, `Resume`, `Stop` as intents that the checkpoint hook reads, which phase 4 calls before every phase (section 14) |
| A stop that is not a failure | `finishRun` (`upgrade.go:302-329`) | `Result.Status` is `Stopped` only from a checkpoint |
| The dry run first, then "the plan changed" | five acceptance maps (`live.go:165-184`) | `Common.Expect`; `Run` asks `PlanChanged` |
| The same pipeline as the CLI | a third copy of the roll (`roll.go:331-448`) | `Run` with the same request as the command |
| One change per cluster, one start per confirmation | `b.claimed` (`live.go:162`); the proposal was not consumed | The backend's claim map and the consumed proposal, section 3. Not in the handle. |
| A change started elsewhere | `adoptRoll`, `adoptUpgrade` (`observed.go:167-308`) | `change.Watch` |
| Memory that does not grow with the session | `b.rolls`, `b.upgrades` never trimmed | The backend keeps the last 20 own and the last 20 watched finished handles; the log of a handle has a cap |
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
	// Pause and Stop are true while a checkpoint can still come: until the
	// last phase of the plan has started and, in a serial phase, its last
	// step was accepted. They are false for a watched change.
	Pause bool
	Stop  bool
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

// PhaseStatus is the state of one phase, derived from the events.
type PhaseStatus string // Pending, Running, Done, Skipped, Failed, Stopped

type PhaseState struct {
	Ref                PhaseRef
	Status             PhaseStatus
	StartedAt, EndedAt time.Time
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
	Steps  []StepResult
	Phases []PhaseState
	Question *PendingQuestion
	// PauseRequested and StopRequested are the intents, whether or not a
	// checkpoint has read them yet.
	PauseRequested, StopRequested bool
	Progress map[StepRef]Progress
	Controls Controls
	// Result is set at Finished.
	Result *Result
	// LastSeq is the sequence number of the newest log entry, and Dropped
	// how many of the oldest the cap removed.
	LastSeq, Dropped uint64
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
// takes the channel first, then reads the view, then waits on the
// channel. It is the pattern of fleettest.World.Changes.
func (h *Handle) Changed() <-chan struct{}
// Events returns the log entries with Seq above after, oldest first. It
// copies nothing but the slice header.
func (h *Handle) Events(after uint64) []LogEntry
func (h *Handle) Answer(id QuestionID, yes bool) error // ErrStaleQuestion, ErrNoControls, ErrFinished
func (h *Handle) Pause() error                          // ErrNoControls, ErrFinished
func (h *Handle) Resume() error
func (h *Handle) Stop() error
func (h *Handle) Done() <-chan struct{}
// Result blocks until Finished. It can be called again.
func (h *Handle) Result() Result
```

There is no `Cancel`: no key calls one. The context given to `Start` is the backend's, and quitting the UI cancels it.

### How the hooks become the handle

`Start` builds `Hooks` over the handle and calls `Run` on a new goroutine, with a `start(ctx, run func(Hooks) (Result, error)) *Handle` underneath that the tests drive with a scripted run (section 11):

| Hook | What the handle does | Blocks the executor |
|---|---|---|
| `Event` | Under `h.mu`: `UpdatePolled` and `NodeProgress` replace `Progress[step]`; `StepChanged` copies `*e.Result` into `Steps[i]` (`run.go` emits a pointer into its own results slice, `batch.go:36`, and the handle never keeps it); `PhaseStarted` and `PhaseEnded` update `Phases`; `PlanReady` replaces `Plan` and resets `Steps` to the plan's steps as `NotAttempted`; every kind but the two progress kinds is appended to the log. Then `version++` and the broadcast. | For the time of a map write. Never on a reader. |
| `Ask` | Registers a `PendingQuestion` with the next ID, sets `Asking`, broadcasts, and waits on the question's `done` channel or `ctx.Done()`. After it wakes it reads the outcome under `h.mu`. The question is registered before the broadcast, so a reader never sees a question it cannot answer. | Until the answer. That is the design: `Run` cannot go on. |
| `Checkpoint` | Reads the intents under `h.mu`. Stop: returns `{Stop: true}`. Pause: sets `Paused`, broadcasts, and waits on the resume channel or `ctx.Done()`; returns `{Waited: true}` after a resume and `{Stop: true, Waited: true}` when a stop ended the pause. Neither: returns the zero value. | While paused. |

The log has a cap, `maxLog = 4096`. Lifecycle events are few: `PlanReady` once per plan computed, two events per phase, at most three `StepChanged` per step, a `Notice` per plan difference, busy row, or gate reason. One source grows with time: the Auto Scaling watch sends a `Notice` for each failed launch, once per activity, and a roll that waits on capacity for hours sends many. Past the cap the oldest entry is dropped and `View.Dropped` counts it; a question and a `StepChanged` are never among the dropped, because the handle keeps the entries since the newest `PlanReady` whatever the cap says. A test asserts that a roll of one nodegroup logs under 40 entries and an upgrade of two hops with six nodegroups under 150.

### Questions

```go
var (
	ErrStaleQuestion = errors.New("the question is no longer open")
	ErrNoControls    = errors.New("the change was started elsewhere and cannot be steered")
	ErrFinished      = errors.New("the change has finished")
)
```

One function resolves a question, under `h.mu`:

```go
// resolve closes the open question with an outcome: yes, no, or stop. It
// returns ErrStaleQuestion when id is not the open question. The first
// call wins; a later one finds no question. Ask reads the outcome after
// its done channel closes.
func (h *Handle) resolve(id QuestionID, outcome answer) error
```

`Answer(id, yes)` calls it with yes or no. `Stop` while a question is open calls it with stop: `Ask` returns no, `Run` ends as `Declined`, and `View.StopRequested` says why, as the old `n` meant "stop after this step". A cancel ends `Ask` with `ctx.Err()`, and `Run` ends as `Interrupted`. A key pressed from a stale screen carries the old ID and is refused, which the old backend could not do (`live.go:1143`).

Precedence, as `REWRITE.md` states it: cancel, then stop, then pause, then answer. A Go `select` has no order, so the precedence is not in a `select`: it is in `resolve` and in the checkpoint, which read every intent under the one mutex before they decide, and in the rule that an outcome once set is never replaced. Pause intent and stop intent are fields apart from the question, so answering while paused leaves the run paused, and a pause requested while asking takes effect at the next checkpoint.

### States

| From | Trigger | To |
|---|---|---|
| (none) | `Start` | `Preparing` |
| `Preparing` | `Ask` hook | `Asking` |
| `Preparing` | `Checkpoint` hook with pause intent (before the first phase) | `Paused` |
| `Preparing` | `PhaseStarted` | `Running` |
| `Preparing` | `Run` returns (refused, nothing to do, declined, stopped at the first checkpoint, hook error, interrupt) | `Finished` |
| `Running` | `Checkpoint` with pause intent | `Paused` |
| `Running` | `Ask` hook (a gate that got worse before a step) | `Asking` |
| `Running` | `Run` returns | `Finished` |
| `Paused` | `Resume` | The state before the pause: `Preparing` or `Running` |
| `Paused` | `Stop` | The state before, then `Finished` as `Stopped` |
| `Paused` | cancel | `Finished` as `Interrupted` |
| `Asking` | `Answer(id, true)` | The state before the question |
| `Asking` | `Answer(id, false)`, or `Stop` | `Finished` as `Declined`, after `Run` returns |
| `Asking` | cancel | `Finished` as `Interrupted` |
| `Finished` | anything | `Finished`; `Answer`, `Pause`, `Resume`, `Stop` return `ErrFinished` |

Only `Run` returning sets `Finished`. A `Stop` that `Run` reads at a checkpoint ends as `Stopped`, with nothing submitted when it was the first checkpoint. A step that fails after a stop was requested ends as `Failed`, as phase 4's `Result.Status` already holds (`TestAStopDoesNotHideAFailure`, `tui/live/upgrade_test.go:434`, carries over as a handle test).

### Phase states

The handle derives them. No code matches a label.

| Events seen | Status |
|---|---|
| No `PhaseStarted` | `Pending`; `Stopped` once the run is `Finished` |
| `PhaseStarted`, no `PhaseEnded` | `Running`; at `Finished` with not-attempted steps: `Stopped` when the result is `Stopped`, `Declined`, or `Interrupted`, else `Failed` |
| `PhaseEnded` with `Skipped` | `Skipped` |
| `PhaseEnded`, every step `Succeeded`, `Skipped`, or `Started` | `Done` |
| `PhaseEnded`, otherwise | `Failed` |

`REWRITE.md` named `Submitting`, `Waiting`, and `Verifying`. The step results carry them: a step is `Started` while its update is waited for, and a `VerifyReport` ends a batch.

### Cleanup and goroutines

| Goroutine | Started by | Ends when | Owner |
|---|---|---|---|
| The run: `Run(ctx, d, req, hooks)` | `Start` | `Run` returns; the handle sets `Result`, `Finished`, closes `done`, broadcasts | The handle |
| The run's own: gate checks, poll fan-out, observers | `Run` | Joined by `Run` (phase 4, section 7) | `Run` |
| The notifier, one per handle | `backend.Start` and `backend.adopt` | After `h.Done()` | The backend |

The notifier, written so that no wakeup is lost:

```go
func (b *Live) follow(id state.ChangeID, h *change.Handle) {
	after := uint64(0)
	for {
		ch := h.Changed() // take the channel before the read
		v := h.View()
		b.store(id, v, h.Events(after))
		after = v.LastSeq
		b.bump()
		select {
		case <-ch:
			continue
		case <-h.Done():
		}
		// The last change may have come between the read and Done.
		v = h.View()
		b.store(id, v, h.Events(after))
		b.release(id, v) // the claim; a refetch of the cluster is queued
		b.bump()
		return
	}
}
```

At `Finished` the handle holds its plan, its steps, and its log until the backend drops it. It holds no client and no observer: `Run` closed them. `internal/change` already has `goleak.VerifyTestMain`.

### `Watch`

```go
// Watch follows a change that something else started: the CLI, the
// console, an automation. It waits for the update in flight on a
// nodegroup or on the control plane, shows the nodes of a nodegroup roll,
// and verifies a nodegroup after it. The handle has no controls. The
// result has one step, with Attached set.
func Watch(ctx context.Context, d Deps, r Resource, o WatchOptions) *Handle

type WatchOptions struct {
	Nodes  bool   // start the node observer of a nodegroup
	Limits Limits // Wait bounds the watch; Poll is its interval
}
```

What it runs, on its goroutine, with the same events as `Run`:

1. `inFlightUpdate` (`busy.go:211`) finds the update in progress, and `describe` (`wait.go:363`) reads the resource. No update in progress and a settled resource: the watch ends at once as `NothingToDo` with no step. The sweep that started it read a state that is gone, and the backend drops the handle without a feed line.
2. `PlanReady` with a plan of one step: `PhaseRef{Target, Kind}` where the kind is `PhaseRoll` for a nodegroup and `PhaseControlPlane` for the cluster; the step is `Attach` with reason `InFlight`. The plan's `Kind` is `NodegroupUpdate`, or by the direction of the update's version against the cluster's version, `ClusterUpgrade` or `ClusterRollback`. The old TUI got that wrong once (`TestAWatchedRollbackSaysRollback`, `roll_test.go:853`).
3. With an update ID: `WaitUpdate`. Without one, as for a resource that the console changed through a path that lists no update: `WaitSettled` with the nodegroup's or the cluster's `done` of phase 4, section 2.
4. Beside the wait of a nodegroup, with `Nodes`: the node observer of `observe.go` with `Identity{StartedAt: update.CreatedAt, TargetVersion: version of a version update}`. `observeNodes` becomes a function that takes an emit function, so the watcher and the runner share it (section 14).
5. After a nodegroup settles: `health.Verify(names, health.Baseline{}, wait)`: `NodegroupActive` runs, the other two are `Skipped` (phase 3, section 5).
6. `StepChanged` with the result; `Result` with one `ClusterResult`.

`Watch` is in `internal/change` because every piece it needs is there and unexported. Phase 4 listed `WaitUpdate`, `WaitSettled`, `noderoll.Attach`, and `health.Verify` as its parts; the list holds.

## 3. Consent in the TUI

The flow, mapped onto phase 4's rule "`Run` submits only with `Policy.Yes`, a reviewed plan that matches, or a yes answer":

| Step | What happens | Where |
|---|---|---|
| The user presses `p`, `a`, `U`, or `B` | `backend.Plan` builds the request and calls `change.NewPlan` under `cmdutil.WithTimeout(ctx, Limits.Read)`: a dry run, the gate included, nothing submitted, and a deadline (`TestPlanHasADeadline`, `live_test.go:621`). The dialog shows the steps, the gate verdict with its reasons, the blockers, and the CLI command. | `backend.Plan` |
| The user presses `y` | `backend.Start(proposalID)`: the checks below, then `change.Start(b.ctx, deps, req)` with the same request and `Common.Expect` set to the reviewed plan. `Policy` is the zero value. | `backend.Start` |
| `Run` computes its first plan | `Diff(Expect, plan)`. `Done` and `NowInFlight`: notices. `KubernetesVersion`, `RollbackTarget`, `NowBlocked`: `Refused`, exit 3 or 4, and the Change screen shows why. `Release`, `Version`, `Sizes`, `Added`, `Removed`, `GateWorse`: the question `PlanChanged` or `GateWarnings`. | `change.Run` |
| The gate of the reviewed plan said `Ask` | No question. The dialog showed the warnings, and `y` on that dialog is the answer; `Run` records them in `Gate.Accepted` (`accept`, `run.go:204`). | `change.Run` |
| A question during the run | `GateWarnings` before a nodegroup step whose gate got worse. The Change screen shows it with `y` and `n`. | The handle |
| The user presses `y` on a question | `Run` computes the plan again and compares it with the plan of the question (phase 4, section 1). | `change.Run` |

The TUI does not set `ConfirmPhases`. The old TUI confirmed every phase with the dry run (`tui/live/upgrade.go:254-256`: "Every phase was confirmed with the dry run. Confirm holds a phase while the run is paused and declines once a stop is asked for"), and a question before each phase would hold an unattended upgrade at phase 2 for ever, since the budgets do not run inside `Ask`. `P` and `S` are the TUI's hold and stop. The engine's `ConfirmPhase` stays what the CLI asks on a terminal without `--yes`.

The TUI has no `--force`, no `--require-healthy`, and no `--skip-health-check`, as before. A blocked plan has `y` disabled in the dialog (`canStart`, `keys.go:235`); `Run` would refuse it anyway, and a test proves that a `Start` of a blocked proposal ends `Refused` with nothing submitted.

### `Start`

```go
package backend

var (
	ErrReadOnly      = errors.New("the UI is read-only; start it with --allow-changes")
	ErrStaleProposal = errors.New("the dry run is no longer the newest; run it again")
	ErrClusterBusy   = errors.New("the UI already runs or watches a change on this cluster")
)

func (b *Live) Start(_ context.Context, id state.ProposalID) (state.ChangeID, error) {
	if !b.allowChanges { // set by New, never changed
		return 0, ErrReadOnly
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.proposal
	switch {
	case p == nil || p.ID != id:
		return 0, ErrStaleProposal
	case len(b.byCluster[p.Action.Cluster]) > 0:
		return 0, ErrClusterBusy
	}
	b.proposal = nil // one confirmation starts one run
	h := change.Start(b.ctx, b.deps, p.request(b.limits(p.Plan.Kind)))
	cid := b.add(h, p.Action.Cluster, false)
	go b.follow(cid, h)
	return cid, nil
}
```

Three rules of this function: `allowChanges` is a field that `New` sets from the flag and no method writes, and `Start` is the only function in the tree that calls `change.Start` with a request of the TUI (`adopt` calls `change.Watch`, which submits nothing). The proposal is consumed under the lock, so a second `y` for the same dialog after a fast `Refused` or `Failed` run cannot start a second run; the old backend had no such rule. The run gets the backend's context, never the caller's: the caller is a `tea.Cmd`, and the model may cancel it.

A test builds a backend without the flag, plans every kind of change, calls `Start` on each, and asserts `ErrReadOnly` and an empty mutation log of the fake. `Answer`, `Pause`, `Resume`, and `Stop` do not need the check: without a start there is no handle with controls, and a watched handle refuses them with `ErrNoControls`. The old `TestChangesAreRefused` (`live_test.go:414`) carries over with that difference.

The runtime unlock (`ctrl+u`, `unlocker`, `live.go:1056-1062`) is dropped (owner decision). A dry run now always runs the gate, so a read-only dry run is the same plan.

## 4. One change per cluster, busy, and changes started elsewhere

### Claims

```go
type Live struct {
	mu        sync.Mutex
	handles   map[state.ChangeID]*entry      // every handle the UI holds
	byCluster map[state.Key][]state.ChangeID // the running ones, own and watched
	ended     map[state.Key]time.Time        // when the last own handle of a cluster ended
	// ...
}
```

The claim is taken under `b.mu` in `Start` and in `adopt`, and released by `follow` when `Done` closes. `TestStartErrorReleasesTheCluster` (`roll_test.go:335`) carries over. `TestClaimSurvivesAKeyChange` (`roll_test.go:520`) does not: the key of a cluster is `{Region, Name}` and never changes.

### Busy

Three questions, three places, no copy:

| Question | Answered by | Shown as |
|---|---|---|
| Is this UI running or watching a change on the cluster? | `byCluster` | The row's `CHANGE` column and the Change screen |
| Is AWS changing the cluster, or one of its resources, now? | `change.Busy` in the plan (a `Busy` blocker, a `Busy` step), and the fresh read before each submit (`Run`) | The dialog's blockers; a refused start |
| Does the last sweep show a resource in flight? | The snapshot's statuses, read once per sweep into `Cluster.InFlight` | The row's `STATUS` column; `p`, `a`, `U`, and `B` say "prod is changing (add-on vpc-cni UPDATING)" instead of opening a dry run (`TestChangeKeysOnABusyClusterSayWhy`) |

An add-on update started elsewhere is the third row only: the cluster reads as changing until a sweep shows it settled, and no watch follows it (owner decision). The old `checkNotBusy` (`live.go:1174-1189`) re-read the cluster before a start with the service's busy check. That read is now inside `Run`: the first plan, the diff, and the fresh read before the submit. The TUI makes no read of its own before a start.

### Changes started elsewhere

After each sweep, under `b.mu`, for every snapshot whose cluster or a nodegroup is in flight (the definition of phase 4, section 3):

1. The cluster has a running handle: nothing. Its own handle reports what happens there, and a foreign update on it is a notice or a `Busy` row of our run.
2. The snapshot's `FetchedAt` is before `ended[key]`: nothing. The sweep read the cluster while our own change was still running, and what it saw was ours. Without this rule a sweep that read `UPDATING` just before our handle ended would adopt our own roll after the claim was released, as a phantom "started elsewhere".
3. The key is `{Target, StepKind, Name}`. If a watched handle for that key is running: nothing.
4. Else `change.Watch(b.ctx, deps, resource, WatchOptions{Nodes: nodes, Limits: b.limits(kind)})`, recorded with `Elsewhere: true` and claimed by cluster. `nodes` is true only when exactly one nodegroup of the cluster is in flight, the rule of `observe.go:65-67`, so a batch that someone else started does not get one informer set per nodegroup.

A watch that finds no update and a settled resource on its first read ends as `NothingToDo` and is dropped without a feed line (section 2). A watched handle ends when the update ends; a later sweep that still shows the resource in flight is a second update and starts a second watch. The identity of a watch is therefore the key plus the update ID that `Watch` found, and the view names it.

What a watched change shows: the same Change screen, the header marked "started elsewhere, watch only", the step list with one step, the update's status from `Progress`, the node table of a nodegroup, and the verification. The key bar shows no `P`, `S`, `y`, or `n` for it, because `Controls` is empty.

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
	Status     view.Status
	Version    fleet.Version
	Latest     fleet.Version // the newest version EKS offers in the region
	Stale      Stale         // counts of nodegroups and add-ons behind
	InFlight   []string      // resources in flight, from the snapshot: "nodegroup ng-a", "add-on vpc-cni"
	Incomplete bool
	Change     ChangeID      // the running change of this UI on the cluster, 0 for none
	Readiness  *Readiness
	// Rollback comes from the last readiness run, which reads the update
	// history. The sweep does not read it.
	Rollback *fleet.Rollback
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
	// Log is the whole log of the handle as the backend has read it,
	// oldest first, each entry with its Seq. It is append-only across
	// versions: the backend appends what Events(after) returned and never
	// rewrites an entry, so a pane frozen at a Seq shows the same lines
	// on every later version.
	Log []change.LogEntry
	// Nodes is the node lifecycle feed of each roll step: every tracker
	// event the latest NodeView carried (section 8).
	Nodes map[change.StepRef][]noderoll.Event
}

// Line is one line of the fleet feed.
type Line struct {
	At      time.Time
	Cluster Key // zero for a line about the fleet or a region
	Status  view.Status
	Text    string
}
```

Typed everywhere: `view.Status` for a mark, `fleet.Version`, `apidoc` enums through `change.View`, `change.RunState`, `change.PhaseStatus`, `health.Outcome` in the report. No field is a label that code reads back. The old `Cluster.Busy` string, `Phase.Name`, `Upgrade.Label`, and `Plan.Blocked` (`state.go:124, 339, 287, 406`) have no successor.

The model holds no handle. Everything it draws of a change is in `state.Change`. A frozen pane (`space`) keeps the `Seq` at the freeze for that pane and draws the entries up to it; the log entries are never removed from a published `Change`, so nothing is copied at the freeze (the old `TestFrozenFeedSurvivesEviction` needed a copy because the backend's lists were capped).

### The fleet feed

What the backend appends, so that the feed's content is defined and testable:

| Event | Line |
|---|---|
| A sweep ends | One summary: "17 of 20 clusters need attention, 2 changing". Then one line per cluster whose status is `Fail` (extended support, a failed read, a `DEGRADED` resource), never one per warning (`TestFirstSweepOfABigFleetSummarizes`, `live_test.go:813`; `TestFirstSweepShowsTheErrorUnderAChange`, `live_test.go:883`). |
| A sweep ends, after the first | One line per cluster whose version, resource statuses, or listing changed since the sweep before: "control plane 1.31 → 1.32", "ng-system UPDATING → ACTIVE", "no longer listed", "new cluster" (`TestSecondSweepReportsChanges`, `live_test.go:215`). |
| A region failed, or regions were skipped | One line per failed region with the fail mark; one line that lists the skipped regions, once per change of that list |
| A change of this UI | "started", each question, "ended: Succeeded" with the status; a watched change: "watching a roll started elsewhere" and its end |
| A readiness run | "readiness: Ready" with the verdict |

`feedLines = 500`, oldest dropped.

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
	ID      ProposalID
	Action  Action
	Plan    *change.Plan
	Doc     apidoc.Plan
	Command string // the CLI command that makes the same change
	Blocked bool   // Plan.Decision() == DecisionBlocked
}
```

`Command` carries `--profile`, `--region`, and on the commands that take Kubernetes access `--kubeconfig` and `--kube-context`, as the UI was started, so the copied command runs against the same account (`TestCopiedCommandsRunAsTheUI`, `live_test.go:907`).

The backend keeps one proposal: the newest, and `Start` consumes it (section 3). `Plan` replaces it, so a dry run that finishes after a newer one never becomes the proposal (`TestAnOlderDryRunCannotReplaceTheConfirmedOne`, `upgrade_test.go:547`).

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
			st := b.build() // under the lock; "Cost" below
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

What bumps: a region answered (`OnRegion`), a sweep ended, a readiness started or ended, a handle changed (through its notifier), a proposal was made or consumed, a cluster was re-read after a change. The clock is not a bump: the model ticks for it (section 7).

### Cost

`build` under the lock does this and no more: copies the slice headers of `b.rows` (rebuilt in `OnRegion` for the regions that answered so far, sorted once at the end of the sweep), reads the stored view, log, and node feed of each handle (`follow` stores them, so `build` copies three headers per handle), and appends the `Changes` and `Feed` headers. At 200 clusters that is under 300 pointer copies: microseconds.

Memory at 200 clusters: the snapshots of one sweep, which `status -A` already holds at its end (phase 2, section 16); the rows, 200 structs; the feed, at most 500 lines; the handles, at most 20 own and 20 watched finished ones plus the running ones, each with a plan and a log of at most 4,096 entries. The sweep in progress holds a second set of snapshots until it ends; the old set is dropped when the rows are replaced. Under 40 MB, measured by the test of section 12.

## 6. The backend and the sweep

```go
package backend

type Options struct {
	Regions      fleet.Regions
	Interval     time.Duration // --interval, at least 10 s
	AllowChanges bool
	ReadTimeout  time.Duration // --timeout: the sweep, a plan, a readiness run
	// WaitTimeout bounds a change the UI starts or watches. With Set false
	// each kind of change gets the default of its command; 0 with Set
	// means no limit, as everywhere.
	WaitTimeout time.Duration
	Set         bool
	Profile, Context string // for the top bar and the copied command
	Kubeconfig, KubeContext string
}

func New(ctx context.Context, f *cmdutil.Factory, o Options) (*Live, error)
func (b *Live) Run(ctx context.Context) error // the sweep loop; returns when ctx ends and every goroutine is joined

// limits returns the budgets of one kind of change.
func (b *Live) limits(k change.Kind) change.Limits
```

The sweep loop, one goroutine:

1. `fetcher.Sweep(ctx, provider, regions, SweepOptions{Need: need, OnRegion: b.regionDone})` under `cmdutil.WithTimeout(ctx, ReadTimeout)`, with `need = fleet.Need{Nodegroups: true, AMI: true, Addons: true, LatestAddons: true, Support: true}`: what `status` reads. Not `Updates`: that is `ListUpdates` plus one `DescribeUpdate` per update of every cluster (`updates.go:86-91`), every minute, for the rollback window alone. The window comes from the readiness run instead, which reads the history of one cluster on demand, and `B` is offered after a readiness run found a window, as the old TUI did. `OnRegion` replaces the rows of that region and bumps, so the fleet fills as regions answer.
2. At the end: the sweep summary into `Sweep`, the feed lines of section 5, adoption (section 4), bump.
3. Wait on `time.After(Interval)` from the factory's `After`, `b.refresh` (a buffered channel of 1 that `Refresh` sends to), `b.refetch` (the keys of clusters whose change ended), or `ctx.Done()`.

A fresh `fleet.Fetcher` per sweep, so the memos of the latest versions do not outlive one.

After a change ends, `follow` sends the cluster's key on `b.refetch`; the loop, between sweeps, runs `fetcher.Fetch` for that cluster alone with the same `need`, replaces its row, and bumps. The whole fleet is not swept for one cluster.

Readiness on demand runs on a goroutine of its own per request, one per cluster at a time (a second request while one runs is refused with `ErrReadinessRunning`), under `ReadTimeout`. It calls `health.CheckReadiness`, which phase 5 extracts from `cluster upgrade-check` (`internal/cmd/cluster/upgradecheck.go:150-186` and `readinessOf`), so the command and the TUI run the same waves:

```go
package health

type ReadinessReport struct {
	Readiness apidoc.Readiness
	Reasons   []string
	Checks    []Result
	Insights  []apidoc.Insight
	Rollback  *fleet.Rollback // from the update history, inside the window only
	Failures  []apidoc.Failure
}

func CheckReadiness(ctx context.Context, c *awsx.Clients, f *fleet.Fetcher, cluster string, category string) (ReadinessReport, error)
```

The command's `document` and `view` stay in the command; its tests are the proof of the extraction.

Failures and skipped regions on screen: the top bar shows `regions 7/8` and `1 skipped` in the caution color; a failed region is a feed line with the fail mark and the row is absent; an incomplete cluster row carries the warn mark in `STATUS` and its card lists the failure messages; a sweep in which no region answered shows `Sweep.Error` on an empty fleet screen (`TestNoRegionAnsweredSaysWhyNotEmpty`, `model_test.go:918`; the message comes from `fleetcmd.SweepError`, which runs the STS check once).

## 7. Model and views

### Libraries

Bubble Tea v2 (`charm.land/bubbletea/v2 v2.1.0`) and Lip Gloss v2 (`charm.land/lipgloss/v2 v2.0.6`), both in `go.mod`, plus `github.com/charmbracelet/x/ansi` and `github.com/rivo/uniseg` for width math, as the old tree. No Bubbles components: the lists, the dialogs, and the bars are a few lines each over `Block`, and a component would bring its own key handling and styles.

### Layout

`Seg`, `Line`, `Block`, `hjoin`, `box`, `overlay`, `Fit`, `Cut`, `wrap`, `wrapExact`, `bar`, `joinRight` are ported from `tui/draw.go` with their tests (`draw_test.go`, the `wrapExact` tests of `model_test.go:1375-1452`). They render styles last, so width math never sees an escape code, and `Block.fit(w, h)` is what makes every frame exactly `w` by `h`. Lip Gloss's `JoinHorizontal` and `Place` measure styled strings and cannot give that guarantee as cheaply; Lip Gloss is used for one thing: `Line.Render`, which paints a `Seg`.

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

`view` gains the roles and their colors, which it already holds as unexported `rgb` values (`theme.go:51-62`), plus the four background layers of the old TUI (`tui/theme.go:28-32`):

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
| `2` | Cluster | The nodegroups and the add-ons of the selected cluster, as `cluster describe` shows them | Readiness: the verdict, the checks with the selected check's details, the insights, the rollback window |
| `3` | Change | For a roll: the node table and the pods of the draining nodes. For an upgrade or a rollback: the phase list with the steps, and the node table of the step that rolls. The header: the kind, the target, the versions, the state, elapsed, the question when one is open. | The change's log, merged with the node feed of the step that rolls |

The Rolls and Upgrade screens of the old TUI are one screen (owner decision). The screen shows the change of the selected cluster, newest first, and `[` `]` page through the changes of the UI when there are several.

Dialogs over the screen: the proposal (the plan, the gate, the blockers, the command; `y`, `n`, `c`), the picker (which nodegroup; which part of an upgrade), the help (`?`). Each is a `dialogParts` with a scrolling body, ported from `dialogs.go`.

### Keys

The `binding` table of `keys.go:15-28` is ported as it is: `keys`, `label`, `desc`, `short`, `bar`, `primary`, `when`, `do`. One table per screen plus the global one; the handler walks it, the key bar lists the active entries with `bar`, and the help lists every active entry. The key bar's narrowing (`view.go:174-222`) is ported with its tests.

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
| `B` | Fleet, Cluster | propose a rollback, only after a readiness run found a window |
| `y` | proposal | start; `n` close; `c` copy the command (OSC 52; the line says "sent to the terminal") |
| `y` `n` | Change, question open | answer |
| `P` | Change, `Controls.Pause` | pause before the next checkpoint, or resume |
| `S` | Change, `Controls.Stop` | stop at the next checkpoint |
| `[` `]` | Change | page through changes |
| `space` | any | freeze the feeds of the screen at the current `Seq`, or follow |
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
	fleet    fleetScreen   // cursor, feed scope, frozen Seq
	cluster  clusterScreen // check cursor
	change   changeScreen  // change index, frozen Seq
	dialog   dialog        // nil, or the proposal, the picker, the help; with its scroll
	planning *planning     // a dry run on its way: its action and a cancel
	starting bool
	warnOnly bool
	notice   notice // the log strip's one-line message and its deadline
}
```

The messages: `stateMsg{State}`, `proposalMsg{id, Proposal, err}`, `startedMsg{ChangeID, err}`, `doneMsg{err}` for a call with no result, `tickMsg`, `tea.WindowSizeMsg`, `tea.KeyPressMsg`. The ordering guards of the old model carry over where they still apply (section 11). The fetch-ID guards have no successor: `Next` returns versions in order, one at a time, and a test asserts that `Update` on a `stateMsg` returns exactly one command. After a start the model moves to the Change screen when the first state that lists the new `ChangeID` arrives; a key pressed before that cancels the move, as `TestNavigationAfterStartIsNotOverridden` had it, and the model matches the ID, not a fetch number, so an older state cannot consume the move.

### The clock

The model ticks once a second with `tea.Tick` while `st.Changes` has a running change, a readiness runs, or a sweep runs. Otherwise no tick is scheduled, and the next `stateMsg` schedules one when needed. The top bar shows the time of the last sweep as a clock time, not as "ago", so it needs no tick. `TestBeforeTheFirstSweep` (`fleet_scale_test.go:95`) carries over: before the first sweep the bar says "sweeping".

### Terminal

| Condition | Behaviour |
|---|---|
| Below 100 by 24 | One line: "refresh ui needs a 100×24 terminal (now 80×20)", exactly filled. The model keeps its state, so a resize back shows the screen. |
| Resize | `tea.WindowSizeMsg` sets `w`, `h`; cursors and scrolls are clamped; the picker keeps its selection visible. |
| `--no-color`, `NO_COLOR`, `TERM=dumb` | `tea.WithColorProfile(colorprofile.ASCII)`; `Theme.Color` returns false, so no background is set and the selection is a `>` marker, not a highlight. Marks and labels carry every status. |
| Non-UTF-8 locale | `Theme.Unicode` is false: ASCII borders, ASCII marks (`[OK]` is sized into the columns), `#` and `.` for bars, `...` for a cut. The old TUI refused to adapt (`tui/theme.go:36-40`); the cost here is one border set and the glyph table that `view` already has. |
| Another program writes over the screen | `ctrl+l`, and a full redraw after each sweep at most every 30 s. |

## 8. The live node view

The node view of a roll is the `NodeProgress` event, coalesced into `View.Progress[step].Nodes`: a `noderoll.Snapshot`, the tracker's events, and `Watching`. Today the event carries `Recent(8)` without a time (`observe.go:35`, `tracker.go:21-24`), which is enough for the CLI panel and loses the node feed that the old `Roll.Events` was. Section 14 asks for two things: `noderoll.Event` gains `At`, and `NodeView` carries every event the tracker keeps, up to its cap of 256 (`trackerEventCapacity`). The backend stores the slice of the latest view as the step's node feed (`state.Change.Nodes`), and the Change screen merges it with the log by time: "ip-10-0-1-5 draining", "ip-10-0-1-9 joined", "ip-10-0-1-5 terminated", between the lifecycle lines.

The Change screen draws the node table itself (sorted draining, joining, old, new, as `rolls.go:91-102`), with the drain progress of each draining node and its pod names, the pressure conditions, and the warnings. The CLI's `view.RollPanel` is not reused: it is a column of lines for a terminal of unknown width, and the TUI's pane has a width and a background. The data is shared; the table is not, and it holds no logic.

The view is best effort beside the authoritative wait, as phase 4, section 9, has it: `Run` starts the observer with `Common.Nodes`, which the backend sets for every roll it starts, and `Watch` sets by the one-nodegroup rule of section 4. A failed `Attach` is a `Notice` in the log and the pane says "no node view: <reason>". The EKS update decides the result; the nodes never do.

The kube client comes from `Deps.Kube`, which is `Factory.Kube`: `kube.Connect` with the global `--kubeconfig` and `--kube-context`, matched by endpoint for each cluster. `kube.Connect` writes nothing, which is why the old TUI avoided the runner's resolver.

## 9. Kubernetes exec plugins and stdio

`kube.Connect` sets `ExecProvider.StdinUnavailable = true` and `InteractiveMode = Never` (`internal/kube/client.go:347-348`), so `aws eks get-token` never reads the terminal and never blocks on it. That covers stdin: the old `detachStdio` pointed `os.Stdin` at `/dev/null` for this reason (`commands/ui.go:163-180`), and that part is gone.

Stderr is not covered. client-go hands a plugin `os.Stderr` and has no field for it, so a plugin's warning lines, and anything that writes to file descriptor 2 inside the process, would draw over the screen. The `ui` command therefore does, for the time the program runs:

1. On unix: `unix.Dup(2)` to keep the terminal's stderr, `unix.Dup2(null, 2)`, and `os.Stderr = null`, as `commands/stderr_unix.go:17-21` does (`syscall.Dup2` does not exist on linux/arm64, a release target). On Windows: reassign `os.Stderr` only, which is what client-go captures. Restore at the end, before the final error prints.
2. `debug.SetCrashOutput(saved)` with the saved descriptor as an `*os.File`, so a panic trace reaches the terminal and is not lost in `/dev/null`.
3. `klog.SetLogger(logr.Discard())` with `defer klog.ClearLogger()`, so client-go's informer and watch messages cost nothing.
4. The `slog` handler of the root (`--log-level`) writes to `IOStreams.ErrOut`, which is the terminal. The command replaces it with `slog.DiscardHandler` for the run.

Bubble Tea gets `tea.WithInput(ios.In)` and `tea.WithOutput(ios.Out)`: the real terminal, which fd 1 still is. The final error of the command, if any, prints after the restore.

## 10. The simulator

The simulator is `tools/fakefleet` and the unchanged binary (owner decision): `go run ./tools/fakefleet > fleet.env; source fleet.env; refresh ui -A --allow-changes`. No fake is linked into the product, no environment variable switches anything in `refresh`, and no code path of `tui`, `backend`, or `change` exists for it. `REWRITE.md` line 491 ("the simulator switch runs the real backend on the fake world") is edited by the owner to match.

What `fakefleet` needs:

| Need | Why |
|---|---|
| One `kubetest` server per cluster, and a kubeconfig written to its directory with one context per cluster, exported as `KUBECONFIG`. Today it exports `KUBECONFIG=<dir>/no-kubeconfig` (`tools/fakefleet/main.go:240`). The server sets the cluster's endpoint in the world, so `kube.Connect` matches by endpoint as against AWS. | The node view, the drain gate, and the verification need Kubernetes access. |
| Scripted failures in `generate` (`main.go:332`): one nodegroup whose next roll fails with `PodEvictionFailure` and a stuck drain, one whose roll leaves a node `NotReady`, one add-on whose update ends `DEGRADED`, one cluster with an `ERROR` insight, one cluster with a successful upgrade in its history within the window, one nodegroup `UPDATING` at start with an update in progress (adoption), a slow region, and a throttled operation through `SetScopedFault`. | Every state of the Change and Cluster screens appears in a demo. |
| Short scripted durations: a roll of 2 minutes, an add-on update of 30 seconds, a control-plane update of 3 minutes, an insights refresh of 20 seconds. | A `-speed 1` demo runs in real time with no clock seam in the product. `-speed` stays for a faster one. |
| A `RollScript` for a roll that fails after some nodes were replaced. | The node table of a half-rolled nodegroup. |
| A fault with a count ("every third `DescribeNodegroup` in `eu-west-1`"), if `Fault` does not express it yet. | A sweep with some incomplete rows, every sweep. |
| `task run:tui:sim`: starts `fakefleet` in the background, sources its environment, runs `refresh ui -A --allow-changes`, and stops `fakefleet` at the end. | One command for a manual session. |

The tests of phase 5 never use `fakefleet`: they build the world in process (section 11).

## 11. Tests

### Model (`internal/tui`)

- **Exact fill.** A port of `TestEveryScreenFillsTheTerminalExactly` (`model_test.go:133`): sizes 100×24, 120×40, 160×42, 231×64, for Unicode and ASCII, with and without color. The harness feeds `stateMsg` values built from a scripted `Backend` (a struct of states and replies, no goroutine), runs the returned commands inline as the old `send` did, and after every step asserts `len(frame) == h`, every `Line.Width() == w`, and `strings.Count(render, "\n")+1 == h`. The script: the fleet, `p` with the picker, the proposal, `G`, `y`, each screen, `?`, a question, `P`, `S`, `space`, `w`, a resize to 80×20 and back.
- No test of `tui` calls `time.Sleep` or `time.After`; `tickMsg` is sent by the test.

### Handle (`internal/change`)

The state table is tested on the unexported `start(ctx, run func(Hooks) (Result, error)) *Handle` with a scripted run: a function that calls the hooks in a script (`Event`, `Ask`, `Checkpoint`, in the order a real run would) and returns a `Result`. No network, so `testing/synctest.Test` applies: every goroutine of the handle is at a known point when the test asserts.

- Each row of the state table: through `Preparing`, `Running`, `Finished`; a question answered yes, no, twice, with a stale ID, after Finished; pause then resume; pause then stop; stop in `Preparing` before the first checkpoint gives `Stopped` and the script records no submit; stop then a failing step (`Failed`, not `Stopped`); `Controls` false after the last phase started; cancel in every state.
- Races, in one bubble: `Answer` from two goroutines (one wins, one `ErrStaleQuestion`); `Stop` and `Answer` at once (one outcome, never two); `Pause` then cancel (`Interrupted`); the executor inside `Event` while a reader calls `View` (no deadlock).
- The log: never blocks the executor (an `Event` while no reader exists returns at once); `Events(after)` is monotonic; the progress kinds are not logged; the cap drops the oldest and keeps the entries since the last `PlanReady`; `View` is immutable (a reader that writes to a slice of a view changes nothing in the handle; the old `TestStateSharesNoMemory`).

The cases that go through the real `Run` use `awsxtest` with the world clock and the fake's handshake (`fleettest.World.AwaitBlocked`, `fault.go:266`) in place of `synctest`, which cannot wait on a loopback socket: a roll of one nodegroup logs under 40 entries; a stop in `Preparing` of an upgrade whose insights refresh is held by a fault leaves an empty mutation log; `Watch` for a nodegroup with an update (wait, nodes, verify), without one (settled: `NothingToDo`), with a failed update, with a `NotReady` new node (exit 5 in the result), a control plane by direction (upgrade, rollback); `ErrNoControls` from each control. `goleak` and `-race` on all of them.

### Backend (`internal/tui/backend`)

Against a world, `awsxtest.NewServer`, a factory built as `cmdtest.NewFake` builds one (`UseWorldClock`), and `kubetest.NewServer` per cluster, each test a scripted world:

- A sweep: rows, a failed region, skipped regions, an empty fleet, no region answered (the STS check), incomplete rows, `OnRegion` versions, `Refresh`, `--interval`; the feed lines of section 5 after the first and the second sweep; no `ListUpdates` in the request log.
- Readiness: a run with and without a rollback window, an error, two requests at once; `B` offered only after it.
- A roll from a proposal: the dialog's plan and command, `Start`, the node view, the node feed, the verification, the re-read of the cluster at the end.
- An add-on update, with one add-on that fails and the next that goes on.
- An upgrade: pause, stop (before the first phase too), a gate that got worse, a changed plan.
- A rollback inside the window and one outside (blocked in the proposal).
- An adopted roll and an adopted control-plane update: watched, no controls, ends when EKS says so, a second update after the first; a sweep that read `UPDATING` before our own handle ended adopts nothing; a foreign add-on update marks the cluster busy and starts no watch.
- Refusals: without `--allow-changes` (every kind), a stale proposal, a consumed proposal (`y` twice), a blocked proposal, a cluster with a running change, a declined question, 32 concurrent `Start` calls of one proposal start one run.
- `Next` honours cancel and returns versions in order; `build` runs under the lock for less than a bound of allocations.
- Shutdown: `Run` returns with every goroutine joined when ctx ends during a sweep, a plan, a change, and a watch.

### End to end

One scripted run through `tea.NewProgram` with `tea.WithInput` on a pipe, `tea.WithOutput` on a buffer, `tea.WithoutSignals`, and `tea.WithWindowSize(120, 40)`: a world built in process with the generator of `fakefleet` (moved into `fleettest` so the tool and the test share it), the key script of the exact-fill test, and an assertion on the last frame and on the mutations in the fake. `teatest` is not in `go.mod`; `github.com/charmbracelet/x/exp/teatest/v2` is tried first, and if it does not build against `charm.land/bubbletea/v2` the test drives the program directly. The old tree had no such test.

### Disposition of the old TUI tests

Every test of `tui`, `tui/state`, `tui/live`, and `sim` is listed in `legacy/PORTING.md` with one of three dispositions. The table below is that list. "Carried" names the new package; "phase 4" names the test of `internal/change`, `internal/health`, or `internal/noderoll` that covers the behaviour, since the TUI no longer owns it; "dropped" gives the reason.

| Old test | Disposition |
|---|---|
| **`tui/model_test.go`** | |
| `TestEveryScreenFillsTheTerminalExactly`, `TestTooSmallTerminal`, `TestLoadingFrameBeforeTheFirstState`, `TestDialogScrollsOnAShortTerminal`, `TestLongErrorKeepsTheDialogKeysOnScreen`, `TestKeyBarKeepsEveryKeyAtMinimumWidth`, `TestResizeKeepsThePickerSelectionVisible`, `TestPickerKeepsItsSelectionOnScreen`, `TestDryRunDialogWrapsLongLines`, `TestUpgradeHeaderCutsALongLabel`, `TestUpgradeHeaderKeepsTheStatus`, `TestUpgradeShowsLongReasonsInFull`, `TestCtrlLRedraws`, `TestSweepRedrawsTheScreen`, `TestWrapExactKeepsQuotedSpaces`, `TestWrapExactFollowsShellQuoting`, `TestWrapExactMeasuresGraphemes`, `TestWrapExactAndHeaderEdgeCases` | Carried: `tui`, as they are |
| `TestPatchFlowRunsARollToTheEnd`, `TestBlockedUpgradeDoesNotStart`, `TestEnterNeverConfirmsAChange`, `TestClusterScreenRunsReadiness`, `TestUpgradeScreenStopAndPause`, `TestAddonUpdateFromTheFleet`, `TestStartIsSentOnce`, `TestCancelIsOffWhileAChangeStarts`, `TestOnlyTheNewestPlanOpens`, `TestEscCancelsAPendingPlan`, `TestNavigationAfterStartIsNotOverridden`, `TestPendingPlanSurvivesKeysThatKeepTheTarget`, `TestMovingTheSelectionCancelsThePlanAndTheFocus`, `TestNoPlanOpensBehindThePicker`, `TestAPlanRequestDropsThePendingFocus`, `TestPlanningNoticeLastsWhileThePlanIsPending`, `TestPickerChoosesAmongStaleNodegroups`, `TestUpgradeQuestionIsAnsweredWithYOrN`, `TestBackendErrorsBecomeNotices`, `TestSelectionFollowsTheClusterNotTheRow` | Carried: `tui`, over the scripted backend |
| `TestOlderStateDoesNotConsumeTheFocusAfterStart` | Carried: `tui`, as "the move to the Change screen waits for the state that lists the started `ChangeID`; an older state does not consume it" |
| `TestPausedFeedHoldsStill`, `TestPauseIgnoresEventsWithTheSameTimestamp`, `TestFrozenFeedSurvivesEviction`, `TestFrozenFeedExplainsAChangeStartedLater` | Carried: `tui`, as one test of the frozen `Seq` over an append-only `state.Change.Log` |
| `TestKeyBarShowsOnlyKeysThatWork`, `TestHelpListsThisScreensKeys`, `TestHelpOmitsKeysThatDoNothingHere`, `TestQuitKeys`, `TestUpdateNeverCallsTheBackend`, `TestCtrlRRefreshesABackendThatCan`, `TestAnEmptyFleetSaysSoAndSurvivesEveryKey`, `TestNoRegionAnsweredSaysWhyNotEmpty`, `TestChangeKeysOnABusyClusterSayWhy`, `TestRollbackKeyOnlyWhenAvailable`, `TestUpgradeKeyOffersTheParts`, `TestUpgradeKeyOffersCatchUpMidway`, `TestPatchKeyRollsALaggingNodegroupToTheControlPlane`, `TestCatchUpAndCountTexts`, `TestUpgradeHeaderNamesALimitedRun` | Carried: `tui` (`ctrl+r` without the type assertion; `B` after a readiness run) |
| `TestOlderStateIsDropped`, `TestFetchIDsAreNeverReused`, `TestOnlyPollingRepliesScheduleATick` | Dropped: there is no fetch ID and no polling loop. `Next` returns versions in order; a test asserts one command per `stateMsg`. |
| `TestLogSourceKeysCycleBothWays` | Dropped: one log per change, no source key |
| `TestAddonUpdateHintDoesNotPromiseAWatchScreen` | Dropped: an add-on update has the Change screen |
| `TestSimulatorRefusesARollback`, `TestUnlockAllowsChangesAndRunsTheDryRunAgain`, `TestUnlockKeyOnlyWhereItCanWork`, `TestLateReadOnlyDryRunRunsAgainAfterUnlock`, `TestCursorStartsOnTheKubectlCluster` | Dropped: no simulator in the product, no unlock, no kubectl cursor (owner decisions) |
| **`tui/draw_test.go`**: all eight | Carried: `tui`, with the layout |
| **`tui/fleet_scale_test.go`**: `TestFleetTableKeepsTheSelectionInView`, `TestFleetCellsKeepAGap`, `TestFleetNodegroupsSayUnknown`, `TestBeforeTheFirstSweep`, `TestKeyBarKeepsWordsOnLetterKeys`, `TestNoColorLeavesTheBackgroundAlone` | Carried: `tui`, as they are |
| **`tui/journal_test.go`**: both | Dropped: no journal, no export (owner decision) |
| **`tui/kubeevents_test.go`**, **`tui/upgrade_hint_test.go`**: all four | Carried: `tui` (the hint of a phase comes from its `PhaseKind`) |
| **`tui/clipboard_test.go`**: `TestCopyUsesTheSystemClipboardTool` | Dropped: `c` sends OSC 52 and says "sent to the terminal"; a system tool is a later feature |
| **`tui/state/state_test.go`**: `TestVersions`, `TestVersionMove` | Phase 2: `fleet.Version` (`internal/fleet/version_test.go`) |
| `TestClusterHealth` | Carried: `backend`, as the test of `row` (status, busy wins, extended support, behind, stale AMI, stale add-on) |
| `TestRollReplacedAndUpgradeProgress` | Dropped: no estimate; the view shows the counts of the snapshot and the step states |
| **`tui/live/live_test.go`** | |
| `TestSweepBuildsTheFleet`, `TestRegionFailureIsReportedAndOthersStay`, `TestSecondSweepReportsChanges`, `TestStateHonorsCancel`, `TestReadinessRunsInTheBackgroundAndMapsTheReport`, `TestReadinessErrorIsAFailedCheck`, `TestNilReportIsAFailedCheck`, `TestPlansAreDryRunsWithTheCLICommand`, `TestChangesAreRefused`, `TestRunSweepsOnTicksAndRefresh`, `TestEveryRegionClosedIsAnErrorNotAnEmptyFleet`, `TestNoRegionAnsweredIsExplained`, `TestAClusterUpdatingOutsideTheTUIIsBusy`, `TestPlanHasADeadline`, `TestRollAgainstFakeAWS`, `TestFailedRollAgainstFakeAWS`, `TestAddonUpdateAgainstFakeAWS`, `TestFirstSweepOfABigFleetSummarizes`, `TestFirstSweepKeepsChangingClusters`, `TestFirstSweepShowsTheErrorUnderAChange`, `TestCopiedCommandsRunAsTheUI` | Carried: `backend` |
| `TestStateSharesNoMemory` | Carried: `change` (`View` is immutable) and `backend` (`State` is immutable) |
| `TestStartRefusesWhileEKSIsChangingTheCluster`, `TestChangesInProgressReadsLiveState` | Phase 4: `TestBusy`, `TestNewPlan_BusyTarget`, `TestRun_FreshReadBeforeSubmit`, `TestRoll_FreshDescribeBeforeEverySubmit` |
| `TestUnknownNewestVersionNeverReadsAsBehind`, `TestNewestVersionIsAskedInASweptRegion`, `TestTheVersionLookupFailsFastLikeTheSweep` | Phase 2: the sweep reads `DescribeClusterVersions` per region with the sweep's clients (`internal/fleet/sweep_test.go`); `row` uses `Latest` only when known |
| `TestAgainstFakeAWS` | Dropped: every backend test runs against the fake |
| `TestServiceLogsGoToThePaneNotTheTerminal` | Dropped: no pane; the `slog` handler is discarded (section 9), and a test of `ui` asserts that nothing reaches `ErrOut` during a run |
| `TestSameClusterKeepsItsIdentityWhenItsKeyChanges` | Dropped: the key never changes |
| `TestSweepNotesTheKubectlCluster`, `TestSweepWarnsWhenTheKubectlClusterIsMissing`, `TestSweepRefusesAKubectlClusterThatIsAnotherCluster`, `TestSweepWaitsForAnUnreadKubectlCluster` | Dropped: no kubectl cursor (owner decision) |
| `TestAllowChangesUnlocksTheSession` | Dropped: no unlock; `TestChangesAreRefused` covers the read-only side |
| **`tui/live/roll_test.go`** | |
| `TestRollStartsIsWatchedAndFinishes`, `TestFailedAndUnwatchedRolls`, `TestStartErrorReleasesTheCluster`, `TestNoKubeAccessStillRollsWithoutTheNodeView`, `TestStartNeedsTheDryRun`, `TestShownVersionIsTheDryRunsCluster`, `TestRollSnapshotNamesTheDrainingPods`, `TestChangesStartedElsewhereAreWatched`, `TestAdoptedRollResumesAndKeepsOthersClaims`, `TestAWatchedRollbackSaysRollback`, `TestAnAdoptedRollCountsFromItsStart`, `TestAResumedWatchKeepsTheVersionRoll`, `TestRollFeedShowsFailedNodeLaunches` | Carried: `backend` and the `Watch` tests of `change` |
| `TestHealthGateBlocksTheRoll`, `TestADrainBlockerStopsTheRoll`, `TestNewHealthFindingsSinceTheDryRunStopTheRoll` | Phase 4: `TestRoll_WholeBatchGatedBeforeFirstStart`, `TestRoll_FreshReadDecidesAgain`, `TestDriver_PDBTightensAfterTheDryRun`; phase 3: `TestWorse` |
| `TestLosingTheNodeViewSinceTheDryRunStopsTheRoll` | Phase 3 and 4: `health.Worse` counts a check that ran in the plan and is `Skipped` now as worse (phase 3, section 4), and `regate` asks or blocks on it (`TestRegate_FailuresAndQuestionBudget`). A backend test keeps the scenario: the kube server is closed between the proposal and the start, and the run asks. |
| `TestACurrentNodegroupDoesNotRoll`, `TestTheDecisionTableDecidesAtStart`, `TestUnknownAMIStatusCanRoll`, `TestARollWithNoRecordedVersionRefuses`, `TestADryRunWithoutASweepVersion` | Phase 4: `TestDecideAMIUpdate`, `TestRoll_NoVersionIsBlocked`, `TestRoll_PayloadEqualsPlan` |
| `TestVerificationRunsAfterASuccessfulRoll` | Phase 4: `TestRoll_Verification`; a backend test asserts that the view shows it |
| `TestUnofferedInstanceTypesWarnInTheDryRun` | Phase 4: `TestPlanScale_Availability` and the roll's availability notice |
| `TestClaimSurvivesAKeyChange` | Dropped: the key never changes |
| **`tui/live/upgrade_test.go`** | |
| `TestUpgradeRunsThroughTheOrchestrator`, `TestUpgradeNeedsItsDryRun`, `TestPauseHoldsAndStopEndsBeforeTheNextPhase`, `TestOneAnswerPerQuestion`, `TestAStopDoesNotHideAFailure`, `TestAStopBetweenPhasesKeepsTheFinishedPhaseDone`, `TestAnOlderDryRunCannotReplaceTheConfirmedOne`, `TestUpgradeScopeReachesThePlanner`, `TestUpgradeOfOneNodegroup`, `TestStartItemMarksTheRollBeforeDone` | Carried: the handle tests and `backend` |
| `TestHealthWarningsBeforeARollAreAsked`, `TestAChangedPlanIsAsked`, `TestBlockedRealPlanFailsWithoutRunning`, `TestDrainBlockersStopTheUpgradeBeforeARoll`, `TestSkippedChecksAloneAreNotAsked`, `TestUpgradeStopsWhenPostRollVerificationFindsIssues` | Phase 4: `TestDriver_AsksAgainBeforeThePhase`, `TestRun_Expect`, `TestRun_NoConsentSendsNothing`, `TestDriver_NodegroupDegradesBetweenPhases`, and the upgrade-engine tests of phase 4's build step 8 |
| `TestControlPlanePhaseShowsProgress` | Dropped: no estimate; the phase shows `Running` with its elapsed time and the update's status from `UpdatePolled` |
| `TestUnlockDuringADryRunKeepsItReadOnly` | Dropped: no unlock |
| **`tui/live/rollback_test.go`**: `TestReadinessShowsTheRollbackWindow`, `TestRollbackDryRunIsTheCLIPlan`, `TestBlockedRollbackDryRun`, `TestRollbackRunsThroughTheEngine` | Carried: `backend` |
| `TestRollbackStopsWhenPostRollVerificationFindsIssues` | Phase 4: the rollback-engine tests of build step 9 |
| **`tui/live/addons_test.go`**: `TestAddonDryRunIsTheServicePreview`, `TestAddonUpdateNeedsTheSameDryRun`, `TestAddonUpdateWhileRollingIsBusy` | Carried: `backend` |
| `TestAddonUpdateRunsInOrderPinnedToThePlan`, `TestAFailedAddonDoesNotStopTheOthers`, `TestAnAddonThatCannotBePreviewedBlocksTheUpdate` | Phase 4: `TestSortByDependency_*`, `TestRun_EventOrder`, `TestNewPlan_Unread` |
| **`sim/world_test.go`** | |
| `TestConcurrentStartsStartOneRoll` | Carried: `backend` (32 concurrent `Start` calls of one proposal) |
| `TestSnapshotSharesNoMemory`, `TestEventSeqIsIncreasingAndMatchesState`, `TestCancelledCallsChangeNothing`, `TestStateHonorsCancelWhileWaitingForTheLock` | Carried: the handle tests (`View` immutable, `Seq` monotonic) and `backend` (`Next` honours cancel) |
| `TestStopAfterCurrentNodegroup`, `TestPauseHoldsBeforeNextPhase` | Carried: the handle tests, through the real engine once built |
| `TestRollReplacesEveryNode`, `TestUpgradeRunsEveryPhase`, `TestAddonUpdate`, `TestScopedUpgradeMovesOnlyItsParts`, `TestCatchUpIsNotBlockedByNextVersionReadiness`, `TestUpgradeBlockedByReadiness`, `TestReadinessStreamsResults`, `TestWarmupShowsAnUpgradeInFlight`, `TestDeterministic`, `TestRunAdvancesPerTickAndStopsWithContext`, `TestSupportWindows` | Dropped: they tested the simulator's own state machine, which has no successor. The world's roll, update, and insights behaviour has its tests in `fleettest`. |

## 12. Performance budgets

| Measure | Budget | How it is measured |
|---|---|---|
| A frame of the Fleet screen at 200 clusters, 231×64 | 8 ms (`REWRITE.md`), and under 4,000 allocations | `BenchmarkFleetFrame` over a state of 200 rows; `testing.AllocsPerRun` in `TestFrameAllocations` gates the PR; `task bench` and the nightly compare the time with `benchstat` |
| A frame of the Change screen during a roll of 40 nodes | 8 ms, under 3,000 allocations | The same, with a `NodeProgress` of 40 nodes and a log of 200 entries |
| `backend.build` at 200 clusters and 5 handles | 50 µs, under 50 allocations | `BenchmarkBuild`; a test asserts the allocation bound |
| The handle's `Event` for `NodeProgress` | 2 µs, 1 allocation (the map value) | `BenchmarkHandleEvent` |
| Idle CPU, fleet loaded, nothing in flight | No work between sweeps: no tick, no build, no `Next` wake | `TestIdleSchedulesNoTick` asserts that `Update` on the last `stateMsg` of an idle fleet returns only `next`; a manual `pprof` over 60 s against `fakefleet -speed 0` shows no samples outside the sweep |
| Memory at 200 clusters, after two sweeps | Under 40 MB of heap after GC | `TestMemoryAfterTwoSweeps` reads `runtime.ReadMemStats` after `runtime.GC()` against a generated fleet; logged, with a gate at 60 MB |

The viewport rule makes the first two budgets hold: a screen renders the rows that fit, never the fleet, so a frame is O(rows on screen).

## 13. Build order

Each step ends with `task dev:full` green and is reviewable alone.

| # | Step | Proof |
|---|---|---|
| 1 | `internal/cmd/ui` with the five flags, `validate` (`-A` with `-r`, the interval minimum, a terminal), the stdio detach; `internal/tui` with `Block`, the key table, the top bar, the key bar, the help, and the Fleet screen; `internal/tui/state` and `internal/tui/backend` with the sweep loop, the feed, `Next`, `Refresh`. Read-only. | The exact-fill test for the Fleet screen; the draw tests; the sweep and feed tests; the shutdown test |
| 2 | The Cluster screen; `health.CheckReadiness` extracted and used by `cluster upgrade-check`; readiness in the backend with the rollback window | The command's tests unchanged; the readiness backend tests; the exact-fill test for the screen |
| 3 | The handle in `change` (`Start`, the log, questions, result); the proposal dialog; `backend.Plan` and `Start` with `Expect`, the consumed proposal, claims, `ErrReadOnly`; an add-on update end to end; the Change screen with the step list and the log | The handle tests but pause and stop; the add-on backend tests; the refusal tests |
| 4 | A roll: `Common.Nodes`, the node table and the node feed, the picker, the post-roll verification shown, the cluster re-read | The roll backend tests; the node-view exact-fill rows |
| 5 | Pause, stop, the phase list; an upgrade and a rollback end to end. Needs phase 4's checkpoint before every phase and serial step (section 14). | The remaining handle tests; the upgrade and rollback backend tests |
| 6 | `change.Watch` and adoption | The `Watch` tests; the adoption backend tests |
| 7 | `fakefleet`: the generator moved into `fleettest` with the scripted failures and short durations, the kube servers and the kubeconfig, the counted fault, `task run:tui:sim`; the end-to-end test | The end-to-end test; a manual session |
| 8 | Close-out: the benchmarks and gates, `docs/commands/ui.md` rewritten (it says that a change run from the UI leaves no record on disk, and names the CLI command for a run that must), `legacy/PORTING.md` rows from section 11 | The gates in CI |

## 14. What phase 4 must provide

The phase 4 agent is building the roll batch and the engines now. This list is final.

| # | Item | Why |
|---|---|---|
| 1 | The `Checkpoint` hook is called before every phase, the first included, after consent and before the first submit; and before each step of a serial phase after that phase's first step. In a fleet, before every cluster, the first included. The hook takes one `StepRef`: `Checkpoint func(ctx context.Context, next StepRef) (Checkpoint, error)`, where a zero `Kind` and `Name` mean the phase itself. A nil hook leaves the CLI unchanged. | Today it runs only before each phase after the first (phase 4 note line 929; `fleet.go:193` has `i > 0`). An upgrade sits in `Preparing` for up to 5 minutes during the insights refresh; a stop pressed there would be ignored and the control-plane update submitted. `S` also means "before the next nodegroup roll" of a serial phase. |
| 2 | `PhaseEnded` carries `Skipped`. | The phase list shows a skipped phase as skipped. The note has it; the code lists it as "arrives with its build step". |
| 3 | `observeNodes` takes an emit function in place of the `runner` receiver, and `inFlightUpdate` returns the update's `CreatedAt` and type beside its ID. | `change.Watch` reuses both without a runner. |
| 4 | `noderoll.Event` gains `At time.Time`, set from the clock of the observer loop; `NodeView` carries every event the tracker keeps (`Tracker.Recent(trackerEventCapacity)`, 256) in place of the last 8. | The node lifecycle feed of a roll in the TUI, with times. The CLI panel keeps showing the last 6. |
| 5 | `Kind` gains `ClusterUpgrade` and `ClusterRollback`, and `Request` gains `UpgradeRequest` and `RollbackRequest`, as the note has them, with `Part` as the type of `--only` values. | The TUI builds them from an `Action`. |

Nothing else. `ConfirmPhase` stays a CLI need that phase 4 already has in its note. The handle copies every `*StepResult` it is given, so `Run` may keep writing to its results slice.

## 15. Decisions and contract edits

### Decided by the owner on draft 1

| # | Decision |
|---|---|
| D1 | The "experimental" label stays on `refresh ui` for the cutover release. |
| D2 | The runtime unlock (`ctrl+u`) is dropped. |
| D3 | The session export (`e`) and the AWS call pane are dropped. The export is the one real loss: a change run from the TUI leaves no record on disk. `docs/commands/ui.md` says so and names the CLI command for a run that needs one. |
| D4 | The kubectl-cluster cursor is dropped. |
| D5 | One Change screen in place of Rolls and Upgrade. |
| D6 | The TUI does not ask before each phase. `P` and `S` are its hold and stop. (Draft 1 proposed the question; it was wrong about the old behaviour.) |
| D7 | The simulator is `tools/fakefleet` and the unchanged binary. No fake in the product, no switch, no import-rule exception. |
| D8 | A foreign add-on update marks the cluster busy from the sweep and is not watched. |
| D9 | The sweep interval is 1 minute with a 10 second minimum; `ui` exits 0 on quit and 1 when it cannot start. |

### Open

Nothing is open.

### Contract edits

1. In the command flags table, the `ui` row says what `--wait-timeout` bounds: "`--wait-timeout`: how long the UI follows a change it starts or watches. Unset, each kind of change gets the default of its command: 40m for a roll, 5m for each add-on, 4h for an upgrade or a rollback. `0` means no limit. The EKS update continues either way."
2. A row for `--interval`: "time between fleet sweeps; default `1m`, at least `10s`. `ctrl+r` sweeps now."
3. Under "Exit codes", one sentence: "`refresh ui` exits `0` when you quit and `1` when it cannot start: no terminal, no credentials, or no region. The result of a change shows in the UI, not in the exit code."

## 16. Changes made while building

The code is the reference where it and the sections above differ. This table covers build steps 1 and 2: the command, the read-only Fleet screen, the sweep backend, the Cluster screen, and the readiness run.

| Section | Note | Built |
|---|---|---|
| 1 | `Backend` has `Plan`, `Start`, `Answer`, `Pause`, `Resume`, `Stop` | The model's interface has `Next`, `Refresh`, and `Readiness`: what the two screens need. `backend.Live.Start` exists with the `ErrReadOnly` check first and `ErrNotBuilt` after it, so the one place that refuses a start exists and is tested before any change screen does. `state.ProposalID` exists for its signature. |
| 5 | `Cluster.Latest`, `Cluster.Change`, `Cluster.Rollback`, `State.Changes`, `state.Change`, `state.Action`, `state.Proposal` | Not built. The sweep does not read the newest version EKS offers, so a row has no `Latest`; the card shows the next hop as the minor above the version. The rest arrives with step 3. |
| 5 | The feed line of a sweep: "17 of 20 clusters need attention, 2 changing" | "20 clusters: 17 need attention, 2 changing, 1 incomplete". The arrow of a status change is `->`, so the feed reads the same without Unicode. |
| 6 | `Options.WaitTimeout` and `Set`, `limits(kind)` | Not built; `--wait-timeout` is registered, validated, and says that it has no effect yet. |
| 6 | `New(ctx, f, o)` | `New(ctx, provider, o)`: the command resolves the provider first, so a credential error prints before the alternate screen opens. `Run()` takes no context: the session's context is the one `New` got. `Readiness` of a cluster that the fleet does not have is `ErrUnknownCluster`. |
| 6 | `health.CheckReadiness` returns the readiness, the reasons, the checks, the insights, and the rollback window | It also returns the snapshot and the rollback insights, which `cluster upgrade-check`'s document needs. The command builds its document from the report. |
| 7 | Lip Gloss paints a `Seg` in `Line.Render` | No Lip Gloss in `tui`. A style per segment made a frame at 200 clusters 17,000 allocations; the theme now gives the SGR codes of a role once (`view.Theme.SGR`), a `painter` holds them, and `Render` writes them. A frame is 2,551 allocations and 0.57 ms. Bubble Tea still draws the program. |
| 7 | Two screens of three: Fleet and Cluster | Built. The Change screen comes with step 3. The `CHANGE` column of the fleet table waits for it. |
| 7 | ASCII terminals: borders, marks, bars, `...` for a cut | The cut marker is one cell in both sets (`…` and `~`), so a cut line keeps its width. The labels and texts that hold `·`, arrows, and `×` are replaced at render time, one ASCII cell each; the frame's cells are the same in both sets. |
| 7 | The model ticks while a change runs, a readiness runs, or a sweep runs | Built for a readiness run and a sweep. A tick never reschedules itself when nothing needs it; the test asserts it. |
| 9 | `slog.DiscardHandler` for the run | Nothing to do: the new tree sets no default `slog` handler. klog is routed by the root command already. The crash output goes to the saved descriptor with `debug.SetCrashOutput`. |
| 11 | The harness runs the command that every message returns | It skips the commands after a state and after a tick: the wait for the next state would block, and the tick would sleep a second and come back for ever while a sweep runs. |
