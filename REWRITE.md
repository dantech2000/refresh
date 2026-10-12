# refresh rewrite plan

Status: draft 8, final. Codex (`gpt-6.1-sol`) rejected drafts 1 and 2 and approved draft 3 with changes. This draft applies those changes. The owner decided on 2026-10-12 that the CLI may change where the change is logical, and that the new tree follows the golang-standards project layout and Google's Go best practices. Draft 5 applied those two references, and Codex approved it with changes. On the same day the owner asked for a conventional codebase that any Go developer can pick up, with no project-specific ceremony. Draft 7 removed the bespoke command layer and the bespoke rules. Codex approved it with changes, and draft 8 applies them. Plan review is closed; Codex reviews code from here. See "Review record" at the end.

## Goal

Build refresh again with the design we would choose now. The result is a conventional Go codebase that any Go developer can pick up: one job per package and no logic copied between front ends. Local work is fast and measured.

The rewrite is done when the new binary meets the v2 contract, passes the parity suite, and ships in one release, and the old tree is deleted.

## Rules

1. **The product does not change.** The loop stays: status, readiness, patch, upgrade. No new feature lands on the branch.
2. **The contract is redesigned once, in writing, before code.** Phase 0 produces `CONTRACT.md`: the command tree, flags, env vars, documents, and exit codes of the new CLI. The owner approves it. After approval it is frozen for the rest of the rewrite. Backwards compatibility with the old CLI is not a goal (owner, 2026-10-12).
3. **Safety behaviour is the specification.** What the old binary changes in AWS and Kubernetes, in what order, and when it refuses, is what the new binary does. A difference needs a written reason.
4. **New code cannot import old code.** The old tree moves to `legacy/`. Code enters the new tree only through a deliberate port.
5. **CI, release, and docs tooling stay at the root.** They are adapted, not rewritten.
6. **`main` keeps releasing the old tree.** A user-facing fix goes to `main` first. The rewrite branch merges `main` every week.
7. **Convention over invention.** The new tree looks like other well-known Go CLIs. It uses the standard layout, cobra as cobra is normally used, and the published Go style guides. The plan adds a rule of its own only where a past bug justifies it, and says which bug.
8. **Each phase starts with a design note and ends with a review.** The note specifies the phase's types and rules. `/code-review` and Codex review the note before code and the diff after.
9. **No changing command is built before phase 0 meets its exit criteria.** The phase 1 foundation (the cobra root, `cmdutil`, `apidoc`, `version`, contexts) may start once the contract is approved, because it depends on nothing in phase 0.

## Why the old design gets replaced

| Problem in the old tree | Evidence | Replacement |
|---|---|---|
| No shared domain model | 4 nodegroup structs, 4 add-on structs, 5 copies of "list then describe each", 5 copies of minor-version parsing, 2 add-on health enums | One `fleet.Snapshot` from one fetcher |
| The nodegroup roll is assembled three times | `commands/nodegroup/actions_update.go`, `services/upgrade/nodegroups.go`, `tui/live/roll.go`; the step order already differs | One `change.RollBatch` use case |
| Gate policy lives in the command layer | `healthProblems` and `healthGateProblems` differ only in a fallback; the TUI has a third copy | `health.Gate` returns a typed verdict |
| Health checks have no interface | "Could not read" has 5 representations; the verdict ignores the score | `Check` interface with a closed outcome set |
| Six poll loops for an EKS update | `DescribeUpdate` loops in 6 files; `monitoring` prints to stdout | Two silent wait strategies in `change` |
| Retry wrapped by hand at 75 call sites | All pass the default config; a 400-line AST guard test enforces it; it stacks on the SDK retryer | A retry policy per operation kind, inside the client |
| Every action repeats a safety-ordered preamble | About 11 action files; 5 prompt mechanisms | Cobra commands with a `Factory`, `IOStreams`, and options that `complete`, `validate`, `run` |
| Table and plain cells are written twice | Columns have drifted in two describe views | A column is defined once |
| Exit codes go through `cli.Exit` | urfave prints them unstyled and exits inside `Run` | A returned `ExitError`, unwrapped in `main` |
| Every argument is both a flag and a positional | 4 `Resolve*` variants, `DescribeTarget`, `PositionalSlot`, the `EKS_CLUSTER_NAME` special case | One rule in the v2 contract |
| Two AWS fakes and a second TUI state machine | `internal/mocks`, `fakeaws`, `internal/sim` | One fake world |

## The v2 contract

Phase 0 writes the full contract. These are the proposed changes from today's CLI. Everything not listed stays.

