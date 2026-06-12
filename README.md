# Refresh

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/release.yml?style=flat-square&label=build)](https://github.com/dantech2000/refresh/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod-go-version/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/blob/main/go.mod)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/dantech2000/refresh?style=flat-square&color=yellow)](https://github.com/dantech2000/refresh/stargazers)
[![Homebrew](https://img.shields.io/badge/homebrew-available-orange?style=flat-square)](https://github.com/dantech2000/homebrew-tap)


![Alt](https://repobeats.axiom.co/api/embed/bc73e7cb2ef4f089dc943258dc6511f76ad86a35.svg "Repobeats analytics image")


A Go-based CLI tool to manage and monitor AWS EKS clusters and nodegroups with health checks, fast list/describe operations, and smart scaling.

## Architecture Overview

The refresh tool is built with clean code principles and follows Go best practices:

```
refresh/
├── main.go                    # Application entry point with CLI setup
├── internal/
│   ├── aws/                   # AWS SDK abstractions
│   │   ├── ami.go            # AMI resolution with thread-safe caching
│   │   ├── cluster.go        # Cluster discovery, name resolution, pattern matching
│   │   ├── nodegroup.go      # Nodegroup operations
│   │   └── errors.go         # AWS error handling and formatting
│   ├── awsconfig/            # Unified AWS config loading
│   │   └── awsconfig.go      # Merges CLI flags, context, and SDK defaults
│   ├── cliconfig/            # Persistent named contexts
│   │   └── store.go          # YAML-backed context store (~/.config/refresh/context.yaml)
│   ├── commands/             # CLI command implementations
│   │   ├── cluster/          # Cluster commands (list, describe/get, compare)
│   │   ├── nodegroup/        # Nodegroup commands (list, describe/get, scale, update)
│   │   ├── addon/            # Add-on commands (list, describe/get, update [--all])
│   │   ├── ctxcmd/           # Context commands (use, current, context add/list/remove)
│   │   ├── workload/         # Workload commands (pdbs)
│   │   ├── runner/           # Shared command primitives (setup, positionals, encoding)
│   │   ├── clusterview/      # Cluster table/tree formatters
│   │   └── factory/          # Service constructors
│   ├── config/               # Shared constants and region helpers
│   ├── mocks/                # Test doubles for AWS client interfaces
│   │   ├── eksapi.go         # Configurable EKSAPI mock (function-field pattern)
│   │   └── builders.go       # Fluent builder for common mock scenarios
│   ├── types/                # Core domain types
│   │   ├── models.go         # NodegroupInfo, UpdateResult, BatchUpdateResult
│   │   ├── monitoring.go     # UpdateProgress, ProgressMonitor, MonitorConfig
│   │   └── status.go         # AMIStatus, DryRunAction (typed enums; plain String, ColorString for display)
│   ├── services/             # Business logic layer
│   │   ├── cluster/          # Cluster service with caching
│   │   ├── nodegroup/        # Nodegroup service with utilization and cost analysis
│   │   │   ├── cost_analyzer.go  # On-demand price estimation via Pricing API
│   │   │   ├── utilization.go    # CloudWatch CPU metrics (batched, cached)
│   │   │   └── ...
│   │   ├── addons/           # Add-on management
│   │   │   ├── health_check.go      # Pre-update health validation and version compatibility
│   │   │   ├── version_analyzer.go  # Available version resolution and comparison
│   │   │   ├── addon_dependencies.go # Dependency ordering for bulk updates
│   │   │   └── ...
│   │   └── common/           # Shared utilities (retry/backoff logic)
│   ├── health/               # Pre-flight health checks
│   │   ├── checker.go        # Health check orchestrator
│   │   ├── nodes.go          # Node health validation
│   │   ├── workloads.go      # Critical workload checks
│   │   └── ...               # Additional health modules
│   ├── monitoring/           # Update progress tracking
│   │   ├── monitor.go        # Concurrent monitoring with channels
│   │   └── display.go        # Progress display formatting
│   ├── dryrun/               # Dry-run mode
│   │   └── dryrun.go         # Preview updates without changes
│   └── ui/                   # Terminal UI components
│       ├── ansi.go           # ANSI-aware width/pad/truncate + status colors
│       ├── ptable.go         # pterm-based table with ANSI-aware columns
│       ├── dynamic_table.go  # Aligned key/value display
│       ├── fun_spinner.go    # Category-aware rotating message spinners (TTY-gated)
│       ├── tree.go           # Tree view for multi-region cluster display
│       └── ...               # Additional UI utilities
└── go.mod                    # Go module dependencies
```

### Key Design Patterns

- **Channel-based Concurrency**: Used in monitoring for concurrent status checks; list operations fan out per-item AWS calls with bounded concurrency
- **Clean Error Handling**: AWS errors are classified by typed error code and formatted with user-friendly messages
- **Dependency Injection**: Services use interfaces for testability
- **Graceful Cancellation**: Update monitoring handles SIGINT/SIGTERM cleanly (updates keep running in AWS)

## Features

-   **Pre-flight Health Checks**: Validate cluster readiness before AMI updates using default EC2 metrics (no additional setup required)
-   **Real-time Monitoring**: Live progress tracking with professional spinner displays and clean completion summaries
-   **Cluster Management**: List clusters and nodegroups with status and versions
-   **Smart Updates**: Update AMI for all or specific nodegroups with rolling updates and optional force mode
-   **Nodegroup Intelligence**: Fast list/describe with optional utilization and cost, and safe scaling with health checks
-   **Security Visibility**: Display cluster deletion protection status and security configuration details
-   **Dry Run Mode**: Preview changes with comprehensive details before execution
-   **Short Flags**: Convenient short flags for all commands (`-c`, `-n`, `-d`, `-f`, etc.)
-   **Enhanced UI**: Color-coded output with perfect table alignment and clear status indicators
-   **Graceful Degradation**: Works with just AWS credentials, provides clear guidance for optional features
-   **Timeouts Everywhere**: Global and per-command `--timeout, -t` to avoid hangs on slow networks (default 60s)
-   **Multi-Region Discovery**: `cluster list` supports `-A/--all-regions` with a concurrency cap (`-C/--max-concurrency`)
-   **Accurate Node Readiness**: Uses Kubernetes API to compute actual ready node counts when kubeconfig is available
-   **Explicit kubeconfig**: `nodegroup update`/`scale` accept `--kubeconfig` for the workload/PDB health checks; an unreachable cluster prints an actionable diagnostic (which kubeconfig/context was tried) and the kube-dependent checks are clearly skipped rather than silently degraded
-   **Sorting Options**: Sort cluster and nodegroup lists with `--sort` and `--desc`
-   **Interactive Cluster Selection**: `cluster diff` auto-prompts a multi-select picker when a pattern matches multiple clusters
-   **Add-on Health Checks**: `addon update --health-check` validates active state and Kubernetes version compatibility before updating
-   **Shell Completion**: `refresh completion bash|zsh|fish` generates completion scripts; `refresh use <TAB>` completes saved context names
-   **Script-friendly Output**: `-o json|yaml|plain` on list/describe commands (`plain` is tab-separated and uncolored for grep/awk), `--no-color` (and `NO_COLOR`), spinners auto-disable when output is piped, `nodegroup update --health-only` exits 0/2/3 for pass/warn/block and supports `-o json|yaml`
-   **Watch Mode**: `cluster list --watch` / `nodegroup list --watch` redraw on an interval (top-style on a TTY, append-only when piped)

## Requirements

### Required (Core Functionality)
-   Go 1.26+
-   AWS credentials (`~/.aws/credentials`, environment variables, or IAM roles)

### Optional (Enhanced Features)
-   `kubectl` and kubeconfig (`~/.kube/config`) - for Kubernetes workload validation (Workloads/PDB currently experimental)
-   CloudWatch metrics - for utilization (CPU supported now; memory via Container Insights in future)
-   AWS Pricing API permissions - for on-demand cost estimates

> **Note**: The tool works with just AWS credentials! Health checks use default EC2 metrics and provide clear guidance for enabling optional features.

### Cost Estimation

The `--show-costs` flag displays estimated monthly costs for nodegroups. Here's how costs are calculated:

**Calculation Method:**
```
Monthly Cost = (Hourly On-Demand Price) x 730 hours x (Number of Nodes)
```

**Data Source:**
- Prices are fetched from the **AWS Pricing API** in real-time
- Queries use your cluster's region to get region-specific pricing
- Fallback to static price map when API is unavailable

**Example:**
```
t3a.large in us-west-2: $0.0752/hour
9 nodes x $0.0752 x 730 hours = $494/month
```

**Limitations:**
- Does not include EBS storage, data transfer, or other EC2-related costs
- Pricing API requires `pricing:GetProducts` IAM permission

**Required IAM Permissions:**
```json
{
  "Effect": "Allow",
  "Action": [
    "pricing:GetProducts"
  ],
  "Resource": "*"
}
```

## Installation

### Homebrew (Recommended)

The easiest way to install `refresh` is via Homebrew:

```bash
# Add the tap
brew tap dantech2000/tap

# Install refresh (as a cask)
brew install --cask refresh

# Verify installation
refresh version
```

**Note:** Starting with v0.2.2, `refresh` is distributed as a Homebrew Cask instead of a Formula.

### Download from Releases

Alternatively, download pre-built binaries from the [releases page](https://github.com/dantech2000/refresh/releases/latest):

1. Go to the [latest release](https://github.com/dantech2000/refresh/releases/latest)
2. Download the appropriate binary for your platform:
   - `refresh_vX.Y.Z_darwin_amd64.tar.gz` (macOS Intel)
   - `refresh_vX.Y.Z_darwin_arm64.tar.gz` (macOS Apple Silicon)
   - `refresh_vX.Y.Z_linux_amd64.tar.gz` (Linux x64)
   - `refresh_vX.Y.Z_windows_amd64.tar.gz` (Windows x64)
3. Extract and move to your PATH:
   ```bash
   # Example for macOS Apple Silicon
   tar -xzf refresh_vX.Y.Z_darwin_arm64.tar.gz
   sudo mv refresh /usr/local/bin/
   chmod +x /usr/local/bin/refresh
   ```

### Build from Source

If you have Go installed:

```bash
# Clone the repository
git clone https://github.com/dantech2000/refresh.git
cd refresh

# Build and install
go build -o refresh .
sudo mv refresh /usr/local/bin/

# Or install directly
go install github.com/dantech2000/refresh@latest
```

### Verify Installation

After installation, verify it works:

```bash
refresh version
refresh --help
```

You should see output showing the version and available commands.

### Updating

To update to the latest version:

```bash
# If installed via Homebrew Cask
brew update && brew upgrade --cask refresh

# If installed via go install
go install github.com/dantech2000/refresh@latest

# If manually installed, download the latest release and replace the binary
```

## Usage

### Command Structure

```text
refresh
├── cluster
│   ├── list (lc)          # List clusters across regions
│   ├── describe / get     # Describe comprehensive cluster info
│   └── diff (compare)     # Compare cluster configurations
├── nodegroup (ng)
│   ├── list               # List nodegroups with AMI status
│   ├── describe / get     # Describe nodegroup details
│   ├── update             # Update nodegroup AMI version (alias: update-ami)
│   └── scale              # Scale nodegroup with health checks
├── addon
│   ├── list               # List cluster add-ons
│   ├── describe / get     # Describe add-on details
│   └── update [--all]     # Update one or every add-on (--all replaces update-all)
├── use [name|-]           # Switch the active context (kubectx-style)
├── current                # Print the active context
├── context (ctx)          # Manage saved contexts (list, add, remove)
└── version                # Show version information
```

### Contexts (kubectx-style)

Stop passing `--cluster`, `--region`, and `--profile` on every invocation. Save
a named context once, then switch between them with `refresh use`.

```bash
# Save contexts
refresh context add --cluster prod-eks --region us-east-1 --profile prod-admin prod
refresh context add --cluster stg-eks  --region us-west-2                       staging

# Switch
refresh use prod          # set prod as active
refresh use -             # swap back to the previous context
refresh use               # interactive picker
refresh current           # print active: prod  cluster=prod-eks  region=us-east-1
refresh context list      # show all; the active one is marked with *

# Per-shell override without changing the global default
REFRESH_CONTEXT=staging refresh nodegroup list
```

Once a context is active, every command picks up its cluster, region, and
profile automatically:

```bash
refresh use prod
refresh nodegroup list           # uses prod-eks in us-east-1 with prod-admin profile
refresh addon update --all       # same context, no flags needed
```

**Resolution order** (highest wins):

1. Explicit CLI flag (`--cluster`, `--region`, `--profile`)
2. `REFRESH_CONTEXT=<name>` env var (per-shell override)
3. Active context from `~/.config/refresh/context.yaml`
4. AWS SDK defaults (`AWS_REGION`, `AWS_PROFILE`, `~/.aws/config`)
5. Kubeconfig current context (cluster name only)

The context file lives at `$XDG_CONFIG_HOME/refresh/context.yaml`
(default `~/.config/refresh/context.yaml`). Override the location with
`REFRESH_CONFIG_HOME`.

### Cluster Management

#### List Clusters (Multi-Region)

Quickly discover EKS clusters across one or many regions with health status:

```bash
# List clusters in default region
refresh cluster list
refresh lc                              # Short alias

# List clusters in specific regions
refresh cluster list -r us-east-1 -r eu-west-1

# List across all supported regions with health checks
refresh cluster list -A -H -t 30s
refresh lc -A -H                        # Short alias

# Control concurrency to avoid throttling
refresh cluster list -A -C 4

# Machine-readable output
refresh cluster list -o json
refresh cluster list -o yaml

# Sorting
refresh cluster list --sort version
refresh cluster list --sort region --desc
```

**Environment Variables:**
- `REFRESH_TIMEOUT` - Override default timeout (e.g., `30s`)
- `REFRESH_MAX_CONCURRENCY` - Control concurrent region queries
- `REFRESH_EKS_REGIONS` - Override regions for `-A` flag (e.g., `us-east-1,eu-west-1`)

#### Describe Cluster

View comprehensive cluster information including security, networking, and add-ons:

```bash
# Basic cluster information
refresh cluster describe staging-blue
refresh dc staging-blue                 # Short alias

# Detailed view with all sections
refresh cluster describe development-blue --detailed

# Include specific information
refresh dc -c prod --show-security --include-addons -H

# Different output formats
refresh dc -c staging -o json
refresh dc -c prod -o yaml
```

**Example Output:**

```
Cluster: staging-blue
Status              │ Active
Version             │ 1.32
Platform            │ eks.16
Health              │ WARN (2 issues)
VPC                 │ vpc-0970eee532cb9987e
Encryption          │ ENABLED (at rest via KMS)
Deletion Protection │ ENABLED
Created             │ 2023-02-09 04:33:25 UTC
```

#### Compare Clusters

Compare configurations across multiple clusters for consistency validation:

```bash
# Basic comparison (exact names)
refresh cluster diff -c dev -c prod
refresh cc -c dev -c prod              # Short alias

# Show only differences
refresh cluster diff -c dev -c staging --show-differences

# Focus on specific aspects
refresh cc -c prod-east -c prod-west -i networking -i security

# Different output formats
refresh cluster diff -c dev -c prod -o json
```

**Interactive cluster selection** — when a pattern matches more than one cluster,
the tool automatically launches a multi-select picker. You can also trigger it
explicitly with `--interactive` to choose from all matching candidates:

```bash
# "prod" matches prod-east, prod-west, prod-central → interactive picker appears
refresh cluster diff --cluster prod

# Force the picker even when patterns are unambiguous
refresh cluster diff -c prod -c staging --interactive
```

The picker lets you space-toggle clusters and confirm with Enter. At least 2 must
be selected for the comparison to proceed.

#### Upgrade Cluster (Orchestrated)

Plan and execute a full cluster upgrade — control plane, then addons in
dependency order (versions compatible with the *target* Kubernetes version),
then nodegroup rolls — with a health gate after every phase:

```bash
# Print the full ordered plan without mutating anything
# (exits non-zero if anything blocks the upgrade)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive (CI) run, skipping a Helm-managed addon
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-ebs-csi-driver

# Leave specific nodegroups alone
refresh cluster upgrade -c prod-east --to 1.33 --skip-nodegroup spot-
```

Key behaviors:

- **Sequential minors** — EKS upgrades one minor at a time, so `--to 1.33` from
  1.31 expands into two hops (1.32, then 1.33), each with its own gates.
- **Readiness gates** — each hop checks EKS Cluster Insights (upgrade
  readiness) and kubelet version skew before touching anything; blockers
  render in the plan and the command exits non-zero without mutating.
- **Resumable by re-derivation** — no state files. Rerunning the same command
  re-inspects the cluster and skips already-satisfied steps, so resuming after
  a failure or Ctrl+C (in-flight EKS updates continue server-side) is just
  running the command again; rerunning after success is a no-op.
- **Custom-AMI nodegroups** are surfaced as manual actions, never mutated.

### Nodegroup Management

#### List Nodegroups

List all managed nodegroups in a cluster with AMI status:

```bash
# List all nodegroups
refresh nodegroup list -c <cluster>
refresh ng list -c <cluster>           # Short alias

# Filter by partial name matching
refresh nodegroup list -c dev -n web
refresh ng list -c dev -n api

# Include utilization and costs (24h window default)
refresh ng list --show-utilization --show-costs -c prod
refresh ng list -U -C -c prod          # Short flags

# Sorting
refresh ng list --sort cpu --desc -c prod
refresh ng list --sort cost -c prod
refresh ng list --sort instance -c prod
```

**Sort Keys:**
- `name`, `status`, `instance`, `nodes`, `cpu`, `cost`

Flags may appear before or after positional arguments — both forms work:
```bash
refresh ng list --show-costs -c prod
refresh ng list -c prod --show-costs
```

#### Describe Nodegroup

Get detailed information about a specific nodegroup:

```bash
# Basic nodegroup description
refresh nodegroup describe -c <cluster> -n <nodegroup>
refresh ng describe -c dev -n api      # Short alias

# Include utilization, costs, and instances
refresh ng describe -c prod -n web --show-utilization --show-costs --show-instances --timeframe 24h

# Different output formats
refresh ng describe -c dev -n api -o json
```

#### Update AMI

Trigger rolling updates to the latest AMI for nodegroups:

```bash
# Update all nodegroups in a cluster
refresh nodegroup update -c <cluster>

# Update specific nodegroup
refresh ng update -c dev -n web

# Update with partial name matching (confirms before proceeding)
refresh ng update -c prod -n api-

# Preview changes without executing (dry run)
refresh ng update -c staging -n web -d
refresh ng update -c staging --dry-run

# Force update (even if already latest)
refresh ng update -c prod -f

# Skip health checks
refresh ng update -c dev -s

# Quiet mode (minimal output)
refresh ng update -c prod -q
```

**Example Output:**

```
# Single nodegroup update
$ refresh ng update -c development-blue -n groupF
Updating nodegroup dev-blue-groupF-20230815230923929900000007...
Update started for nodegroup dev-blue-groupF-20230815230923929900000007

# Multiple matches with confirmation
$ refresh ng update -c development-blue -n group
Multiple nodegroups match pattern 'group':
  1) dev-blue-groupD-20230814214633237700000007
  2) dev-blue-groupE-20230815204000720600000007
  3) dev-blue-groupF-20230815230923929900000007
Update all 3 matching nodegroups? (y/N): y
```

#### Scale Nodegroup

Scale nodegroups with health checks and monitoring:

```bash
# Scale with health checks and wait for completion
refresh nodegroup scale -c prod -n web --desired 10 --health-check --wait

# Scale with custom timeout
refresh ng scale -c dev -n api --desired 5 --op-timeout 5m
```

### EKS Add-ons Management

Manage cluster add-ons with direct API integration:

```bash
# List all add-ons for a cluster
refresh addon list <cluster>
refresh addon list -c <cluster> -H     # Include health status

# Describe specific add-on
refresh addon describe <cluster> vpc-cni
refresh addon describe -c prod -a vpc-cni -o yaml

# Update add-on to latest version
refresh addon update <cluster> vpc-cni latest
refresh addon update -c prod -a vpc-cni --version latest

# Validate health and k8s version compatibility before updating
refresh addon update -c prod -a vpc-cni --health-check
```

**`--health-check` flag** (`-H`) runs two pre-update validations:
1. Confirms the add-on is `ACTIVE` — refuses if it is `CREATING` or `UPDATING`
2. Validates the target version is compatible with the cluster's Kubernetes version

Add-ons in `DEGRADED` state are still allowed to update so you can remediate broken
add-ons. If the versions API is unreachable the check is skipped gracefully.

#### Update All Add-ons

Bulk update all cluster add-ons to their latest compatible versions:

```bash
# Update all add-ons
refresh addon update --all -c prod

# Dry-run to preview updates
refresh addon update --all -c staging --dry-run

# Validate health before each update
refresh addon update --all -c prod --health-check

# Update in parallel for faster execution
refresh addon update --all -c dev --parallel

# Wait for all updates to complete
refresh addon update --all -c prod --wait

# Skip specific add-ons
refresh addon update --all -c prod --skip vpc-cni --skip kube-proxy
```

**Update Modes:**
- **Sequential** (default): Updates add-ons one at a time, safer for production
- **Parallel**: Updates all add-ons simultaneously, faster but higher risk

### Partial Name Matching

Both `--cluster` and `--nodegroup` flags support partial name matching:

**Cluster Matching:**
```bash
refresh cluster list -c dev          # Matches development-blue, dev-staging, etc.
refresh cluster list -c blue         # Matches development-blue, staging-blue, etc.
```

**Nodegroup Matching:**
```bash
refresh ng list -c dev -n web        # Matches nodegroups containing "web"
refresh ng list -c dev -n 20230815   # Matches nodegroups created on that date
```

When multiple items match, the tool shows all matches and asks for confirmation before proceeding. For `cluster diff`, a multi-select picker is shown so you can choose 2 or more clusters to compare in a single step.

### Man Page Documentation

The refresh CLI includes built-in man page generation and installation for comprehensive offline documentation:

```bash
# Install man page to user directory (no sudo required)
refresh install-man
refresh install-manpage              # Alternative alias

# View installed man page
man refresh
```

**Installation Behavior:**
- Automatically installs to user-writable directories (`$HOME/.local/share/man/man1`)
- Works on macOS, Linux, and other Unix-like systems without sudo
- Man page is automatically discoverable via standard MANPATH
- Creates directory structure if it doesn't exist

## Health Checks

The refresh tool includes comprehensive pre-flight health checks that validate cluster readiness before AMI updates using **default AWS metrics** (no additional setup required).

### Health Check Commands

```bash
# Run health check only (no update)
refresh ng update -c dev -H
refresh ng update --cluster dev --health-only

# Update with health checks (default behavior)
refresh ng update -c dev
refresh ng update --cluster dev

# Skip health checks
refresh ng update -c dev -s
refresh ng update --cluster dev --skip-health-check

# Force update (bypasses health checks)
refresh ng update -c dev -f
refresh ng update --cluster dev --force
```

### Sample Health Check Output

```
Cluster Health Assessment:

[████████████████████] Node Health          PASS
[████████████████████] Cluster Capacity     PASS
[████████████████████] Critical Workloads   PASS
[██████████████████▒▒] Pod Disruption Budgets WARN
[█████████████████▒▒▒] Resource Balance     WARN

Status: READY WITH WARNINGS (2 issues found)

Using default EC2 metrics (CPU only)
Memory metrics require Container Insights setup

Details:
- Average CPU utilization: 45.2%
- CPU distribution and utilization within acceptable ranges
- 9 deployments missing PDBs

Warnings:
- 9 deployments missing PDBs
- Moderate CPU utilization detected
```

### Health Check Categories

**Works Out-of-the-Box (Default AWS Metrics)**:
- **Node Health**: All nodegroups must be in ACTIVE status (EKS API)
- **Cluster Capacity**: Sufficient CPU resources using default EC2 metrics (minimum 30% headroom)
- **Resource Balance**: CPU utilization distribution across nodes using default EC2 metrics

**Requires Additional Setup**:
- **Critical Workloads**: All kube-system pods running (requires kubectl access)
- **Pod Disruption Budgets**: Missing PDBs for user workloads (requires kubectl access)
- **Memory Metrics**: Requires Container Insights or CloudWatch agent setup

### Graceful Degradation

When optional services aren't available, the tool provides helpful guidance:
- **No kubectl access**: "Install kubectl and configure cluster access to enable this check"
- **No Container Insights**: "Memory metrics require Container Insights setup"
- **Limited metrics**: "Using default EC2 metrics (CPU only)"

## CLI Short Flags

All commands support convenient short flags for faster typing:

```bash
# List with short flags
refresh ng list -c prod -n web

# Quick update with dry-run
refresh ng update -c staging -n api -d -q

# Force update with health check skip
refresh ng update -c prod -f -s

# Health check only with quiet mode
refresh ng update -c test -H -q

# Cluster operations
refresh lc -A -H                    # List all clusters with health
refresh dc -c prod -o json          # Describe cluster as JSON
refresh cc -c dev -c prod -d        # Compare clusters (differences only)
```

### Common Flags (All Commands)

- `-c, --cluster` - EKS cluster name or pattern
- `-n, --nodegroup` - Nodegroup name or pattern
- `-o, --format` - Output format (table, json, yaml)
- `-t, --timeout` - Timeout duration (e.g., 30s, 5m)

### Update AMI Flags

- `-f, --force` - Force update (even if already latest)
- `-d, --dry-run` - Preview changes without executing
- `-w, --no-wait` - Don't wait for update completion
- `-q, --quiet` - Minimal output mode
- `-s, --skip-health-check` - Skip pre-flight validation
- `-H, --health-only` - Run health checks only
- `-p, --poll-interval` - Status checking interval

### Cluster Command Flags

- `-A, --all-regions` - Query all EKS-supported regions
- `-r, --region` - Specific region(s) to query (repeatable)
- `-H, --show-health` - Include health status
- `-C, --max-concurrency` - Max concurrent region queries
- `--sort` - Sort by field (name, status, version, region)
- `--desc` - Sort in descending order

### Compare Cluster Flags

- `-d, --show-differences` - Show only differences
- `-i, --include` - Compare specific aspects (networking, security, addons, versions)
- `--interactive` - Force interactive multi-select picker for cluster selection

### Short Command Aliases

- `lc` - `cluster list`
- `dc` - `cluster describe`
- `cc` - `cluster diff`
- `ng` - `nodegroup`

## Development

### Prerequisites

- Go 1.26+
- golangci-lint (for linting)
- Task (optional, for task automation)

### Building

```bash
# Build
go build -o refresh .

# Run tests
go test ./...

# Run linter
golangci-lint run ./...

# Full development check
task dev:full
```

### Project Structure

The codebase follows clean architecture principles:

- **internal/awsconfig**: Unified AWS config loading (CLI flags > context > SDK defaults)
- **internal/cliconfig**: Persistent YAML-backed named contexts
- **internal/config**: Thread-safe configuration with singleton pattern
- **internal/types**: Core domain types with proper Go idioms
- **internal/aws**: AWS SDK abstractions with caching and error handling
- **internal/services**: Business logic layer with service interfaces
- **internal/commands**: CLI command implementations following urfave/cli best practices
- **internal/ui**: Terminal UI components using pterm

### Key Patterns Used

1. **Concurrency**: Channels for monitoring, mutexes for shared state
2. **Error Handling**: Custom error types with classification
3. **Caching**: Thread-safe caches with TTL support
4. **CLI**: Hierarchical commands with urfave/cli/v3

## Release Process

### Prerequisites

- Ensure you have push access to both repositories:
  - `dantech2000/refresh` (main repository)
  - `dantech2000/homebrew-tap` (Homebrew tap)
- GitHub Personal Access Token (`GH_PAT`) is configured in repository secrets
- GoReleaser is installed locally for testing

### Release Steps

1. **Update Version Number**

   Update the version in `internal/commands/version.go`:
   ```go
   var (
       version   = "v0.3.0" // <- Update this version
       commit    = ""
       buildDate = ""
   )
   ```

2. **Run Pre-Release Checks**
   ```bash
   # Full development check (format, lint, test, build)
   task dev:full

   # Test GoReleaser configuration
   task release:test

   # Optional: Dry run of release process (local only)
   task release:dry
   ```

3. **Validate Setup**
   ```bash
   # Check if ready for release
   task release:check
   ```

4. **Create and Push Release Tag**
   ```bash
   # Create tag and push (triggers GitHub Actions)
   task release:tag

   # Or manually:
   git tag -a v0.3.0 -m "Release v0.3.0"
   git push origin v0.3.0
   ```

5. **Monitor Release Process**

   After pushing the tag:
   - GitHub Actions will automatically trigger
   - GoReleaser will build binaries for all platforms
   - GitHub release will be created with artifacts
   - Homebrew Cask will be updated in `homebrew-tap` repository
   - Users can install with: `brew install --cask dantech2000/tap/refresh`

### Useful Task Commands

```bash
# Development workflow
task dev:quick               # Format, vet, build, test version
task dev:full                # Full check including lint and tests

# Release workflow
task release:check           # Verify ready for release
task release:test            # Test GoReleaser config (no release)
task release:dry             # Full dry run (local only)
task release:tag             # Create and push release tag

# Testing
task run:version             # Test version command
task run:help                # Show help
task run:cluster:list        # Test cluster list command
task run:ng:list             # Test nodegroup list command

# Utilities
task clean                   # Clean build artifacts
task deps                    # Download and tidy dependencies
task deps:update             # Update dependencies
```

### Post-Release Verification

After a successful release:

1. **Check GitHub Release**: Verify release appears with all artifacts
2. **Test Homebrew Installation**: Follow the [Installation instructions](#installation) to test the Homebrew tap
3. **Verify Version**: Run `refresh version` to confirm the new version is available
4. **Update Documentation**: If needed, update examples in README

### Troubleshooting

- **Build Failures**: Run `task release:test` to check GoReleaser config
- **Permission Issues**: Verify `GH_PAT` token has correct permissions
- **Version Conflicts**: Ensure version in `internal/commands/version.go` matches git tag

## Project Status & Health

The badges at the top of this README provide a quick overview of the project's health:

| Badge | What It Shows | What to Watch For |
|-------|---------------|-------------------|
| **Release** | Latest version number | New releases, version progression |
| **Build Status** | GitHub Actions workflow status | Green = builds passing, Red = build issues |
| **Go Report Card** | Code quality grade (A+ to F) | Aim for A+ rating, watch for downgrades |
| **Go Version** | Minimum Go version required | Compatibility with current Go releases |
| **License** | Project license (MIT) | License compliance information |
| **Stars** | GitHub stars count | Community interest and growth |
| **Homebrew** | Homebrew installation availability | Package distribution status |

### Quick Health Check
- **Green Build Badge** = Latest code builds successfully, releases work
- **A+ Go Report** = Code quality is excellent
- **Current Go Version** = Using modern Go features and best practices

### Dependency Management
This project includes automated dependency management:
- **Dependabot** - Automated dependency updates with security patches

### Security Updates (January 2026)
The following security vulnerabilities have been addressed by updating dependencies:
- **golang.org/x/net** updated from v0.38.0 to v0.49.0 (fixes CVE-2025-22872, CVE-2025-22870, CVE-2024-45338, and HTTP/2 CONTINUATION DoS)
- **google.golang.org/protobuf** updated to v1.36.11 (latest stable version)

## Security

-   Does not log or store credentials
-   Sanitizes input parameters
-   **Deletion Protection Visibility**: Displays EKS cluster deletion protection status in `cluster describe` command
-   **Security Configuration**: Shows encryption status, logging configuration, and IAM role information

---

**Refresh** is a production-ready CLI tool for managing AWS EKS node groups with comprehensive monitoring, dry-run capabilities, and intelligent partial name matching.
