# Features TODO (Consolidated)

This file aggregates actionable items from Phase 1 feature specs, pruned to exclude work already implemented in code.

## Enhanced Cluster Operations

 - [x] SDK client retry/backoff: wrap EKS/EC2/IAM/STS calls with context-aware retry policy (throttle, transient errors)
 - [x] Config management: centralize CLI/env defaults into a small config package (timeouts, concurrency, regions)
 - [x] UI sorting: add sort options for cluster lists (e.g., by name, status, version, region)
 - [x] CLI help/examples: expand README/help text with more examples and tips
- [ ] Optional: interactive selection for cluster comparison

Notes (done): pagination, context timeouts, multi-region with concurrency cap, cache TTLs, deterministic cache keys, colored table alignment, partition-aware region defaults.

## EKS Add-ons Management (new)

- [ ] Service package: `internal/services/addons` with list/describe/update APIs
- [ ] Commands: `list-addons`, `describe-addon`, `update-addon`, `update-all-addons`, `addon-security-scan`
- [ ] Health validation: pre/post checks integrated with health framework
- [ ] Compatibility DB: versions matrix, validation helpers
- [ ] Bulk operations: dependency resolution, optional parallel, dry-run, progress
- [ ] UI: rich table + JSON/YAML, filtering/sorting
- [ ] Reliability: retry/backoff, throttling control, pagination
- [ ] Tests: unit + integration; performance benchmarks vs targets

## Advanced Nodegroup Management (new)

- [ ] Service: nodegroup intelligence (list/describe/scale/recommendations)
- [ ] Metrics: CloudWatch utilization collection (batch), caching
- [ ] Cost: pricing + Cost Explorer integration; analyzer module
- [ ] Workloads: Kubernetes client integration; PDB validation; workload analysis
- [ ] Spot: opportunity analysis and guidance
- [ ] UI: tables + JSON; filtering/sorting; dry-run support
- [ ] Tests: unit + integration; performance targets; docs

### UI/Table Rendering Improvements

- [ ] Abstract table rendering into a reusable helper that:
  - Computes dynamic column widths from data and headers
  - Pads colored strings correctly (ANSI-aware)
  - Supports optional/conditional columns cleanly
  - Handles truncation with ellipsis and keeps grid intact
  - Provides common separators and alignment utilities
- [ ] Replace ad-hoc prints in: `list-nodegroups`, `describe-nodegroup` (instances block), `nodegroup-recommendations`, and cluster list/describe outputs.


