# CLAUDE.md

Guidance for AI agents and for people who work in this repo.

## What this is

`refresh` is a Go CLI that keeps Amazon EKS clusters up to date. The product is
one loop: `refresh status` (what is out of date in the fleet), `cluster
upgrade-check` (is the cluster ready), `nodegroup update` and `addon update`
(patch, behind a health gate), and `cluster upgrade` (the whole upgrade, in
order). `cluster rollback` undoes an upgrade. The rest is reads (`cluster
list/describe`, `nodegroup list/describe`, `addon list/describe`), `nodegroup
scale`, contexts, and an experimental TUI (`refresh ui`).

Features that compete with eksctl, k9s, or Kubecost were removed on purpose:
cost estimation, utilization tables, `cluster diff`, a standalone PDB command.
Do not add them again.

`CONTRACT.md` is the public interface: commands, flags, env vars, documents,
exit codes. A change to any of them is a contract change. Do not make one
without the owner's sign-off.

Module path: `github.com/dantech2000/refresh`. The binary is `cmd/refresh`.

## Build, test, lint

Use the Taskfile targets.

```bash
task build          # go build -o dist/refresh ./cmd/refresh (CGO_ENABLED=0)
task test           # go test ./...
task test:race      # go test -race -shuffle=on -count=2 ./...  (COUNT=n to change)
task lint           # golangci-lint at the CI-pinned version (config: .golangci.yml)
task vet            # go vet ./...
task vuln           # govulncheck ./...
task deadcode       # fail on code that nothing reaches, tests included
task tidy:check     # go mod tidy -diff
task docs:check     # regenerate docs/reference and api/schema, fail if either changed
task docs:build     # the published site: v2, v1, schemas, redirects (tools/mkdocs/build_site.py)
task docs:build:current  # mkdocs build --strict, current docs only
task fuzz           # every FuzzXxx target for FUZZTIME (default 30s)
task test:parity    # the parity suite
task test:live      # read-only checks against a real account (AWS_PROFILE): AMI paths, calendar, add-on versions, regions, offerings, schemas
task run:sim -- status -A   # the binary against a fake fleet, no AWS account
task run:tui:sim    # the TUI against the fake fleet
task dev:full       # everything CI runs; run it before you push
```

CI runs the same checks on every PR: tidy, the docs freshness check, `go test
-race` with coverage, a shuffled `-race -count=5` run, golangci-lint,
govulncheck, and deadcode. A nightly workflow runs `-race -count=20`, every
fuzz target for 60s, and govulncheck.

**Toolchain.** The Taskfile sets `GOTOOLCHAIN` to the `toolchain` line of
`go.mod`, which is the Go that CI uses. A newer local Go formats differently.
The versions of golangci-lint, govulncheck, and deadcode are pinned in the
Taskfile `vars` and must match the workflows.

**The fake fleet.** `tools/fakefleet` serves a fake AWS and a fake Kubernetes
API from one world: many clusters in many regions, a slow region, a closed
region, throttled calls, and clusters in named situations (a PDB that blocks a
drain, a degraded add-on, a roll that fails). `task run:sim -- <args>` starts
it, runs the binary against it, and prints which cluster shows what. Use it to
see real output with no account. `FAKEFLEET_FLAGS="-speed 60"` makes the
clock of the world run faster, so a roll ends in seconds.

## Layout

The standard Go project layout. All product code is under `internal/`.

