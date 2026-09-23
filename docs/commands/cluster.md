# cluster

Discover and operate on EKS clusters: list them (optionally across every
region), describe one in depth, run a read-only upgrade-readiness check, and
orchestrate a full control-plane → add-on → nodegroup upgrade.

```bash
refresh cluster <list|describe|upgrade-check|upgrade> [args] [flags]
```

The cluster argument is a positional on most subcommands, or `--cluster/-c`,
falling back to the [active context](../concepts/contexts.md). The read-only
subcommands also fall back to the kubeconfig's current cluster; `upgrade`
never does. See
[Cluster resolution](../concepts/configuration.md#cluster-resolution).

---

## list

Fast multi-region cluster discovery with integrated health status. Direct EKS
API calls — no CloudFormation dependency.

```bash
refresh cluster list [name-pattern] [flags]
```

Scope with `-A` (every EKS-supported region) or repeated `-r`. Filter and sort
in-process, then render as a table, structured output, or a region/cluster
tree. See [Regions](../concepts/configuration.md#regions) for how `-r`, the
global `--region`, and `REFRESH_EKS_REGIONS` combine.

With several regions, a region that fails prints one warning on stderr and
the command still exits `0`. If no region answers (for example, the timeout
ends first), the command fails instead of printing an empty list. The
default `-A` sweep skips regions these credentials can't use, the same way
`status -A` does. A cluster that could not be read is a stderr warning too.

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
| `--max-concurrency, -C` | Global: max concurrent region requests |
| `--timeout, -t` | Global operation timeout (default `60s`; env `REFRESH_TIMEOUT`) |

!!! tip "`tree` view"
    `-o tree` (or `--tree`) renders a region → cluster hierarchy and implies
    `--all-regions`, so it's the quickest way to eyeball the whole fleet. An
    explicit `-o` wins over `--tree`.

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
| `--kube-context` | Kubeconfig context for `--check-readiness`, even if its server does not match the cluster endpoint (see [kubeconfig matching](../concepts/configuration.md#matching-the-kubeconfig-to-the-target-cluster)) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

The support window uses the cluster's upgrade policy. `-o json`/`-o yaml`
include it as `supportType` (`STANDARD` clusters are auto-upgraded at the end
of standard support). `cluster upgrade-check` reports it the same way.

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

### Upgrade-readiness checks

EKS computes these insights server-side and **owns the catalog — it adds and
changes checks over time** — so treat this list as representative, not
exhaustive. The authoritative, always-current set for *your* cluster comes from
`--show-passing` (each row prints a short ID you can pass to `--id`):

```bash
refresh cluster upgrade-check -c prod-east --show-passing
```

Common `UPGRADE_READINESS` checks:

- **Deprecated APIs** — workloads calling Kubernetes APIs removed in the target
  version. Conditional (only appears when EKS detects such usage) and named for
  the version, e.g. *"Deprecated APIs removed in 1.33"*. The detail view (`--id`)
  names the calling clients (user agent, last-seen, 30-day request count).
- **Kubelet version skew** / **kube-proxy version skew** — node components too
  far behind the control plane to upgrade safely.
- **EKS add-on version compatibility** — installed add-ons vs. the target version.
- **Amazon Linux 2 compatibility** — nodes on AL2 (end-of-life; no AMIs for newer versions).
- **Cluster health issues** — control-plane health problems that would block an upgrade.

A second category, `MISCONFIGURATION` (`--category MISCONFIGURATION`), covers
EKS Hybrid Nodes.

Drill into any insight with `--id` — it accepts the **short ID** from the table,
the full insight ID, or a **case-insensitive name substring**:

```bash
refresh cluster upgrade-check -c prod-east --id "deprecated"   # by name
refresh cluster upgrade-check -c prod-east --id bc8b2f86       # by short ID
```

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--category` | Insight category: `UPGRADE_READINESS` (default), `MISCONFIGURATION` |
| `--status` | Filter by insight status: `PASSING`, `WARNING`, `ERROR`, `UNKNOWN` |
| `--show-passing` | Include `PASSING` insights (hidden by default) |
| `--id` | Show the detail view for one insight — accepts its short ID (from the table), full ID, or a case-insensitive name substring |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

`upgrade-check` reads the insights EKS already has. It does not start an
insights refresh, so right after a control-plane change the list can be
empty or stale. `cluster upgrade` refreshes them before each hop.

With `-o plain`, stdout has only the insight rows. The readiness verdict,
support, control plane, and version skew go to stderr.

### Examples

```bash
# Readiness summary for prod-east
refresh cluster upgrade-check -c prod-east

# Include passing checks, as JSON for a gate
refresh cluster upgrade-check -c prod-east --show-passing -o json

# Drill into one insight (by name, short ID, or full ID)
refresh cluster upgrade-check -c prod-east --id "deprecated"
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

### Readiness gates

**Cluster Insights.** Before each control-plane step (at plan time for the
first hop, and against live state before every later hop), `refresh` asks EKS
to re-evaluate the cluster's insights (`StartInsightsRefresh`) and waits up to
5 minutes for the refresh to finish. EKS otherwise re-evaluates insights only
about once a day, so right after a control-plane hop the next version usually
has no insights at all. The readiness step is **blocked** when:

- an insight for the hop version reports `ERROR` or `UNKNOWN`,
- EKS has no insights for the hop version yet, or
- the refresh fails or does not finish in time.

EKS no longer enforces insights when it updates the cluster version, so this
gate is the only check for deprecated APIs and for the kubelet skew of nodes
outside managed nodegroups (Fargate, Karpenter, self-managed, hybrid, and Auto
Mode nodes). `--skip-insights-check` turns it off. Use it only when you have
checked those yourself; the plan then carries a warning.

The refresh needs the IAM actions `eks:StartInsightsRefresh` and
`eks:DescribeInsightsRefresh`, in addition to `eks:ListInsights`.

`--dry-run` starts no refresh, because `StartInsightsRefresh` is a write API.
It reads the insights EKS already has. `ERROR` insights still block the
preview. Missing or `UNKNOWN` insights show as a warning, because a real run
refreshes them first and blocks until EKS has evaluated the hop version.

**Nodegroup pre-flight.** Before each nodegroup roll, after the nodegroup is
confirmed `ACTIVE` with no health issues, `refresh` runs the same
[pre-flight health checks](../concepts/health-checks.md) as
[`nodegroup update`](nodegroup.md#update), scoped to that nodegroup:

- A PodDisruptionBudget that allows 0 disruptions for pods on the nodegroup
  (including a PDB whose status is not synced) stops the roll, before EKS is
  asked to roll anything. With `--force` (which lets EKS evict through PDBs)
  it is a warning instead.
- A `BLOCK` health decision stops the roll.
- Health warnings need `--yes` or a confirmation at the prompt.

The PDB check reads the cluster through the kubeconfig context whose API server
matches the cluster endpoint. Without one, `refresh` prints a warning, skips the
PDB check, and continues. `--skip-health-check` turns off these nodegroup
checks.

### Nodegroup rolls

A nodegroup that lags the control plane normally rolls once per hop, straight
to the hop target. The plan rolls a nodegroup early, to the current
control-plane version, only when the next control-plane step would put it past
the kubelet skew limit (3 minor versions). A nodegroup already past that limit,
or one that is custom-AMI or skipped, blocks the plan instead.

If an installed add-on is not compatible with the live control-plane version
(for example, after an interrupted hop), the plan first adds a catch-up hop
that updates it for the current version, before the next control-plane step.

If a nodegroup is already `UPDATING` when its turn comes, `refresh` waits for
that update to settle, reads the version again, and then skips it or rolls it.
During long waits, throttling, server, and network errors are retried;
permanent errors such as `AccessDenied` fail at once.

The live node-roll panel shows only when stdout is a color terminal. Piped,
CI, and `NO_COLOR` runs print text progress.

!!! note "Resumable by design"
    The plan is re-derived from live cluster state on every run — no state
    file. Rerunning after a failure (or Ctrl+C) resumes where it left off, and
    rerunning after success is a no-op. On failure or an interrupt, `refresh`
    prints the exact resume command. It repeats `--profile`, `--region`,
    `--skip`, `--skip-nodegroup`, `--force`, `--skip-insights-check`,
    `--skip-health-check`, and `--yes` when you gave them, so the rerun
    changes the same things in the same account and region. A rerun at the
    target version moves no control plane, so it needs no insights.

!!! warning "This mutates the control plane"
    `cluster upgrade` confirms each mutating phase unless you pass `--yes`.
    A multi-hop upgrade legitimately runs for hours; the default timeout is
    `4h` (`REFRESH_TIMEOUT` does not change it). Ctrl+C or the timeout while
    the plan is built exits as an interrupt and changes nothing. Start with
    `--dry-run`.

### Flags

| Flag | Description |
|---|---|
| `--to` | **Required.** Target Kubernetes version (e.g. `1.33`) |
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--dry-run, -d` | Print the full ordered plan without mutating anything |
| `--yes, -y` | Skip per-phase confirmation prompts |
| `--force` | Force nodegroup rolls when pods can't be drained due to PDBs |
| `--skip-insights-check` | Upgrade without the Cluster Insights readiness check (deprecated APIs, kubelet skew of nodes outside managed nodegroups). Risky: EKS does not block the upgrade itself |
| `--skip-health-check` | Roll nodegroups without the pre-flight PDB drain-blocker and health checks (not recommended) |
| `--skip, -s` | Add-on name to skip, exact and case-insensitive (repeatable; for add-ons managed via Helm/GitOps) |
| `--skip-nodegroup` | Nodegroup name pattern to skip (repeatable) |
| `--quiet, -q` | Suppress progress output |
| `--poll-interval, -p` | How often to poll in-flight updates (default `15s`) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain`. With `json`/`yaml`, stdout gets one document: the plan for `--dry-run` or a blocked plan, else `{plan, report}` after the run. Progress goes to stderr, and a run without `--dry-run` needs `--yes`. With `plain`, stdout gets the plan as TSV and everything else goes to stderr |
| `--timeout, -t` | Overall upgrade timeout (default `4h`; not read from `REFRESH_TIMEOUT`) |

!!! tip "Exit code in dry-run"
    A dry-run (or any run) whose plan contains a **blocker** prints the plan and
    exits non-zero without mutating — handy as a readiness gate in CI.

!!! note "Kubernetes access for the live roll view"
    The nodegroup phase renders the same live per-node roll panel as
    [`nodegroup update`](nodegroup.md#update); see that page for the Kubernetes
    RBAC it uses (`list` on nodes/pods/events, plus `watch` for streaming).

### Examples

```bash
# Print the plan only (exits non-zero if anything blocks the upgrade)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive (CI) run, skipping a Helm-managed add-on
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-load-balancer-controller

# Machine-readable run: {plan, report} on stdout, progress on stderr
refresh cluster upgrade -c prod-east --to 1.33 --yes -o json | jq '.report'
```

See the [upgrade lifecycle](../concepts/lifecycle.md) for how this fits the
`status → upgrade-check → patch → upgrade` loop.
