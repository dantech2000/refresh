## v0.2.0 - Nodegroup Intelligence (WIP)

- Added `list-nodegroups`, `describe-nodegroup`, `scale-nodegroup`, and `nodegroup-recommendations` commands
- Optional utilization (CPU via EC2 metrics) and cost (AWS Pricing) in list/describe
- Health-aware scaling with optional wait and PDB pre-checks
- Instance details in describe (ID/type/launch/lifecycle/state)
- Timeframe flag for utilization (1h/3h/24h); outputs show selected window
- Table alignment improvements (dynamic widths, ANSI-aware headers)
- Workloads/PDB output is experimental and gated pending kubeconfig handling

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Global `--timeout, -t` flag (with `REFRESH_TIMEOUT`) to cap operation durations
- `list-clusters`: `--max-concurrency, -C` (and `REFRESH_MAX_CONCURRENCY`) to limit concurrent region requests
- `list-clusters`: `REFRESH_EKS_REGIONS` env var to override the default region set for `-A/--all-regions`
- Global `--max-concurrency, -C` flag to set a default concurrency for multi-region operations

### Enhanced
- Accurate node readiness: when kubeconfig is available, readiness reflects Kubernetes `NodeReady==True` count
- Robust AWS pagination for clusters, nodegroups, and add-ons to avoid truncated results
- Concurrency limiting in multi-region listing to reduce throttling and stabilize latencies
- Deterministic cache keys for stable caching behavior across runs
- Improved AWS error messages via unified formatting in nodegroup flows
- Monitoring retry backoff respects context cancellation
- Documentation updates for partition awareness (commercial default; Gov/China via explicit regions/env)

### Fixed
- Cache race in `internal/services/cluster/cache.go` by avoiding writes under `RLock`
- Colored table alignment for STATUS column in `list-clusters`

## [0.1.9] - 2025-08-06

### Added
- **Man Page Installation**: New `install-man` (alias: `install-manpage`) command for installing comprehensive offline documentation
- **User-First Installation**: Man pages install to user-writable directories (`$HOME/.local/share/man/man1`) without requiring sudo
- **Cross-Platform Support**: Man page installation works seamlessly on macOS, Linux, and Unix-like systems
- **Auto-Discovery**: Installed man pages are automatically found by the `man` command via standard MANPATH
- **Smart Directory Selection**: Automatically selects first writable directory with fallback hierarchy
- **Permission Checking**: Verifies write permissions before installation with clear error messages

### Enhanced
- **CLI Documentation**: Users can now access complete documentation offline with `man refresh`
- **Installation Experience**: No more permission denied errors when installing documentation
- **Directory Structure**: Automatic creation of man page directory structure if it doesn't exist

### Removed
- **Deprecated --man Flag**: Removed the old `--man` flag that output raw man page content in favor of proper installation command

### Technical Improvements
- **Write Permission Validation**: Pre-installation checks ensure successful man page installation
- **MANPATH Integration**: Automatic detection of whether setup instructions are needed
- **Directory Priority Logic**: Smart selection from `$HOME/.local/share/man/man1` → `$HOME/.local/man/man1` → `$HOME/man/man1`

## [0.1.8] - 2025-08-05

### Added
- **Default Metrics Integration**: Redesigned health checks to use AWS default metrics (EC2 CPU utilization) requiring no additional setup
- **Kubernetes Client Integration**: Implemented working kubeconfig integration for comprehensive workload validation
- **Enhanced CloudWatch Integration**: Added proper EC2 instance discovery via EKS and Auto Scaling APIs
- **Graceful Degradation**: Clear messaging when optional services (Container Insights, kubectl) aren't available
- **Real-time Health Check Progress**: Professional spinner-based UI with progress bars and status indicators
- **Sample Health Check Output**: Comprehensive health assessment display with color-coded status
- **Health Check Documentation**: Added detailed usage examples and prerequisite explanations

### Fixed
- **Removed Mock Data**: Eliminated hardcoded fake metrics in health checks that could provide false confidence
- **CloudWatch Metrics Fallback**: Fixed issue where system returned fake utilization values when metrics unavailable
- **Container Insights Detection**: Added proper detection and setup guidance for Container Insights
- **Memory Metrics Handling**: Clear messaging that memory metrics require additional setup
- **Output Duplication**: Resolved issue where completion summary would duplicate progress display
- **Kubernetes Client Implementation**: Fixed placeholder function that always returned nil

### Enhanced
- **Health Check Categories**: Clear distinction between blocking and warning-level checks
- **Prerequisites Documentation**: Detailed explanation of what works out-of-the-box vs requires setup  
- **Error Messages**: User-friendly guidance with actionable steps for missing services
- **CLI Short Flags**: Added convenient short flags for all commands (`-c`, `-n`, `-d`, `-f`, etc.)
- **AWS Error Handling**: Improved error messages with specific guidance for credential and configuration issues

### Technical Improvements
- **EC2 Metrics Queries**: Direct integration with AWS/EC2 namespace for CPU utilization
- **Auto Scaling Integration**: Added ASG client for comprehensive instance discovery
- **Node Discovery Enhancement**: Improved mapping between EKS nodegroups and EC2 instances  
- **Health Check Engine**: Robust validation system with decision logic (PROCEED/WARN/BLOCK)
- **Progress Monitoring**: Clean ANSI escape code handling for seamless live updates

## [0.1.7] - 2024-XX-XX

### Added
- Pre-flight health checks with comprehensive cluster validation
- Real-time update progress monitoring with live status updates
- Short flag support for all CLI commands
- Enhanced AWS error handling with user-friendly messages

### Fixed
- Output duplication issue in progress monitoring
- ANSI escape code handling for clean terminal output

### Security
- Security patch for golang.org/x/oauth2 module (bump from 0.10.x to 0.27.0)

## [0.1.6] - Previous Release

### Added
- Dry-run functionality for preview mode
- Pattern matching for clusters and nodegroups
- Progress monitoring with completion summaries

### Enhanced
- Code organization with logical package structure
- Maintainability improvements

---

## Health Check System Overview

The refresh tool now includes a comprehensive health check system that validates cluster readiness before AMI updates:

### Core Health Checks

1. **Node Health** (BLOCKING)
   - Validates all nodegroups are in ACTIVE status
   - Uses EKS API (no additional setup required)

2. **Cluster Capacity** (BLOCKING) 
   - Ensures sufficient CPU resources for rolling updates (minimum 30% headroom)
   - Uses default EC2 metrics from AWS/EC2 namespace (no additional setup required)

3. **Critical Workloads** (BLOCKING)
   - Validates all kube-system pods are running
   - Requires kubectl access and kubeconfig

4. **Pod Disruption Budgets** (WARNING)
   - Identifies deployments missing PDB protection
   - Requires kubectl access and kubeconfig

5. **Resource Balance** (WARNING)
   - Analyzes CPU utilization distribution across nodes
   - Uses default EC2 metrics from AWS/EC2 namespace (no additional setup required)

### Design Philosophy

- **Zero Prerequisites**: Core functionality works with just AWS credentials
- **Graceful Degradation**: Clear guidance when optional features require additional setup
- **Real Data Only**: No fake or simulated metrics - if data isn't available, we say so
- **User-Friendly**: Actionable error messages and setup instructions
- **Professional UI**: Clean progress displays with spinner animations and progress bars

### Future Enhancements

See `ideas.md` for planned features including:
- Cost analysis using AWS Cost Explorer API
- Advanced rollback capabilities
- Enhanced monitoring and alerting
- CI/CD integration features