| Area | Today | Proposed |
|---|---|---|
| Targets | A cluster is a flag or a positional in every command | The positional names the thing the command acts on. `-c/--cluster` names the cluster for nodegroup and add-on commands. |
| Resource names | A cluster, nodegroup, or add-on name is also a substring pattern | Every resource name is exact. Patterns go through `-f/--filter` on list and fleet commands. A filter that does not parse is an error, never an empty match. |
| `EKS_CLUSTER_NAME` | Read by `nodegroup update` only | One env var for the cluster in every command. `CONTRACT.md` states the precedence of flag, env var, and context. Fleet mode ignores the env var, as it does today. |
| Documents | Each service has its own nodegroup and add-on shape | `refresh.drod.dev/v2`: one shape per resource, reused in every document |
| Health results | A number score; skipped is `Pass` plus a flag | Outcome is `Pass`, `Warn`, `Fail`, `Skipped`, or `Unreadable`. No score. |
| Add-on health | Two enums | One enum |
| Deprecated flags | Hidden aliases and a removed-shorthand table | None; the slate is clean |
| Help and completion | urfave layout and scripts | Cobra's layout and scripts |
| Describe views | Plain and table show different fields | Both derive from the same sections |

What does not change: the exit codes and their meanings, one document on stdout for machine formats, `[]` and `{}` in place of `null`, the `failures` list, stderr for everything human in machine runs, the flag letters, and "machine runs never prompt".

## What moves over

The behaviour and the tests of these are frozen. Their signatures are adapted to the code standards.

- `StartNodegroupRoll` and the `DecideAMIUpdate` table.
- The upgrade engine: phases, resume from live state, rollback planning.
- The pausable deadline.
- `regionsweep`, bounded fan-out, paging, `RunAlongside`.
- The failure model. Its document type goes to `apidoc`, and its classifier goes to `awsx`.
- Typed AWS error classification and `FormatAWSError`.
- `noderoll.Tracker` and the informer-with-fallback observer. The observer, 638 lines today, is split into smaller files.
- The theme, tokens, table, and `ErrorLines`, with the mutable palette global and fatih removed.
- The TUI `Block` layout, key-binding tables, and immutable `State`.
- Schema generation from Go types, the livecheck tests, and `fakefleet`.
- The pure evaluators in `health`.
- Signal handling, credential resolution without STS, and the context conflict error.

## Repository layout on the rewrite branch

One Go module, laid out by golang-standards/project-layout. The old tree sits under `legacy/` until cutover.

```
cmd/refresh/main.go     the binary; it builds the root command and runs it
internal/
  apidoc/               every v2 document type, the kind registry, enums, the failure type
  awsx/                 config load, Clients, retry policies, paging, error types, IAM table
  awsx/awsxtest/        HTTP server that answers the AWS APIs from a world
  fleet/                Snapshot, fetcher, regionsweep, version type, busy detection
  fleet/fleetdata/      support calendar, AMI SSM paths, add-on order, each with an "as of" date
  fleet/fleettest/      the world model and its clock
  health/               Check, Result, Gate, drain blockers
  change/               waits, RollBatch, add-on update, scale, upgrade engine, rollback, Operation
  noderoll/             observer, tracker, Attach
  kube/                 kube client resolution
  kube/kubetest/        Kubernetes fakes driven by a world
  cmd/root/             the root cobra command and the global flags
  cmd/cluster/          one package per command group, as gh and kubectl do; also
  cmd/nodegroup/        cmd/addon, cmd/status, cmd/contextcmd, cmd/version, cmd/ui
  cmdutil/              Factory, IOStreams, exit errors, output-format flags, target resolution
  view/                 theme, columns, human views, plain output
  tui/                  model, state, backend
api/schema/v2/          the generated JSON Schemas
test/parity/            the scenario runner, the scenarios, the goldens, the legacy traces
tools/genschema/        schema generator
tools/fakefleet/        fake fleet server for manual testing
docs/                   user documentation (MkDocs)
legacy/                 the old tree, deleted at cutover
```

How each project-layout directory is used:

| Directory | Use here |
|---|---|
| `/cmd` | `cmd/refresh`. `main` wires signals, runs the root command, and maps the error to an exit code. |
| `/internal` | All product code. The compiler keeps it private. |
| `/pkg` | Not used. Nothing is offered for import by other projects. |
| `/api` | The published JSON Schemas. The docs build publishes this directory. |
| `/test` | The parity suite: an external test app and its data. |
| `/tools` | `genschema` and `fakefleet`. They import `internal`. |
| `/docs` | As now. |
| `/build`, `/scripts` | Not used. See the deviations. |
| `/vendor`, `/web`, `/configs`, `/init`, `/deployments`, `/examples`, `/third_party`, `/githooks`, `/assets`, `/website` | Not used. The project has nothing for them. |

