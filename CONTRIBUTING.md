# Contributing to refresh

Thanks for your interest in improving `refresh` — a Go CLI for managing and
monitoring AWS EKS clusters and node groups.

## Prerequisites

- Go **1.26+** (the module pins `go 1.26.0` / `toolchain go1.26.4`).
- [Task](https://taskfile.dev) for the build/test targets (optional but
  recommended).
- [golangci-lint](https://golangci-lint.run) v2 for linting.

## Development workflow

```bash
task build          # go build -o refresh . (CGO_ENABLED=0)
task test           # go test ./...
task lint           # golangci-lint run ./...
task vet            # go vet ./...
task vuln           # govulncheck ./...
task dev:full       # fmt + vet + lint + test + build — run before pushing
```

Run the race detector on anything touching concurrency:

```bash
go test -race ./...
```

## Conventions

These mirror `CLAUDE.md` (the agent/contributor guide):

- **CLI framework:** urfave/cli **v3**. Handlers are
  `func(ctx context.Context, cmd *cli.Command) error`.
- **Output:** every list/describe command supports `-o table|json|yaml|plain`
  (and `tree` for `cluster list`) via `runner.EncodeStdout`. Honor
  `--no-color` / `NO_COLOR`.
- **Short-flag convention:** a short flag must not mean contradictory things on
  commands a user might mix up. Reserved: `-o`=`--format`, `-c`=`--cluster`,
  `-n`=`--nodegroup`, `-t`=`--timeout`, `-r`=`--region`, `-w`=`--watch`,
  `-H`=`--show-health`. In particular `-w` is **watch-only** (it is *not*
  `--wait`/`--no-wait`) and `-H` is **show-health-only** (not `--health-only`
  /`--health-check`) — the mutating update commands spell those out in long
  form. When in doubt, leave a flag long-form rather than reuse a short letter.
- **AWS calls:** wrap in `common.WithRetry`; format errors with
  `awsinternal.FormatAWSError`; set
  `ClientRequestToken: aws.String(common.IdempotencyToken())` on mutating
  (`Update*`) calls.
- **Concurrency:** fan out per-item AWS calls with `common.ForEachParallel`
  (bounded) and thread `ctx` everywhere.
- **Output structs:** add **both** `json:` and `yaml:` tags — `yaml.v3` ignores
  `json` tags. (`runner.EncodeStdout` round-trips YAML through JSON, but new
  top-level types should still be tagged.)
- **Testing:** use the configurable EKS mock + fluent builders in
  `internal/mocks`; no live AWS in unit tests.

## Commit messages & PRs

- Conventional-commit style subjects (`feat:`, `fix:`, `refactor:`, `ci:`,
  `docs:`). The repo squash-merges, so the PR title becomes the commit.
- Keep PRs focused. Make sure `task dev:full` is green before opening one.
- CI runs build, `go vet`, `go test -race`, `golangci-lint`, and `govulncheck`.

## Where work is tracked

Issues and roadmap live in **Linear (team `REF`, project "Refresh — EKS CLI")**.
There is intentionally no in-repo TODO file.
