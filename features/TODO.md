# Feature Status

## Shipped

### Cluster Operations
- List clusters with multi-region support (`-A/--all-regions`), concurrency cap, health status
- Describe cluster with security, networking, add-ons, encryption details
- Cluster diff (side-by-side config comparison) with interactive multi-select picker
- Sorting options (`--sort`, `--desc`) for cluster and nodegroup lists
- Pagination on all list operations
- Context system: `refresh use`, `refresh current`, `refresh context` (kubectx-style)

### EKS Add-ons Management
- List / Describe / Update with health validation and version compatibility checks
- Bulk update (`addon update --all`) with parallel mode, skip list, dry-run, wait
- Dependency ordering for sequenced bulk updates
- `--parallel` + `--dependency-order` conflict guard
- Post-update health re-check

### Nodegroup Management
- List / Describe / Scale / Update (rolling AMI update with health checks + dry-run)
- CPU utilization via CloudWatch `GetMetricData` (batched, cached)
- Cost display via Pricing API with static price map fallback

### Architecture & Infra
- Unified AWS config loading (`internal/awsconfig`)
- Persistent named contexts (`internal/cliconfig`, YAML-backed)
- SDK retry/backoff on all EKS/EC2/IAM/STS calls
- Centralized CLI/env config package
- Extracted command formatters, split types, modular UI components

### UI / Table Rendering
- Dynamic ANSI-aware column widths, colored padding, optional/conditional columns
- Truncation with ellipsis, tree view for multi-region display
- Fun spinner with per-category rotating messages

## Remaining Work

### Testing
- [ ] Increase unit test coverage toward 90%+ across services
- [ ] Integration tests against real EKS clusters (multi-region, add-on ops, scaling)
- [ ] Performance benchmarks for list/describe operations
- [ ] Cost calculation accuracy validation against AWS billing

### Documentation
- [ ] Man page content review and update for new commands (context, addon, diff)
- [ ] Migration guide from eksctl / AWS CLI workflows

### Improvements
- [ ] Workloads/PDB checks require kubeconfig — add `--kubeconfig` flag and diagnostics
- [ ] Memory metrics support (requires Container Insights setup)
- [ ] Scale dry-run: detailed impact output (node delta, cost delta, PDB specifics)
- [ ] Filtering by status/version on `cluster list` (sorting done, filtering not yet)
