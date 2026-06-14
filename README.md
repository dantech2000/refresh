# Refresh

[![CI](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/test.yml?branch=main&style=flat-square&label=CI)](https://github.com/dantech2000/refresh/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![codecov](https://codecov.io/gh/dantech2000/refresh/branch/main/graph/badge.svg?style=flat-square)](https://codecov.io/gh/dantech2000/refresh)
[![Latest release](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)

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

## Usage

> Coming from eksctl or `aws eks`? See the
> [migration guide](docs/migration.md) for a command-by-command cheat-sheet.
> Full per-command flag reference lives in [`docs/reference/`](docs/reference/)
> and via `refresh <command> --help`.

### Command structure

```text
refresh
├── status                  # Fleet patch posture across clusters/regions (front door)
├── cluster
│   ├── list                # List clusters across regions
│   ├── describe (get)      # Comprehensive cluster info
│   ├── upgrade-check       # Upgrade readiness: Cluster Insights + version skew (read-only)
│   └── upgrade             # Orchestrate a full upgrade (control plane → addons → nodegroups)
├── nodegroup (ng)
│   ├── list                # List nodegroups with AMI status
│   ├── describe (get)      # Nodegroup details
│   ├── scale               # Scale desired/min/max with optional health checks
│   └── update (update-ami) # Roll nodegroups to the latest recommended AMI
├── addon
│   ├── list                # List cluster add-ons
│   ├── describe (get)      # Add-on details
│   └── update [--all]      # Update one add-on, or every add-on with --all
├── use [name|-]            # Switch the active context (kubectx-style)
├── current                 # Print the active context
├── context (ctx)           # Manage saved contexts (list, add, remove)
├── version                 # Version info
├── completion              # Shell completion (bash, zsh, fish)
└── install-man             # Install the man page
```

Flags may appear before or after positional arguments. Most commands take the
cluster as a positional or via `--cluster/-c`; once a context is active, you can
omit it entirely.

### Contexts (kubectx-style)

Stop passing `--cluster`, `--region`, and `--profile` on every invocation. Save
a named context once, then switch with `refresh use`.

```bash
# Save contexts (binds cluster + optional region/profile)
refresh context add prod  --cluster prod-eks  --region us-east-1 --profile prod
refresh context add stage --cluster stage-eks --region us-west-2 --profile stage

# Switch
refresh use prod          # set prod active; later commands target it
refresh use -             # toggle back to the previous context
refresh use               # no name: interactive picker
refresh current           # print the active context
refresh context list      # list all; the active one is marked with *

# Per-shell override without changing the saved default
REFRESH_CONTEXT=stage refresh nodegroup list
```

**Resolution order** (highest wins): explicit CLI flag → `REFRESH_CONTEXT` env →
active saved context → AWS SDK defaults (`AWS_REGION`/`AWS_PROFILE`) → kubeconfig
current context (cluster name only). Contexts are stored at
`$XDG_CONFIG_HOME/refresh/context.yaml` (default `~/.config/refresh/context.yaml`).

### Fleet status (`refresh status`)

The Monday-morning command: one table showing, per cluster, the Kubernetes
version, EKS support window, nodegroup AMI staleness, and addons behind latest —
across every region.

```bash
refresh status                              # clusters in the current region
refresh status -A                           # all EKS-supported regions
refresh status -r us-east-1 -r us-west-2    # specific regions
refresh status prod                         # only clusters matching "prod"
refresh status -A -o json                   # machine-readable for CI
refresh status -A --sort stale --desc       # most-stale clusters first
```

```text
FLEET  2 clusters · 1 region(s)

● 0 current   ▲ 2 need attention

   CLUSTER           REGION     VERSION  SUPPORT          COMPUTE       STALE AMI  ADDONS
▲  development-blue  us-east-1  1.34     standard (170d)  6 nodegroups  0          ▲ 6 (aws-ebs-csi-driver…)
▲  staging-blue      us-east-1  1.34     standard (170d)  3 nodegroups  0          ▲ 6 (aws-ebs-csi-driver…)

2 clusters · 0 stale nodegroups · 12 addons behind · 0 extended/unsupported
```

**Exit codes** (for CI/cron): `0` everything current and in standard support ·
`2` something stale (nodegroup AMI or addon behind latest) · `3` a cluster is on
extended or unsupported.

### Cluster management

```bash
# List clusters
refresh cluster list                      # current region
refresh cluster list -A -H                # all regions, with health
refresh cluster list -r us-east-1 -r eu-west-1
refresh cluster list -A -C 4              # cap concurrent region queries
refresh cluster list dev                  # filter by name pattern (positional)
refresh cluster list --filter status=ACTIVE
refresh cluster list -o tree              # region/cluster hierarchy
refresh cluster list --sort version
refresh cluster list --watch --watch-interval 5s

# Describe a cluster
refresh cluster describe staging-blue              # positional cluster
refresh cluster describe -c prod --detailed
refresh cluster describe -c prod --check-readiness # real Ready/desired node counts
refresh cluster describe -c prod -o json
```

```text
staging-blue   ● ACTIVE   us-east-1

▸ OVERVIEW
  version     1.34 · eks.16
  status      ● ACTIVE
  endpoint    https://ABC123.gr7.us-east-1.eks.amazonaws.com
  vpc         vpc-0970eee5 (10.0.0.0/16)
  encryption  ● enabled (KMS)
  created     2023-02-09

▸ NODEGROUPS  3 active · 6 nodes
  NAME        INSTANCE     NODES  STATUS
  groupC      m5.2xlarge   2      ● ACTIVE
  groupD      m5.2xlarge   3      ● ACTIVE
  monolithB   m5.2xlarge   1      ● ACTIVE

▸ ADD-ONS  6 installed
  …
```

> By default the `NODES` column shows the **desired** count. Add
> `--check-readiness` (with a reachable kubeconfig) to show measured
> `Ready/desired`; when the cluster API is unreachable it stays at the desired
> count rather than reporting a fabricated ready figure.

### Upgrade readiness (`refresh cluster upgrade-check`)

A read-only pre-flight before an upgrade — the same **EKS Cluster Insights** the
console shows, plus a local **version-skew** picture (control plane vs each
managed nodegroup, and installed addons vs the latest compatible version) with
ordered, actionable findings. Nothing is mutated.

```bash
refresh cluster upgrade-check -c prod-east
refresh cluster upgrade-check -c prod-east --show-passing   # include PASSING insights
refresh cluster upgrade-check -c prod-east --status ERROR   # filter by status
refresh cluster upgrade-check -c prod-east -o json          # machine-readable
refresh cluster upgrade-check -c prod-east --id <insight-id> # detail view
```

```text
UPGRADE READINESS  development-blue   ▲ REVIEW

▸ INSIGHTS  0
  ● no upgrade insights to address

▸ VERSION SKEW  control plane 1.34
  ▲ addon aws-ebs-csi-driver is behind latest compatible (v1.59.0-eksbuild.1 → v1.61.1-eksbuild.1)
  ▲ addon coredns is behind latest compatible (v1.13.2-eksbuild.7 → v1.13.2-eksbuild.10)
  ▲ addon kube-proxy is behind latest compatible (v1.34.6-eksbuild.5 → v1.34.6-eksbuild.11)
```

PASSING insights are hidden by default; `--category` defaults to
`UPGRADE_READINESS`. Insights are computed asynchronously by EKS, so the table
surfaces each insight's last refresh time.

### Cluster upgrade (orchestrated)

Plan and execute a full upgrade — control plane, then addons in dependency
order, then nodegroup rolls — with a health gate after every phase.

```bash
# Print the full ordered plan without mutating anything (non-zero exit if blocked)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive (CI), skipping a Helm-managed addon
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-ebs-csi-driver

# Leave specific nodegroups alone
refresh cluster upgrade -c prod-east --to 1.33 --skip-nodegroup spot-
```

- **Sequential minors** — EKS upgrades one minor at a time, so `--to 1.33` from
  1.31 expands into two hops, each with its own gates.
- **Readiness gates** — each hop checks Cluster Insights and kubelet skew before
  touching anything; blockers render in the plan and the command exits non-zero
  without mutating.
- **Resumable by re-derivation** — no state files. Rerun the same command to
  resume after a failure or Ctrl+C (in-flight EKS updates continue server-side);
  rerunning after success is a no-op.
- **Custom-AMI nodegroups** are surfaced as manual actions, never mutated.

### Nodegroup management

```bash
# List (cluster as positional or -c)
refresh nodegroup list my-cluster
refresh ng list -c my-cluster
refresh ng list my-cluster --filter amiStatus=outdated   # filter by key=value
refresh ng list my-cluster --filter name=web
refresh ng list my-cluster --check-readiness             # real Ready/desired
refresh ng list my-cluster --sort status --desc
refresh ng list my-cluster -o plain | awk '{print $1}'

# Describe (nodegroup as second positional or -n)
refresh nodegroup describe my-cluster ng-default
refresh ng describe -c prod -n web --show-instances --show-workloads

# Scale
refresh ng scale -c prod -n web --desired 10 --health-check --wait
refresh ng scale -c prod -n web --desired 3 --check-pdbs   # validate PDBs first
refresh ng scale -c dev  -n api --desired 5 --op-timeout 5m
```

```text
NODEGROUPS  my-cluster · 3

NAME                                     STATUS    INSTANCE    AMI       NODES
my-cluster-groupC-2025…                  ● ACTIVE  m5.2xlarge  ● Latest      2
my-cluster-groupD-2025…                  ● ACTIVE  m5.2xlarge  ● Latest      3
my-cluster-monolithB-2025…               ● ACTIVE  m5.2xlarge  ● Latest      1
```

#### Update AMIs

Roll nodegroups to the latest recommended AMI, with pre-flight health gates and
live monitoring (waiting for completion is the default; use `--no-wait` to
return immediately).

```bash
refresh nodegroup update -c my-cluster              # all nodegroups in the cluster
refresh ng update -c dev -n web                     # one nodegroup
refresh ng update -c prod -n api-                   # partial match (confirms first)
refresh ng update -c staging --dry-run              # preview only (-d)
refresh ng update -c staging -n web --dry-run --changelog  # + full AMI release notes
refresh ng update -c prod -f                        # force even if already latest
refresh ng update -c dev --skip-health-check        # skip pre-flight checks (-s)
refresh ng update -c dev --health-only              # run health checks only
refresh ng update -c dev --live                     # live per-node roll view

# Fleet mode: roll matching nodegroups across all discovered clusters (serial)
refresh ng update --all-clusters --dry-run
refresh ng update --all-clusters -r us-east-1 --yes

# Unattended / CI
refresh ng update -c prod --yes --require-healthy -o json
```

**Exit codes:** `0` success · `2` health warnings (`--health-only` /
`--require-healthy`) · `3` health blocked · `4` an update failed to start ·
`5` post-roll verification found issues.

### EKS add-ons

```bash
# List / describe
refresh addon list my-cluster
refresh addon list -c prod -H                      # include health
refresh addon describe my-cluster vpc-cni
refresh addon describe -c prod -a coredns -o yaml

# Update a single add-on (version is optional; defaults to latest)
refresh addon update my-cluster vpc-cni            # → latest
refresh addon update my-cluster coredns v1.11.1    # pin a version
refresh addon update my-cluster vpc-cni --health-check   # validate ACTIVE + compatibility
refresh addon update my-cluster vpc-cni --dry-run

# Update every add-on (--all)
refresh addon update my-cluster --all --dependency-order --wait
refresh addon update my-cluster --all --parallel
refresh addon update my-cluster --all --skip vpc-cni --skip kube-proxy --dry-run
```

`--dependency-order` updates in a safe order (vpc-cni → coredns/kube-proxy →
others); `--parallel` is faster but unordered (the two are mutually exclusive).
`--health-check` confirms the add-on is `ACTIVE` and version-compatible before
updating. The `--parallel/--skip/--dependency-order` flags apply only with
`--all`.

### Output formats & flags

Every `list`/`describe` command supports `-o table|json|yaml|plain`
(`-o tree` additionally on `cluster list`). Common flags: `-c/--cluster`,
`-o/--format`, `-t/--timeout`, `-h/--help`. `REFRESH_TIMEOUT`,
`REFRESH_MAX_CONCURRENCY`, and `REFRESH_EKS_REGIONS` override the corresponding
defaults. Run `refresh <command> --help` (or browse
[`docs/reference/`](docs/reference/)) for the full, always-current flag list.

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
