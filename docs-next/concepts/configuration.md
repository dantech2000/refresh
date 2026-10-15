# Configuration

`refresh` has no configuration file of its own, apart from the saved
[contexts](contexts.md). Flags, environment variables, the active context, and
the AWS configuration decide what a command does.

## Profile and region

A command picks the AWS profile and the region in this order. The first match
wins.

1. The flags `--profile` and `-r/--region`.
2. The AWS environment variables: `AWS_PROFILE`, and `AWS_REGION` or
   `AWS_DEFAULT_REGION`.
3. The active [context](contexts.md).
4. The AWS configuration: `~/.aws/config` and `~/.aws/credentials`.

An environment variable that disagrees with the active context is an error.
See [A context applies as a unit](contexts.md#a-context-applies-as-a-unit).

For the ways to log in, see [Logging in to AWS](logging-in.md).

## Cluster resolution

Each command names its target with a positional argument or with
`-c/--cluster`.

| Command | Positional | The cluster comes from |
|---|---|---|
| `cluster describe`, `upgrade-check`, `upgrade`, `rollback` | `[cluster]` | The positional, then the order below |
| `nodegroup list`, `addon list` | none | `-c`, then the order below |
| `nodegroup describe`, `nodegroup scale` | `<nodegroup>` | `-c`, then the order below |
| `nodegroup update` | `[nodegroup...]`; none means all | `-c`, then the order below |
| `addon describe` | `<addon>` | `-c`, then the order below |
| `addon update` | `[addon...]`, or `--all` | `-c`, then the order below |
| `status`, `cluster list`, `ui` | none | A sweep of regions |

The order:

1. The explicit value: the positional or `-c`.
2. `REFRESH_CLUSTER`.
3. The active context.
4. For a command that only reads: the cluster of the current kubeconfig
   context.

A command that changes something never uses step 4, with or without
`--dry-run`. `nodegroup update --all-clusters` ignores steps 2 to 4.

Step 4 counts only a context that names an EKS cluster and its region: a
cluster ARN (as `aws eks update-kubeconfig` writes it), an eksctl cluster
entry, or an `aws eks get-token` or `aws-iam-authenticator` credential plugin
with an EKS API server. A context of kind or of a self-managed cluster names
no cluster. The command then reads the region of that cluster, unless
`-r/--region` names one.

If nothing resolves, the command fails and names the ways to set a cluster:

```text
Error: no cluster specified
Set one with:
  -c/--cluster, or [cluster]      for this command, whichever it has
  REFRESH_CLUSTER                 for this shell
  refresh context use <name>      until you change it
```

### Names are exact

A cluster, nodegroup, or add-on name must match exactly. If a name matches
nothing, the command fails and lists the close matches:

```text
Error: cluster "prod-payments" not found in us-east-1
Did you mean:
  prod-payments-00
List the clusters with: refresh cluster list -r us-east-1
```

Before any request, refresh checks each name against the EKS rules. A name
has only letters, digits, hyphens, and underscores, and starts with a letter or
digit. A cluster or add-on name has 1 to 100 characters, and a nodegroup name 1
to 63. The check applies to an argument, `-c`, `REFRESH_CLUSTER`, and the
cluster of the active context. A name that breaks the rules is an error, exit
`1`, and the message names the value and the rule. An empty argument or an
empty `-c ""` is an error too: it does not fall back to the defaults.

### Filters

To select by pattern, use `-f/--filter` on the commands that have it:

```bash
refresh cluster list -f name=prod-*
refresh nodegroup update -c prod -f name=web-*
```

- A filter is `key=value`. Repeat the flag for more filters. Every filter
  must match.
- `*` matches any text, also no text. No other character is special.
- A value without `*` must equal the whole value. Write `name=*prod*` for
  "contains prod".
- Keys and values compare without regard to case.
- A filter that does not parse, or that has an unknown key, is an error.

| Command | Filter keys |
|---|---|
| `status` | `name`, `region`, `version`, `support` |
| `cluster list` | `name`, `region`, `status`, `version` |
| `nodegroup list`, `nodegroup update` | `name`, `status`, `instanceType`, `amiStatus`, `version` |

When a filter selects the targets of a change, the confirmation lists the
matches.

## Global flags

These flags work on every command.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-r, --region` | `REFRESH_EKS_REGIONS` for sweeps | from the context or the AWS configuration | AWS region. A command that sweeps regions takes several: repeat the flag or separate the values with commas. |
| `--profile` | `AWS_PROFILE` | | AWS shared-config profile. |
| `-t, --timeout` | `REFRESH_TIMEOUT` | `1m` | Budget for the API calls of a read command, and for the reads of a changing command before its first change. `0` means no limit; a negative value is an error. |
| `--max-concurrency` | `REFRESH_MAX_CONCURRENCY` | `32` | Limit for AWS requests in flight in one region. |
| `--no-color` | `NO_COLOR` | | Disable color. |
| `--log-level` | `REFRESH_LOG_LEVEL` | `warn` | `debug`, `info`, `warn`, or `error`. |
| `--verbose` | | | Same as `--log-level debug`. |
| `--kubeconfig` | `KUBECONFIG` | `~/.kube/config` | Kubeconfig file. |
| `--kube-context` | | | Kubeconfig context to use. It is trusted even when its server does not match the cluster endpoint. |
| `-h, --help` | | | Help. |
| `--version` | | | Print the version. On the root command only. |

`-o/--output` is not global. Each command that prints a document has it, with
the formats that the command supports. Put it after the command name.

### Flag letters

A letter has one meaning in every command. A letter that is not in the table
is not reserved; `-a` is free, because an add-on is a positional.

| Letter | Flag |
|---|---|
| `-c` | `--cluster` |
| `-n` | `--nodegroup` |
| `-o` | `--output` |
| `-r` | `--region` |
| `-t` | `--timeout` |
| `-d` | `--dry-run` |
| `-y` | `--yes` |
| `-q` | `--quiet` |
| `-w` | `--watch` |
| `-f` | `--filter` |
| `-A` | `--all-regions` |

## Flags of every changing command

`nodegroup update`, `nodegroup scale`, `addon update`, `cluster upgrade`,
and `cluster rollback` share these flags.

| Flag | Meaning |
|---|---|
| `-d, --dry-run` | Print the plan and the verdict of the health gate. Change nothing. Never prompt. |
| `-y, --yes` | Do not prompt. Required with `-o json` or `-o yaml`, and without a terminal. |
| `--no-wait` | Return once AWS accepts the change. Not on `cluster upgrade` and `cluster rollback`. |
| `--wait-timeout` | How long to wait for the change. `0` means no limit. |
| `--poll-interval` | Time between two status polls. Default `15s`. |
| `--skip-health-check` | Skip the health gate. |
| `--require-healthy` | Stop on any health warning, and do not ask. |
| `--force` | Go past drain blockers, with a warning. Not on `addon update`. |
| `-q, --quiet` | Print no progress. Questions, warnings, and the result still show. |

The exit code of a dry run comes from the findings alone, whatever `--yes`
says. See [Exit codes](exit-codes.md#dry-runs).

## Regions

A command for one cluster uses one region, from the order in
[Profile and region](#profile-and-region).

`status`, `cluster list`, `ui`, and `nodegroup update --all-clusters` can
read several regions. This is a sweep. The regions of a sweep are:

1. The regions of `-r`, when it is given.
2. With `-A`, or when no region is configured: the list in
   `REFRESH_EKS_REGIONS` (comma-separated). If the variable is not set, the
   regions that every account of the partition has enabled. This second case
   is the default sweep.
3. Otherwise, the configured region.

`-A` together with `-r` is a usage error.

refresh checks the shape of every region name before it sends a request: a
partition prefix, a geographic word, and a number, such as `us-east-1`,
`us-gov-west-1`, or `cn-north-1`. This applies to `-r`, `REFRESH_EKS_REGIONS`,
the region of a context, and the region of the AWS configuration
(`AWS_REGION`, `AWS_DEFAULT_REGION`, or the profile). A name of another shape,
such as `notaregion` or `bogus-1`, is an error, exit `1`. An empty name, such
as `-r ""`, is an error too. refresh does not keep a list of regions, because
AWS adds new ones. A name of the right shape that is not a region, such as
`us-east-9`, reaches AWS, and the error says that EKS does not answer there.

In a default sweep, a region that these credentials cannot use is skipped with
one note on stderr. It is not a failure. A region that you name with `-r` or
`REFRESH_EKS_REGIONS` is never skipped: if it cannot be read, it is a failure,
and the command exits `4`. If no region answers, the command fails with
exit `1`.

Opt-in regions are not in the default sweep. Name them with `-r` or
`REFRESH_EKS_REGIONS`.

## Timeouts

| Flag | Default | Bounds |
|---|---|---|
| `-t, --timeout` | `1m` | The API calls of a read command. In a changing command: the reads up to the first change. With `--watch`: each refresh. |
| `--wait-timeout` | `40m` for `nodegroup update`, `5m` for `nodegroup scale`, `5m` for each add-on, `4h` for `cluster upgrade` and `cluster rollback` | Everything after the first change: the waits, the later phases, and the verification. |
| `--poll-interval` | `15s` | The time between two polls of a wait. |

A question does not count against either budget.

If `--wait-timeout` passes before the change finishes, the command exits `4`.
The change may still be running in AWS. Run the command again to see where it
is.

After a roll, the command also waits for the new nodes to become `Ready`: at
most 2 minutes, and never past the time left of `--wait-timeout`.

## Environment variables

| Variable | Meaning |
|---|---|
| `REFRESH_CLUSTER` | The cluster, for every command that needs one. |
| `REFRESH_CONTEXT` | The active context for this shell. |
| `REFRESH_CONFIG_HOME` | The directory of `context.yaml`. |
| `REFRESH_EKS_REGIONS` | The regions of a sweep with `-A`, comma-separated. |
| `REFRESH_TIMEOUT` | Default of `--timeout`. |
| `REFRESH_MAX_CONCURRENCY` | Default of `--max-concurrency`. |
| `REFRESH_LOG_LEVEL` | Default of `--log-level`. |
| `REFRESH_NO_UPDATE_CHECK` | Any value other than `0`, `false`, or `no` turns off the update check of `refresh version`. |
| `REFRESH_IN_CLUSTER_NAME` | The name of the EKS cluster that the pod runs in, when `refresh` runs inside a cluster. |
| `NO_COLOR` | Any non-empty value disables color. |
| `KUBECONFIG` | The kubeconfig file or list. |

The AWS SDK variables (`AWS_PROFILE`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, and
the others) work as they do in the AWS CLI.

## Kubeconfig

Some features read the Kubernetes API of the cluster:

- The health checks `Workloads`, `DrainBlockers`, `PDBCoverage`, and
  `Utilization`, and the real node counts of `Nodes`.
- `--check-readiness` on `cluster describe` and `nodegroup list`.
- `nodegroup describe --show-workloads`.
- The verification after a roll, and the live view of a roll.

### How refresh finds the cluster in the kubeconfig

1. The kubeconfig is `--kubeconfig`, then `$KUBECONFIG`, then
   `~/.kube/config`.
2. With `--kube-context`, `refresh` uses that context as named. It prints a
   note when the server of the context does not match the endpoint of the
   cluster.
3. Without `--kube-context`, `refresh` uses a context whose server matches
   the endpoint of the EKS cluster. It tries the current context first.
4. Inside a pod, `refresh` uses the in-cluster configuration only when its
   host matches the endpoint, or when `REFRESH_IN_CLUSTER_NAME` equals the
   name of the cluster.

If no context points at the cluster, `refresh` has no Kubernetes access for
that run, and it says so. To add a context:

```bash
aws eks update-kubeconfig --name prod --region us-east-1
```

What happens without Kubernetes access is in
[Health checks](health-checks.md#kubernetes-access).

### Access entries

On a cluster that uses EKS access entries, the identity needs an access entry
and an access policy for the Kubernetes reads. `AmazonEKSAdminViewPolicy` with
cluster scope covers every read. `AmazonEKSViewPolicy` does not cover nodes.

```bash
aws eks create-access-entry --cluster-name prod --principal-arn "$ROLE_ARN"
aws eks associate-access-policy --cluster-name prod --principal-arn "$ROLE_ARN" \
  --policy-arn arn:aws:eks::aws:cluster-access-policy/AmazonEKSAdminViewPolicy \
  --access-scope type=cluster
```

The Kubernetes reads are `list` on nodes, pods, deployments, events, and
PodDisruptionBudgets, and `list` on the node metrics of `metrics.k8s.io`.
`watch` on nodes and events lets the live view of a roll follow changes
without polling.

## Required IAM permissions

`refresh` calls these AWS APIs. A read-only role needs the read actions. A
role that patches or upgrades also needs the `Update*` actions and
`eks:StartInsightsRefresh`. When a call is denied, the error names the
action.

| Action | Used by |
|---|---|
| `sts:GetCallerIdentity` | Region sweeps in which no region answered (credential check) |
| `eks:ListClusters` | `status`, `cluster list`, `ui`, `nodegroup update --all-clusters`, and the close matches for a cluster name that is not found |
| `eks:DescribeCluster` | Every cluster command |
| `eks:ListNodegroups`, `eks:DescribeNodegroup` | `status`, `nodegroup *`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, health checks, the busy check of `addon update` |
| `eks:ListAddons` | `status`, `addon list`/`update`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, the busy check of `nodegroup update`/`scale` |
| `eks:DescribeAddon`, `eks:DescribeAddonVersions` | `status`, `addon *`, `cluster upgrade-check`/`upgrade`/`rollback`; `eks:DescribeAddon` also in `cluster describe` and the busy check of `nodegroup update`/`scale` |
| `eks:DescribeClusterVersions` | `status`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback` (support calendar; `refresh` falls back to a built-in calendar) |
| `eks:ListInsights`, `eks:DescribeInsight` | `cluster upgrade-check`, `cluster upgrade`, `cluster rollback` |
| `eks:StartInsightsRefresh`, `eks:DescribeInsightsRefresh` | `cluster upgrade` (not with `--dry-run` or `--skip-insights-check`) |
| `eks:UpdateNodegroupVersion` | `nodegroup update`, `cluster upgrade`, `cluster rollback` |
| `eks:UpdateNodegroupConfig` | `nodegroup scale` |
| `eks:UpdateClusterVersion` | `cluster upgrade`, `cluster rollback` |
| `eks:UpdateAddon` | `addon update`, `cluster upgrade`, `cluster rollback` |
| `eks:DescribeUpdate` | `nodegroup update`, `nodegroup scale`, `addon update` (not with `--no-wait`), `cluster upgrade`, `cluster rollback`, `cluster upgrade-check` (rollback window) |
| `eks:ListUpdates` | `cluster rollback`, `cluster upgrade-check` (rollback window), `addon update` and `nodegroup scale` (the type of the update that keeps a target busy), `ui` (watch a roll or upgrade started elsewhere) |
| `ssm:GetParameter` | Latest recommended AMI: `status`, `cluster describe`, `nodegroup list`/`describe`/`update` |
| `ec2:DescribeImages` | `status` (AMI age) |
| `ec2:DescribeInstances` | `status` (Karpenter detection), `nodegroup describe --show-instances`, current AMI of a nodegroup without a launch-template image (`status`, `cluster describe`, `nodegroup list`/`describe`/`update`), the vCPUs in use for the quota check when the AWS/Usage metric has no data points |
| `ec2:DescribeLaunchTemplateVersions` | Current AMI of a launch-template nodegroup: `status`, `nodegroup list`/`describe`/`update` |
| `ec2:DescribeSubnets`, `ec2:DescribeInstanceTypeOfferings` | Instance-type availability warning in `nodegroup update`/`scale` |
| `ec2:DescribeInstanceTypes` | The vCPUs of the nodes that a roll or a scale-up launches, and of the instances in use when the AWS/Usage metric has no data points, for the quota check of `nodegroup update`/`scale`, `cluster upgrade`, `cluster rollback` |
| `ec2:DescribeVpcs` | `cluster describe` |
| `autoscaling:DescribeAutoScalingGroups` | Health checks, the instances of a nodegroup (`status`, `cluster describe`, `nodegroup list`/`describe`/`update`) |
| `autoscaling:DescribeScalingActivities` | A roll's failed node launches (such as the EC2 vCPU quota): `nodegroup update`, `cluster upgrade`, `cluster rollback`, `ui` |
| `cloudwatch:GetMetricData` | Health checks (CPU capacity, control plane, quota usage) |
| `servicequotas:GetServiceQuota` | Health checks (EC2 vCPU quota) |

A denied read does not always stop a command. The command prints what it could
read, lists the rest under `INCOMPLETE DATA` or in `failures`, and exits `4`.
A health check that cannot read is `Unreadable`. See
[Health checks](health-checks.md#outcomes).