The layout makes every directory optional, so an unused directory is not a deviation. `internal/` is flat, without the optional `internal/app` and `internal/pkg`, because there is one binary. Two real deviations, with reasons:

- **CI and release config stay at the root** (`.github/`, `.goreleaser.yml`, `Taskfile.yml`, release-please, `codecov.yml`), not under `/build`. GitHub requires `.github/workflows`, and the owner asked to keep this tooling as it is.
- **`legacy/`** is not in the layout. It is temporary.

One effect on users: the install path becomes `go install github.com/dantech2000/refresh/cmd/refresh@latest`. The README and two docs pages change at cutover. Homebrew is not affected.

Go's `internal` rule stops the new tree from importing `legacy/internal/...`. depguard stops `legacy/` from importing the new `internal/...`. `./...` covers both trees.

### Phase 0 move

The exit check is authoritative: `task dev:full` is green on the branch with no test deleted or skipped. Known work:

| Item | Action |
|---|---|
| `main.go`, `main_*_test.go`, `goleak_test.go`, root `testdata/` | Move to `legacy/` |
| `internal/` | Move to `legacy/internal/` |
| Import paths | Rewrite `refresh/internal/` to `refresh/legacy/internal/` |
| Schema generator | The module root stays the repo root. Give the generator and `schemagen` a separate source root (`genschema/main.go:24,52`, `schemagen/comments.go:34,67`, `schemagen_test.go:191`). |
| Schema paths relative to a package | `commands/contract/schema_test.go:24`, `tools/livecheck/schema_test.go:21` |
| Tests that read root docs | `diag/reason_test.go:194` and `aws/awserr/permissions_test.go:14-20` read `docs/` from the repo root; the source scan at `permissions_test.go:75-76` stays scoped to `legacy/` |
| Retry guard test | Scope its scan and allowlist paths to `legacy/` (`common/awscall_guard_test.go:31,64,304`) |
| Other source-walking tests | Scope each to `legacy/`: layering, factory, render guard, flag canon |
| Taskfile | `build`, the release build (`:39`), `docs:gen`, livecheck, integration, and bench paths (`:119,144,149`) target `legacy/`; add `build:next` for `./cmd/refresh` |
| `-X` version symbols | Taskfile and GoReleaser point at `legacy/internal/commands` until cutover |
| GoReleaser `main:` | `./legacy` until cutover |
| Workflows | Add the `rewrite` branch to the lint and test triggers; fix the docs-generation commands (`test.yml:56-57`) |
| `.golangci.yml` | Current rules apply to `legacy/`; the new tree gets the configuration in "Code standards" |
| Codecov | A `legacy` flag and a `next` flag, with separate uploads; no ratchet on `next` until phase 2 |
| Dependabot, release-please, release, docs, and auto-merge workflows | No change |

### Dependency direction

`apidoc` and `awsx` import no other refresh package. `fleet` imports them. `health` and `change` import `fleet`. `cmd/...`, `view`, and `tui` import everything below them. A `...test` package is imported only by tests, `test/`, and `tools/`. depguard enforces this.

## Code standards

The new tree follows the published Go guides. A Go developer who knows them needs no project-specific onboarding.

