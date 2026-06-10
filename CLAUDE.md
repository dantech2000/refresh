# CLAUDE.md

Guidance for AI agents (and humans) working in this repo.

## What this is

`refresh` — a Go CLI to manage and monitor AWS EKS clusters and nodegroups:
health checks, fast list/describe, add-on management, and smart scaling.
Module path: `github.com/dantech2000/refresh`. Entry point: `main.go`.

## Build / test / lint

Prefer the Taskfile targets; raw commands shown for reference.

```bash
task build          # go build -o refresh . (CGO_ENABLED=0)
task test           # go test ./...
task test:coverage  # coverage profile + html
task lint           # golangci-lint run ./...
task vet            # go vet ./...
task dev:full       # fmt + vet + lint + test + build (run before pushing)
go test ./... -race # race detector
```

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
`internal/types`, `internal/mocks`.

## Conventions (follow these when editing)

- **CLI framework:** urfave/cli **v2** today (a v2→v3 migration is planned — see Linear REF-11).
  Note the v2 quirk: flags must precede positional args.
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

## Known gotchas

- `gopkg.in/yaml.v3` ignores `json` tags — output structs need explicit `yaml:` tags too, or YAML
  keys diverge from JSON (tracked in Linear).
- Pricing API client is pinned to `us-east-1`; per-region pricing comes from the query filter, not
  the client region.
- Cost estimates are on-demand only and currently use the nodegroup's first instance type.

## Where work is tracked

All issues, bugs, and roadmap live in **Linear, team `REF`** (project "Refresh — EKS CLI"),
sequenced into Phase 1–4 milestones. There is intentionally no in-repo TODO file.

## Release

Tag-driven via GoReleaser + GitHub Actions; version is stamped into
`internal/commands/version.go` by ldflags. Distributed via Homebrew cask + release binaries.
See README "Release Process".
