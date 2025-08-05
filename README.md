# Refresh

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/release.yml?style=flat-square&label=build)](https://github.com/dantech2000/refresh/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/blob/main/go.mod)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/dantech2000/refresh?style=flat-square&color=yellow)](https://github.com/dantech2000/refresh/stargazers)
[![Homebrew](https://img.shields.io/badge/homebrew-available-orange?style=flat-square)](https://github.com/dantech2000/homebrew-tap)


![Alt](https://repobeats.axiom.co/api/embed/bc73e7cb2ef4f089dc943258dc6511f76ad86a35.svg "Repobeats analytics image")


A Go-based CLI tool to manage and monitor AWS EKS node groups with comprehensive pre-flight health checks using default AWS metrics.

## Features

-   **🔍 Pre-flight Health Checks**: Validate cluster readiness before AMI updates using default EC2 metrics (no additional setup required)
-   **📊 Real-time Monitoring**: Live progress tracking with professional spinner displays and clean completion summaries
-   **📋 Cluster Management**: List all managed node groups with AMI status (✅ Latest, ❌ Outdated, ⚠️ Updating)
-   **🔄 Smart Updates**: Update AMI for all or specific nodegroups with rolling updates and optional force mode
-   **👀 Dry Run Mode**: Preview changes with comprehensive details before execution
-   **⚡ Short Flags**: Convenient short flags for all commands (`-c`, `-n`, `-d`, `-f`, etc.)
-   **🎨 Enhanced UI**: Color-coded output with progress bars and clear status indicators
-   **🛡️ Graceful Degradation**: Works with just AWS credentials, provides clear guidance for optional features

## Requirements

### ✅ Required (Core Functionality)
-   Go 1.24+
-   AWS credentials (`~/.aws/credentials`, environment variables, or IAM roles)

### ⚠️ Optional (Enhanced Features)
-   `kubectl` and kubeconfig (`~/.kube/config`) - for Kubernetes workload validation
-   Container Insights setup - for memory utilization metrics
-   CloudWatch agent - for advanced memory and disk metrics

> **Note**: The tool works with just AWS credentials! Health checks use default EC2 metrics and provide clear guidance for enabling optional features.

## Installation

### 🍺 Homebrew (Recommended)

The easiest way to install `refresh` is via Homebrew:

```bash
# Add the tap
brew tap dantech2000/tap

# Install refresh
brew install refresh

# Verify installation
refresh version
```

### 📦 Download from Releases

Alternatively, download pre-built binaries from the [releases page](https://github.com/dantech2000/refresh/releases/latest):

1. Go to the [latest release](https://github.com/dantech2000/refresh/releases/latest)
2. Download the appropriate binary for your platform:
   - `refresh_v0.1.7_darwin_amd64.tar.gz` (macOS Intel)
   - `refresh_v0.1.7_darwin_arm64.tar.gz` (macOS Apple Silicon)
   - `refresh_v0.1.7_linux_amd64.tar.gz` (Linux x64)
   - `refresh_v0.1.7_windows_amd64.tar.gz` (Windows x64)
3. Extract and move to your PATH:
   ```bash
   # Example for macOS/Linux
   tar -xzf refresh_v0.1.7_darwin_arm64.tar.gz
   sudo mv refresh /usr/local/bin/
   chmod +x /usr/local/bin/refresh
   ```

### 🔧 Build from Source

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

### ✅ Verify Installation

After installation, verify it works:

```bash
refresh version
refresh --help
```

You should see output showing the version and available commands.

### 🔄 Updating

To update to the latest version:

```bash
# If installed via Homebrew
brew update && brew upgrade refresh

# If installed via go install
go install github.com/dantech2000/refresh@latest

# If manually installed, download the latest release and replace the binary
```

## Usage

### List Nodegroups

List all managed nodegroups in a cluster, showing their status and AMI state:

```sh
go run main.go list --cluster <cluster-name>

# Filter nodegroups using partial name matching
go run main.go list --cluster <cluster-name> --nodegroup <partial-name>
```

**Example output:**

```
development-blue
├── dev-blue-groupD-20230814214633237700000007
│   ├── Status: ACTIVE
│   ├── Instance Type: t3a.large
│   ├── Desired: 15
│   ├── Current AMI: ami-0ce9a7e5952499323
│   └── AMI Status: ❌ Outdated

├── dev-blue-groupE-20230815204000720600000007
│   ├── Status: ACTIVE
│   ├── Instance Type: t3a.large
│   ├── Desired: 16
│   ├── Current AMI: ami-0ce9a7e5952499323
│   └── AMI Status: ❌ Outdated

└── dev-blue-groupF-20230815230923929900000007
    ├── Status: ACTIVE
    ├── Instance Type: t3a.large
    ├── Desired: 14
    ├── Current AMI: ami-0ce9a7e5952499323
    └── AMI Status: ❌ Outdated
```

-   `✅ Latest`: Nodegroup is using the latest recommended AMI for the cluster
-   `❌ Outdated`: Nodegroup AMI is not the latest
-   `⚠️ Updating`: Nodegroup is currently being updated (status and AMI status both show this)

### Update AMI for Nodegroups

Trigger a rolling update to the latest AMI for all or a specific nodegroup:

```sh
# Update all nodegroups
go run main.go update-ami --cluster <cluster-name>

# Update a specific nodegroup
go run main.go update-ami --cluster <cluster-name> --nodegroup <nodegroup-name>

# Update nodegroups using partial name matching
go run main.go update-ami --cluster <cluster-name> --nodegroup <partial-name>

# Force update (replace all nodes, even if already latest)
go run main.go update-ami --cluster <cluster-name> --force

# Preview changes without executing (dry run)
go run main.go update-ami --cluster <cluster-name> --dry-run
go run main.go update-ami --cluster <cluster-name> --nodegroup <partial-name> --dry-run
```

**Example output:**

```
# Single nodegroup update
$ go run main.go update-ami --cluster development-blue --nodegroup groupF
Updating nodegroup dev-blue-groupF-20230815230923929900000007...
Update started for nodegroup dev-blue-groupF-20230815230923929900000007

# Multiple matches with confirmation
$ go run main.go update-ami --cluster development-blue --nodegroup group
Multiple nodegroups match pattern 'group':
  1) dev-blue-groupD-20230814214633237700000007
  2) dev-blue-groupE-20230815204000720600000007
  3) dev-blue-groupF-20230815230923929900000007
Update all 3 matching nodegroups? (y/N): y
Updating nodegroup dev-blue-groupD-20230814214633237700000007...
Update started for nodegroup dev-blue-groupD-20230814214633237700000007
Updating nodegroup dev-blue-groupE-20230815204000720600000007...
Update started for nodegroup dev-blue-groupE-20230815204000720600000007
Updating nodegroup dev-blue-groupF-20230815230923929900000007...
Update started for nodegroup dev-blue-groupF-20230815230923929900000007

# Dry run preview
$ go run main.go update-ami --cluster development-blue --nodegroup group --dry-run
DRY RUN: Preview of nodegroup updates for cluster development-blue

UPDATE: Nodegroup dev-blue-groupD-20230814214633237700000007 would be updated
UPDATE: Nodegroup dev-blue-groupE-20230815204000720600000007 would be updated
UPDATE: Nodegroup dev-blue-groupF-20230815230923929900000007 would be updated

Summary:
- Nodegroups that would be updated: 3
- Nodegroups that would be skipped: 0

Would update:
  - dev-blue-groupD-20230814214633237700000007
  - dev-blue-groupE-20230815204000720600000007
  - dev-blue-groupF-20230815230923929900000007

To execute these updates, run the same command without --dry-run
```

**Partial Name Matching:**

Both `--cluster` and `--nodegroup` flags support partial name matching to make it easier to work with long names:

**Cluster Matching:**
- `--cluster development` matches `development-blue`, `development-prod`, etc.
- `--cluster blue` matches `development-blue`, `staging-blue`, etc.

**Nodegroup Matching:**
- `--nodegroup groupF` matches `dev-blue-groupF-20230815230923929900000007`
- `--nodegroup monolith` matches all nodegroups containing "monolith"
- `--nodegroup 20230815` matches all nodegroups created on that date

When multiple items match, the tool will show all matches and ask for confirmation before proceeding.

**List Command Filtering:**

You can also filter the list output using the same partial matching:

```sh
# Show only nodegroups containing "group"
$ go run main.go list --cluster development-blue --nodegroup group
development-blue
├── dev-blue-groupD-20230814214633237700000007
│   ├── Status: ACTIVE
│   └── AMI Status: ❌ Outdated

├── dev-blue-groupE-20230815204000720600000007
│   ├── Status: ACTIVE
│   └── AMI Status: ❌ Outdated

└── dev-blue-groupF-20230815230923929900000007
    ├── Status: ACTIVE
    └── AMI Status: ❌ Outdated

# Show only monolith nodegroups
$ go run main.go list --cluster development-blue --nodegroup monolith
development-blue
├── dev-blue-monolithD-20230816000007673100000007
└── dev-blue-monolithE-20230816002441701900000007
```

When multiple nodegroups match in update commands, the tool will show all matches and ask for confirmation before proceeding.

## 🔍 Health Checks

The refresh tool includes comprehensive pre-flight health checks that validate cluster readiness before AMI updates using **default AWS metrics** (no additional setup required).

### Health Check Commands

```bash
# Run health check only (no update)
refresh update-ami -c development-blue -H
refresh update-ami --cluster development-blue --health-only

# Update with health checks (default behavior)
refresh update-ami -c development-blue
refresh update-ami --cluster development-blue

# Skip health checks
refresh update-ami -c development-blue -s
refresh update-ami --cluster development-blue --skip-health-check

# Force update (bypasses health checks)
refresh update-ami -c development-blue -f
refresh update-ami --cluster development-blue --force
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

**✅ Works Out-of-the-Box (Default AWS Metrics)**:
- **Node Health**: All nodegroups must be in ACTIVE status (EKS API)
- **Cluster Capacity**: Sufficient CPU resources using default EC2 metrics (minimum 30% headroom)
- **Resource Balance**: CPU utilization distribution across nodes using default EC2 metrics

**⚠️ Requires Additional Setup**:
- **Critical Workloads**: All kube-system pods running (requires kubectl access)
- **Pod Disruption Budgets**: Missing PDBs for user workloads (requires kubectl access)
- **Memory Metrics**: Requires Container Insights or CloudWatch agent setup

### Graceful Degradation

When optional services aren't available, the tool provides helpful guidance:
- **No kubectl access**: "Install kubectl and configure cluster access to enable this check"
- **No Container Insights**: "Memory metrics require Container Insights setup"
- **Limited metrics**: "Using default EC2 metrics (CPU only)"

## ⚡ CLI Short Flags

All commands support convenient short flags for faster typing:

```bash
# List with short flags
refresh list -c prod -n web

# Quick update with dry-run
refresh update-ami -c staging -n api -d -q

# Force update with health check skip
refresh update-ami -c prod -f -s

# Health check only with quiet mode
refresh update-ami -c test -H -q
```

**Common Short Flags**:
- `-c, --cluster` - EKS cluster name or pattern
- `-n, --nodegroup` - Nodegroup name or pattern
- `-f, --force` - Force update if possible
- `-d, --dry-run` - Preview changes without executing
- `-q, --quiet` - Minimal output mode
- `-s, --skip-health-check` - Skip pre-flight validation
- `-H, --health-only` - Run health checks only

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
   - Homebrew formula will be updated in `homebrew-tap` repository
   - Users can install with: `brew install dantech2000/tap/refresh`

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
| **Build Status** | GitHub Actions workflow status | ✅ Green = builds passing, ❌ Red = build issues |
| **Go Report Card** | Code quality grade (A+ to F) | Aim for A+ rating, watch for downgrades |
| **Go Version** | Minimum Go version required | Compatibility with current Go releases |
| **License** | Project license (MIT) | License compliance information |
| **Stars** | GitHub stars count | Community interest and growth |
| **Homebrew** | Homebrew installation availability | Package distribution status |

### Quick Health Check
- **Green Build Badge** ✅ = Latest code builds successfully, releases work
- **A+ Go Report** ✅ = Code quality is excellent
- **Current Go Version** ✅ = Using modern Go features and best practices

### Dependency Management
This project includes automated dependency management:
- **Dependabot** - Automated dependency updates with security patches

## Security

-   Does not log or store credentials
-   Sanitizes input parameters

---

**Refresh** is a production-ready CLI tool for managing AWS EKS node groups with comprehensive monitoring, dry-run capabilities, and intelligent partial name matching.
