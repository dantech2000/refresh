# Refresh

[![CI](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/test.yml?branch=main&style=flat-square&label=CI)](https://github.com/dantech2000/refresh/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![codecov](https://codecov.io/gh/dantech2000/refresh/branch/main/graph/badge.svg?style=flat-square)](https://codecov.io/gh/dantech2000/refresh)
[![Latest release](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![Docs](https://img.shields.io/badge/docs-drod.dev%2Frefresh-blue?style=flat-square)](https://drod.dev/refresh/)

Refresh is a command-line tool that keeps Amazon EKS clusters up to date. It
covers four steps: status, readiness, patch, and upgrade. Every change has a
health gate before it, a dry run, and a wait with a verification after it.

1. **`refresh status`** shows what is out of date in the fleet: stale AMIs,
   nodegroups behind the control plane, add-ons with a newer version, and
   clusters that left standard support.
2. **`refresh cluster upgrade-check`** reports whether a cluster is ready for
   its next upgrade, from the EKS cluster insights and the version skew. It
   changes nothing.
3. **`refresh nodegroup update`** and **`refresh addon update`** patch a
   cluster: they roll nodegroups onto the latest AMI and update add-ons.
4. **`refresh cluster upgrade`** upgrades a cluster one minor version at a
   time: the control plane, the add-ons that the new version needs, each
   nodegroup, then the other add-ons.

`refresh cluster rollback` returns a cluster to the version before its
upgrade, within 7 days. The `list` and `describe` commands for clusters,
nodegroups, and add-ons cover the daily reads.

The full documentation is at [drod.dev/refresh](https://drod.dev/refresh/).

## Features

- **A health gate before every change.** Nodes, capacity, control plane,
  system workloads, PodDisruptionBudgets that would stop a drain, and the EC2
  vCPU quota. A gate that blocks exits `3`, and nothing changed.
- **A dry run for every change.** `--dry-run` prints the plan and the verdict
  of the health gate. Its exit code says what the real run would do.
- **Fleet status.** `refresh status -A` reads every region. Data that could
  not be read is marked incomplete, never shown as current.
- **Fleet updates.** `nodegroup update --all-clusters` rolls the clusters of
  the regions you name, one after another, after one confirmation.
- **Verification after a roll.** The nodegroups are `ACTIVE`, the new nodes
  are `Ready`, and no pod is newly `Pending`. An issue exits `5`.
- **No state file.** Every plan comes from the live state of the cluster. If
  a run stops, run the same command again.
- **Exact names.** A cluster, nodegroup, or add-on name is exact. Select by
  pattern with `-f name=web-*`.
- **Unattended runs.** Without a terminal, or with `-o json`, a change never
  waits on a prompt: it fails and names `--yes`. Changing calls carry an
  idempotency token.
- **Output for scripts.** `-o json|yaml` prints exactly one document on stdout
  (`apiVersion: refresh.drod.dev/v2` and a `kind`), with a published
  [JSON Schema](https://drod.dev/refresh/reference/schemas/) for each kind.
  `-o plain` is TSV. One exit-code contract holds for every command.
- **Contexts.** A context saves a cluster with its region and AWS profile, so
  you do not repeat `-c`, `-r`, and `--profile`.

## Requirements

- AWS credentials from the standard chain, with the
  [required IAM permissions](https://drod.dev/refresh/concepts/configuration/#required-iam-permissions).
- Optional: a kubeconfig context for the cluster. The health checks that read
  the Kubernetes API need it, and so do `--check-readiness` and the live view
  of a roll. Without it, a changing command asks before it goes on.
- Go 1.26 or later, only to build from source.

## Installation

### Homebrew (recommended)

```bash
brew install --cask dantech2000/tap/refresh
refresh version
```

The cask installs the man pages.

### Pre-built binaries

Download an archive for macOS, Linux, or Windows from the
[latest release](https://github.com/dantech2000/refresh/releases/latest), then
put the binary on your `PATH`:

```bash
tar -xzf refresh_*_darwin_arm64.tar.gz
sudo mv refresh /usr/local/bin/
```

The archive has the man pages in `man/man1`. The release checksums are signed
with cosign, and each archive has an SBOM. See
[Installation](https://drod.dev/refresh/getting-started/installation/) for the
verification steps.

### From source

```bash
go install github.com/dantech2000/refresh/cmd/refresh@latest
```

Releases up to 0.16 installed from `github.com/dantech2000/refresh@latest`.
That path no longer builds a binary.

## Quickstart

```bash
# 1. What is out of date in every region?
refresh status -A

# 2. Is the cluster ready to upgrade?
refresh cluster upgrade-check prod

# 3. Patch: see the plan, then roll
refresh nodegroup update -c prod --dry-run
refresh nodegroup update -c prod
refresh addon update --all -c prod

# 4. Upgrade the cluster
refresh cluster upgrade prod --to 1.35

# Within 7 days: roll the upgrade back
refresh cluster rollback prod --dry-run
```

A context saves the cluster, the region, and the profile:

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
refresh context use prod     # later commands use prod
refresh context use -        # switch back to the context before
```

`refresh ui` (experimental) shows the fleet, the readiness of a cluster, and
changes as they run, in a full-screen terminal UI. It is read-only unless you
start it with `--allow-changes`: then it can update add-ons, roll a nodegroup,
upgrade a cluster, or roll one back, each after a dry run that you confirm.

Each flag letter has one meaning in every command: `-c` cluster, `-n`
nodegroup, `-o` output, `-r` region, `-t` timeout, `-d` dry-run, `-y` yes,
`-q` quiet, `-w` watch, `-f` filter, `-A` all regions.

### Coming from 0.16 or earlier

The release after 0.16 changed the command line, some exit codes, and the JSON
documents. Read [Migrating from 0.16](https://drod.dev/refresh/migrating/)
before you upgrade a machine that runs scripts.

## Exit codes

Every command follows one contract:

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | Error or interrupt |
| `2` | Needs attention: warnings or items that are out of date |
| `3` | Blocked or unsupported: a gate stopped the change, and nothing changed |
| `4` | Incomplete data or partial failure |
| `5` | The change was applied, and the check after it found issues |

When one run has several results, the highest code wins, and an interrupt is
always `1`. See [Exit codes](https://drod.dev/refresh/concepts/exit-codes/)
for the codes of each command, with a CI example.

## Development

```bash
task build        # build ./dist/refresh (CGO_ENABLED=0)
task test         # go test ./...
task lint         # golangci-lint at the CI-pinned version
task dev:full     # fmt, vet, lint, tidy, deadcode, govulncheck, docs, race tests, build (run before pushing)
task fuzz         # run every fuzz target for 30s (FUZZTIME=2m to change)
task docs:gen     # regenerate docs/reference and api/schema from the binary
task run:sim -- status -A   # run the binary against a fake fleet, with no AWS account
```

The code follows the standard Go project layout: `cmd/refresh` is the binary,
and the product code is under `internal/`. The commands are cobra commands
with a `Factory` and `IOStreams`, as `gh` and `kubectl` write them. See
[`CLAUDE.md`](CLAUDE.md) for the architecture and the conventions.

## Release process

Releases are automated with [release-please](https://github.com/googleapis/release-please)
and [GoReleaser](https://goreleaser.com):

1. Changes land with [Conventional Commit](https://www.conventionalcommits.org)
   messages (`feat:`, `fix:`, `docs:`, and so on).
2. release-please keeps a release PR that bumps the version and updates the
   changelog from those commits.
3. Merging the release PR tags the release. GoReleaser then builds the
   binaries for each platform, signs the checksums with cosign, attaches SBOMs,
   publishes the GitHub release, and updates the Homebrew cask in
   `dantech2000/homebrew-tap`.

The version is set at build time with ldflags, so there is no version constant
to edit. Check the release config locally with `task release:test` or
`task release:dry`.

## Security

- Refresh never logs or stores credentials. It reads them through the standard
  AWS SDK credential chain.
- `cluster describe` shows deletion protection, secrets encryption, and
  control-plane logging.
- Changing calls carry an idempotency token (`ClientRequestToken`), so a
  retried request cannot start a second update.
- When AWS denies a call, the error names the missing IAM action.
- Refresh installs nothing on the system. The man pages come with the Homebrew
  cask and the release archives.

## License

Released under the [MIT License](LICENSE).
