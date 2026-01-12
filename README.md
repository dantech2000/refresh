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
│   │   ├── cluster.go        # Cluster discovery and name resolution
│   │   ├── nodegroup.go      # Nodegroup operations
│   │   └── errors.go         # AWS error handling and formatting
│   ├── commands/             # CLI command implementations
│   │   ├── cluster_group.go  # Cluster commands (list, describe, compare)
│   │   ├── nodegroup_group.go # Nodegroup commands (list, describe, scale, update-ami)
│   │   ├── addon_group.go    # Add-on commands (list, describe, update)
│   │   └── ...               # Individual command implementations
│   ├── config/               # Configuration management
│   │   └── config.go         # Thread-safe config with environment variable support
│   ├── types/                # Core domain types
│   │   └── types.go          # NodegroupInfo, UpdateProgress, MonitorConfig, etc.
│   ├── services/             # Business logic layer
│   │   ├── cluster/          # Cluster service with caching
│   │   ├── nodegroup/        # Nodegroup service with analytics
│   │   └── common/           # Shared utilities (retry logic)
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
│       ├── output.go         # Status and output helpers
│       ├── table.go          # Table rendering with pterm
│       ├── progress.go       # Spinners and progress bars
│       └── ...               # Additional UI utilities
└── go.mod                    # Go module dependencies (2025/2026 versions)
```

### Key Design Patterns

- **Thread-safe Configuration**: Singleton pattern with `sync.RWMutex` for concurrent access
- **Channel-based Concurrency**: Used in monitoring for concurrent status checks
- **Clean Error Handling**: AWS errors are classified and formatted with user-friendly messages
- **Dependency Injection**: Services use interfaces for testability
- **Graceful Shutdown**: Signal handling for clean termination

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
-   **Sorting Options**: Sort cluster and nodegroup lists with `--sort` and `--desc`

## Requirements

### Required (Core Functionality)
-   Go 1.24+
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
- Only **Linux On-Demand** pricing is used (Spot/Reserved pricing not included)

**Example:**
```
t3a.large in us-west-2: $0.0752/hour
9 nodes x $0.0752 x 730 hours = $494/month
```

**Limitations:**
- Spot instances are calculated at On-Demand rates (actual costs may be 60-90% lower)
- Does not include EBS storage, data transfer, or other EC2-related costs
- Pricing API requires `pricing:GetProducts` IAM permission

**Required IAM Permission:**
```json
{
  "Effect": "Allow",
  "Action": "pricing:GetProducts",
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
│   ├── describe (dc)      # Describe comprehensive cluster info
│   └── compare (cc)       # Compare cluster configurations
├── nodegroup (ng)
│   ├── list               # List nodegroups with AMI status
│   ├── describe           # Describe nodegroup details
│   ├── update-ami         # Update nodegroup AMI version
│   ├── scale              # Scale nodegroup with health checks
│   └── recommendations    # Get optimization recommendations
├── addon
│   ├── list               # List cluster add-ons
│   ├── describe           # Describe add-on details
│   └── update             # Update add-on version
└── version                # Show version information
```

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
# Basic comparison
refresh cluster compare -c dev -c prod
refresh cc -c dev -c prod              # Short alias

# Show only differences
refresh cluster compare -c dev -c staging --show-differences

# Focus on specific aspects
refresh cc -c prod-east -c prod-west -i networking -i security

# Different output formats
refresh cluster compare -c dev -c prod -o json
```

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

**Important:** Flags must come before positional arguments (urfave/cli v2 requirement):
```bash
# CORRECT
refresh ng list --show-costs -c prod

# INCORRECT (flags after positional arg will be ignored)
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
refresh nodegroup update-ami -c <cluster>

# Update specific nodegroup
refresh ng update-ami -c dev -n web

# Update with partial name matching (confirms before proceeding)
refresh ng update-ami -c prod -n api-

# Preview changes without executing (dry run)
refresh ng update-ami -c staging -n web -d
refresh ng update-ami -c staging --dry-run

# Force update (even if already latest)
refresh ng update-ami -c prod -f

# Skip health checks
refresh ng update-ami -c dev -s

# Quiet mode (minimal output)
refresh ng update-ami -c prod -q
```

**Example Output:**

```
# Single nodegroup update
$ refresh ng update-ami -c development-blue -n groupF
Updating nodegroup dev-blue-groupF-20230815230923929900000007...
Update started for nodegroup dev-blue-groupF-20230815230923929900000007

# Multiple matches with confirmation
$ refresh ng update-ami -c development-blue -n group
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

#### Nodegroup Recommendations

Get optimization recommendations for nodegroups:

```bash
# Get recommendations for cost and right-sizing
refresh nodegroup recommendations -c prod --cost-optimization --right-sizing

# Include spot instance analysis
refresh ng recommendations -c dev --spot-analysis --timeframe 30d
```

**Note:** Recommendations output is currently in Phase 1 (placeholder).

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
```

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

When multiple items match, the tool shows all matches and asks for confirmation before proceeding.

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
refresh ng update-ami -c dev -H
refresh ng update-ami --cluster dev --health-only

# Update with health checks (default behavior)
refresh ng update-ami -c dev
refresh ng update-ami --cluster dev

# Skip health checks
refresh ng update-ami -c dev -s
refresh ng update-ami --cluster dev --skip-health-check

# Force update (bypasses health checks)
refresh ng update-ami -c dev -f
refresh ng update-ami --cluster dev --force
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
refresh ng update-ami -c staging -n api -d -q

# Force update with health check skip
refresh ng update-ami -c prod -f -s

# Health check only with quiet mode
refresh ng update-ami -c test -H -q

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

### Short Command Aliases

- `lc` - `cluster list`
- `dc` - `cluster describe`
- `cc` - `cluster compare`
- `ng` - `nodegroup`

## Development

### Prerequisites

- Go 1.24+
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
task dev:full-check
```

### Project Structure

The codebase follows clean architecture principles:

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
4. **CLI**: Hierarchical commands with urfave/cli/v2

## Release Process

### Prerequisites

- Ensure you have push access to both repositories:
  - `dantech2000/refresh` (main repository)
  - `dantech2000/homebrew-tap` (Homebrew tap)
- GitHub Personal Access Token (`GH_PAT`) is configured in repository secrets
- GoReleaser is installed locally for testing

### Release Steps

1. **Update Version Number**

   Update the version in `main.go`:
   ```go
   var versionInfo = VersionInfo{
       Version:   "v0.1.3",  // <- Update this version
       Commit:    "",
       BuildDate: "",
   }
   ```

2. **Run Pre-Release Checks**
   ```bash
   # Full development check (format, lint, test, build)
   task dev:full-check

   # Test GoReleaser configuration
   task release:test

   # Optional: Dry run of release process (local only)
   task release:dry-run
   ```

3. **Validate Setup**
   ```bash
   # Check if ready for release
   task release:check

   # Validate Homebrew formula syntax
   task tap:validate
   ```

4. **Create and Push Release Tag**
   ```bash
   # Create tag and push (triggers GitHub Actions)
   task release:tag VERSION=v0.1.3

   # Or manually:
   git tag -a v0.1.3 -m "Release v0.1.3"
   git push origin v0.1.3
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
task dev:quick-test          # Format, vet, build, test version
task dev:full-check          # Full check including lint and tests

# Release workflow
task release:check           # Verify ready for release
task release:test            # Test GoReleaser config (no release)
task release:dry-run         # Full dry run (local only)
task release:tag VERSION=v0.1.x  # Create and push release tag

# Testing
task run:version             # Test version command
task run:list                # Test list command
task run:help                # Show help

# Homebrew tap
task tap:validate            # Validate formula syntax
task tap:test-local          # Instructions for local testing

# Utilities
task clean                   # Clean build artifacts
task deps                    # Download and tidy dependencies
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
- **Homebrew Formula Issues**: Run `task tap:validate` to check syntax
- **Version Conflicts**: Ensure version in `main.go` matches git tag

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

## Security

-   Does not log or store credentials
-   Sanitizes input parameters
-   **Deletion Protection Visibility**: Displays EKS cluster deletion protection status in `cluster describe` command
-   **Security Configuration**: Shows encryption status, logging configuration, and IAM role information

---

**Refresh** is a production-ready CLI tool for managing AWS EKS node groups with comprehensive monitoring, dry-run capabilities, and intelligent partial name matching.
