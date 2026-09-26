# cluster

Discover and operate on EKS clusters: list them (optionally across every
region), describe one in depth, run a read-only upgrade-readiness check,
orchestrate a full control-plane → add-on → nodegroup upgrade, and roll an
upgrade back one minor version.

```bash
refresh cluster <list|describe|upgrade-check|upgrade|rollback> [args] [flags]
```

The cluster argument is a positional on most subcommands, or `--cluster/-c`,
falling back to the [active context](../concepts/contexts.md). The read-only
subcommands also fall back to the kubeconfig's current cluster; `upgrade`
and `rollback` never do. See
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

With several regions, a region that fails is a `Region` failure: `-o
json`/`-o yaml` list it under `failures`, and stderr names it on one line.
The command prints the clusters it gathered, then exits `4` (incomplete
data). If no region answers (for example, the timeout ends first), the
command fails with exit `1` instead of printing an empty list. The default
`-A` sweep skips regions these credentials can't use, the same way `status
-A` does, and a skipped region does not count as a failure.

A cluster or nodegroup that could not be read also makes the command exit
`4`. Its row keeps what was read and has `"incomplete": true`; the table
marks its `NODES` cell with `○` (`unknown` when nothing was counted) and
lists the failure under `INCOMPLETE DATA`. With `--watch`, the watch keeps
running after a partial result.

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "ClusterList",
  "clusters": [
    {"name": "prod", "status": "ACTIVE", "region": "us-east-1", "incomplete": true, "...": "..."}
  ],
  "count": 1,
  "failures": [
    {
      "kind": "Nodegroup",
      "name": "web",
      "cluster": "prod",
      "region": "us-east-1",
      "operation": "eks:DescribeNodegroup",
      "reason": "Throttled",
      "retryable": true,
      "error": "ThrottlingException: Rate exceeded",
      "awsErrorCode": "ThrottlingException"
    }
  ]
}
```

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
| `--detailed` | Show comprehensive networking and security information |
| `--no-health` | Skip the health checks (health is shown by default) |
| `--show-security` | Add the `SECURITY` section: service role, KMS key, deletion protection, endpoint access. `-o json`/`-o yaml` always carry these fields |
| `--no-addons` | Skip the EKS add-on section (add-ons are shown by default) |
| `--check-readiness, -R` | Measure real Kubernetes node readiness (`Ready/desired`) via the cluster API, and list the nodegroups (no `--detailed` needed); without it the `NODES` column shows the desired count only |
| `--kubeconfig` | Path to the kubeconfig for `--check-readiness` (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context for `--check-readiness`, even if its server does not match the cluster endpoint (see [kubeconfig matching](../concepts/configuration.md#matching-the-kubeconfig-to-the-target-cluster)) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

!!! note "Changed in 0.11"
    `-d`, `-s`, and `-a` were removed; use `--detailed`, `--show-security`,
    and `--no-addons`. `--show-health` and `--include-addons` were on by
    default, so they did nothing. They still work in 0.11 as hidden flags that
    print a deprecation warning, and `--show-health=false` /
    `--include-addons=false` act as `--no-health` / `--no-addons`. They go
    away in 0.12.

The support window uses the cluster's upgrade policy. `-o json`/`-o yaml`
include it as `supportType` (`STANDARD` clusters are auto-upgraded at the end
of standard support). `cluster upgrade-check` reports it the same way.

If some add-ons or nodegroups can't be read, `describe` prints the rest,
lists each one under the top-level `failures` (`-o json`/`-o yaml`), names it
on stderr (or under `INCOMPLETE DATA` in the table), and exits `4`
(incomplete data). An add-on or nodegroup list that was not collected is
left out of the document; one that was collected and is empty is `[]`.

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
EKS Hybrid Nodes. After an in-place upgrade, EKS also reports
`ROLLBACK_READINESS` insights (`--category ROLLBACK_READINESS`).

While a [rollback](#rollback) is available, the report has a `rollback` line
with the previous version, the date the window closes, and the rollback
readiness insight counts. The JSON document has the same data under
`rollback`. The key is left out when no rollback is available or the update
history can't be read.

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
| `--status` | Filter by insight status: `PASSING`, `WARNING`, `ERROR`, `UNKNOWN`. `PASSING` needs no `--show-passing` |
| `--show-passing` | Include `PASSING` insights (hidden by default) |
| `--id` | Show the detail view for one insight of `--category` — accepts its short ID (from the table), full ID, or a case-insensitive name substring |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--exit-zero` | Exit `0` even when the check finds warnings, blockers, or unreadable items (report mode) |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

### Exit codes (CI gate)

The exit code follows the readiness verdict:

| Code | Verdict | Meaning |
|---|---|---|
| `0` | `READY` | No finding |
| `2` | `REVIEW` | A `WARNING` insight, a nodegroup behind the control plane, an addon behind latest, or a control-plane health warning |
| `3` | `NOT READY` | An `ERROR` or `UNKNOWN` insight, a nodegroup at the kubelet skew limit, or a failed control-plane health check |
| `4` | `INCOMPLETE` | Nothing blocks, but a nodegroup or add-on could not be read (listed under `failures` in `-o json`) |
| `1` | | An error (AWS error, cluster not found, interrupt) |

Precedence is `3`, then `4`, then `2`: the item that could not be read could
be a blocker.

`UNKNOWN` blocks because `cluster upgrade` refuses a hop on it too. With
`-o json`/`-o yaml`, the document is printed first, then the exit code
applies. With `--id`, the exit code reflects that one insight. `--exit-zero`
keeps the report and always exits `0` on a completed check. See
[Exit codes](../concepts/exit-codes.md#cluster-upgrade-check).

`upgrade-check` reads the insights EKS already has. It does not start an
insights refresh, so right after a control-plane change the list can be
empty or stale. `cluster upgrade` refreshes them before each hop.

With `-o plain`, stdout has only the insight rows. The readiness verdict,
support, control plane, and version skew go to stderr.

### Examples

```bash
# Readiness summary for prod-east
refresh cluster upgrade-check -c prod-east

# Include passing checks, as JSON for a gate (exit 2 or 3 fails the job)
refresh cluster upgrade-check -c prod-east --show-passing -o json

# Report only: same JSON, always exit 0
refresh cluster upgrade-check -c prod-east -o json --exit-zero

# Drill into one insight (by name, short ID, or full ID)
refresh cluster upgrade-check -c prod-east --id "deprecated"
```

---

## upgrade

Plan and execute a full EKS cluster upgrade to a target Kubernetes version:
control plane → nodegroups → add-ons, with a health gate after every phase.

```bash
refresh cluster upgrade [cluster] --to <version> [flags]
```

EKS upgrades one minor version at a time, so a multi-minor jump expands into
sequential **hops**. Each hop runs these phases:

1. Readiness: cluster insights and kubelet version skew.
2. Control plane.
3. Required add-ons: the add-ons whose installed version is not compatible
   with the new control-plane version (`DescribeAddonVersions`). kube-proxy
   is usually one, because its version follows the Kubernetes minor.
4. Nodegroup rolls.
5. The other add-ons: those behind the latest compatible version but still
   compatible with the new control plane.

This is the order in the
[EKS user guide](https://docs.aws.amazon.com/eks/latest/userguide/update-cluster.html):
update the nodes, then the add-ons. The one exception is an add-on that the
new control plane cannot run: it goes before the rolls. A hop leaves out a
phase with nothing to do. Each add-on phase runs in dependency order and moves
each add-on to the latest version compatible with the hop target. In
`-o json` and `-o yaml`, a required add-on step has `beforeNodegroups: true`.

### Readiness gates

**Cluster Insights.** Before each control-plane step (at plan time for the
first hop, and against live state before every later hop), `refresh` asks EKS
to re-evaluate the cluster's insights (`StartInsightsRefresh`) and waits up to
5 minutes for the refresh to finish. EKS otherwise re-evaluates insights only
about once a day, so right after a control-plane hop the next version usually
has no insights at all. The readiness step is **blocked** when:

- an insight for the hop version reports `ERROR` or `UNKNOWN`,
- EKS has no insights for the hop version yet, or
- the refresh fails or does not finish in time, or the insights can't be
  read. The gate fails closed, and the read is also a
  [failure](../concepts/output.md#failures) in the plan's `failures`.

EKS no longer enforces insights when it updates the cluster version, so this
gate is the only check for deprecated APIs and for the kubelet skew of nodes
outside managed nodegroups (Fargate, Karpenter, self-managed, hybrid, and Auto
Mode nodes). `--skip-insights-check` turns it off. Use it only when you have
checked those yourself; the plan then carries a notice.

The refresh needs the IAM actions `eks:StartInsightsRefresh` and
`eks:DescribeInsightsRefresh`, in addition to `eks:ListInsights`.

`--dry-run` starts no refresh, because `StartInsightsRefresh` is a write API.
It reads the insights EKS already has. `ERROR` insights still block the
preview. Missing or `UNKNOWN` insights show as a notice, because a real run
refreshes them first and blocks until EKS has evaluated the hop version.
Insights that can't be read are a failure, so the dry run exits `4`.

**Nodegroup pre-flight.** Before each nodegroup roll, after the nodegroup is
confirmed `ACTIVE` with no health issues, `refresh` runs the same
[pre-flight health checks](../concepts/health-checks.md) as
[`nodegroup update`](nodegroup.md#update), scoped to that nodegroup:

- A drain blocker on the nodegroup stops the roll, before EKS is asked to
  roll anything. A drain blocker is a PodDisruptionBudget that allows 0
  disruptions (including a PDB whose status is not synced), or a pod that
  more than one PDB selects. With `--force` (which lets EKS evict through
  PDBs) it is a warning instead.
- A `BLOCK` health decision stops the roll.
- Health warnings need `--yes` or a confirmation at the prompt.

The PDB check reads the cluster through the kubeconfig context whose API server
matches the cluster endpoint, or through the context that you name with
`--kube-context`. Without one, `refresh` prints a warning, skips the
PDB check, and continues. `--skip-health-check` turns off these nodegroup
checks.

### Nodegroup rolls

A nodegroup that lags the control plane normally rolls once per hop, straight
to the hop target. The plan rolls a nodegroup early, to the current
control-plane version, only when the next control-plane step would put it past
the kubelet skew limit (3 minor versions). A nodegroup already past that limit,
or one that is custom-AMI or skipped, blocks the plan instead.

An Amazon Linux 2 nodegroup (`AL2_*` AMI types) blocks any hop to Kubernetes
1.33 or later: EKS publishes no AL2 AMIs past 1.32, so its roll would fail
after the control plane moved. Replace it with an AL2023 or Bottlerocket
nodegroup first, or leave it at its version with `--skip-nodegroup`, which
makes it a `Manual` step.

If an installed add-on is not compatible with the live control-plane version
(for example, after an interrupted hop), the plan first adds a catch-up hop
that updates it for the current version, before the next control-plane step.
A run that stops in the last add-on phase needs no catch-up: those add-ons
still run on the live control plane, and the rerun updates them.

If a nodegroup is already `UPDATING` when its turn comes, `refresh` waits for
that update to settle, reads the version again, and then skips it or rolls it.
During long waits, throttling, server, and network errors are retried;
permanent errors such as `AccessDenied` fail at once.

The live node-roll panel shows only when stdout is a color terminal. Piped,
CI, and `NO_COLOR` runs print text progress.

!!! note "Resumable by design"
    The plan is re-derived from live cluster state on every run — no state
    file. Rerunning after a failure (or Ctrl+C) resumes where it left off, and
    rerunning after success is a no-op. On a failure, an interrupt, or a
    timeout, `refresh` prints the exact resume command. It repeats `--profile`, `--region`,
    `--skip`, `--skip-nodegroup`, `--kubeconfig`, `--kube-context`,
    `--wait-timeout`, `--force`, `--skip-insights-check`,
    `--skip-health-check`, and `--yes` when you gave
    them, so the rerun changes the same things in the same account and region. A rerun at the
    target version moves no control plane, so it needs no insights.

!!! warning "This mutates the control plane"
    `cluster upgrade` confirms each mutating phase unless you pass `--yes`.
    Without a terminal, a run without `--yes` or `--dry-run` fails before any
    AWS call. A multi-hop upgrade legitimately runs for hours; the default
    `--wait-timeout` is `4h` (`REFRESH_TIMEOUT` does not change it). When
    `--wait-timeout` runs out, the error says `timed out` and tells you to
    increase `--wait-timeout`. Ctrl+C says `interrupted`. Both exit `1`. If
    either happens while the plan is built, nothing changes. If it happens
    during a phase, in-flight EKS updates continue in AWS. Start with
    `--dry-run`.

### Flags

| Flag | Description |
|---|---|
| `--to` | **Required.** Target Kubernetes version (e.g. `1.33`) |
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--dry-run, -d` | Print the full ordered plan without mutating anything |
| `--yes, -y` | Skip per-phase confirmation prompts (required with `-o json`/`yaml` or without a terminal) |
| `--force` | Force nodegroup rolls when pods can't be drained due to PDBs |
| `--skip-insights-check` | Upgrade without the Cluster Insights readiness check (deprecated APIs, kubelet skew of nodes outside managed nodegroups). Risky: EKS does not block the upgrade itself |
| `--skip-health-check` | Roll nodegroups without the pre-flight PDB drain-blocker and health checks (not recommended) |
| `--kubeconfig` | Path to the kubeconfig for the PDB drain-blocker checks and the live roll panel (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint (see [kubeconfig matching](../concepts/configuration.md#matching-the-kubeconfig-to-the-target-cluster)) |
| `--skip` | Add-on name to skip, exact and case-insensitive (repeatable; for add-ons managed via Helm/GitOps) |
| `--skip-nodegroup` | Nodegroup name pattern to skip (repeatable) |
| `--quiet, -q` | Suppress progress output |
| `--poll-interval` | How often to poll in-flight updates (default `15s`; must be greater than `0`) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain`. With `json`/`yaml`, stdout gets one document: the plan for `--dry-run` or a blocked plan, else `{plan, report, failures}` after the run. Progress goes to stderr, and a run without `--dry-run` needs `--yes`. With `plain`, stdout gets the plan as TSV and everything else goes to stderr |
| `--wait-timeout` | How long to wait for the whole upgrade to finish (default `4h`; `0` = no limit; not read from `REFRESH_TIMEOUT`) |
| `--timeout, -t` | Global API timeout when given before the subcommand (`refresh -t 2m cluster upgrade ...`). After the subcommand it is a deprecated alias of `--wait-timeout` in 0.11 and prints a warning |

!!! tip "Exit code in dry-run"
    A dry-run (or any run) whose plan contains a **blocker** prints the plan and
    exits `3` without mutating, so it works as a readiness gate in CI. A plan
    with no blocker whose planner could not read something (the check that
    EKS offers the target version, the insights, or an add-on's version
    catalog) exits `4`. A real run with such a failure goes ahead as before,
    and exits `4` when it finishes.

### JSON document

With `-o json` or `-o yaml`, `--dry-run` and a blocked plan print the plan.
An executed run prints `{plan, report, failures}`:

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "UpgradeRun",
  "plan": {
    "clusterName": "prod", "currentVersion": "1.31", "targetVersion": "1.32",
    "hops": [{"from": "1.31", "to": "1.32", "steps": ["..."]}],
    "notices": ["insight warnings for 1.32: Deprecated APIs removed in 1.32"],
    "failures": []
  },
  "report": {
    "status": "Failed",
    "completed": [],
    "stoppedAt": "control plane 1.31 → 1.32",
    "remaining": ["addons for 1.32 (2 update(s), dependency order)"],
    "failure": {"kind": "Update", "name": "prod", "region": "us-east-1",
                "reason": "UpdateFailed", "retryable": false,
                "error": "control plane upgrade to 1.32 failed: insufficient subnet IPs",
                "updateId": "9c8b7a6d-..."}
  },
  "failures": [
    {"kind": "Update", "name": "prod", "region": "us-east-1",
     "reason": "UpdateFailed", "retryable": false,
     "error": "control plane upgrade to 1.32 failed: insufficient subnet IPs",
     "updateId": "9c8b7a6d-..."}
  ]
}
```

Each plan step has a `type` (`Readiness`, `ControlPlane`, `Addon`, or
`Nodegroup`) and a `status` (`Pending`, `Completed`, `Blocked`, or
`Manual`). The `-o plain` columns keep the lower-case words
(`control-plane`, `pending`).

A `Manual` step is one refresh leaves to you: a custom-AMI nodegroup, or
an add-on or nodegroup skipped with `--skip` / `--skip-nodegroup`. When a run
ends with manual steps, the table view says the upgrade is not complete and
lists them, instead of "Upgrade complete". The exit code does not change.

`plan.notices` are advisory and never change the exit code. `plan.failures`
are the reads the planner could not make. `report.status` says how the run
ended, and `report.failure` says why it stopped:

| `report.status` | Meaning |
|---|---|
| `Succeeded` | Every pending phase finished, or there was nothing to do |
| `Failed` | A phase failed (`failure` says why: for example `UpdateFailed`) |
| `Blocked` | A gate stopped the run: the live readiness re-check, a pre-roll nodegroup gate, or an add-on's post-update health gate. `failure` is set when the gate could not read what it needed |
| `Interrupted` | Ctrl+C or SIGTERM (`failure.reason` is `Interrupted`). Started EKS updates keep running |
| `TimedOut` | `--wait-timeout` passed (`failure.reason` is `Timeout`). Started EKS updates keep running |
| `Aborted` | You declined a phase confirmation |

The top-level `failures` lists the plan's failures and the report's
failure. Each is also named once: on stderr, or under `INCOMPLETE DATA` in
the table view.

!!! note "Kubernetes access for the live roll view"
    The nodegroup phase renders the same live per-node roll panel as
    [`nodegroup update`](nodegroup.md#update); see that page for the Kubernetes
    RBAC it uses (`list` on nodes/pods/events, plus `watch` for streaming).

### Examples

```bash
# Print the plan only (exits 3 if anything blocks the upgrade)
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

---

## rollback

Roll a cluster back to the previous minor version (N to N-1) after an
in-place upgrade. refresh follows the order in the AWS guide
[Roll back a cluster to a previous Kubernetes version](https://docs.aws.amazon.com/eks/latest/userguide/rollback-cluster.html):

1. Check the prerequisites and the `ROLLBACK_READINESS` cluster insights.
2. Roll back the managed nodegroups that run N to N-1.
3. Downgrade the add-ons whose version N-1 cannot run to the newest version
   compatible with N-1. EKS does not roll back add-ons itself.
4. Roll back the control plane (`UpdateClusterVersion` to N-1) and wait for
   the `VersionRollback` update to finish.

```bash
refresh cluster rollback [cluster] [flags]
```

### Prerequisites

EKS accepts a rollback only when all of these are true. refresh checks them
first, prints the plan, and exits `3` when one fails:

- The upgrade to the current version finished less than 7 days ago. EKS
  counts from when the upgrade finished, but the update history records only
  when it started, so refresh refuses only once the window has certainly
  closed; in the last few hours it warns and lets EKS decide. Just before the
  first change, refresh checks the window, the target, and the insights again.
  refresh reads the date from the cluster's update history (`ListUpdates`).
  EKS counts from the end of the upgrade, so the dates are approximate, and
  EKS has the final word.
- The cluster was upgraded in place to its current version. A cluster created
  at its version cannot roll back.
- The target is one minor back and a version EKS still supports.
- If the target is in extended support, the cluster's upgrade policy is
  `EXTENDED`. Change it first with
  `aws eks update-cluster-config --name <cluster> --upgrade-policy supportType=EXTENDED`.
  Extended support charges then apply.
- The cluster is `ACTIVE` with no update in progress.
- No `ROLLBACK_READINESS` insight is `ERROR` or `UNKNOWN`. `WARNING` is a
  notice. EKS refreshes stale insights itself when the rollback starts.

EKS also rejects a rollback when an EKS feature enabled on the cluster does
not exist in N-1. refresh shows the EKS error.

EKS does not roll back self-managed nodes, hybrid nodes, or Fargate pods.
Move them to N-1 yourself before you roll back. Fargate pods at N cause an
`ERROR` kubelet skew insight until you delete them. EKS rolls back
EKS Auto Mode nodes itself, before the control plane.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--dry-run, -d` | Print the plan without changing anything. Never prompts |
| `--yes, -y` | Skip the confirmation prompt (required with `-o json`/`yaml` or without a terminal) |
| `--force` | Force nodegroup rollbacks when pods can't be drained due to PDBs (the same meaning as in `cluster upgrade`) |
| `--skip-insights-check` | Roll back despite `ERROR` or `UNKNOWN` rollback-readiness insights. refresh also sets `force` on `UpdateClusterVersion`, so EKS skips them too. Not recommended |
| `--skip-health-check` | Roll back nodegroups without the pre-flight PDB drain-blocker and health checks (not recommended) |
| `--kubeconfig` | Path to the kubeconfig for the PDB drain-blocker checks and the live roll panel |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint |
| `--skip-nodegroup` | Nodegroup name pattern to leave alone (repeatable). Move it to N-1 yourself |
| `--rollback-timeout` | How long EKS may take before it cancels the control-plane rollback (`RollbackConfig`), from `2h` to `168h`. Default: EKS's `12h` |
| `--quiet, -q` | Suppress progress output |
| `--wait-timeout` | How long to wait for the whole rollback to finish (default `4h`; `0` = no limit) |
| `--poll-interval` | How often to poll in-flight updates (default `15s`) |
| `--format, -o` | `table` (default), `json`, `yaml`. With `json`/`yaml`, stdout gets one document: the `RollbackPlan` for `--dry-run` or a blocked plan, else a `RollbackRun` (`{plan, report, failures}`) after the run |

`--force` in `cluster upgrade` already means "evict pods despite PDBs", so
the insight bypass is `--skip-insights-check`, the name `cluster upgrade`
uses for its own insight check.

### How it runs

refresh asks once before it changes anything. The question lists the
nodegroups it rolls back, the add-ons it downgrades, and the control-plane
rollback. Before each nodegroup rollback, the same pre-flight checks as
[`cluster upgrade`](#nodegroup-rolls) run, including PodDisruptionBudgets
that would block the drain.

The plan is derived from live cluster state on every run. A rerun after a
failure or Ctrl+C skips nodegroups already at N-1 and add-ons already
compatible with N-1. A rerun after a finished rollback finds the
`VersionRollback` update in the history and has nothing to do.

A plan step has the same `type` and `status` values as an
[upgrade plan](#json-document). `report.status` has the same values as in
`cluster upgrade`.

### Rollback exit codes

| Code | Meaning |
|---|---|
| `0` | The rollback finished, or there was nothing to do. A `--dry-run` with no blocker |
| `1` | A phase failed, you declined the confirmation, the run was interrupted, or it timed out |
| `3` | Blocked, nothing changed: outside the rollback window, no in-place upgrade, a blocking insight, the upgrade policy, or an unsupported target. Also with `--dry-run` |
| `4` | The planner could not read something, such as the target's support status or an add-on's version catalog |

### Examples

```bash
# Print the plan only (exits 3 if anything blocks the rollback)
refresh cluster rollback prod-east --dry-run

# Roll back, after one confirmation
refresh cluster rollback prod-east

# Non-interactive run with a 4h EKS rollback timeout
refresh cluster rollback prod-east --yes --rollback-timeout 4h -o json | jq '.report'
```
