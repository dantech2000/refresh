# nodegroup

Inspect and operate on a cluster's managed nodegroups: list them with AMI
freshness, describe one in depth, scale desired/min/max size (with optional PDB
and health gating), and roll nodegroups to the latest recommended AMI with
pre-flight health checks and live monitoring.

```bash
refresh nodegroup <list|describe|scale|update> [args] [flags]
```

The group has the alias `ng`. The cluster is a positional on most subcommands,
or `--cluster/-c`, falling back to the
[active context](../concepts/contexts.md). `list` and `describe` also fall back
to the kubeconfig's current cluster; `scale` and `update` never do. See
[Cluster resolution](../concepts/configuration.md#cluster-resolution).

---

## list

List the managed nodegroups in a cluster with their status, instance type, node
counts, and AMI freshness (whether each is on the latest recommended AMI).

The `NODES` column shows the desired node count by default. Add
`--check-readiness` to measure real Kubernetes node readiness (`Ready/desired`)
via the cluster API; when the cluster is unreachable it degrades to the desired
count rather than reporting a fabricated ready figure.

AMI freshness is judged against each nodegroup's own Kubernetes version, not
the control plane's. A nodegroup on an older minor than the control plane
shows a warning in the `VERSION` column (`(behind)` in `-o plain`).

If the latest-AMI lookup fails (for example, a missing `ssm:GetParameter`),
the AMI column shows `unknown (lookup failed)` and one warning goes to stderr.
The lookup is advisory: the row has an `amiLookupFailure` object with the
same keys as a failure, and the exit code does not change (see
[Advisory AMI lookup](../concepts/output.md#advisory-ami-lookup)).

If some nodegroups can't be described, the command prints the rest, lists
each one under `failures` with `-o json`/`-o yaml`, names it on stderr (or
under `INCOMPLETE DATA` in the table), and exits `4` (incomplete data):

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "NodegroupList",
  "cluster": "prod",
  "nodegroups": [
    {
      "name": "api",
      "amiStatus": "Unknown",
      "amiLookupFailure": {
        "kind": "Nodegroup",
        "name": "api",
        "cluster": "prod",
        "region": "us-east-1",
        "operation": "ssm:GetParameter",
        "reason": "AccessDenied",
        "retryable": false,
        "error": "AccessDeniedException: ... not authorized to perform: ssm:GetParameter",
        "awsErrorCode": "AccessDeniedException"
      },
      "...": "..."
    }
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

```bash
refresh nodegroup list [cluster] [flags]
```

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--filter, -f` | Filter, `key=value` (keys: `name`, `status`, `instanceType`, `amiStatus`); repeatable |
| `--check-readiness, -R` | Measure real Kubernetes node readiness (`Ready/desired`) via the cluster API |
| `--kubeconfig` | Path to the kubeconfig for `--check-readiness` (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint (see [kubeconfig matching](../concepts/configuration.md#matching-the-kubeconfig-to-the-target-cluster)) |
| `--sort` | Sort by field: `name` (default), `status`, `instance`, `nodes` |
| `--desc` | Sort descending |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--watch, -w` | Re-run and redraw every `--watch-interval` until interrupted (not with `-o json`/`-o yaml`) |
| `--watch-interval` | Refresh interval for `--watch` (default `10s`) |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
# Only nodegroups on a stale AMI
refresh nodegroup list my-cluster --filter amiStatus=outdated

# Plain TSV for scripting
refresh nodegroup list my-cluster -o plain | awk -F'\t' 'NR>1 {print $1}'

# Watch a roll progress live
refresh nodegroup list my-cluster --watch
```

---

## describe

Detailed information for one nodegroup: scaling config, instance type(s),
AMI/release version and freshness, and optional per-instance and workload
placement details.

```bash
refresh nodegroup describe [cluster] [nodegroup] [flags]
```

`describe` has the alias `get`. The nodegroup name may be the second positional
or `--nodegroup/-n`. If a `refresh` context is active and you pass one
positional argument without `--cluster` or `--nodegroup`, that argument is the
nodegroup name, and the cluster comes from the context.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name |
| `--nodegroup, -n` | Nodegroup name (or pass as second positional) |
| `--show-instances, -I` | Include EC2 instance details (`instances` with `-o json`; left out without this flag) |
| `--show-workloads, -W` | Include workload/pod placement info |
| `--kubeconfig` | Path to the kubeconfig for `--show-workloads` (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint (see [kubeconfig matching](../concepts/configuration.md#matching-the-kubeconfig-to-the-target-cluster)) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

`--show-workloads` reads the cluster through the kubeconfig context whose
server matches the cluster endpoint, or through the context that you name with
`--kube-context`. If no context is usable, the workloads show as unavailable.

### Examples

```bash
refresh nodegroup describe my-cluster ng-default
refresh nodegroup describe my-cluster ng-default --show-instances --show-workloads
refresh nodegroup describe ng-default   # in the active context's cluster
```

---

## scale

Change a managed nodegroup's desired/min/max size. Any subset of
`--desired/--min/--max` may be set, and at least one is required (exit `1`
before any AWS call without one). Unspecified bounds are left unchanged.
Only `--desired` changes the node count. If you omit `--desired`, a `--max`
below the current desired size, or a `--min` above it, fails before any change
(exit `1`). Pass `--desired` with the new bounds to scale and change the
bounds together.

```bash
refresh nodegroup scale [cluster] -n <nodegroup> [flags]
```

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name (or pass as positional) |
| `--nodegroup, -n` | **Required.** Nodegroup name |
| `--desired` | Desired node count |
| `--min` | Minimum node count |
| `--max` | Maximum node count |
| `--health-check` | Validate cluster health before and after scaling |
| `--check-pdbs` | Refuse a scale-down that could remove more of a Pod Disruption Budget's pods than it allows |
| `--force` | With `--check-pdbs`, scale down anyway and print the blocking PDBs as a warning |
| `--wait` | Wait for the EKS update to finish, then check that the nodegroup has the requested sizes |
| `--wait-timeout` | How long to wait for the scaling to finish with `--wait` (default `5m`; added on top of `--timeout`; `0` = no limit). Replaces `--op-timeout`, which still works in 0.11 with a deprecation warning |
| `--kubeconfig` | Kubeconfig for workload/PDB checks (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint |
| `--dry-run, -d` | Preview the scaling impact without executing. Never prompts |
| `--yes, -y` | Scale without the confirmation prompt (required without a terminal) |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`). With `--wait`, `--wait-timeout` is added on top |

!!! warning "Confirmation (new in 0.11)"
    Before it changes a size, `scale` asks for confirmation, for example
    `Scale prod/ng-default desired 3 → 1? [y/N]`. Only `y` or `yes`
    continues. `--yes` skips the prompt. Without a terminal, a run without
    `--yes` or `--dry-run` fails before any AWS call. Add `--yes` to scripts.

!!! warning "A scale-down does not honor PDBs"
    When a scaling change lowers the desired size, EKS terminates the removed
    nodes without waiting for Pod Disruption Budgets. With `--check-pdbs`,
    `refresh` refuses the scale-down (exit 3, before any change) if it could
    remove more of a PDB's pods than the PDB allows, and lists those PDBs. The
    Auto Scaling group picks which nodes go, so the gate assumes the removed
    nodes are the ones that hold the most of the PDB's pods. The gate fails
    closed: if it can't read the PDBs or pods (for example, no Kubernetes
    access), it refuses the scale-down too. Pass `--force` to scale down
    anyway. Combine `--dry-run --check-pdbs` to see the verdict and the
    blocking PDBs before you touch anything. The preview exits with the code
    the real run would: `3` if the gate refuses, `1` if it can't read the
    PDBs. A PDB check that can't read what it needs is a
    [failure](../concepts/output.md#failures), listed under
    `INCOMPLETE DATA`. With `--force`, the scale goes ahead without the
    check, and the command exits `4`. See
    [Scale-down PDB gate](../concepts/health-checks.md#scale-down-pdb-gate).

!!! note "Busy clusters"
    EKS runs one update at a time on a cluster. Before the prompt, `scale`
    reads what EKS is changing (the control plane, each nodegroup, each
    add-on). If something is changing, it exits `3` and names it. Nothing
    changed. `--dry-run` does not check.

With `--wait`, `refresh` follows the EKS update that the scaling request
starts. If the update fails or is cancelled, the command exits non-zero with
the update's error details. When the update succeeds, `refresh` reads the
nodegroup and fails if a requested size (`--desired`, `--min`, `--max`) does
not match. With `--desired`, it also waits for the nodegroup to be `ACTIVE`.
Throttling and network errors during the wait are retried. A permanent error,
such as a missing `eks:DescribeUpdate` permission, stops the wait at once.

### Examples

```bash
# Scale up to 5 nodes
refresh nodegroup scale my-cluster -n ng-default --desired 5

# Safe scale-down: preview the PDB gate's verdict and any blocking PDBs
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --dry-run

# Scale down for real, refused if a PDB blocks it, and wait for it to settle
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --wait

# Scale down even though a PDB blocks it (the blockers print as a warning)
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --force --yes
```

---

## update

Roll managed nodegroups to the latest recommended AMI, with pre-flight health
gates and live monitoring. This is the flagship patch command.

```bash
refresh nodegroup update [cluster] [nodegroup] [flags]
```

`update` has the alias `update-ami`. Omitting the nodegroup updates all
nodegroups in the cluster.

Each nodegroup rolls to the latest AMI for its own Kubernetes version. The
roll keeps the nodegroup on its minor version; use
[`cluster upgrade`](cluster.md#upgrade) to move it to a newer one. The latest
AMI comes from the SSM path of the nodegroup's AMI family (Amazon Linux,
Bottlerocket, or Windows). The nodegroup pattern rules, including the
confirmation for a name that is not exact, are in
[Nodegroup patterns](../concepts/configuration.md#nodegroup-patterns).

Before the roll, `refresh` runs the
[pre-flight health checks](../concepts/health-checks.md), scoped to the
nodegroups that match. `--quiet` does not skip them.

!!! warning "PDB drain gate (breaking change)"
    A PodDisruptionBudget that allows 0 disruptions, or a pod that more than
    one PDB selects, stops EKS draining a node, so the roll stalls after EKS
    has started it. `update` now checks each nodegroup it would roll after
    the health checks and refuses the run (exit `3`, nothing started) when it
    finds such a blocker. Before, it only warned. The error names each
    blocker. To go on, let the workloads recover, relax the PDB (or narrow
    its selector so each pod matches one PDB), or pass `--force`, which rolls
    anyway with a warning on stderr. `--dry-run` shows the blockers and exits
    `3` where the real run would refuse. The gate needs Kubernetes access:
    without it, the gate is skipped with the usual notice. `--skip-health-check`
    skips it too. In fleet mode each cluster is gated on its own, and a
    refused cluster has the status `DrainBlocked`.

!!! note "Busy clusters"
    EKS runs one update at a time on a cluster. Before the health checks and
    any prompt, `update` reads what EKS is changing: the control plane, each
    nodegroup, and each add-on. If something is changing, the run exits `3`
    and names it, for example `prod is busy (add-on vpc-cni UPDATING);
    nothing was started`. A selected nodegroup that is already `UPDATING`
    does not count: the run skips it (`AlreadyUpdating`). `--dry-run` and
    `--health-only` do not check. In fleet mode, a busy cluster is skipped
    with the status `Busy` and the rest of the fleet goes on.

!!! note "Custom-AMI nodegroups are skipped"
    Nodegroups whose AMI is managed via a launch template (`AmiType=CUSTOM`)
    are detected and **skipped** with guidance: their AMI rolls when you publish
    a new launch-template version and point the nodegroup at it, not via this
    command. `--force` and `--reroll` do not change this. `--dry-run` shows
    them with the action `SkipCustom`.

!!! note "Re-roll a nodegroup that is already on the latest AMI"
    A nodegroup already on the latest AMI is skipped. To roll it anyway, pass
    `--reroll`. `--force` also rolls it, but EKS then evicts pods even when a
    PodDisruptionBudget blocks the drain.

### Fleet mode

`--all-clusters` discovers clusters across regions (scope with `-r`) and rolls
them serially with one batch confirmation, an aggregate summary, and a
**worst-outcome** exit code.

```bash
refresh nodegroup update --all-clusters --dry-run            # fleet-wide plan
refresh nodegroup update --all-clusters -r us-east-1 --yes   # execute in one region
```

Fleet mode selects nodegroups only with `-n`. It rejects positional
arguments, `--cluster`, and `--kube-context`, because it matches each cluster
to a kubeconfig context by endpoint. An exported `EKS_CLUSTER_NAME` is
ignored. `--wait-timeout` applies to each cluster separately. `--health-only`
asks for no batch confirmation, because it changes nothing.

Discovery uses the same region rules as `status -A`: `-r`, then
`REFRESH_EKS_REGIONS`, then every EKS region in the partition. A global
`--region` before the subcommand only picks the partition. See
[Regions](../concepts/configuration.md#regions).

In the default region sweep (no `-r`, no `REFRESH_EKS_REGIONS`), regions your
credentials can't use are skipped with one stderr note. Examples are regions
an SCP denies and opt-in regions that are not enabled. Skipped regions don't
change the exit code; `-o json` lists them under `skippedRegions`. Any other
listing failure (throttling, a server error, a timeout), or any failure in a
region you named, is a `Region` failure: listed under `INCOMPLETE DATA` (a
`warning:` line on stderr with `-o json|yaml`), an entry in `failures`, and
exit `4`. If no region can be listed, or discovery
does not finish within `--wait-timeout`, the run fails at once with exit `1`:
nothing was gathered.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or partial pattern (overrides the active context; the kubeconfig is not used). Falls back to `EKS_CLUSTER_NAME` unless a positional is clearly the cluster (with `--nodegroup`, or two positionals). See [cluster resolution](../concepts/configuration.md#cluster-resolution) |
| `--nodegroup, -n` | Nodegroup name or partial pattern (if unset, update all). An exact name selects only that nodegroup. A pattern that is not an exact name needs confirmation, or `--yes` without a terminal (see [nodegroup patterns](../concepts/configuration.md#nodegroup-patterns)) |
| `--all-clusters` | Fleet mode: roll matching nodegroups across all discovered clusters (serial); scope with `-r` |
| `--region, -r` | Region(s) for `--all-clusters` discovery (default: partition EKS regions / `REFRESH_EKS_REGIONS`) |
| `--dry-run, -d` | Preview changes without executing |
| `--changelog` | In dry-run, print the `amazon-eks-ami` release notes between the current and target AMI for AL2/AL2023 nodegroups. Bottlerocket and Windows nodegroups get a link to their own release notes |
| `--force` | Force the roll: pass the PDB drain gate with a warning, and EKS evicts pods even when a PodDisruptionBudget blocks the drain (PDBs are bypassed). Also rolls nodegroups already on the latest AMI. To re-roll without bypassing PDBs, use `--reroll` |
| `--reroll` | Roll nodegroups that are already on the latest AMI instead of skipping them (for example, to replace nodes). PodDisruptionBudgets are honored |
| `--no-wait` | Don't wait for update completion (start-and-return) |
| `--quiet, -q` | Minimal output. `--quiet` does not prompt. A run that needs a confirmation (warn-level health findings, a nodegroup pattern that is not an exact name, the fleet batch) stops unless you pass `--yes` |
| `--skip-health-check` | Skip pre-flight health validation |
| `--health-only` | Run the health check only, don't update (exit `0`=pass / `2`=warn / `3`=block) |
| `--yes, -y` | Assume yes: skip confirmation prompts (a nodegroup pattern that is not an exact name, warn-level health) for CI |
| `--require-healthy` | Treat warn-level health findings as a hard stop (exit `2`) instead of prompting |
| `--skip-verify` | Skip post-roll verification (nodes ACTIVE, no new stuck pods) |
| `--live` | Force the live per-node roll panel, also when stdout is not a color terminal (a snapshot at most every 15s, only on change) |
| `--kubeconfig` | Kubeconfig for workload/PDB checks (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--kube-context` | Kubeconfig context to use, even if its server does not match the cluster endpoint (not with `--all-clusters`) |
| `--poll-interval` | Polling interval for update status (default `15s`; must be greater than `0`) |
| `--wait-timeout` | How long to wait for the update to finish (default `40m`; applies per cluster with `--all-clusters`; `0` = no limit; not read from `REFRESH_TIMEOUT`) |
| `--timeout, -t` | Global API timeout when given before the subcommand. After the subcommand it is a deprecated alias of `--wait-timeout` in 0.11 and prints a warning |
| `--format, -o` | `table` (default), `json`, or `yaml`: one document on stdout (the run summary, the `--dry-run` preview, or the `--health-only` verdict), with notices on stderr |

!!! warning "Unattended / CI"
    Without a TTY, with `--quiet`, or with `-o json`/`-o yaml`, a run that
    would otherwise prompt fails fast unless you pass `--yes`. For cron, pair `--yes` with
    `--require-healthy` and `-o json`. See
    [stdout and stderr](../concepts/output.md#stdout-and-stderr).

!!! note "Kubernetes access for the live roll view"
    During a roll, `refresh` renders a live per-node panel (draining / joining /
    Ready, pod-eviction progress, Warning events) read from the Kubernetes API
    via `--kubeconfig`. It needs the `list` verb on nodes, pods, and events;
    granting `watch` as well upgrades it to streaming, so node transitions
    appear as they happen instead of on the next poll. Access is optional and
    degrades gracefully: without `watch` the panel polls, and without any
    Kubernetes access the roll still runs — you just get the coarse EKS update
    status instead of per-node detail. The panel is on by default only when
    stdout is a color terminal. When output is piped, in CI, or with
    `NO_COLOR`, you get the standard progress lines instead. `--live` forces
    the panel there too, as a snapshot at most every 15s and only when
    something changed.

### JSON document

With `-o json` or `-o yaml`, a run prints one document. `nodegroups` has one
entry per selected nodegroup, with its `status`. A nodegroup that failed also
has a `failure`, and the top-level `failures` lists every failure of the run.
See [Failures](../concepts/output.md#failures) for the failure object.

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "NodegroupUpdate",
  "cluster": "prod",
  "nodegroups": [
    {"name": "ng-1", "status": "Succeeded", "updateId": "5e6f7a8b-..."},
    {"name": "ng-2", "status": "Failed", "updateId": "0a1b2c3d-...",
     "failure": {"kind": "Update", "name": "ng-2", "cluster": "prod", "region": "us-east-1",
                 "reason": "UpdateFailed", "retryable": false,
                 "error": "NodeCreationFailure: instances failed to join", "updateId": "0a1b2c3d-..."}},
    {"name": "ng-3", "status": "Skipped", "reason": "AlreadyLatest"}
  ],
  "failures": [
    {"kind": "Update", "name": "ng-2", "cluster": "prod", "region": "us-east-1",
     "reason": "UpdateFailed", "retryable": false,
     "error": "NodeCreationFailure: instances failed to join", "updateId": "0a1b2c3d-..."}
  ]
}
```

| `status` | Meaning |
|---|---|
| `Started` | The update started, and the run did not wait for it (`--no-wait`) |
| `Succeeded` | The EKS update ended `Successful` |
| `Skipped` | The run did not roll the nodegroup. `reason` is `AlreadyUpdating`, `AlreadyLatest`, or `CustomAMI` |
| `Failed` | The nodegroup could not be read, its update could not start, or EKS ended the update `Failed` |
| `Cancelled` | EKS ended the update `Cancelled` |
| `InProgress` | The update started, but the run stopped watching it: `--wait-timeout` passed (`Timeout`), Ctrl+C (`Interrupted`), or its status could not be polled (`NotMonitored`). The EKS update may still be running |
| `NotAttempted` | The run stopped before it reached this nodegroup |
| `DrainBlocked` | The run would roll the nodegroup, but the PDB drain gate refused the run, so nothing started. `drainBlockers` names this nodegroup's blockers |

The document also has `verification` (the post-roll checks and issues) and
`health` (the pre-flight verdict), when they ran. A nodegroup that
post-roll verification can't describe is a failure (exit `4`), not a
verification issue. The `--dry-run` preview has an `action` per nodegroup
instead of a status: `Update`, `ForceUpdate`, `SkipUpdating`, `SkipLatest`,
or `SkipCustom`. A nodegroup it can't describe has the action `Unknown`
and a `failure` (exit `4`). A nodegroup to roll that a PDB would block has
`drainBlockers`, and the preview exits `3` unless `--force` is set.

With `--all-clusters`, `clusters` has one entry per cluster: `cluster`,
`region`, `status`, and the cluster's `nodegroups`, `verification`, and
`health`. A cluster that could not be processed has a `failure` of its own.
The top-level `failures` lists every failure of the fleet, including the
regions discovery could not list.

| Cluster `status` | Meaning | Exit |
|---|---|---|
| `Succeeded` | The cluster did what the run asked, with no failure | `0` |
| `Incomplete` | The run finished, but some data could not be read (also a `--health-only` pass whose checks could not read everything) | `4` |
| `Failed` | An update could not start or did not succeed, the nodegroups could not be selected, or the health check could not run | `4` |
| `HealthBlocked` | The pre-flight health check blocked the cluster | `3` |
| `HealthWarned` | Health warnings stopped the cluster (`--health-only`, `--require-healthy`) | `2` |
| `VerifyFailed` | The updates succeeded, but post-roll verification found issues | `5` |
| `Interrupted`, `TimedOut` | Ctrl+C, or the cluster's `--wait-timeout`. Started updates keep running | `1` |
| `NotAttempted` | The run stopped before it reached the cluster | `1` |
| `Busy` | EKS was already changing the cluster, so the run skipped it; `changesInProgress` names what was changing | `3` |
| `DrainBlocked` | The PDB drain gate refused the cluster's roll; nothing started | `3` |

A fleet `--dry-run` entry has `status` `Planned`, `Incomplete` (the `plan`
has nodegroups it could not read), `DrainBlocked` (the real run's drain gate
would refuse the plan), or `Failed` (no `plan`; see `failure`).

### Exit-code contract

`nodegroup update` returns a meaningful exit code so unattended runs can branch:

| Code | Meaning |
|---|---|
| `0` | Success — updates started/completed as expected |
| `1` | An error, an interrupt, a monitoring timeout, or an EKS update that ended `Failed` or `Cancelled` or could not be monitored |
| `2` | Health **warnings** (with `--health-only` or `--require-healthy`) |
| `3` | **Blocked**: a pre-flight check failed, a PodDisruptionBudget would block the drain (without `--force`), or EKS is already changing the cluster; nothing was rolled. A `--dry-run` exits `3` where the drain gate would refuse |
| `4` | A **failure**: a nodegroup that could not be read, an update that could not start, or a `--dry-run` preview with a nodegroup it could not read |
| `5` | Post-roll **verification** found issues (nodes not Ready / newly-stuck pods) |

See [Exit codes](../concepts/exit-codes.md) for the full reference and a CI
`case` example.

### Examples

```bash
# Preview a single nodegroup roll with the AMI release notes
refresh nodegroup update my-cluster ng-default --dry-run --changelog

# Roll one nodegroup, requiring a clean health gate
refresh nodegroup update -c prod -n ng-default --require-healthy

# Health gate only — no roll (CI readiness check)
refresh nodegroup update -c prod --health-only -o json

# Unattended cron patch with a JSON summary
refresh nodegroup update -c prod --yes --require-healthy -o json

# Fleet-wide dry-run, then execute
refresh nodegroup update --all-clusters -r us-east-1 -r us-west-2 --dry-run
refresh nodegroup update --all-clusters -r us-east-1 -r us-west-2 --yes
```
