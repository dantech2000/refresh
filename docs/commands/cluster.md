# cluster

Discover and operate on EKS clusters: list them (optionally across every
region), describe one in depth, run a read-only upgrade-readiness check, and
orchestrate a full control-plane → add-on → nodegroup upgrade.

```bash
refresh cluster <list|describe|upgrade-check|upgrade> [args] [flags]
```

The cluster argument is a positional on most subcommands, or `--cluster/-c`,
falling back to the [active context](../concepts/contexts.md).

---

## list

Fast multi-region cluster discovery with integrated health status. Direct EKS
API calls — no CloudFormation dependency.

```bash
refresh cluster list [name-pattern] [flags]
```

Scope with `-A` (every EKS-supported region) or repeated `-r`. Filter and sort
in-process, then render as a table, structured output, or a region/cluster
tree.

### Flags

| Flag | Description |
|---|---|
| `--all-regions, -A` | Query all EKS-supported regions |
| `--region, -r` | Specific region(s) to query (repeatable) |
| `--filter, -f` | Filter clusters, `key=value` (keys: `name`, `status`, `version`); repeatable |
| `--sort` | Sort by field: `name` (default), `status`, `version`, `region` |
| `--desc` | Sort descending |
| `--show-health, -H` | Include health status for each cluster |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain`, `tree` |
| `--tree, -T` | Hierarchical region/cluster tree (implies `--all-regions`) |
| `--watch, -w` | Re-run and redraw every `--watch-interval` until interrupted |
| `--watch-interval` | Refresh interval for `--watch` (default `10s`) |
| `--max-concurrency, -C` | Max concurrent region requests |
| `--timeout, -t` | Operation timeout (default `60s`; env `REFRESH_TIMEOUT`) |

!!! tip "`tree` view"
    `-o tree` (or `--tree`) renders a region → cluster hierarchy and implies
    `--all-regions`, so it's the quickest way to eyeball the whole fleet.

### Examples

```bash
# Every active cluster across all regions
refresh cluster list -A --filter status=ACTIVE

# Just clusters whose name contains "prod", sorted by version (newest first)
refresh cluster list prod --sort version --desc

# Fleet hierarchy
refresh cluster list -o tree

# Live dashboard, redrawn every 5s
refresh cluster list --watch --watch-interval 5s

# Machine-readable for a script
refresh cluster list -A -o json
```

---

## describe

Detailed information for a single cluster: networking, security configuration,
add-ons, and health.

```bash
refresh cluster describe [cluster] [flags]
```

`describe` has the alias `get`.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--detailed, -d` | Show comprehensive networking and security information |
| `--show-health, -H` | Include health status (default `true`) |
| `--show-security, -s` | Include security configuration analysis |
| `--include-addons, -a` | Include EKS add-on information (default `true`) |
| `--check-readiness, -R` | Measure real Kubernetes node readiness (`Ready/desired`) via the cluster API; without it the `NODES` column shows the desired count only |
| `--kubeconfig` | Path to the kubeconfig for `--check-readiness` (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
refresh cluster describe prod-east
refresh cluster get prod-east --detailed --show-security
refresh cluster describe prod-east -o json
```

---

## upgrade-check

Read-only upgrade-readiness report. Nothing is mutated — this is the pre-flight
read before [`cluster upgrade`](#upgrade).

```bash
refresh cluster upgrade-check [cluster] [flags]
```

Surfaces AWS **Cluster Insights** (the same upgrade checks the EKS console
shows) plus a local **version-skew** picture: control-plane version vs. each
managed nodegroup, and installed add-ons vs. the latest compatible version —
with ordered, actionable findings.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--category` | Insight category: `UPGRADE_READINESS` (default), `MISCONFIGURATION` |
| `--status` | Filter by insight status: `PASSING`, `WARNING`, `ERROR`, `UNKNOWN` (repeatable) |
| `--show-passing` | Include `PASSING` insights (hidden by default) |
| `--id` | Show the detail view (recommendation + resources) for a single insight ID |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
# Readiness summary for prod-east
refresh cluster upgrade-check -c prod-east

# Include passing checks, as JSON for a gate
refresh cluster upgrade-check -c prod-east --show-passing -o json

# Drill into one insight
refresh cluster upgrade-check -c prod-east --id <insight-id>
```

---

## upgrade

Plan and execute a full EKS cluster upgrade to a target Kubernetes version:
control plane → add-ons → nodegroups, with a health gate after every phase.

```bash
refresh cluster upgrade [cluster] --to <version> [flags]
```

EKS upgrades one minor version at a time, so a multi-minor jump expands into
sequential **hops**. Each hop runs: readiness (cluster insights + kubelet
version skew) → control plane → add-ons (dependency order, versions compatible
with the hop target) → nodegroup rolls.

!!! note "Resumable by design"
    The plan is re-derived from live cluster state on every run — no state
    file. Rerunning after a failure (or Ctrl+C) resumes where it left off, and
    rerunning after success is a no-op. On failure, `refresh` prints the exact
    resume command.

!!! warning "This mutates the control plane"
    `cluster upgrade` uses strict credential validation and confirms each
    mutating phase unless you pass `--yes`. A multi-hop upgrade legitimately
    runs for hours; the default timeout is `4h`. Start with `--dry-run`.

### Flags

| Flag | Description |
|---|---|
| `--to` | **Required.** Target Kubernetes version (e.g. `1.33`) |
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--dry-run, -d` | Print the full ordered plan without mutating anything |
| `--yes, -y` | Skip per-phase confirmation prompts |
| `--force` | Force nodegroup rolls when pods can't be drained due to PDBs |
| `--skip, -s` | Add-on to skip (repeatable; for add-ons managed via Helm/GitOps) |
| `--skip-nodegroup` | Nodegroup name pattern to skip (repeatable) |
| `--quiet, -q` | Suppress progress output |
| `--poll-interval, -p` | How often to poll in-flight updates (default `15s`) |
| `--format, -o` | Plan output format: `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Overall operation timeout (default `4h`; env `REFRESH_TIMEOUT`) |

!!! tip "Exit code in dry-run"
    A dry-run (or any run) whose plan contains a **blocker** prints the plan and
    exits non-zero without mutating — handy as a readiness gate in CI.

### Examples

```bash
# Print the plan only (exits non-zero if anything blocks the upgrade)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive (CI) run, skipping a Helm-managed add-on
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-load-balancer-controller
```

See the [upgrade lifecycle](../concepts/lifecycle.md) for how this fits the
`status → upgrade-check → patch → upgrade` loop.
