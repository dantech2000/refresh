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
task build          # go build -o refresh . (CGO_ENABLED=0)
task test           # go test ./...
task test:coverage  # coverage profile + html
task lint           # golangci-lint run ./...  (config: .golangci.yml)
task vet            # go vet ./...
task vuln           # govulncheck ./...
task dev:full       # fmt + vet + lint + test + build (run before pushing)
go test ./... -race # race detector
```

CI mirrors this: build, `go vet`, `go test -race` (+ coverage to Codecov),
`golangci-lint`, and `govulncheck` all run on every PR.

Requires Go 1.26+ (`go.mod` pins `go 1.26.0` / `toolchain go1.26.4`).

## Architecture

Layered, with dependency injection via interfaces (so everything is mockable):

```
command (CLI wiring)  internal/commands/{cluster,nodegroup,addon,ctxcmd,workload}
  → runner            internal/commands/runner   (SetupAWS, WithSpinner, EncodeStdout, positional/flag helpers)
  → factory           internal/commands/factory  (service constructors)
  → service           internal/services/*        (business logic + AWS calls)
  → view              internal/commands/clusterview, internal/ui (tables/formatting)
```

Supporting packages: `internal/aws` (SDK abstractions, error formatting), `internal/awsconfig`
(unified config loading), `internal/cliconfig` (YAML context store), `internal/health`
(pre-flight checks), `internal/monitoring` (update progress), `internal/dryrun`,
`internal/types`, `internal/mocks`, `internal/services/upgrade` (cluster upgrade
orchestrator: plan generation + control-plane/addon/nodegroup phases + sequencing
engine; resumable by re-deriving the plan from live cluster state, not state files).

## Conventions (follow these when editing)

- **CLI framework:** urfave/cli **v3** (migrated from v2 in REF-11). Handlers are
  `func(ctx context.Context, cmd *cli.Command) error`; flags may appear before or after
  positional args.
- **Output:** every list/describe command supports `-o table|json|yaml|plain[|tree]` via
  `runner.EncodeStdout`. `plain` is uncolored TSV for grep/awk. Honor `--no-color`/`NO_COLOR`;
  spinners auto-disable when piped.
- **AWS calls:** wrap in `common.WithRetry`; format errors with `awsinternal.FormatAWSError`.
  Set `ClientRequestToken: aws.String(common.IdempotencyToken())` on mutating calls (Update*).
- **Concurrency:** fan out per-item AWS calls with `common.ForEachParallel` (bounded); thread
  `ctx` everywhere; multi-region work uses a concurrency cap.
- **Testing:** use the configurable EKS mock + fluent builders in `internal/mocks` — no live AWS
  in unit tests. Add `json` tags to any new output struct (see the YAML-tag gotcha below).
- **Context:** commands derive from the signal-cancellable root via `runner.SetupAWS`; don't build
  `context.Background()` in command actions.

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
throttling/5xx/transient errors while honoring `ctx`. `FormatAWSError(err, op)`
(`internal/aws/errors.go`) turns SDK errors into actionable messages (e.g. a
missing IAM permission lists the action) — wrap every surfaced AWS error.

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
`With*` catalog (clusters, nodegroups, addons, insights, updates).

## Adding a new command

Follow the layered flow (model it on the `cluster` command):

1. **Command def** — add a `*cli.Command` in `internal/commands/<group>/command.go`
   with flags (`-o/--format`, `--timeout`, …). Action signature is
   `func(ctx context.Context, cmd *cli.Command) error`.
2. **Action** — in `actions.go`: validate `--format` with
   `runner.ValidateFormat`, get AWS config via `runner.SetupAWS(ctx, cmd)`,
   resolve the cluster with `runner.ResolveClusterOrList`, run the fetch inside
   `runner.WithSpinner`.
3. **Service** — construct it through `internal/commands/factory` (don't
   `eks.NewFromConfig` in the action); put business logic + AWS calls in
   `internal/services/<group>`.
4. **Output** — `runner.EncodeStdout(cmd.String("format"), payload)`; if it
   returns `handled==false`, fall through to a `clusterview`/`ui` table renderer.
5. **Tests** — drive the service with `internal/mocks`; tag output structs with
   both `json:` and `yaml:`.

## Known gotchas

- `gopkg.in/yaml.v3` ignores `json` tags. `runner.EncodeStdout` round-trips YAML
  through JSON so keys stay camelCase (REF-59), but still add explicit `yaml:`
  tags to any struct you might marshal directly.
- **The docs command reference is generated.** After changing any command or
  flag, run `task docs:gen` — the hidden `gen-docs` command walks the CLI tree
  into `docs/reference/`, and a CI step fails if the committed reference is stale.
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