1. [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments).
2. [Google's Go style guide](https://google.github.io/styleguide/go/), including its [best practices](https://google.github.io/styleguide/go/best-practices.html).
3. The layout in [golang-standards/project-layout](https://github.com/golang-standards/project-layout). It is community guidance, not an official standard, and it agrees with Go's own [module layout guide](https://go.dev/doc/modules/layout) for a command with supporting packages.

Tooling is the common set: `gofmt`, `goimports`, `go vet`, and golangci-lint with its standard linters plus `errorlint`, `revive`, `gocritic`, `misspell`, and `unparam`. No custom linter and no size limits enforced by a script.

The points from those guides that matter most here, because the old tree broke them:

- **Packages are named for what they provide.** No `common`, `types`, or `helpers`.
- **Interfaces are small and defined by the consumer**, and only where there is a real need.
- **Tests use real clients against a fake server** where the dependency is HTTP. A small seam stays where HTTP cannot reach, such as the clock. Shared test doubles live in `<pkg>test` packages.
- **No package-level mutable state.** Dependencies are passed in.
- **Errors that callers must tell apart are typed, and wrapped with `%w`.** Callers use `errors.Is` and `errors.As`, never string matching.
- **Options go in a struct** when a function has many of them. A simple function keeps a simple signature.
- **Table-driven tests** with the standard library and `go-cmp`.

Three rules of this project's own, each from a past bug:

| Rule | Reason |
|---|---|
| Terminal output goes through `IOStreams`, from `main`, the command packages, `cmdutil`, `view`, and `tui` only | `monitoring` and `dryrun` printed from below, so the TUI needed a `Quiet` flag and machine output could be corrupted |
| A status or outcome is a named type, never a parsed string | The TUI matched engine phases by label prefix |
| A package that starts a goroutine has a goleak check | Leaks were found this way before |

## Performance

The network sets most of the wall time. The plan controls the number of calls, the local work, and how soon the user sees output.

### Baseline, old binary, 2026-10-12, Apple laptop, `-trimpath -s -w`, 10 runs

| Measure | Value |
|---|---|
| `--help`, `current`, `cluster list --help` | 14 ms |
| `version`, stdout piped | 13 ms. Higher earlier readings were measurement noise; the skip path is one env read and one TTY check. |
| Package init, 335 packages | 4 ms |
| Binary, stripped | 94 MB |

### Gates

Deterministic gates run on every PR:

- **Request count and peak concurrency** per scenario, read from the fake world.
- **Allocations** for the table renderer, the snapshot derivations, and the TUI view (`testing.AllocsPerRun`).
- **Binary size**: no larger than the old binary.

Timing gates run nightly and in `task bench`, not on PRs, because shared runners are noisy. They compare paired, repeated, non-race benchmarks with `benchstat` and fail on a significant regression above 20 percent.

Targets for the timing gates: 20 ms cold start for a command with no network call, 100 ms to first visible output, 8 ms for a TUI frame at 200 clusters, and a `status -A` local overhead that phase 2 sets from its first measurement.

### Design choices

- One fetch per cluster per command. Every derived view reads the same `Snapshot`.
- An exact cluster name resolves with one describe call (a v2 contract change).
- Serial describe loops (`cluster/insights.go:361`, `cluster/busy.go:64`) become bounded fan-out.
- A per-run memo for latest add-on versions, AMI SSM values, and the support calendar. Its key includes the account, region, and version. A failed read is not cached.
- Clients built once per region, on one shared HTTP transport.
- One retry layer.
- On a TTY, a sweep redraws the table as regions answer, always in the final sort order. Machine and plain output print once at the end.
- A command that needs no AWS config or kube client does not build one.
- The TUI backend publishes a versioned snapshot on change. A clock tick drives the elapsed-time displays. The model does not deep-copy the state every 100 ms under the backend lock (`tui/live/live.go:721-753`).
- Known local hot spots get benchmarks: the PDB selector cross-product (`health/pdbs.go:350-358`) and repeated grapheme-width math (`tui/draw.go:47-65`).

## Key designs

Each section gives the shape. The phase design note gives the full rules listed under "Design note must specify".

### Clients, retry, and deadlines (`awsx`)

- `awsx.Clients` holds every service client for one region, built once and passed in as a concrete value.
- One retry layer, the SDK retryer, 5 attempts, full jitter, 5 s cap. This is an intended change: the old stack could make up to 15 requests for one call.

| Policy | Used for | Behaviour |
|---|---|---|
| `Read` | Describe and list calls | 5 attempts |
| `Sweep` | Every call inside a default sweep's region callback | Short dial timeout; a dial failure is not retried |
| `Poll` | Calls inside a wait loop | 1 attempt; the loop continues through transient errors and stops on a permanent one |
| `Mutate` | Update calls | 5 attempts; one idempotency token for all of them |

- Deadlines keep today's levels: the aggregate budget from `--timeout`, the wait deadline, and credential setup nested inside the operation deadline. Zero and negative values keep their meaning. The pausable deadline stays.

Design note must specify: the error classification table, the dial scope, and the token lifetime.

### Domain model (`fleet`)

- `fleet.Snapshot` holds one cluster, its nodegroups, its add-ons, the latest versions, and failures.
- `fleet.Fetch(ctx, clients, cluster, Need)` reads what `Need` asks for. `Need` values expand their own dependencies.
- Each field group has a provenance: collected, partial, not requested, not attempted, or failed. Each item can carry its own failure. Unknown is never the zero value. A cancelled sweep keeps its unevaluated rows as not attempted.
- A snapshot records its source and time. A mutation re-reads what it is about to change.
- Kubernetes data is a second source with the same provenance rule.

### Health (`health`)

```go
type Check interface {
    Name() string
    Run(ctx context.Context, in Input) Result
}
// Result: Outcome (Pass, Warn, Fail, Skipped, Unreadable), message, details, failures.
```

- `Input` carries the snapshot, the targets, and the optional kube and metrics clients. No setters.
- `health.Gate(results, policy)` returns `Go`, `Ask`, or `Block` with reasons. A front end supplies only the confirm function.
- A drain blocker is one check. The policy says whether `--force` overrides it.

### Changes (`change`)

**Waits.** Two silent strategies that emit typed events. `WaitUpdate` follows an update ID. `WaitSettled` follows a resource to a postcondition when no update ID exists.

Design note must specify the success predicate and terminal-error table per resource. Known cases: an add-on is done at `ACTIVE` with the requested version; a control plane at `ACTIVE` with the target version; a nodegroup is settled in some non-`ACTIVE` terminal states so the next gate can judge it.

**Rolls.** `RollBatch` keeps today's semantics for one cluster: plan every target, gate the whole batch before any start, start sequentially, wait together, verify against one pre-roll baseline. The fleet form runs batches one cluster after another, each with its own budget. `cluster upgrade` runs a batch of one per nodegroup step.

Design note must specify: continue after a submit failure, a missing update ID, not-attempted rows, when verification is suppressed, and the aggregation of mixed outcomes into one exit code.

**Operations.** A dry run returns a `Plan` and never an `Operation`. `Start(ctx, plan)` returns an `Operation` at once. Everything after that is in the lifecycle, including read calls: the busy check, the insights refresh, and plan revalidation. The refusal boundary is "before any update is submitted". A refusal is a `Finished` operation with a `Refused` result and no accepted row.

An operation has a state. Each phase inside it has its own state.

| Operation state | Meaning | Next |
|---|---|---|
| `Preparing` | Re-reading live state, revalidating the plan, running gates | `Running`, `Asking`, `Finished` |
| `Running` | A phase is submitting or waiting | `Paused`, `Asking`, `Finished` |
| `Paused` | Holding between phases | `Running`, `Finished` |
| `Asking` | A question is open | `Preparing`, `Running`, `Paused`, `Finished` |
| `Finished` | Terminal, with one result | none |

Phase states: `Pending`, `Submitting`, `Waiting`, `Verifying`, `Done`, `Failed`, `Skipped`. An upgrade moves through `Submitting` once per phase, after the phase before it is done and confirmed.

- When the plan changed since the dry run, the operation asks, as the TUI does today for changed upgrade steps. A changed nodegroup version still refuses.
- Acceptance is per phase. `Accepted(phase)` returns one row per resource: the target, the update ID, or the error. `--no-wait` is allowed only for an operation whose submissions need no earlier wait or question. That is a single batch.
- Events go to an append-only log inside the operation. A subscriber reads from a cursor, so a slow or absent reader never blocks the executor. Progress events for one resource coalesce to the latest. Lifecycle events and questions are never dropped. The log closes at `Finished`.
- A question is registered before its event is published. `Answer(id, value)` consumes the question atomically: exactly one answer wins and a second gets a stale error. Unattended runs answer through the same call from policy.
- Pause intent and stop intent are stored apart from the question. Answering while paused leaves the operation paused.
- Precedence: cancel, then stop, then pause, then answer. Cancel and stop wake every waiter on a question or a pause.
- `Result()` blocks until `Finished` and can be called again.
- The context given to `Start` owns the run. Cancelling it is an interrupt. `Stop()` means stop after the current phase.
- At `Finished` the operation releases its claim, stops its observers, and closes its waiters.
- `Controls()` lists what the caller may do. `change.Observe(resource)` returns an operation for a change started elsewhere, with no controls. An upgrade that finds in-flight work is a normal operation with controls: it waits for that work as a phase, then submits the rest.
- Cluster claims and plan revalidation live in `change`.

Design note must specify: the full transition table for both state levels, and a race test for each interleaving of answer, pause, stop, and cancel.

### Commands (`internal/cmd`, `internal/cmdutil`)

Cobra is used directly, in the pattern that `gh` and `kubectl` use. Nothing wraps it.

```go
// internal/cmd/nodegroup/update.go
type updateOptions struct {
    io      *cmdutil.IOStreams
    clients func(ctx context.Context) (*awsx.Clients, error)

    cluster    string
    nodegroups []string
    dryRun     bool
    yes        bool
}

func newCmdUpdate(f *cmdutil.Factory) *cobra.Command {
    o := &updateOptions{io: f.IOStreams, clients: f.Clients}
    cmd := &cobra.Command{
        Use:   "update [nodegroup...]",
        Short: "Roll nodegroups to the latest AMI",
        RunE: func(cmd *cobra.Command, args []string) error {
            if err := o.complete(cmd, args); err != nil {
                return err
            }
            if err := o.validate(); err != nil {
                return err
            }
            return o.run(cmd.Context())
        },
    }
    cmdutil.AddClusterFlag(cmd, &o.cluster)
    cmdutil.AddMutationFlags(cmd, &o.dryRun, &o.yes)
    return cmd
}
```

- **`Factory`** gives a command its lazy dependencies: IO streams, AWS clients, the kube client, the context store. Tests build a factory that points at the fake server.
- **`IOStreams`** holds `In`, `Out`, `ErrOut`, whether each is a TTY, and the color setting. It is the only way a command reaches the terminal.
- **Options with `complete`, `validate`, `run`.** `complete` fills options from flags, args, env, and context. `validate` rejects bad combinations before any AWS call. `run` does the work.
- **Shared flag helpers** in `cmdutil` add the same flag the same way everywhere: `-c`, `-o`, `-r`, `-d`, `-y`, `--timeout`.
- **Output.** `cmdutil.Print(io, format, doc, view)` writes JSON or YAML from the document, or the table or plain view.
- **Exit codes.** A command returns a `cmdutil.ExitError{Code, Err}`. `main` unwraps it with `errors.As`. `SilenceErrors` and `SilenceUsage` are set on the root, so `main` prints every error once.
- **Unattended check.** `cmdutil.RequireYes(io, format, yes, dryRun)` returns the `--yes` error when a mutation would need a prompt it cannot show: a machine format, or no TTY. `--yes` and `--dry-run` are exempt. It reads no input.
- **Prompts.** `cmdutil.Confirm(ctx, io, question)` asks on stderr. It honours cancellation and pauses the operation deadline while it waits.
- **Cobra wiring.** The root sets `SetIn`, `SetOut`, and `SetErr` from the streams, and `main` calls `ExecuteContext` with the signal context. `ExitError` implements `Unwrap`. An exit with a code and no message prints nothing.
- **Completion and docs** come from cobra: its completion command, `ValidArgsFunction` for live cluster, nodegroup, and add-on names, and `cobra/doc` for the reference pages and man pages.

Every command runs in this order, and nothing before step 4 touches the network:

1. `complete`: fill options from flags, args, env, and the refresh context.
2. `validate`: reject bad input.
3. `cmdutil.RequireYes`, for a mutating command.
4. Build clients and config, lazily, from the factory.
5. `run`.

A mutating command resolves its target the mutation-safe way even with `--dry-run`: flag, env, and refresh context in the order `CONTRACT.md` gives, exact names, no kubeconfig fallback, and no list hint.

One test enforces this. It holds its own list of the mutating commands, builds a fresh root and factory for each with buffered streams and a fake server, runs the command, and asserts:

- Every mutating command is on the list and has the shared mutation flags.
- Invalid input fails before the `--yes` error, with no client built, no request sent, and no read from stdin.
- A valid unattended mutation without `--yes` or `--dry-run` returns the `--yes` error, with the same zero counts.
- `--yes` runs the change. `--dry-run` reads but submits no mutation.
- JSON and YAML runs never read stdin, on a simulated TTY too, and during later questions.
- A run that produces a document writes exactly one, for success, blocked, and partial results. An error before any result leaves stdout empty.
- With no authorized target and a kubeconfig that points at `prod`, a real run and a dry run both refuse.

### Views (`view`)

- A list column is `{Title, Plain(row), Human(row), MinWidth, MaxWidth, Align, Truncate}`.
- A describe view is a list of sections built from the document. Plain derives `FIELD`/`VALUE` rows from the same sections.
- One color decision per writer. No pterm, no fatih.

### The fake world (`fleettest`, `awsxtest`, `kubetest`)

- `fleettest.World` is one world model: clusters, nodegroups, add-ons, nodes, pods, and updates that progress on the world's clock. It replaces `internal/sim`, `internal/mocks`, and the instant state changes in `fakeaws`.
- `awsxtest` serves the AWS APIs over HTTP from a world, as `fakeaws` does. Tests and both binaries reach it with real SDK clients.
- `kubetest` has two forms. In-process tests use client-go fake clientsets driven by the world. Subprocess scenarios that need informers use a pinned envtest API server that the world writes to, plus a metrics stub. A phase 0 spike confirms this choice against a small hand-written list and watch server.
- World time is virtual and can run fast. Process deadlines use real time. The runner normalizes timestamps in output.

### Tests

1. Table tests for pure decision functions.
2. Whole-command tests through the fake world: stdout, stderr, exit code, request count.
3. Lifecycle tests for `change`, `noderoll`, and `tui`: concurrency, cancellation, attach, pause and stop, with `-race`, goleak, and handshakes in place of sleeps.

Ported: the schema, enum, IAM-table, exit-code-help, flag-letter, fuzz, and livecheck tests. Not ported: guard tests that an API shape replaces.

## Parity suite

The contract changes, so byte comparison against the old binary is not the check. Two checks replace it.

1. **Mutation parity.** A scenario pairs a legacy command line with its v2 equivalent on the same world. The mutating requests to AWS and Kubernetes must match in target, payload, order, and count. The exit code must match. A difference goes in an allowlist with a reason, and the owner approves each entry.
2. **Contract goldens.** Each v2 command and format has reviewed golden output on fixed worlds. Machine documents validate against the v2 schemas.
3. **Moving-world scenarios.** Mutation parity on a fixed world cannot show that a gate ran on fresh state. These scenarios change the world between the preview, the confirmation, the submission, and the phases. They assert that each prerequisite settled before the next submit, that the gate and verification reads happened, and what the final world state is.

The runner starts each binary as a subprocess, with a PTY where a scenario needs prompts or signals. Each run gets a fresh world, an isolated `HOME`, and deterministic update IDs. Volatile fields are normalized by a stated list. Idempotency tokens are renamed by first occurrence, so equal tokens stay equal and different tokens stay different. A lost-response scenario asserts that a retried call makes one logical update.

`legacy/PORTING.md` triages every legacy test file as ported, covered by a scenario, or dropped with a reason. A test that names a past regression is never dropped.

## Phases

### Phase 0: branch, move, contract, baseline

1. Create branch `rewrite`. Do the move. `task dev:full` is green.
2. Write `CONTRACT.md`. The owner approves it.
3. Settle the drift table. Fix those bugs on `main` and merge, so legacy mutation behaviour is the right reference.
4. Port `fakeaws` into `fleettest` and `awsxtest` as the fake world. Today it serves EKS, STS, SSM, and Auto Scaling activities; most other calls return 400. Write an operation coverage matrix for every AWS call the product makes: EKS, STS, EC2, Auto Scaling, SSM, CloudWatch, and Service Quotas. For each, prove a success response, access denied, throttling, paging, a delayed response, and clean handler exit after cancellation. Run the Kubernetes spike.
5. Build the runner in `test/parity`. Record the legacy mutation traces for every mutating scenario.

Exit: legacy is green; the contract is approved; the coverage matrix is complete; every mutating command has scenarios for success, dry run, declined prompt, blocked gate, failed update, failed verification, resume, and interrupt.

**Checkpoint.** Review the cost so far before phase 1. The fake world, the scenarios, and the bug fixes serve the old tree even if the rewrite stops here.

### Phase 1: foundation

- `cmd/refresh`, `cmd/root`, `cmdutil`, `apidoc`, `awsx`, `kube`, `view` core, `tools/genschema` writing `api/schema/v2`, the lint configuration.
- Commands: `version` with the update check, `context`, `use`, `current`, `completion`, `install-man`, `gen-docs`.
- Exit: goldens pass; the lint configuration is in place; the command-order test passes; the deterministic gates run in CI.

### Phase 2: read side

- `fleet.Snapshot`, the fetcher, the sweep, the data tables, watch mode.
- Commands: `cluster list`, `cluster describe`, `nodegroup list`, `nodegroup describe`, `addon list`, `addon describe`, `status`.
- Exit: goldens in all formats; request-count assertions; `task test:live` passes for the read commands.

### Phase 3: health

- `Check`, the checks, `Gate`, drain blockers.
- Commands: `cluster upgrade-check`, the `--health-only` paths.
- Exit: goldens; a table test for each outcome of each check; `task test:live` passes.

### Phase 4: changes

In this order, each with mutation parity before the next:

1. The waits and `Operation`, then `addon update`.
2. `nodegroup scale`.
3. `RollBatch`, then `nodegroup update`, then the fleet form.
4. The upgrade engine, then `cluster upgrade` and `cluster rollback`.

Exit: mutation parity for every scenario from phase 0.

### Phase 5: TUI

- State, backend, model. The simulator switch runs the real backend on the fake world.
- Exit: the exact-fill screen tests pass; scripted runs complete a roll, an add-on update, an upgrade, a rollback, and an adopted change.

### Phase 6: cutover

- Rewrite the hand-written docs and `CLAUDE.md`. Update the Taskfile, the workflows, and GoReleaser (`main: ./cmd/refresh`). Update the `go install` path in the README and the docs. `mkdocs build --strict` passes.
- Cutover checklist:
  - Every build path in the Taskfile, the workflows, and GoReleaser targets `./cmd/refresh` and `./tools/...`.
  - The three `-X` symbols (`version`, `commit`, `buildDate`) name the new package. A release-style build prints all three, and the update hint works.
  - MkDocs publishes `api/schema`, and the docs freshness check covers `docs/reference` and `api/schema`.
  - `completion` and `install-man` work from the release binary.
- Run `task test:live` and a manual upgrade on a test cluster.
- Delete `legacy/`. Run `go mod tidy`. Merge to `main`. Release with a migration note that lists every contract change.

## Drift table to settle in phase 0

| Item | Proposed behaviour |
|---|---|
| `cluster upgrade` runs no post-roll verify | Run it after each nodegroup roll, including attached rolls. Issues exit 5. An unreadable verification exits 4. |
| `cluster upgrade` runs no busy precheck | Define which in-flight change types each phase accepts. Refuse the others. |
| A failed critical-pod list reads as "No critical pods found" | `Unreadable`, with a failure entry. A partial read is recorded too. |
| `DEGRADED` add-on maps two ways | One mapping: a failure. A degraded add-on can still be updated to repair it. |
| Unreadable PDBs: overridable in one command, hard error in the other | Block in both. `--force` overrides in both, with the warning kept. |
| `PromptContinueWithWarnings` writes to stdout | stderr, `[y/N]` |
| `fleetExit` takes the numeric maximum | Keep the order, stated as a table, with a test for each mixed result |
| Plain and table differ in describe views | Settled by the v2 view rule |

## Decisions for the owner

1. **`CONTRACT.md`** in phase 0. This is the main one.
2. **The drift table.** Default: the proposed behaviour.
3. **`-o tree`.** Default: keep it, drawn with the same tokens as the table.
4. **Version at cutover.** Default: the next minor with a breaking-change note. 1.0 is the alternative.
5. **Disk cache** for slow-changing data. Default: none; decide after the phase 2 measurements.

## Risks

| Risk | Control |
|---|---|
| The old tree holds 109 bug fixes, and a rewrite loses the ones nobody remembers | Mutation parity, the `PORTING.md` triage, and behaviour-frozen ports |
| The rewrite costs more than incremental extraction would | The phase 0 checkpoint |
| The branch drifts from `main` | Merge weekly; only fixes and dependency bumps land on `main` |
| The fake hides a real AWS difference | `task test:live` at the end of phases 2, 3, and 6 |
| An abstraction does not fit a hard case | Each phase design note is reviewed before code; phase 4 ports the hardest command of each kind first |
| Two contract changes interact badly | The contract is written and approved as one document before any command exists |

## Review record

| Review | Verdict | Main findings | Answer |
|---|---|---|---|
| Codex, draft 1 | REJECT | Nested module move incomplete; retry and deadline changes; concurrent rolls break batch semantics; one wait loop is not enough; hooks are not enough for the TUI; conformance harness too thin | One module; retry policies; `RollBatch`; two waits; `Operation`; fake world and subprocess runner |
| Codex, draft 2 | REJECT | Move manifest gaps; `Operation` protocol undefined; fake clock and Kubernetes server; retry numbers; batch and wait outcomes; exact-name and streaming against a frozen contract; provenance; "unchanged" ports against the standards; flaky timing gates; `Mutates` mixed with target policy | Manifest extended and the exit check made authoritative; `Operation` state machine, event log, and question IDs; envtest and a spike; numbers stated; design notes carry the per-resource tables; the contract is redesigned in v2; provenance values; ports are behaviour-frozen with adapted signatures; deterministic PR gates and nightly timing; `NeedsYes` split from `TargetPolicy` |
| Codex, draft 3 | APPROVE WITH CHANGES | No blocker to starting phase 0. `Operation` assumed all submits come first; question ownership and stop precedence undefined; refusal boundary wrong for upgrades; two tests missing from the move table; parity misses temporal safety; token normalization; exactness for every resource name | Phase states and per-phase acceptance; atomic answers and precedence; `Preparing` state and the "before any update is submitted" boundary; move table rows; moving-world scenarios; token renaming; the v2 table |
| Codex, draft 5 (layout and standards only) | APPROVE WITH CHANGES | Some test seams cannot go through HTTP; the fake's service coverage was understated; the standards table overstated the Google guide; the linters enforce less than claimed; cutover wiring needs checks; only two layout deviations are real | Transport-first testing with justified seams; a coverage matrix in phase 0; qualified rules with project policy labelled; honest enforcement column and a config in the phase 1 note; a cutover checklist; two deviations |
| Codex, draft 7 (commands and standards only) | APPROVE WITH CHANGES | The cobra pattern is idiomatic. Flag registration alone does not enforce the safety order; dry-run target safety unstated; the prompt helper lacked context and format; cobra IO and context wiring unstated; leftover wording from removed rules | A five-step order with a command-running test; mutation-safe resolution stated; `RequireYes` split from `Confirm(ctx, ...)`; root wiring stated; wording reconciled |
| Codex, drafts 1 and 2 | Prefer incremental extraction on `main` | | Not adopted. The owner chose a rewrite. A checkpoint follows phase 0. |
