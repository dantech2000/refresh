# Refresh

[![CI](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/test.yml?branch=main&style=flat-square&label=CI)](https://github.com/dantech2000/refresh/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![codecov](https://codecov.io/gh/dantech2000/refresh/branch/main/graph/badge.svg?style=flat-square)](https://codecov.io/gh/dantech2000/refresh)
[![Latest release](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![Docs](https://img.shields.io/badge/docs-drod.dev%2Frefresh-blue?style=flat-square)](https://drod.dev/refresh/)

**The EKS upgrade companion.** Refresh is a Go CLI for the Kubernetes patching
and upgrade lifecycle on Amazon EKS: status, readiness, patch, and upgrade.
Pre-flight health gates, dry-run previews, and live progress monitoring let you
keep a fleet current without surprises.

The core loop:

1. **`refresh status`** reports fleet patch posture: stale AMIs, version skew,
   and extended-support exposure across every cluster and region.
2. **`refresh cluster upgrade-check`** reports upgrade readiness from EKS
   Cluster Insights and a local version-skew check. It changes nothing.
3. **`refresh nodegroup update`** and **`refresh addon update`** patch with
   health gates, dry-run, and real-time monitoring.
4. **`refresh cluster upgrade`** runs a full upgrade in order (control plane,
   then nodegroups, then add-ons) with a health gate after every phase.

`list` and `describe` commands for clusters, nodegroups, and add-ons cover the
day-to-day reads.

## Features

- **Pre-flight health checks** validate cluster readiness before a roll using
  default EC2 metrics. No Container Insights or extra setup is required.
- **Live node-roll view** shows nodes draining, terminating, and joining in
  real time from live Kubernetes state. It is on by default for a
  single-nodegroup roll on a color terminal, and `--live` forces it. When the
  cluster API is unreachable it falls back to standard monitoring. EKS stays
  the source of truth for the result.
- **Fleet status** (`refresh status -A`) reports the Kubernetes version, the
  EKS support window with extended-support cost, stale AMIs, nodegroups behind
  the control plane, add-ons behind the latest version, and control-plane
  health across all clusters and regions. Data it could not read is marked
  incomplete, never shown as current.
- **Fleet updates** (`nodegroup update --all-clusters`) find clusters across
  regions and roll them one at a time, with one batch confirmation, a summary
  of every cluster, and the exit code of the worst outcome.
- **Real node readiness.** With `--check-readiness`, node counts come from the
  Kubernetes API (`Ready` out of desired). Without it, list and describe show
  the desired count, never an estimated ready count.
- **Post-roll verification** confirms that nodegroups settle `ACTIVE` with no
  newly stuck pods. A failure has its own exit code, and `--skip-verify` turns
  the check off.
- **AMI changelog.** A dry run summarizes the `amazon-eks-ami` releases between
  the current and target AMI for Amazon Linux nodegroups, and `--changelog`
  prints the full notes. Bottlerocket and Windows nodegroups get a link to
  their release notes. The changelog never blocks an update, including when
  you are offline.
- **Safe cluster upgrades.** `cluster upgrade` refreshes EKS Cluster Insights
  before each version hop, checks for PDB drain blockers before each nodegroup
  roll, and prints the exact command to resume after a failure.
- **Unattended runs.** Mutating calls are idempotent, exit codes are
  documented, and a run never waits on a prompt without a terminal or with
  `-o json`: it fails and names `--yes`.
- **Custom-AMI aware.** Nodegroups with `AmiType=CUSTOM` are classified
  `Custom` and skipped on update, with guidance.
- **Contexts** (kubectx-style) bind a cluster to a region and profile, so you
  do not repeat `--cluster`, `--region`, and `--profile`.
- **Readable without color.** Every status pairs a symbol with a label, so the
  output stays clear with `--no-color`, in a pipe, or on a terminal without
  UTF-8. Truecolor falls back to 256 colors or none, by terminal capability.
- **Script-friendly output.** `-o json|yaml` prints exactly one versioned
  document on stdout (`apiVersion: refresh.drod.dev/v1` and a `kind`), with a
  published [JSON Schema](https://drod.dev/refresh/reference/schemas/) for each
  kind. Progress and notices go to stderr. `-o plain` is TSV: a header row,
  then one row per item.

## Requirements

**Required**

- AWS credentials (`~/.aws/credentials`, environment variables, or an IAM role)
  with the [required IAM permissions](https://drod.dev/refresh/concepts/configuration/#required-iam-permissions).
- Go 1.26 or later, only to build from source.

**Optional**

- A kubeconfig, for the Kubernetes-backed features: workload and Pod
  Disruption Budget checks in the pre-flight health checks, `nodegroup scale
  --check-pdbs`, real node readiness (`--check-readiness`), and the live roll
  view. Refresh uses the context whose server matches the cluster endpoint, or
  the one you name with `--kube-context`.
- CloudWatch metrics. The capacity and resource-balance checks use default EC2
  CPU metrics. Memory checks need Container Insights.

Refresh works with AWS credentials alone. When an optional feature is not
available, the affected check is marked skipped and says what to set up.

## Installation

### Homebrew (recommended)

```bash
brew install --cask dantech2000/tap/refresh
refresh version
```

### Pre-built binaries

Download an archive for macOS (Intel or Apple Silicon), Linux, or Windows from
the [latest release](https://github.com/dantech2000/refresh/releases/latest),
then put the binary on your `PATH`:

```bash
tar -xzf refresh_*_darwin_arm64.tar.gz
sudo mv refresh /usr/local/bin/
```

The release checksums are signed with cosign, and each archive has an SBOM.

### From source

```bash
go install github.com/dantech2000/refresh@latest
```

### Updating

```bash
brew upgrade --cask refresh                        # Homebrew
go install github.com/dantech2000/refresh@latest   # go install
```

## Quickstart

The full command and flag reference, guides, and examples are at
[drod.dev/refresh](https://drod.dev/refresh/). You can also run
`refresh <command> --help`. Coming from eksctl or `aws eks`? See the
[migration guide](https://drod.dev/refresh/migration/).

```bash
# 1. What is stale across the fleet? (all regions, CI-friendly exit codes)
refresh status -A

# 2. Is the cluster ready to upgrade? (read-only: EKS Cluster Insights and version skew)
refresh cluster upgrade-check -c prod

# 3. Patch: preview first, then roll with health gates and the live view
refresh nodegroup update -c prod --dry-run
refresh nodegroup update -c prod

# 4. Upgrade the whole cluster (control plane, add-ons, nodegroups), with gates
refresh cluster upgrade -c prod --to 1.33
```

Contexts bind a cluster to a region and profile:

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
refresh use prod     # later commands target prod
refresh use -        # switch back to the previous context
```

Every `list` and `describe` command supports `-o table|json|yaml|plain`.
`--no-color` and `NO_COLOR` are honored, and spinners stay off when stderr is
not a terminal.

Each shorthand means the same thing on every command: `-c` cluster, `-n`
nodegroup, `-a` addon, `-o` format, `-r` region, `-t` timeout, `-d` dry-run,
`-y` yes, `-q` quiet, `-w` watch, `-f` filter. The commands that change a
cluster share `--dry-run`, `--yes`, and `--wait-timeout`.

`cluster upgrade`, `addon update`, `nodegroup scale`, and `nodegroup update
--all-clusters` ask for confirmation before they act. `nodegroup update` asks
when a health check warns. In scripts, pass `--yes`.

### Upgrading refresh

Read the [changelog](CHANGELOG.md) before you upgrade across a minor version.

- **0.12** adds `apiVersion` and `kind` to every JSON and YAML document and
  reports partial failures in one `failures` list. Scripts that parse the
  output may need changes.
- **0.11** gives each shorthand one meaning across the CLI and renames some
  flags. See [Migrating to 0.11](https://drod.dev/refresh/migration/#migrating-to-011).

## Exit codes

Every command follows one contract:

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | Error or interrupt (Ctrl+C) |
| `2` | Needs attention: warnings or stale items (`status`, `cluster upgrade-check`, `nodegroup update` health warnings) |
| `3` | Blocked or unsupported: a gate stopped the operation (`cluster upgrade-check`, `cluster upgrade`, `nodegroup update`, `nodegroup scale --check-pdbs`), or a cluster is on extended support |
| `4` | Incomplete data or partial failure: some items or regions could not be read, or some updates failed |
| `5` | Post-action verification failed (`nodegroup update`, `nodegroup scale`, `addon update`) |

`cluster upgrade-check` works as a CI gate. It exits `2` for warnings, `3` for
blockers, and `4` when a nodegroup or add-on could not be read. Pass
`--exit-zero` to get the report without failing the job. See
[Exit codes](https://drod.dev/refresh/concepts/exit-codes/) for the codes each
command returns, with CI examples.

## Health checks

Pre-flight health checks validate cluster readiness before a roll, using
default AWS metrics. They run before `nodegroup update` and before each
nodegroup roll in `cluster upgrade`. `--health-only` runs the checks without
an update, and `--skip-health-check` skips them.

**With default AWS metrics**

- **Node health.** Nodegroups are `ACTIVE`. With a kubeconfig, the check uses
  real Ready counts, and fewer than 50% Ready nodes blocks the roll.
- **Cluster capacity.** Enough CPU headroom, from default EC2 metrics.
- **Control plane.** etcd database size, from EKS control-plane metrics.
- **Service quotas.** EC2 vCPU quota headroom.
- **Resource balance.** CPU distribution across nodes.

**With cluster access (kubeconfig)**

- **Critical workloads.** System pods in `kube-system` are running.
- **Pod Disruption Budgets.** PDBs and pods that would block the drain of the
  nodegroups that roll. `nodegroup scale --check-pdbs` refuses a scale-down
  that a PDB does not allow.
- **Node utilization.** CPU and memory headroom from the metrics API.

A check that needs a service you do not have is marked skipped, with guidance.
Use `--kubeconfig` or `--kube-context` to point the Kubernetes-backed checks at
a specific cluster. If the cluster is unreachable, the error names the
kubeconfig and context that were tried. See
[Pre-flight health checks](https://drod.dev/refresh/concepts/health-checks/).

## Development

```bash
task build       # build ./dist/refresh (CGO_ENABLED=0)
task test        # go test ./...
task lint        # golangci-lint at the CI-pinned version
task dev:full    # fmt, vet, lint, tidy, deadcode, govulncheck, docs, race tests, build (run before pushing)
task fuzz        # run every fuzz target for 30s (FUZZTIME=2m to change)
task docs:gen    # regenerate docs/reference and docs/schema from the CLI
```

The code is layered, with dependency injection for testing:
command, runner, factory, service, view. See [`CLAUDE.md`](CLAUDE.md) for the
architecture and conventions.

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
- `cluster describe` shows deletion protection, secrets encryption, control-plane
  logging, and the cluster IAM role.
- Mutating calls carry an idempotency token (`ClientRequestToken`), so a
  retried request cannot start a second update.
- When AWS denies a call, the error names the missing IAM action.

## License

Released under the [MIT License](LICENSE).
