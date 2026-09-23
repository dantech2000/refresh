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
[active context](../concepts/contexts.md).

---

## list

List the managed nodegroups in a cluster with their status, instance type, node
counts, and AMI freshness (whether each is on the latest recommended AMI).

The `NODES` column shows the desired node count by default. Add
`--check-readiness` to measure real Kubernetes node readiness (`Ready/desired`)
via the cluster API; when the cluster is unreachable it degrades to the desired
count rather than reporting a fabricated ready figure.

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
| `--sort` | Sort by field: `name` (default), `status`, `instance`, `nodes` |
| `--desc` | Sort descending |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--watch, -w` | Re-run and redraw every `--watch-interval` until interrupted |
| `--watch-interval` | Refresh interval for `--watch` (default `10s`) |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

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
or `--nodegroup/-n`.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name |
| `--nodegroup, -n` | Nodegroup name (or pass as second positional) |
| `--show-instances, -I` | Include EC2 instance details |
| `--show-workloads, -W` | Include workload/pod placement info |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
refresh nodegroup describe my-cluster ng-default
refresh nodegroup describe my-cluster ng-default --show-instances --show-workloads
```

---

## scale

Change a managed nodegroup's desired/min/max size. Any subset of
`--desired/--min/--max` may be set; unspecified bounds are left unchanged.

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
| `--check-pdbs` | Refuse a scale-down when a Pod Disruption Budget that allows 0 disruptions covers pods on the nodegroup's nodes |
| `--force` | With `--check-pdbs`, scale down anyway and print the blocking PDBs as a warning |
| `--wait` | Wait for the scaling operation to complete |
| `--op-timeout` | Scaling operation timeout for `--wait` (default `5m`; added on top of `--timeout`; `0` = no limit) |
| `--kubeconfig` | Kubeconfig for workload/PDB checks (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--dry-run` | Preview the scaling impact without executing |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

!!! warning "A scale-down does not honor PDBs"
    When a scaling change lowers the desired size, EKS terminates the removed
    nodes without waiting for Pod Disruption Budgets. With `--check-pdbs`,
    `refresh` refuses the scale-down (exit 1, before any change) if a PDB that
    allows 0 disruptions covers pods on the nodegroup's nodes, and lists those
    PDBs. Pass `--force` to scale down anyway. Combine `--dry-run --check-pdbs`
    to see the verdict and the blocking PDBs before you touch anything.

### Examples

```bash
# Scale up to 5 nodes
refresh nodegroup scale my-cluster -n ng-default --desired 5

# Safe scale-down: preview the PDB gate's verdict and any blocking PDBs
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --dry-run

# Scale down for real, refused if a PDB blocks it, and wait for it to settle
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --wait

# Scale down even though a PDB blocks it (the blockers print as a warning)
refresh nodegroup scale my-cluster -n ng-default --desired 2 --check-pdbs --force
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

!!! note "Custom-AMI nodegroups are skipped"
    Nodegroups whose AMI is managed via a launch template (`AmiType=CUSTOM`)
    are detected and **skipped** with guidance: their AMI rolls when you publish
    a new launch-template version, not via this command.

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
ignored.

In the default region sweep (no `-r`, no `REFRESH_EKS_REGIONS`), regions your
credentials can't use are skipped with one stderr note. Examples are regions
an SCP denies and opt-in regions that are not enabled. Skipped regions don't
change the exit code. Any other listing failure (throttling, a server error, a
timeout), or any failure in a region you named, is reported on stderr and in
the summary (`discoveryErrors` in `-o json`), and the run exits `4`. The run
also exits `4` right away if no region can be listed.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or partial pattern (overrides the active context; the kubeconfig is not used). Falls back to `EKS_CLUSTER_NAME` unless a positional is clearly the cluster (with `--nodegroup`, or two positionals). See [cluster resolution](../concepts/configuration.md#cluster-resolution) |
| `--nodegroup, -n` | Nodegroup name or partial pattern (if unset, update all). An exact name selects only that nodegroup |
| `--all-clusters` | Fleet mode: roll matching nodegroups across all discovered clusters (serial); scope with `-r` |
| `--region, -r` | Region(s) for `--all-clusters` discovery (default: partition EKS regions / `REFRESH_EKS_REGIONS`) |
| `--dry-run, -d` | Preview changes without executing |
| `--changelog` | In dry-run, print full `amazon-eks-ami` release notes between the current and target AMI |
| `--force, -f` | Force the update where possible |
| `--no-wait` | Don't wait for update completion (start-and-return) |
| `--quiet, -q` | Minimal output |
| `--skip-health-check, -s` | Skip pre-flight health validation |
| `--health-only` | Run the health check only, don't update (exit `0`=pass / `2`=warn / `3`=block) |
| `--yes, -y` | Assume yes: skip confirmation prompts (multi-match selection, warn-level health) for CI |
| `--require-healthy` | Treat warn-level health findings as a hard stop (exit `2`) instead of prompting |
| `--skip-verify` | Skip post-roll verification (nodes ACTIVE, no new stuck pods) |
| `--live` | Force the live per-node roll panel, also when stdout is not a color terminal (a snapshot at most every 15s, only on change) |
| `--kubeconfig` | Kubeconfig for workload/PDB checks (defaults to `$KUBECONFIG`, then `~/.kube/config`) |
| `--poll-interval, -p` | Polling interval for update status (default `15s`) |
| `--timeout, -t` | Max time to wait for update completion (default `40m`; applies per cluster with `--all-clusters`; `0` = no limit) |
| `--format, -o` | `table` (default), `json`, or `yaml`: one document on stdout (the run summary, the `--dry-run` preview, or the `--health-only` verdict), with notices on stderr |

!!! warning "Unattended / CI"
    Without a TTY, or with `-o json`/`-o yaml`, a run that would otherwise
    prompt fails fast unless you pass `--yes`. For cron, pair `--yes` with
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

### Exit-code contract

`nodegroup update` returns a meaningful exit code so unattended runs can branch:

| Code | Meaning |
|---|---|
| `0` | Success — updates started/completed as expected |
| `2` | Health **warnings** (with `--health-only` or `--require-healthy`) |
| `3` | Health **blocked** — a pre-flight check failed; nothing was rolled |
| `4` | One or more nodegroup updates **failed to start** |
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
