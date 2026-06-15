# Refresh

[![CI](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/test.yml?branch=main&style=flat-square&label=CI)](https://github.com/dantech2000/refresh/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![codecov](https://codecov.io/gh/dantech2000/refresh/branch/main/graph/badge.svg?style=flat-square)](https://codecov.io/gh/dantech2000/refresh)
[![Latest release](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![Docs](https://img.shields.io/badge/docs-drod.dev%2Frefresh-blue?style=flat-square)](https://drod.dev/refresh/)

**The EKS upgrade companion.** A Go CLI for the Kubernetes patching and upgrade
lifecycle on AWS EKS: **status → readiness → patch → upgrade**. Built around a
safety story — pre-flight health gates, dry-run previews, and live progress
monitoring — so you can keep a fleet current without surprises.

The core loop:

1. **`refresh status`** — fleet patch posture: what's stale, version skew, and
   extended-support exposure across every cluster and region.
2. **`refresh cluster upgrade-check`** — upgrade readiness: EKS Cluster Insights
   plus a local version-skew picture (read-only, mutates nothing).
3. **`refresh nodegroup update` / `refresh addon update`** — patch safely with
   health gates, dry-run, and real-time monitoring.
4. **`refresh cluster upgrade`** — orchestrate a full upgrade: control plane →
   addons → nodegroups, with a health gate after every phase.

Fast `list`/`describe` for clusters, nodegroups, and add-ons rounds out the
day-to-day workflow.

## Features

- **Pre-flight health checks** validate cluster readiness before a roll using
  default EC2 metrics — no Container Insights or extra setup required.
- **Live node-roll view** (`nodegroup update --live`) shows nodes draining,
  terminating, and coming online in real time from live Kubernetes state.
  Degrades to standard monitoring when a cluster isn't reachable; EKS stays
  authoritative.
- **Fleet status** (`refresh status -A`) reports version, EKS support window
  (with extended-support cost), stale AMIs, and addons-behind across all
  clusters/regions, with CI-friendly exit codes.
- **Fleet updates** (`nodegroup update --all-clusters`) discover clusters across
  regions and roll them serially with one batch confirmation, an aggregate
  summary, and a worst-outcome exit code — the "patch Tuesday" command.
- **Real node readiness** — with `--check-readiness`, node counts come from the
  Kubernetes API (`Ready`/desired); without it, list/describe show the desired
  count rather than a fabricated ready figure.
- **Post-roll verification** confirms nodegroups settle `ACTIVE` with no newly
  stuck pods (distinct exit code on failure; `--skip-verify` to opt out).
- **AMI changelog** — dry-run summarizes the current→target `amazon-eks-ami`
  release delta; `--changelog` prints the full notes. Degrades gracefully
  offline and never blocks the update.
- **Unattended-friendly** — idempotent mutating calls, documented exit codes, a
  JSON run summary, and fail-fast (no hanging prompts) without a TTY.
- **Custom-AMI aware** — `AmiType=CUSTOM` nodegroups are classified `Custom` and
  skipped on update with guidance instead of being mis-rolled.
- **Contexts** (kubectx-style) bind a cluster to a region/profile so you stop
  repeating `--cluster/--region/--profile`.
- **Consistent design system** — status tokens pair a glyph with a label so
  color is *additive* (legible with `--no-color`, piped, or on non-UTF-8
  terminals); truecolor degrades to 256/none by terminal capability.
- **Script-friendly output** — `-o json|yaml|plain` on list/describe commands
  (`plain` is uncolored TSV for grep/awk); `--no-color`/`NO_COLOR` honored;
  spinners auto-disable when piped.

## Requirements

**Required**

- Go 1.26+ (only to build from source)
- AWS credentials (`~/.aws/credentials`, environment variables, or IAM roles)

**Optional (enhanced features)**

- `kubectl` / a kubeconfig — for Kubernetes-backed checks: workload and Pod
  Disruption Budget validation in pre-flight health checks, `nodegroup scale
  --check-pdbs`, and real node readiness (`--check-readiness`).
- CloudWatch metrics — capacity and resource-balance health checks use default
  EC2 CPU metrics out of the box (memory requires Container Insights).

The tool works with just AWS credentials; optional features degrade gracefully
with actionable guidance.

## Installation

### Homebrew (recommended)

```bash
brew install --cask dantech2000/tap/refresh
refresh version
```

`refresh` is distributed as a Homebrew **Cask** (since v0.2.2).

### Pre-built binaries

Download from the [latest release](https://github.com/dantech2000/refresh/releases/latest)
for macOS (Intel/Apple Silicon), Linux, or Windows, then extract onto your `PATH`:

```bash
tar -xzf refresh_*_darwin_arm64.tar.gz
sudo mv refresh /usr/local/bin/ && chmod +x /usr/local/bin/refresh
```

### From source

```bash
go install github.com/dantech2000/refresh@latest
```

### Updating

```bash
brew upgrade --cask refresh          # Homebrew
go install github.com/dantech2000/refresh@latest   # go install
```

## Quickstart

> Coming from eksctl or `aws eks`? See the [migration guide](docs/migration.md).
> **The full command & flag reference, guides, and examples live at
> [drod.dev/refresh](https://drod.dev/refresh/)** — or run `refresh <command> --help`.

The core loop is `status → readiness → patch → upgrade`:

```bash
# 1) What's stale across the fleet? (all regions; CI-friendly exit codes)
refresh status -A

# 2) Am I ready to upgrade? (read-only: EKS Cluster Insights + version skew)
refresh cluster upgrade-check -c prod

# 3) Patch safely — preview first, then roll with health gates + a live view
refresh nodegroup update -c prod --dry-run
refresh nodegroup update -c prod

# 4) Orchestrate a full upgrade (control plane → addons → nodegroups, gated)
refresh cluster upgrade -c prod --to 1.33
```

**Contexts** (kubectx-style) bind a cluster to a region/profile so you stop
repeating `--cluster/--region/--profile`:

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
refresh use prod          # later commands target prod; `refresh use -` toggles back
```

Every `list`/`describe` command supports `-o table|json|yaml|plain` (`plain` is
uncolored TSV for grep/awk); `--no-color`/`NO_COLOR` are honored and spinners
auto-disable when piped. The full, always-current reference lives at
[drod.dev/refresh](https://drod.dev/refresh/) and under
[`docs/reference/`](docs/reference/).


## Health checks

Pre-flight health checks validate cluster readiness before a roll using **default
AWS metrics** (no extra setup). They run automatically before `nodegroup update`;
`--health-only` runs them without updating, `--skip-health-check` bypasses them.

**Out of the box (default AWS metrics)**

- **Node health** — nodegroups `ACTIVE`; real Ready counts when a kubeconfig is
  available.
- **Cluster capacity** — sufficient CPU headroom from default EC2 metrics.
- **Resource balance** — CPU distribution across nodes.

**Requires cluster/kubeconfig access**

- **Critical workloads** — kube-system pods running.
- **Pod Disruption Budgets** — missing PDBs for user workloads (also surfaced by
  `nodegroup scale --check-pdbs`).

When optional services aren't available the affected check is clearly marked
skipped, with guidance, rather than silently degraded. Pass `--kubeconfig` to
point the Kubernetes-backed checks at a specific cluster; an unreachable cluster
prints which kubeconfig/context was tried.

## Development

```bash
task build          # build the binary (CGO_ENABLED=0)
task test           # go test ./...
task lint           # golangci-lint run ./...
task dev:full       # fmt + vet + lint + test + build (run before pushing)
task docs:gen       # regenerate docs/reference from the CLI tree
```

The codebase is layered with dependency injection for testability: `command →
runner → factory → service → view`. See [`CLAUDE.md`](CLAUDE.md) for the
architecture tour and conventions.

## Release process

Releases are automated with [release-please](https://github.com/googleapis/release-please)
and [GoReleaser](https://goreleaser.com):

1. Land changes with [Conventional Commit](https://www.conventionalcommits.org)
   messages (`feat:`, `fix:`, `docs:`, …).
2. release-please maintains a **release PR** that bumps the version and updates
   the changelog from those commits.
3. Merging the release PR tags the release, which triggers GoReleaser to build
   and sign cross-platform binaries (SBOM + cosign), publish the GitHub release,
   and update the Homebrew cask in `dantech2000/homebrew-tap`.

The version is injected at build time via ldflags — there is no version constant
to bump by hand. Validate the release config locally with `task release:test` or
`task release:dry`.

## Security

- Never logs or stores credentials; sanitizes input parameters.
- Surfaces EKS cluster deletion-protection status, encryption, logging, and IAM
  role configuration in `cluster describe`.
- Mutating calls are idempotent (`ClientRequestToken`); AWS errors are formatted
  with the missing IAM action when permission is denied.

## License

Released under the [MIT License](LICENSE).