```
cmd/refresh/            main: signals, the root command, the exit code
internal/
  apidoc/               every document type, the kind registry, enums, Failure
  awsx/                 config load, Clients, retry policies, paging, error classes, the IAM table
  awsx/awsxtest/        an HTTP server that answers the AWS APIs from a world
  fleet/                Snapshot, the fetcher, the region sweep, Version
  fleet/fleetdata/      facts that refresh ships with: support calendar, AMI SSM paths, regions
  fleet/fleettest/      the world model and its clock
  health/               the checks, Gate, drain blockers, post-roll verification
  change/               plans, waits, rolls, add-on update, scale, the upgrade and rollback engines
  noderoll/             the node observer and tracker of a roll
  kube/                 kube client resolution
  kube/kubetest/        Kubernetes fakes driven by a world
  fanout/               bounded fan-out and a per-run memo
  exitcode/             the six exit codes
  contexts/             the saved contexts (context.yaml)
  updatecheck/          the release check of `refresh version`
  build/                version, commit, build date (set with -X)
  cmdutil/              Factory, IOStreams, ExitError, flag helpers, target resolution, Print
  cmd/root/             the root command and the global flags
  cmd/<group>/          one package per command group: cluster, nodegroup, addon, status,
                        contextcmd, version, ui, gendocs
  cmd/changecmd/        what the five changing commands share: flags, renderers, the run loop
  cmd/fleetcmd/         what the sweep commands share: regions, progress, kube access
  cmd/cmdtest/          runs a whole command against the fake AWS, for tests
  view/                 theme, list columns, describe sections, plain output, the live region
  tui/, tui/backend/    the Bubble Tea model, and the backend that feeds it
api/schema/v2/          the generated JSON Schemas; the docs site publishes them
test/parity/            the parity suite
tools/genschema/        writes api/schema/v2 from the Go types
tools/fakefleet/        the fake fleet server
tools/mkdocs/           the versioned site build, the v1 banner, the schema hook
docs/                   user documentation (Material for MkDocs); docs/reference is generated
```

### Dependency direction

`apidoc` and `awsx` import no other refresh package. `fleet` imports them.
`health` and `change` import `fleet`. `cmd/...`, `cmdutil`, `view`, and `tui`
are on top. Nothing below them reads a flag or writes to a terminal.
`exitcode` and `fanout` import nothing of refresh. A `...test` package is
imported only by tests, `test/`, and `tools/`. An `imports_test.go` in each of
`apidoc`, `awsx`, `fleet`, `health`, `change`, `noderoll`, `cmdutil`, `tui`,
and `tui/backend` enforces this. If one fails, move the code. Do not edit the
test to allow the import.

## Code standards

The code follows the published Go guides: Effective Go, Go Code Review
Comments, and Google's Go style guide. Do not add project-specific
abstractions or rules. `REWRITE.md` ("Code standards") has the reasoning.

- Packages are named for what they provide. No `common`, `types`, `helpers`.
- Interfaces are small and defined by the consumer, only where needed.
- No package-level mutable state. Pass dependencies in.
- Errors that a caller must tell apart are typed and wrapped with `%w`. Use
  `errors.Is` and `errors.As`. Never match error text.
- Options go in a struct when a function has many.
- Tests are table-driven, with the standard library and `go-cmp`.

Three rules are this project's own. Each comes from a past bug.

| Rule | Reason |
|---|---|
| Terminal output goes through `IOStreams`, from `main`, the command packages, `cmdutil`, `view`, and `tui` only | Code below the commands once printed, and machine output could be corrupted |
| A status or an outcome is a named type, never a parsed string | The old TUI matched engine phases by label prefix |
| A package that starts a goroutine has a goleak `TestMain` | Leaks were found this way |

## How the pieces work

**AWS calls (`awsx`).** `awsx.Clients` holds every service client of one
region. There is one retry layer, the SDK retryer, with a policy per kind of
call: `Read` (5 attempts), `Sweep` (short dial timeout, a dial failure is not
retried), `Poll` (1 attempt; the wait loop goes on through transient errors),
and `Mutate` (5 attempts with one idempotency token). Do not wrap a call in
your own retry. Page a list call with `awsx.ListAll`. Classify an error with
`awsx.Classify`; `awsx.FormatAWSError` turns it into a message that names the
IAM action. A new AWS call needs a row in `awsx.RequiredPermissions` and the
same row in the IAM table of `docs/concepts/configuration.md`
(`TestRequiredPermissionsMatchDocs`).

