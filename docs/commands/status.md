# refresh status

Fleet patch posture across clusters and regions — the front door.

```bash
refresh status [name-pattern] [flags]
```

Reports, per cluster: Kubernetes version, EKS support window (standard vs.
extended support, with extended-support cost exposure), stale AMIs, and add-ons
behind their latest compatible version. Exits non-zero when something needs
attention, so it doubles as a CI gate.

## Flags

| Flag | Description |
|---|---|
| `--all-regions, -A` | Query all EKS-supported regions (or the `REFRESH_EKS_REGIONS` list) |
| `--region, -r` | Specific region(s) to query (repeatable) |
| `--sort` | Sort by field: `cluster` (default), `region`, `version`, `support`, `stale` |
| `--desc` | Sort descending |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |

Global flags that matter here: `--max-concurrency, -C` sets how many clusters
`status` evaluates at once in each region. It sweeps `min(4, -C)` regions at
once. `--timeout, -t` bounds the whole sweep.

Without `-A` or `-r`, `status` reads the configured region. See
[Regions](../concepts/configuration.md#regions) for how `-r`, the global
`--region`, and `REFRESH_EKS_REGIONS` combine.

With `--all-regions` and no `-r` or `REFRESH_EKS_REGIONS`, regions these
credentials can't use (an SCP denial, a region not enabled for the account)
are skipped with one note on stderr and don't count as failed regions. A
region you name with `-r` still fails if it's denied. Other region failures
print one warning line each, and `-o json`/`-o yaml` list them under
`failures` as `{"region", "error"}`.

## Columns

| Column | Shows |
|---|---|
| `CLUSTER`, `REGION`, `VERSION` | The cluster and its control-plane version |
| `SUPPORT` | `standard`, `extended`, or `unsupported`, with the end date. An extended cluster shows the extra cost per hour. A cluster whose upgrade policy is `STANDARD` shows "auto-upgrades at end of standard support" instead: EKS upgrades it and it never pays for extended support |
| `COMPUTE` | Managed nodegroups (with a count), `Auto Mode`, `Karpenter`, or `none` |
| `STALE AMI` | Nodegroups not on the latest AMI for their own Kubernetes version. `· N behind CP` counts nodegroups on an older minor than the control plane. `n/a` for Auto Mode and Karpenter |
| `ADDONS` | Add-ons behind their latest compatible version |
| `HEALTH` | Control-plane health issues that AWS reports for the cluster |

`-o plain` has the same columns plus a trailing `ERRORS` column (`-` when the
row is complete). The `--sort stale` key counts both stale AMIs and
nodegroups behind the control plane.

The support dates come from `DescribeClusterVersions`. If that call fails,
`status` uses a built-in calendar, and `-o plain` marks the `SUPPORT` cell
with a trailing `*`. Karpenter is detected per cluster from the instance tags
(`kubernetes.io/cluster/<name>=owned` or `eks:eks-cluster-name=<name>`, plus
the Karpenter nodepool tag).

## Incomplete data

A row whose data `refresh` could not read is never shown as current. Examples
are a failed `DescribeCluster`, a nodegroup or add-on that could not be read,
a failed latest-AMI lookup (for example, a missing `ssm:GetParameter`), and a
cluster that a timed-out sweep never reached. Such a row gets the unknown
marker (`○`), an "N incomplete" count, and an `INCOMPLETE DATA` block that
lists its errors. `-o json` puts them in the row's `errors` list. With
`-o json`, `-o yaml`, or `-o plain`, stderr also names each such cluster with
a one-line reason. The command then exits `4`, unless a higher-priority code
applies.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Every cluster is current and in standard support |
| `2` | Something needs attention: a stale AMI, an add-on behind, a nodegroup behind the control plane, or a control-plane health issue |
| `3` | A cluster is on extended support or unsupported |
| `4` | Incomplete data, or a region could not be listed |
| `1` | An error, or nothing could be gathered: every region failed or was skipped |

Precedence is `3`, then `2`, then `4`. `4` means some data is missing. If
nothing could be gathered, `status` fails with `1` instead. See
[Exit codes](../concepts/exit-codes.md#status).

## Examples

```bash
# Everything, everywhere
refresh status -A

# Only clusters whose name contains "prod", in two regions
refresh status prod -r us-east-1 -r us-west-2

# Worst support posture first
refresh status -A --sort support --desc

# Machine-readable for a dashboard / CI gate
refresh status -A -o json
```
