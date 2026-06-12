# Refresh

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/release.yml?style=flat-square&label=build)](https://github.com/dantech2000/refresh/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![codecov](https://codecov.io/gh/dantech2000/refresh/branch/main/graph/badge.svg?style=flat-square)](https://codecov.io/gh/dantech2000/refresh)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod-go-version/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/blob/main/go.mod)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/dantech2000/refresh?style=flat-square&color=yellow)](https://github.com/dantech2000/refresh/stargazers)
[![Homebrew](https://img.shields.io/badge/homebrew-available-orange?style=flat-square)](https://github.com/dantech2000/homebrew-tap)
[![Docs](https://img.shields.io/badge/docs-drod.dev%2Frefresh-blue?style=flat-square)](https://drod.dev/refresh/)


![Alt](https://repobeats.axiom.co/api/embed/bc73e7cb2ef4f089dc943258dc6511f76ad86a35.svg "Repobeats analytics image")


**The EKS upgrade companion.** A Go-based CLI for the Kubernetes patching and
upgrade lifecycle on AWS EKS: **status → readiness → patch → upgrade**. Built
around a safety story — pre-flight health gates, dry-run previews, and live
progress monitoring — so you can keep a fleet current without surprises.

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

> **📖 Full documentation: [drod.dev/refresh](https://drod.dev/refresh/)** — installation,
> every command and flag (an auto-generated reference that can't drift from the binary),
> concepts, a usage cookbook, and the changelog. This README is the quick on-ramp.

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
│   │   ├── cluster/          # Cluster commands (list, describe/get, upgrade-check, upgrade)
│   │   ├── nodegroup/        # Nodegroup commands (list, describe/get, scale, update)
│   │   ├── addon/            # Add-on commands (list, describe/get, update [--all])
│   │   ├── ctxcmd/           # Context commands (use, current, context add/list/remove)
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
│   │   ├── nodegroup/        # Nodegroup service (list/describe, scale, AMI updates)
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
-   **Fleet Status**: `refresh status -A` shows patch posture (version, EKS support window + extended-support cost, stale AMIs, addons behind) across all clusters/regions, with CI-friendly exit codes
-   **Cluster Management**: List clusters and nodegroups with status and versions
-   **Smart Updates**: Update AMI for all or specific nodegroups with rolling updates and optional force mode
-   **Fleet Updates**: `nodegroup update --all-clusters [-r region ...]` discovers clusters across regions and rolls them serially (blast-radius control) with one batch confirmation, an aggregate per-cluster summary, and a worst-outcome exit code — the "patch Tuesday" command
-   **Post-roll Verification**: after a roll, confirms nodegroups are ACTIVE and no pods are newly stuck Pending (vs a pre-roll snapshot); distinct exit code 5 on issues; `--skip-verify` to opt out
-   **AMI Changelog**: dry-run shows the current→target AMI release delta and a best-effort summary of `amazon-eks-ami` release notes (kernel, containerd, CVEs); `--changelog` for full notes; degrades gracefully offline (never blocks the update)
-   **Unattended Updates**: `nodegroup update --yes --require-healthy -o json` runs in CI/cron — idempotent (ClientRequestToken), documented exit codes (0 ok / 2 warn / 3 blocked / 4 update-failed / 5 verify-failed), JSON run summary, and fail-fast (no hanging prompts) without a TTY
-   **Custom-AMI aware**: custom-AMI nodegroups (`AmiType=CUSTOM`, managed via launch template) are classified `Custom` rather than stale/current and are skipped on update with clear guidance instead of being mis-rolled
-   **Nodegroup Intelligence**: Fast list/describe and safe scaling with health checks
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
-   **Add-on Health Checks**: `addon update --health-check` validates active state and Kubernetes version compatibility before updating
-   **Shell Completion**: `refresh completion bash|zsh|fish` generates completion scripts; `refresh use <TAB>` completes saved context names
-   **Script-friendly Output**: `-o json|yaml|plain` on list/describe commands (`plain` is tab-separated and uncolored for grep/awk), `--no-color` (and `NO_COLOR`), spinners auto-disable when output is piped, `nodegroup update --health-only` exits 0/2/3 for pass/warn/block and supports `-o json|yaml`
-   **Watch Mode**: `cluster list --watch` / `nodegroup list --watch` redraw on an interval (top-style on a TTY, append-only when piped)

## Requirements

### Required (Core Functionality)
-   Go 1.26+
-   AWS credentials (`~/.aws/credentials`, environment variables, or IAM roles)

### Optional (Enhanced Features)
-   `kubectl` and kubeconfig (`~/.kube/config`) - for the Kubernetes workload and Pod Disruption Budget (PDB) checks inside pre-flight health checks and `nodegroup scale --check-pdbs`
-   CloudWatch metrics - capacity and resource-balance health checks use default EC2 CPU metrics (memory via Container Insights in future)

> **Note**: The tool works with just AWS credentials! Health checks use default EC2 metrics and provide clear guidance for enabling optional features.

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

### Other install methods

`go install`, pre-built binaries with **cosign signature verification**, shell
completion, and the man page are all covered in the
**[installation guide](https://drod.dev/refresh/getting-started/installation/)**.

Verify your install:

```bash
refresh version
```

## Usage

`refresh` follows a four-stage loop — **status → readiness → patch → upgrade**:

```bash
# 1) What's stale across the fleet?
refresh status -A

# 2) Am I ready to upgrade?
refresh cluster upgrade-check -c prod

# 3) Patch safely — preview first, then roll with health gates
refresh nodegroup update -c prod --dry-run --changelog
refresh nodegroup update -c prod --yes --require-healthy
refresh addon update --all --dependency-order --wait

# 4) Orchestrate a full cluster upgrade
refresh cluster upgrade -c prod --to 1.33
```

Contexts (kubectx-style) save you from repeating `--cluster` / `--region` / `--profile`:

```bash
refresh context add prod --cluster prod-use1 --region us-east-1 --profile prod
refresh use prod
```

Every list/describe command supports `-o table|json|yaml|plain` (and `tree` for
`cluster list`); all flags appear in `refresh <command> --help` and `man refresh`.

**For the full picture, see the documentation site:**

- [Command guide](https://drod.dev/refresh/commands/) — every command, with worked examples
- [Generated flag reference](https://drod.dev/refresh/reference/) — straight from the CLI, always current
- [Cookbook](https://drod.dev/refresh/usage/cookbook/) — fleet mode, unattended/CI runs, safe scaling
- [Concepts](https://drod.dev/refresh/concepts/lifecycle/) — lifecycle, AWS auth, output formats, exit codes
- [Migrating from eksctl / aws CLI](https://drod.dev/refresh/migration/)

## Safety & health checks

Mutating commands run **pre-flight health checks** (capacity, node readiness,
PodDisruptionBudgets, critical workloads) before they touch anything, support
`--dry-run` previews, stream live progress, and verify the result afterwards.
`nodegroup update` exposes a documented exit-code contract — 0 success / 2 health
warnings / 3 blocked / 4 failed to start / 5 verification issues — for CI use.

See [Concepts → exit codes](https://drod.dev/refresh/concepts/exit-codes/) for the
full contract and the [docs](https://drod.dev/refresh/) for health-check details.

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
