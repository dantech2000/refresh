# Refresh

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/releases/latest)
[![GitHub Workflow Status](https://img.shields.io/github/actions/workflow/status/dantech2000/refresh/release.yml?style=flat-square&label=build)](https://github.com/dantech2000/refresh/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/dantech2000/refresh?style=flat-square)](https://goreportcard.com/report/github.com/dantech2000/refresh)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/dantech2000/refresh?style=flat-square&color=blue)](https://github.com/dantech2000/refresh/blob/main/go.mod)
[![License](https://img.shields.io/github/license/dantech2000/refresh?style=flat-square&color=green)](https://github.com/dantech2000/refresh/blob/main/LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/dantech2000/refresh?style=flat-square&color=yellow)](https://github.com/dantech2000/refresh/stargazers)
[![Homebrew](https://img.shields.io/badge/homebrew-available-orange?style=flat-square)](https://github.com/dantech2000/homebrew-tap)


![Alt](https://repobeats.axiom.co/api/embed/bc73e7cb2ef4f089dc943258dc6511f76ad86a35.svg "Repobeats analytics image")


A Go-based CLI tool to manage and monitor AWS EKS node groups using your local kubeconfig and AWS credentials.

## Features

-   List all managed node groups in your EKS cluster
-   See AMI status for each nodegroup (✅ Latest, ❌ Outdated, ⚠️ Updating)
-   Detect and show nodegroups that are currently being updated
-   Update the AMI for all or specific nodegroups (rolling by default, with optional force)
-   Color-coded, readable CLI output

## Requirements

-   Go 1.23+
-   AWS credentials (`~/.aws/credentials` or environment variables)
-   kubeconfig (`~/.kube/config`)

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
   - `refresh_v0.1.4_darwin_amd64.tar.gz` (macOS Intel)
   - `refresh_v0.1.4_darwin_arm64.tar.gz` (macOS Apple Silicon)
   - `refresh_v0.1.4_linux_amd64.tar.gz` (Linux x64)
   - `refresh_v0.1.4_windows_amd64.tar.gz` (Windows x64)
3. Extract and move to your PATH:
   ```bash
   # Example for macOS/Linux
   tar -xzf refresh_v0.1.4_darwin_arm64.tar.gz
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

This is a work in progress...