**The read model (`fleet`).** `fleet.Fetch` reads what a `Need` asks for into
one `Snapshot`: the cluster, its nodegroups, its add-ons, the latest versions,
and the failures. Each group of fields has a provenance (collected, partial,
not requested, not attempted, failed). Unknown is never the zero value. Every
view and every check reads the snapshot. A command fetches once.

**Failures.** A partial result is never a success. A read that fails becomes
an `apidoc.Failure` in the top-level `failures` of the document
(`fleet.FailureFromError`), the row gets `incomplete: true`, and the command
exits 4. Print with `cmdutil.ReportFailures` and return
`cmdutil.IncompleteError`.

**Health (`health`).** A check returns a `Result` with an outcome from a
closed set: `Pass`, `Warn`, `Fail`, `Skipped`, `Unreadable`. There is no
score. `health.Gate(results, policy)` returns a `Verdict`: `Go`, `Ask`, or
`Block`, with the reasons and the exit code. A front end supplies only the
confirm function. Gate policy does not live in a command.

**Changes (`change`).** A changing command builds a plan (`change.NewPlan`),
and `change.Run` executes it with three hooks: events, questions, and the
checkpoint between phases. A dry run returns the plan and stops. The busy
rule, the gate, plan revalidation, the waits (`WaitUpdate` follows an update
ID, `WaitSettled` follows a resource to a postcondition), and the one rule for
the exit code are all in `change`. The CLI and the TUI are front ends of the
same `Run`. Do not assemble a roll in a command.

**Views (`view`).** A list column is defined once and gives the table cell and
the plain cell. A describe view is a list of sections; plain output derives
its `FIELD`/`VALUE` rows from the same sections. Machine formats do not go
through `view`: `cmdutil.Print` encodes the document.

## The output contract

`docs/concepts/output.md` is the user-facing text. In short:

- `-o json|yaml` prints exactly one document on stdout. Everything for people
  goes to stderr. A machine run never prompts.
- A document is a type that implements `apidoc.Document`. It starts with
  `apiVersion: refresh.drod.dev/v2` and `kind`. A new kind goes in the kind
  registry of `apidoc`, gets a schema (`task docs:gen`), and a golden test
  that validates real output against it (`cmdutiltest.ValidateJSON`).
- No `null`: a list is `[]`, a map is `{}`, and a key with no data is left out.
- refresh's own enum values are PascalCase. AWS values pass through.
- `-o plain` is TSV: a header row, then rows, `-` for an empty cell.
- Exit codes: `0` ok, `1` error or interrupt, `2` needs attention, `3` blocked,
  `4` incomplete or partial failure, `5` the check after a change found
  issues. The highest wins; an interrupt is always 1. Return a
  `cmdutil.ExitError`; `main` unwraps it.

## Adding a command

Cobra is used directly, as `gh` and `kubectl` use it. Model a read command on
`internal/cmd/addon/list.go` and a changing command on
`internal/cmd/nodegroup/scale.go`.

1. **Options.** A struct with the streams and the lazy dependencies from the
   `Factory`, and one field per flag.
2. **The command.** `newCmdX(f *cmdutil.Factory) *cobra.Command`, with `Use`,
   `Short`, `Long`, `Example`, and `Args`. `RunE` calls `complete`, `validate`,
   then `run`.
3. **Flags.** Use the `cmdutil` helpers so that a flag is the same everywhere:
   `AddOutputFlag`, `AddClusterFlag` (read) or `AddChangeClusterFlag`
   (changing), `AddFilterFlag`, `AddSortFlags`, `AddWatchFlags`,
   `AddChangeFlags`. A flag letter has one meaning (`TestFlagShorthands`).
4. **`complete`** fills the options from flags, args, env, and the context.
   **`validate`** rejects bad input. Neither touches the network.
