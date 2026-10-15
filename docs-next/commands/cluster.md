# cluster

| Command | Use |
|---|---|
| [`cluster list`](#list) | List the clusters of one or more regions. |
| [`cluster describe`](#describe) | Describe one cluster with its nodegroups and add-ons. |
| [`cluster upgrade-check`](#upgrade-check) | Report whether a cluster is ready for its next upgrade. |
| [`cluster upgrade`](#upgrade) | Upgrade a cluster to a Kubernetes version. |
| [`cluster rollback`](#rollback) | Roll a cluster back to the version before its upgrade. |

The cluster is the positional argument. Without it, the cluster comes from
`REFRESH_CLUSTER`, then the active context. `list`, `describe`, and
`upgrade-check` also take the cluster of the current kubeconfig context.
`upgrade` and `rollback` never do.

## list

```bash
refresh cluster list [flags]
```

```text
CLUSTERS  16 in 2 regions

CLUSTER              REGION     STATUS    VERSION
dev-billing-41       us-west-2  ● ACTIVE  1.34
dev-ml-17            us-west-2  ● ACTIVE  1.31
dev-orders-32        us-east-1  ● ACTIVE  1.34
dev-orders-56        us-east-1  ● ACTIVE  1.31
```

Without a region flag, the command reads the configured region. `-r` names one
or more regions, and `-A` reads every region.

The list has no node count. `refresh status` and `cluster describe` show the
nodegroups.

| Flag | Meaning |
|---|---|
| `-A, --all-regions` | Read every region. |
| `-f, --filter` | Select by `key=value`. Keys: `name`, `region`, `status`, `version`. |
| `--sort` | `name` (default), `region`, `status`, or `version`. |
| `--desc` | Sort in descending order. |
| `--show-health` | Run the health checks that need no Kubernetes access on every cluster, and add a `HEALTH` column. The column does not change the exit code. |
| `-w, --watch` | Read again and redraw every `--watch-interval`, until Ctrl+C. |
| `--watch-interval` | Time between two reads. Default `10s`. |

A cluster or a region that could not be read is listed under `INCOMPLETE DATA`
or in `failures`, and the command exits `4`. If no region answers, the command
exits `1`.

```bash
# Two regions, the newest version first
refresh cluster list -r us-east-1 -r eu-west-1 --sort version --desc

# Every region, only the production clusters that are being updated
refresh cluster list -A -f name=prod-* -f status=UPDATING
```

## describe

```bash
refresh cluster describe [cluster] [flags]
```

The command prints every section: the overview with the support window, the
network, the security configuration, the nodegroups, the add-ons, and the
health.

```text
prod-payments-00   ● ACTIVE   us-east-1

OVERVIEW
  status            ● ACTIVE
  version           1.34
  platform version  eks.1
  support           ● standard until 2026-12-02 (49 days); dates from refresh, not from EKS

NODEGROUPS  3
  NAME  STATUS    VERSION  INSTANCE  NODES
  ng-a  ● ACTIVE  1.34     m5.large  4
  ng-b  ● ACTIVE  1.34     m5.large  5
  ng-c  ● ACTIVE  1.34     m5.large  4

ADD-ONS  4
  NAME                    VERSION             STATUS    HEALTH
  coredns                 v1.34.2-eksbuild.1  ● ACTIVE  Healthy
  vpc-cni                 v1.34.2-eksbuild.1  ● ACTIVE  Healthy

HEALTH  9
  summary  ● Pass
  NAME           OUTCOME  MESSAGE
  Nodes          ● Pass   13/13 nodes ready (estimated)
  Capacity       ● Pass   CPU mean 30%, peak 38%, 13 instances
  Workloads      Skipped  pass --check-readiness to run the Kubernetes checks
  Quotas         ● Pass   57% of the vCPU quota in use (146 of 256)
  Kubernetes     Skipped  pass --check-readiness to run the Kubernetes checks
```

The `HEALTH` section runs the [health checks](../concepts/health-checks.md)
that a change runs before it starts. It shows health, not upgrade readiness:
an insight in `ERROR` shows in `cluster upgrade-check` and not here. The
section does not change the exit code.

| Flag | Meaning |
|---|---|
| `--check-readiness` | Read the Kubernetes API too: the `Ready` nodes of each nodegroup, and the health checks that need it. |

If a part of the cluster cannot be read, the command prints what it has, names
the part under `INCOMPLETE DATA` or in `failures`, and exits `4`.

## upgrade-check

```bash
refresh cluster upgrade-check [cluster] [flags]
```

The command reports whether a cluster is ready for its next Kubernetes upgrade.
It reads the EKS cluster insights, the version skew of the nodegroups and
add-ons, the control-plane metrics, the support window, and the rollback
window after a recent upgrade. It changes nothing.

```text
upgrade blocked: Insights: 1 ERROR insight (Deprecated APIs removed in Kubernetes v1.35) (pass --exit-zero to report only)
prod-platform-06   ✗ NOT READY   ca-central-1 · 1.34 · standard (49d)*

READINESS
  reason 1  ✗ Insights: 1 ERROR insight (Deprecated APIs removed in Kubernetes v1.35)

INSIGHTS  1
  NAME  INSIGHT                                      STATUS   VERSION
  -     Deprecated APIs removed in Kubernetes v1.35  ✗ ERROR  1.35

NODEGROUP SKEW  4
  NAME  VERSION  BEHIND
  ng-a  1.34     0
  ng-b  1.34     0

CHECKS  3
  NAME          OUTCOME  MESSAGE
  Insights      ✗ Fail   1 ERROR insight (Deprecated APIs removed in Kubernetes v1.35)
  VersionSkew   ● Pass   every nodegroup on 1.34 and every add-on at its latest
  ControlPlane  Skipped  control-plane metrics not emitted (EKS 1.28+ on a supported platform version)
```

After a recent upgrade, the report has a `ROLLBACK` section with the version
to roll back to and the time until which a rollback is possible.

| Flag | Meaning |
|---|---|
| `--category` | Insight category: `UPGRADE_READINESS` (default), `MISCONFIGURATION`, or `ROLLBACK_READINESS`. |
| `--status` | Show only the insights with these statuses: `PASSING`, `WARNING`, `ERROR`, `UNKNOWN`. This narrows what is shown, not what is judged. |
| `--show-passing` | Show the `PASSING` insights too. |
| `--insight` | Show one insight in full, by its ID or by the short ID of the table. |
| `--exit-zero` | Print the report and exit `0`. |

The exit code is the verdict:

| Code | Verdict |
|---|---|
| `0` | Ready. |
| `2` | Review: there are warnings. |
| `3` | Blocked. |
| `4` | Incomplete: a check, a nodegroup, or an add-on could not be read. |

`-o plain` prints every section as `FIELD` and `VALUE` rows.

## upgrade

```bash
refresh cluster upgrade [cluster] --to <version> [flags]
```

The command upgrades a cluster one minor version at a time. For each minor
version it runs four phases:

1. It moves the control plane.
2. It updates the add-ons that the new control plane cannot run.
3. It rolls every managed nodegroup, one after another.
4. It updates the other add-ons.

EKS and `refresh` do not upgrade self-managed nodes, hybrid nodes, or Fargate
pods. Move self-managed and hybrid nodes yourself after the control plane.
Delete the Fargate pods after the upgrade: they come back at the new version.
The plan says this in a notice.

A dry run prints the plan:

```bash
refresh cluster upgrade dev-orders-32 --to 1.35 --dry-run
```

```text
Plan   ● Proceed   ClusterUpgrade

PLAN
  operation       ClusterUpgrade
  cluster         dev-orders-32
  region          us-east-1
  version         1.34
  target version  1.35
  decision        ● Proceed

STEPS  8
  NAME                         KIND          ACTION  REASON         FROM                TO                  NOTE
  control-plane@1.35           ControlPlane  Submit  VersionBehind  1.34                1.35                -
  eks-pod-identity-agent@1.35  Addon         Submit  VersionBehind  v1.34.2-eksbuild.1  v1.35.2-eksbuild.1  -
  vpc-cni@1.35                 Addon         Submit  VersionBehind  v1.34.1-eksbuild.1  v1.35.2-eksbuild.1  -
  coredns@1.35                 Addon         Submit  VersionBehind  v1.34.2-eksbuild.1  v1.35.2-eksbuild.1  -
  kube-proxy@1.35              Addon         Submit  VersionBehind  v1.34.2-eksbuild.1  v1.35.2-eksbuild.1  -
  ng-a@1.35                    Nodegroup     Submit  VersionBehind  1.34                1.35                -
  ng-b@1.35                    Nodegroup     Submit  VersionBehind  1.34                1.35                -
  ng-c@1.35                    Nodegroup     Submit  VersionBehind  1.34                1.35                -

HEALTH GATE
  decision  ● Go

NOTICES
  1  self-managed and hybrid nodes are not upgraded by EKS or refresh: move them to 1.35 yourself after the control plane upgrades. Fargate pods stay at 1.34: delete them after the upgrade, and they come back at 1.35
```

A real run prints its progress on stderr:

```text
refreshing the cluster insights of dev-orders-32 (waits up to 5m0s)
phase: control plane, 1.35
◷ control-plane@1.35: Started (update 00000000-0000-4000-8000-000000000002)
● control-plane@1.35: Succeeded
phase: add-ons before the nodegroup rolls, 1.35
◷ vpc-cni@1.35: Started (update 00000000-0000-4000-8000-000000000004)
● vpc-cni@1.35: Succeeded
phase: nodegroups, 1.35
◷ ng-a@1.35: Started (update 00000000-0000-4000-8000-000000000007)
● ng-a@1.35: Succeeded
```

### Before any change

1. The command reads the whole cluster.
2. It asks EKS to refresh the cluster insights, and waits for that for up to 5
   minutes. An `ERROR` or `UNKNOWN` insight for the next version blocks. A
   nodegroup that the new version would put past the kubelet skew also blocks.
   The refresh is the one request that a blocked run sends. A dry run does not
   send it.
3. It runs the [health gate](../concepts/health-checks.md) for every nodegroup
   that the plan would roll. So a PodDisruptionBudget that would stop a drain
   is known before the control plane moves.

Each gate runs again right before its own phase. Before every change that it
sends, the command reads the cluster again and checks that the change is still
allowed.

### Questions

On a terminal, the command asks before each phase. `--yes` skips the questions.
`--yes` is required with `-o json`, `-o yaml`, and without a terminal.

### If the run stops

The plan comes from the live state of the cluster. There is no state file.

- If a phase fails, the run ends with exit `1`. The message names the phases
  that completed and the command that goes on from there.
- If the run stops for any reason, run the same command again. It goes on with
  what is left.
- `--to` with the version that the control plane already runs only catches up
  the add-ons and the nodegroups.

### Busy clusters

The command refuses while someone else changes the cluster. It waits for a
change of its own that is still running, for example a roll that an earlier
run started. If that change fails, the command says so before it continues.

### One part at a time

| Flag | Meaning |
|---|---|
| `--to` | The Kubernetes version to reach, such as `1.35`. Required. |
| `--only` | Limit the run to these parts: `control-plane`, `addons`, `nodegroups`. |
| `-n, --nodegroup` | Roll only these nodegroups, by exact name. |
| `--skip-addon` | Leave these add-ons alone, by exact name. |
| `--skip-nodegroup` | Leave these nodegroups alone, by exact name. |
| `--skip-insights-check` | Do not refresh or read the cluster insights. Not recommended. |
| `--skip-verify` | Skip the verification after each nodegroup roll. |
| `--wait-timeout` | How long to wait for the whole upgrade. Default `4h`. `0` means no limit. A question does not count. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply, except `--no-wait`.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Done, or nothing to do. |
| `1` | An error, a question answered no, an interrupt, or a phase that failed. The phases before it stay done. |
| `2` | With `--dry-run` only: a real run would ask about warnings. |
| `3` | Blocked: the cluster is busy, a target of a phase is already changing, a gate stops the change, `--require-healthy` and a warning, or the insights or the kubelet skew block the next version. Nothing changed, or, when a gate stops a later phase, nothing more changed and the message names the phases that completed. |
| `4` | Incomplete: a wait timed out, or something could not be read. |
| `5` | A nodegroup was rolled and the verification found an issue, or an add-on was updated and is not healthy. |

When a gate stops a later phase, the run exits `3`, nothing more changed, and
the message names the phases that completed.

### Examples

```bash
# Upgrade two minor versions without a prompt
refresh cluster upgrade prod --to 1.36 --yes

# Move only the control plane
refresh cluster upgrade prod --to 1.35 --only control-plane

# Roll the nodegroups later, and leave one alone
refresh cluster upgrade prod --to 1.35 --only nodegroups --skip-nodegroup gpu

# In CI: a JSON document on stdout, progress on stderr
refresh cluster upgrade prod --to 1.35 --yes -o json > run.json
```

## rollback

```bash
refresh cluster rollback [cluster] [flags]
```

The command rolls a cluster back one minor version, within 7 days of an
in-place upgrade. EKS rolls back only the control plane. So the command runs
three phases:

1. It rolls the managed nodegroups back, one after another.
2. It downgrades the add-ons that the older version cannot run.
3. It rolls the control plane back.

```bash
refresh cluster rollback staging-ml-07 --dry-run
```

```text
Plan   ● Proceed   ClusterRollback

STEPS  9
  NAME                    KIND          ACTION  REASON        FROM                TO                  NOTE
  ng-a                    Nodegroup     Submit  VersionAhead  1.35                1.34                -
  ng-b                    Nodegroup     Submit  VersionAhead  1.35                1.34                -
  eks-pod-identity-agent  Addon         Submit  VersionAhead  v1.35.2-eksbuild.1  v1.34.2-eksbuild.1  -
  vpc-cni                 Addon         Submit  VersionAhead  v1.35.2-eksbuild.1  v1.34.2-eksbuild.1  -
  control-plane           ControlPlane  Submit  VersionAhead  1.35                1.34                -
```

### What cannot roll back

- A cluster that was created at its version.
- An upgrade that is more than 7 days old.
- A control plane while a managed nodegroup is above the target version. A
  nodegroup that you skip, and one with a custom AMI, are yours to move first.
- An add-on that the target version cannot run, when every version of it that
  the target runs is newer than the installed one. A rollback never upgrades an
  add-on. It moves an add-on to the newest version that the target runs and
  that is at or below the installed one.

EKS and `refresh` do not roll back self-managed nodes or Fargate pods.

`refresh cluster upgrade-check` shows the rollback window of a cluster.

### Questions

The command asks once, with the whole order in the question. `--yes` skips the
question. If the cluster changed while the question was open, the command asks
again about what changed. It refuses, whatever the answer and the flags, when
the version to roll back to is no longer the one that was confirmed.

### Flags

| Flag | Meaning |
|---|---|
| `--skip-nodegroup` | Leave these nodegroups alone, by exact name. |
| `--skip-insights-check` | Roll back past blocking insights, and send `force` to EKS, which then skips its own insight checks too. Not recommended. |
| `--rollback-timeout` | The time that EKS gives the control-plane rollback: whole minutes from `2h` to `168h`. `0` leaves the default of EKS. |
| `--skip-verify` | Skip the verification after each nodegroup roll. |
| `--wait-timeout` | How long to wait for the whole rollback. Default `4h`. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply, except `--no-wait`.

### Exit codes

The codes are those of [`cluster upgrade`](#exit-codes). Exit `3` also means
that the cluster cannot roll back: no upgrade in the last 7 days, a blocking
insight, or a nodegroup above the target.
