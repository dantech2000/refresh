# refresh status

`refresh status` shows every cluster with what is out of date: the Kubernetes
version and its EKS support window, the nodegroups whose AMI is not the latest,
and the add-ons that have a newer version.

```bash
refresh status [flags]
```

Without a region flag, the command reads the configured region. `-r` names one
or more regions. `-A` reads every region. See
[Regions](../concepts/configuration.md#regions).

```bash
refresh status -r us-east-1
```

```text
FLEET  8

CLUSTER                REGION     VERSION  SUPPORT           COMPUTE       STALE AMI                ADDONS                      HEALTH
▲ dev-orders-32        us-east-1  1.34     standard (49d)*   3 nodegroups  1/3 (130d)               1 (vpc-cni)                 ok
▲ dev-orders-56        us-east-1  1.31     extended (43d)*   4 nodegroups  1/4 (130d)               0                           ok
▲ dev-platform-08      us-east-1  1.34     standard (49d)*   2 nodegroups  1/2 (130d)               0                           ok
▲ prod-billing-24      us-east-1  1.33     extended (288d)*  1 nodegroup   0                        1 (vpc-cni)                 ok
▲ prod-data-48         us-east-1  1.32     extended (160d)*  4 nodegroups  1/4 (130d), 1 behind CP  1 (eks-pod-identity-agent)  ok
▲ prod-payments-00     us-east-1  1.34     standard (49d)*   3 nodegroups  1/3 (130d)               0                           ok
▲ staging-checkout-16  us-east-1  1.35     standard (164d)*  2 nodegroups  0                        2 (coredns,vpc-cni)         ok
▲ staging-data-40      us-east-1  1.32     extended (160d)*  1 nodegroup   0                        0                           ok

● 0 current   ▲ 8 need attention
5 stale AMIs · 5 add-ons behind · 1 nodegroup behind the control plane · 4 clusters out of standard support

▲ dev-orders-32 has stale AMIs or add-ons: refresh cluster upgrade-check dev-orders-32 -r us-east-1
```

Under the table, the first line counts the clusters by what they need, and
the second line adds up what is out of date. The last line names the most
urgent cluster, an unsupported one first, with the command that checks
whether it is ready to upgrade.

## Columns

| Column | Content |
|---|---|
| `SUPPORT` | `standard`, `extended`, or `unsupported`, with the days left. A `*` marks dates that `refresh` ships with, because EKS did not report them. |
| `COMPUTE` | The managed nodegroups, Auto Mode, Karpenter, or none. |
| `STALE AMI` | "1/3 (130d)": one of three nodegroups does not run the latest AMI for its own Kubernetes version, and the oldest such AMI is 130 days old. "1 behind CP": one nodegroup is on an older Kubernetes version than the control plane. |
| `ADDONS` | The add-ons that have a newer compatible version. |
| `HEALTH` | The health issues that EKS reports for the control plane, the nodegroups, and the add-ons, as one count, such as `2 issues`. An add-on that is `DEGRADED` with no issue listed counts as one. The JSON has each part: `healthIssues` of the cluster and of each nodegroup, and `issues` and `status` of each add-on. |

## Flags

| Flag | Meaning |
|---|---|
| `-A, --all-regions` | Read every region. |
| `-f, --filter` | Select by `key=value`. Keys: `name`, `region`, `version`, `support`. |
| `--sort` | `name` (default), `region`, `version`, `support`, or `stale`. |
| `--desc` | Sort in descending order. |
| `-o, --output` | `table`, `json`, `yaml`, or `plain`. |

## Incomplete data

A cluster or a region that could not be read is listed under `INCOMPLETE DATA`,
or in `failures` with `-o json` and `-o yaml`. The command then exits `4`. See
[Failures](../concepts/output.md#failures).

## Exit codes

When several apply, the highest wins.

| Code | Meaning |
|---|---|
| `0` | Every cluster is current and in standard support. |
| `2` | Something needs attention: an AMI, an add-on, or a nodegroup behind the control plane, or a health issue of the control plane, a nodegroup, or an add-on. |
| `3` | A cluster is in extended support, or unsupported. |
| `4` | A part of the fleet could not be read. |
| `1` | An error, or no region answered. |

## Examples

```bash
# Every region, the clusters with the most to do first
refresh status -A --sort stale --desc

# The clusters that left standard support
refresh status -A -f support=extended

# The production clusters of two regions
refresh status -r us-east-1,eu-west-1 -f name=prod-*

# The names of the clusters with a stale AMI
refresh status -o json | jq -r '.clusters[] | select(any(.nodegroups[]?; .ami.status == "Outdated")) | .name'
```