5. **A changing command** calls `cmdutil.RequireYes` before it builds a
   client, resolves its cluster with `cmdutil.ResolveCluster(f, name, false)`
   (never the kubeconfig, also with `--dry-run`), and runs through
   `changecmd`.
6. **Output.** `cmdutil.Print(ios, format, doc, view)`.
7. **Help.** A changing command lists its exit codes in `Long`
   (`TestChangeCommandsAgreeOnExitCodes`). Add the command to a help group
   (`TestEveryCommandIsInAHelpGroup`).
8. **Docs.** Run `task docs:gen` and commit `docs/reference`. Write the
   hand-written page in `docs/commands/`.

## Testing

1. **Table tests** for pure decision functions (the gate table, the exit-code
   table, the planners).
2. **Whole-command tests** through the fake world. `cmdtest.NewFake(t, world,
   region)` starts a fake AWS and gives a factory that reaches it; `e.Run(ctx,
   args...)` runs the real root command; `e.Out` and `e.Err` hold what it
   wrote. Compare with `cmdutiltest.Golden(t, file, got, *update)`; run the
   test with `-update` to rewrite the golden files, and read the diff.
   `e.Server` counts requests, and a fault set on the world
   (`World.SetFault`) makes a call fail: denied, throttled, a lost response.
3. **Lifecycle tests** for `change`, `noderoll`, and `tui`, with `-race`,
   goleak, and handshakes. Do not sleep to wait for a goroutine.

Real SDK clients talk to `awsxtest` over HTTP. There are no mocks of the AWS
interfaces. Kubernetes tests use `kubetest`. World time is virtual:
`UseWorldClock` makes a wait as fast as the fake answers.

CI runs `-race -count=5 -shuffle=on`, so a test must not depend on order or
leave package state behind. A test that sets env vars cannot use
`t.Parallel()`.

**The parity suite (`test/parity`).** A scenario is a command line on a seeded
world. The suite runs the binary as a subprocess, records which facts it had
read before each changing request, and compares that evidence, the changing
requests, and the exit code with the recorded trace of the same scenario from
the last release of the old CLI (`testdata/legacy/*.json`). An intended
difference has an entry with a reason in `testdata/allowlist.json`.
`test/parity/README.md` has the rules. A new safety behaviour gets a scenario.

## Gotchas

- **The command reference and the schemas are generated.** After a change to a
  command, a flag, or a document type, run `task docs:gen`. CI fails when
  `docs/reference` or `api/schema` is stale.
- **gen-docs removes stale pages.** It deletes `refresh*` pages in
  `docs/reference` that no command produces any more, and keeps other files.
- **`-o` is not a global flag.** Each command registers it with its formats.
- **`context add` shadows `--region` and `--profile` on purpose.** No other
  command may shadow a global flag.
- **The TUI points stderr at the null device while it runs**, so a kubeconfig
  exec plugin cannot write over the screen.
- **Docs.** `uv run --frozen mkdocs build --strict` must pass. The site
  publishes to <https://drod.dev/refresh/> on a merge to `main`.
- **The site has two versions.** `tools/mkdocs/build_site.py` builds `docs/`
  into `/refresh/v2/` and the 0.16 docs (pinned commit `V1_REF`) into
  `/refresh/v1/`. It copies `api/schema` to `/refresh/schema/`, and writes
  `versions.json` and a redirect stub at the root for every page path. Do not
  edit the v1 docs in history; fix a v1 build problem in the override config
  of the script.

## Where work is tracked

Issues live in Linear, team `REF` (project "Refresh — EKS CLI"). There is no
TODO file in the repo.

## Release

release-please keeps a release PR up to date from the Conventional Commit
messages on `main`. Merging it tags the release. GoReleaser then builds the
binaries, generates the man pages (`gen-docs --format man`) into the archives
and the Homebrew cask, signs the checksums, and updates the cask. The version,
commit, and build date are set in `internal/build` with ldflags. `task
release:test` checks the config and that a release-style build prints all
three.
