# CLAUDE.md

Guidance for AI agents (and humans) working in this repo.

## What this is

`refresh` — the **EKS upgrade companion**: a Go CLI for the cluster
upgrade/patching lifecycle — *status → readiness → patch → upgrade*. The core
loop is `refresh status` (what's stale across the fleet) → `cluster
upgrade-check` (am I ready) → `nodegroup update` / `addon update` (patch safely,
with pre-flight health gates, dry-run, and live monitoring) → `cluster upgrade`
(orchestrate the whole thing). Supporting surface: `cluster list/describe`,
`nodegroup list/describe/scale`, `addon *`, and kubectx-style contexts.

Browse-y features that competed with eksctl/k9s/Kubecost (cost estimation,
CloudWatch utilization tables, `cluster diff`, the standalone `workload pdbs`
command) were intentionally removed in the Phase 2 surface trim (REF-78) — don't
re-add them. PDB awareness still lives where it earns its keep: the pre-flight
health checks and `nodegroup scale --check-pdbs`.

Module path: `github.com/dantech2000/refresh`. Entry point: `main.go`.

## Build / test / lint

Prefer the Taskfile targets; raw commands shown for reference.

```bash
task build          # go build -o dist/refresh . (CGO_ENABLED=0)
task test           # go test ./...
task test:coverage  # coverage profile + html
task test:race      # go test -race -shuffle=on -count=2 ./...  (COUNT=n to change)
task lint           # golangci-lint (CI-pinned version via go run; config: .golangci.yml)
task vet            # go vet ./...
task vuln           # govulncheck ./...  (pinned)
task deadcode       # fail on code unreachable even from tests (pinned)
task tidy:check     # go mod tidy -diff
task docs:check     # regenerate docs/reference, fail if it changed
task dev:full       # fmt, vet, lint, tidy:check, deadcode, docs:check, test:race, build (run before pushing)
```

CI mirrors `dev:full` on every PR: tidy check, docs reference check,
`go test -race` (+ coverage to Codecov), a shuffled `-race -count=5` stress
job, `golangci-lint` (includes govet and gofmt), `govulncheck`, and
`deadcode`. A push to main runs only the coverage job (Codecov's base). A
nightly workflow runs `-race -count=20` and `govulncheck` on main.

**Toolchain pinning.** The Taskfile sets `GOTOOLCHAIN` to the `toolchain`
line in `go.mod`, which is the Go that CI uses. A newer local Go formats
differently, so without the pin a change can pass `task fmt` and fail CI's
gofmt. `task lint` builds golangci-lint with that toolchain for the same
reason, and CI uses `install-mode: goinstall`. To opt out for one run, set
`GOTOOLCHAIN=local task ...`. Tool versions (golangci-lint, govulncheck,
deadcode) are pinned in the Taskfile `vars` and must match the workflows.

Requires Go 1.26+ (`go.mod` pins the `go` and `toolchain` versions).

## Architecture

Layered, with dependency injection via interfaces (so everything is mockable):

```
command (CLI wiring)  internal/commands/{statuscmd,cluster,nodegroup,addon,ctxcmd}
  → runner            internal/commands/runner   (AWS setup, cluster/region resolution, kube client, EncodeStdout, spinners)
  → factory           internal/commands/factory  (service constructors)
  → service           internal/services/*        (business logic + AWS calls)
  → view              internal/commands/{clusterview,statusview}, internal/rollview, internal/render, internal/ui
```

**runner** helpers (use these, don't re-implement them):

- `SetupAWS(ctx, cmd)` opens the API context with the global `--timeout`,
  loads the config, and checks credentials. `SetupAWSWithDeadline(ctx, cmd, d)`
  takes an explicit deadline (`d <= 0` = none) for commands that scope their
  own long waits (`nodegroup update`, `nodegroup scale --wait`, `addon update`).
  Prompts under the returned context pause the deadline and wait on the
  signal context (see `apiContext`).
- Cluster resolution: `ResolveClusterOrList` for read-only commands (flag →
  positional → refresh context → kubeconfig current cluster; prints the
  cluster list hint to stderr and returns an error when nothing resolves);
  `ResolveCluster` for mutating commands (no kubeconfig fallback, never lists);
  `ResolveClusterNoPrompt` for mutating runs that must never prompt (e.g.
  `cluster upgrade -o json`). Exact names beat substring matches.
  `nodegroup update` reads `EKS_CLUSTER_NAME` in code; never give a
  `--cluster` flag an env `Sources` (urfave reports it as set).
- `Regions(cmd, allRegions)` is the only way to read a subcommand's
  repeatable `-r/--region`: it folds in the global `--region` placed before
  the subcommand (without a sweep) and returns nil when there is no explicit
  list, so `REFRESH_EKS_REGIONS` / the partition default apply.
- `ResolveClusterKubeClient` returns the Kubernetes client for the target
  cluster: a kubeconfig context whose server matches the EKS endpoint,
  `--kube-context` as named, or in-cluster config only when
  `REFRESH_IN_CLUSTER_NAME` matches. On a mismatch it warns once and returns
  nil. Build health checkers with `health.NewCheckerForConfig(awsCfg, kube,
  metrics)` so every command gets the same EKS/CloudWatch/ASG/Service Quotas
  wiring.

Supporting packages: `internal/aws` (SDK abstractions, AMI lookup, cluster name
resolution), `internal/aws/awserr` (see below), `internal/awsconfig` (unified
config loading), `internal/cliconfig` (YAML context store), `internal/config`
(defaults, env var names), `internal/health` (pre-flight checks, PDB gates,
kube target matching), `internal/monitoring` (update progress),
`internal/dryrun`, `internal/types`, `internal/mocks`,
`internal/services/upgrade` (cluster upgrade orchestrator: plan generation +
control-plane/addon/nodegroup phases + sequencing engine; resumable by
re-deriving the plan from live cluster state, not state files).

**`internal/aws/awserr`** is a leaf package: `FormatAWSError`, `ListAllPages`,
`Summary`, and the typed error classifiers (smithy API codes, net/url errors,
the SDK retryables). It imports neither `internal/ui` nor `internal/health`,
which breaks the `internal/aws` → `internal/ui` → `internal/health` cycle, so
the health checks can format AWS errors too. `internal/aws` re-exports
`FormatAWSError` and `ListAllPages`, so existing callers keep their imports.
Classify errors with `errors.As`, never by matching strings.

**Output contract** (docs: `docs/concepts/output.md`):

- `-o json|yaml`: stdout holds exactly one document (`runner.EncodeStdout`).
  Progress, notices, prompts, spinners, and credential help go to stderr or
  are dropped. Check `runner.IsMachineFormat(format)` before printing anything
  human. Machine runs never prompt: fail with an error that names `--yes`.
  Empty lists encode as `[]`, not `null`.
- `-o plain`: pure TSV. A header row whose names match the table view, then
  one row per item; `-` for empty cells; no title, footer, or blank lines
  (`internal/ui/plaintest` asserts this). Describe commands use `FIELD`/`VALUE`.
- Human stderr writes go through `ui.Stderr`, which strips ANSI unless stderr
  itself is a color TTY. Color is decided per stream: use `ui.ColorFor(w, …)`
  / `ui.StderrColor(…)` for stderr, never the stdout-derived fatih global.
  `--no-color`, any non-empty `NO_COLOR`, and `TERM=dumb` disable both.
- Prompts use `ui.ReadLine` / `ui.Confirm` (one shared, ctx-cancellable stdin
  reader).

**Output / rendering** (`internal/render`): the human-facing design system —
palette (Catppuccin, truecolor with 256/none downgrade + capability detection),
status tokens (`glyph + label + color`, with an ASCII fallback so color is
always *additive*), primitives (section/table/KV/callout/bar) that reuse the
ANSI-width math in `internal/ui`, and `LiveRegion` for in-place redraw (degrades
to appended snapshots when piped). **Rule: render in the view layer, never from
services.** Machine formats (`-o json/yaml/plain`) do NOT go through `render` —
they stay in `runner.EncodeStdout`, byte-for-byte unchanged; each restyled view
branches `if ui.PlainOutput()` to keep the `-o plain` TSV path.

**Live node-roll** (`internal/noderoll`): observes a managed-nodegroup roll in
real time (nodes draining/joining/terminating) from live Kubernetes Node state —
the per-node truth EKS's coarse `DescribeUpdate` can't give. `KubeObserver`
scopes by the `eks.amazonaws.com/nodegroup` label and classifies by Ready /
cordon-or-drain-taint, with old-vs-new from either the `nodegroup-image` AMI
label or a roll-start `CaptureBaseline`. It is watch-backed when possible
(`StartInformers`: node and event informers, snapshots read the local cache;
pods are listed per draining node with a `spec.nodeName` field selector, never
cluster-wide) and falls back to per-poll List calls when informers can't start — the
watch is an upgrade, never a requirement. Note this applies only to the
repeatedly-polled live view; one-shot reads (pre-flight health checks,
`--check-pdbs`, post-roll verify) correctly stay as single List calls. `Tracker` diffs snapshots into a
lifecycle event feed. Testable with **zero AWS / zero cluster** via
`client-go/kubernetes/fake` (see `observer_test.go`) and a `ScriptedObserver`
(`DemoTimeline`). `refresh nodegroup update --simulate` (hidden flag) drives the
whole live panel from the scripted observer — demos, asciinema, and manual QA
with no AWS.

## Conventions (follow these when editing)

- **CLI framework:** urfave/cli **v3** (migrated from v2 in REF-11). Handlers are
  `func(ctx context.Context, cmd *cli.Command) error`; flags may appear before or after
  positional args.
- **Output:** every list/describe command supports `-o table|json|yaml|plain[|tree]` via
  `runner.EncodeStdout`, and follows the output contract above. A partial result (items that
  could not be read) is never a success: name the failures on stderr, add `failures` to the
  JSON/YAML payload, and exit non-zero.
- **AWS calls:** wrap in `common.WithRetry`; format errors with `awsinternal.FormatAWSError`
  (or `awserr.FormatAWSError` below `internal/ui`); page list calls with `ListAllPages`.
  Set `ClientRequestToken: aws.String(common.IdempotencyToken())` on mutating calls (Update*).
  New IAM actions go in the permission hint in `awserr/errors.go` and in the IAM table in
  `docs/concepts/configuration.md`.
- **Concurrency:** fan out per-item AWS calls with `common.ForEachParallel` (bounded); thread
  `ctx` everywhere; multi-region work uses a concurrency cap. Run a best-effort observer next
  to an authoritative wait with `common.RunAlongside`.
- **Testing:** use the configurable EKS mock + fluent builders in `internal/mocks` — no live AWS
  in unit tests. Inject failures with the typed errors (`mocks.AccessDenied()`,
  `mocks.Throttling()`, `mocks.NotFound()`, `mocks.APIError(code, msg)`), not `errors.New`.
  Test a whole command (flags, setup, stdout/stderr, exit code) with `internal/mocks/fakeaws`:
  `fakeaws.New(t, clusters...)` serves EKS/STS over `AWS_ENDPOINT_URL`, and
  `fakeaws.Run(t, fakeaws.App(cmds...), args...)` returns stdout, stderr, and the error. These
  tests set env vars, so they can't use `t.Parallel()`. Don't sleep to wait for goroutines;
  use a handshake (a channel the code under test signals). CI runs `-race -count=5
  -shuffle=on`, so tests must not depend on order or leak package state.
- **Context:** commands derive from the signal-cancellable root via `runner.SetupAWS` /
  `SetupAWSWithDeadline`; don't build `context.Background()` in command actions.

## Code patterns (copy these)

Retry + idempotency on a mutating call (token computed **once**, outside the retry):

```go
token := common.IdempotencyToken() // stable across SDK transport retries
out, err := common.WithRetry(ctx, common.DefaultRetryConfig,
    func(rc context.Context) (*eks.UpdateNodegroupVersionOutput, error) {
        return eksClient.UpdateNodegroupVersion(rc, &eks.UpdateNodegroupVersionInput{
            ClusterName:        aws.String(clusterName),
            NodegroupName:      aws.String(ng),
            ClientRequestToken: aws.String(token),
        })
    })
if err != nil {
    return awsinternal.FormatAWSError(err, "updating nodegroup version")
}
```

`WithRetry[T]` (`internal/services/common/retry.go`) is generic and retries
throttling/5xx/transient errors with full jitter while honoring `ctx`; it does
not retry permanent errors such as AccessDenied. `FormatAWSError(err, op)`
(`internal/aws/awserr/errors.go`, re-exported by `internal/aws`) turns SDK
errors into actionable messages (e.g. a missing IAM permission lists the
actions) and wraps the original, so `errors.Is`/`errors.As` still work —
wrap every surfaced AWS error.

Unit test with the fluent EKS mock (no live AWS):

```go
api := mocks.NewEKSAPI().
    WithCluster("prod", "1.32").
    WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2X8664).
    Build()
svc := nodegroup.NewService(api, /* ec2, asg, … */)
// drive svc methods against api; assert on the returned summaries/errors
```

`internal/mocks` exposes a configurable `EKSAPI` plus the `EKSAPIBuilder`
(`NewEKSAPI().With…().Build()`); see `internal/mocks/builders.go` for the full
`With*` catalog (clusters, nodegroups, addons, insights, update status
scripts, `WithPageSize` to force paging). Unknown clusters return a typed
`ResourceNotFoundException`, as EKS does.

## Adding a new command

Follow the layered flow (model it on the `cluster` command):

1. **Command def** — add a `*cli.Command` in `internal/commands/<group>/command.go`
   with flags (`-o/--format`, `--timeout`, …). Action signature is
   `func(ctx context.Context, cmd *cli.Command) error`.
2. **Action** — in `actions.go`: validate `--format` with
   `runner.ValidateFormat`, get AWS config via `runner.SetupAWS(ctx, cmd)`,
   resolve the cluster with `runner.ResolveClusterOrList` (read-only) or
   `runner.ResolveCluster` (mutating), run the fetch inside
   `runner.WithSpinner`. Don't add a local flag that shadows a global one
   (`--timeout`, `--region`, `--max-concurrency`, …) unless it means something
   different; `main_flags_test.go` fails otherwise.
3. **Service** — construct it through `internal/commands/factory` (don't
   `eks.NewFromConfig` in the action); put business logic + AWS calls in
   `internal/services/<group>`.
4. **Output** — `runner.EncodeStdout(cmd.String("format"), payload)`; if it
   returns `handled==false`, fall through to a `clusterview`/`ui` table renderer.
5. **Tests** — drive the service with `internal/mocks` and the command with
   `internal/mocks/fakeaws`; tag output structs with both `json:` and `yaml:`
   (a reflection test enforces matching tags).

## Known gotchas

- `gopkg.in/yaml.v3` ignores `json` tags. `runner.EncodeStdout` round-trips YAML
  through JSON so keys stay camelCase (REF-59), but still add explicit `yaml:`
  tags to any struct you might marshal directly.
- **The docs command reference is generated.** After changing any command or
  flag, run `task docs:gen` — the hidden `gen-docs` command walks the CLI tree
  into `docs/reference/`, and a CI step fails if the committed reference is stale.
  Behaviour changes also need the hand-written pages (`docs/concepts/`,
  `docs/commands/`); `uv run --frozen mkdocs build --strict` must pass.
- **Docs live in-repo** under `docs/` (Material for MkDocs, via a `uv`-managed
  hash-locked venv) and publish to <https://drod.dev/refresh/> on merge to `main`.
  Cost/utilization/`cluster diff`/`workload pdbs` were removed in the Phase 2
  trim (REF-78) — don't re-add them.

## Where work is tracked

All issues, bugs, and roadmap live in **Linear, team `REF`** (project "Refresh — EKS CLI"),
sequenced into phased milestones (Phase 1 quick wins & CI hardening, Phase 2
surface trim & refocus, … through Phase 9 tests/docs). There is intentionally no
in-repo TODO file.

## Release

Tag-driven via GoReleaser + GitHub Actions; version is stamped into
`internal/commands/version.go` by ldflags. Distributed via Homebrew cask + release binaries.
See README "Release Process".
