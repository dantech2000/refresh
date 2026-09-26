# Configuration & AWS auth

`refresh` uses your existing AWS credentials and (optionally) kubeconfig — there
is no separate config to bootstrap. This page covers how it resolves the target
account/region and the global flags and environment variables.

## AWS credential & region resolution

`refresh` resolves the **profile** and **region** in this order (first match
wins):

1. **Explicit CLI flags** — `--profile` / `--region` (global; work on every
   command).
2. **Standard AWS environment variables** — `AWS_PROFILE` /
   `AWS_DEFAULT_PROFILE` and `AWS_REGION` / `AWS_DEFAULT_REGION` (resolved by
   the AWS SDK).
3. **The active `refresh` context** — see [Contexts](contexts.md).
4. **AWS SDK defaults** — `~/.aws/config`, `~/.aws/credentials`, SSO, IMDS, etc.

Flags always win, so you can override the active context for a single
invocation:

```bash
refresh status --profile prod --region us-east-1
```

The active context applies as a unit. Its cluster, profile, and region belong
together, so an environment variable never replaces only one of them:

- If an environment variable sets a value that the context leaves empty, the
  environment variable applies. For example, `AWS_PROFILE` applies to a
  context that has no profile.
- If an environment variable has the same value as the context, nothing
  changes.
- If an environment variable has a different value than the context, the
  command fails before any AWS call. For example, `AWS_PROFILE=staging` with a
  context whose profile is `prod` is an error. Without this rule, the command
  would use the staging credentials with the region and cluster of the prod
  context.

To fix a conflict, do one of these steps:

1. Pass `--profile` or `--region` to choose the value for one command.
2. Unset the environment variable that the error names.
3. Switch to a context that matches, or set `REFRESH_CONTEXT`.

A context file that exists but cannot be read or parsed is also an error. Every
command that reads the context fails, and the error names the file. Only a
missing file means "no context".

Credentials themselves come from the standard SDK chain — `refresh` never stores
them.

Before its first AWS call, a command resolves the credentials from that chain.
It makes no extra request to do this. If no source gives credentials, or the
SSO session has expired, the command stops with the setup help. Keys that
resolve but are revoked or expired fail on the first AWS call, with the same
help.

## Cluster resolution

Every command that targets one cluster resolves it in this order (first match
wins):

1. The `--cluster, -c` flag.
2. The first positional argument.
3. The cluster of the active `refresh` context (`refresh use <name>`).
4. The cluster of the current kubeconfig context. Only read-only commands use
   this step. `KUBECONFIG` can list several files, separated by `:` (`;` on
   Windows). refresh merges them as `kubectl` does.

`addon describe` and `nodegroup describe` take `[cluster] [name]`. If a context
is active and you pass one positional argument without `--cluster` or the name
flag, that argument is the name, and the cluster comes from the context. So
`refresh addon describe vpc-cni` describes `vpc-cni` in the cluster of the
active context. Two positional arguments are always the cluster and the name.

`nodegroup update` also reads the `EKS_CLUSTER_NAME` environment variable.
No other command reads it. The variable never overrides a cluster that is
clearly on the command line:

- If you pass `--cluster`, `--cluster` is the cluster.
- If you pass `--nodegroup` and one positional argument, the positional
  argument is the cluster.
- If you pass two positional arguments, the first is the cluster and the
  second is the nodegroup.
- In all other cases, `EKS_CLUSTER_NAME` is the cluster, and one positional
  argument is the nodegroup pattern. refresh prints
  `Using cluster <name> from EKS_CLUSTER_NAME` to stderr.

```bash
export EKS_CLUSTER_NAME=staging
refresh nodegroup update prod --nodegroup ng-a  # prod / ng-a
refresh nodegroup update prod ng-a              # prod / ng-a
refresh nodegroup update ng-a                   # staging / ng-a (with a note)
refresh nodegroup update                        # staging / all nodegroups (with a note)
```

Mutating commands (`cluster upgrade`, `addon update`, `nodegroup update`,
`nodegroup scale`) never use the kubeconfig. A kubeconfig that points at
another cluster cannot select the target of a change. If a mutating command
takes the cluster from the active context, it prints
`Using cluster <cluster> (from context <name>)` to stderr.

If none of the steps gives a cluster, the command fails with a non-zero exit.
Read-only commands (`describe`, `list`, `upgrade-check`) also print the
available clusters to stderr. Mutating commands print only the error.

The name can be a partial pattern:

- An exact name always wins. `-c prod` selects `prod`, not `prod-legacy`.
- If only one cluster contains the pattern, `refresh` asks you to confirm it.
  Without a terminal, mutating commands fail and name the candidate. Read-only
  commands use the candidate and print a note to stderr.
- If several clusters contain the pattern, `refresh` asks you to pick one.
  Without a terminal, the command fails and lists the candidates.
- `--yes` does not accept a partial cluster name. In scripts, pass the exact
  name.
- With `-o json` or `-o yaml`, `refresh` never asks, even on a terminal. The
  pattern resolves as it does without a terminal.

Prompts wait until you answer or press Ctrl+C. The time you take to answer
does not count against `--timeout`.

### Nodegroup patterns

The nodegroup in `nodegroup update` can also be a partial pattern. A pattern
that is not an exact nodegroup name always needs a confirmation:

- An exact name always wins. `-n web` selects `web`, not `payments-web`.
- If only one nodegroup contains the pattern, `refresh` asks you to confirm
  it. If several nodegroups contain the pattern, `refresh` asks you to confirm
  all of them.
- Without a terminal, or with `-o json`/`-o yaml`, the command fails and names
  the candidates. To accept them, pass `--yes`.
- In fleet mode (`--all-clusters`), the batch confirmation (or `--yes`)
  accepts the pattern in each cluster.

```bash
export EKS_CLUSTER_NAME=staging
refresh nodegroup update prod        # staging / "prod" is a nodegroup pattern:
                                     # a match such as prod-mirror needs confirmation
```

## Global flags

These are accepted on every command:

| Flag | Default | Description |
|---|---|---|
| `--profile` | — | AWS shared-config profile (overrides the active context) |
| `--region, -r` | — | AWS region (overrides the active context) |
| `--timeout, -t` | `60s` | Timeout for API calls on list, describe, and check commands (e.g. `60s`, `2m`). See [Timeouts](#timeouts) |
| `--max-concurrency, -C` | `8` | Max concurrency for multi-region operations. For `status`, the clusters evaluated at once in each region; `status` sweeps `min(4, -C)` regions at once |
| `--log-level` | `warn` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `--verbose` | off | Shortcut for `--log-level debug` |
| `--no-color` | off | Disable colored output (a non-empty `NO_COLOR` is also honored) |

A global flag works the same before or after the subcommand:
`refresh -t 5s cluster list` and `refresh cluster list -t 5s` are equal.

`addon update` has its own `--timeout` for its update API calls, with a
longer default (see [Timeouts](#timeouts)). In 0.11, `cluster upgrade` and
`nodegroup update` still accept a local `--timeout/-t` as a deprecated alias
of `--wait-timeout`. On these three commands, put the global API timeout
before the subcommand: `refresh -t 2m cluster upgrade ...`.

## Flag shorthands

Each one-letter shorthand has one meaning on every command:

| Shorthand | Long flag |
|---|---|
| `-c` | `--cluster` |
| `-n` | `--nodegroup` |
| `-a` | `--addon` |
| `-o` | `--format` |
| `-r` | `--region` |
| `-t` | `--timeout` |
| `-d` | `--dry-run` |
| `-y` | `--yes` |
| `-q` | `--quiet` |
| `-w` | `--watch` |
| `-f` | `--filter` |

A few uppercase shorthands exist outside this set, each with one meaning:
`-A` (`--all-regions`), `-C` (`--max-concurrency`), `-H` (`--show-health`),
`-R` (`--check-readiness`), `-T` (`--tree`), `-I` (`--show-instances`), and
`-W` (`--show-workloads`). A test fails the build if a shorthand gets a
second meaning.

A shorthand removed in 0.11 fails with its replacement, for example
`-d was removed from 'cluster describe' in 0.11.0; use --detailed`. See
[Migrating to 0.11](../migration.md#migrating-to-011).

## Commands that change a cluster

`addon update`, `nodegroup scale`, `nodegroup update`, and `cluster upgrade`
share these flags:

| Flag | Meaning |
|---|---|
| `--dry-run, -d` | Show what would change. Never prompts and never changes anything |
| `--yes, -y` | Skip the confirmation prompts |
| `--wait-timeout` | How long to wait for the change to finish (where the command waits) |
| `--kubeconfig`, `--kube-context` | The Kubernetes access for health, PDB, and live-roll checks (`nodegroup scale`, `nodegroup update`, `cluster upgrade`) |

`addon update` and `nodegroup scale` ask before they change anything.
`cluster upgrade` asks before each phase. `nodegroup update` asks when a
nodegroup pattern is not an exact name or health checks warn. A prompt
continues only on `y` or `yes`. With `-o json`/`-o yaml`, or without a
terminal, a run that would ask fails with an error that names `--yes`.

!!! note
    Logs go to **stderr**; data goes to **stdout**. Spinners write nothing
    when stderr is not a terminal, and `--log-level debug` surfaces
    service-level detail (cache hits, retries, fallbacks).

## Regions

A single-cluster command uses one region: `--region`, then `AWS_REGION` /
`AWS_DEFAULT_REGION`, then the active context, then the AWS config. An
environment variable that disagrees with the region of the active context is an
error (see [AWS credential & region resolution](#aws-credential-region-resolution)).

`status`, `cluster list`, and `nodegroup update --all-clusters` can scan
several regions. They pick the regions in this order:

1. A local `-r/--region` after the subcommand. It is repeatable:
   `refresh status -r us-east-1 -r us-west-2`.
2. Without `-A`, a global `--region` before the subcommand. So
   `refresh --region eu-west-1 status` equals `refresh status -r eu-west-1`.
3. For a sweep (`status -A`, `cluster list -A`, `nodegroup update
   --all-clusters`): `REFRESH_EKS_REGIONS`, a comma-separated list, and
   otherwise every EKS region in the partition of the home region.
4. Without a sweep: the one configured region (`AWS_REGION`, the active
   context, or the AWS config).

With a sweep, a global `--region` sets only the home region and so the
partition: `refresh --region cn-north-1 cluster list -A` sweeps the China
partition.

In the default sweep (every region of the partition), a region that these credentials cannot use
(an SCP denial, an opt-in region that is not enabled) is skipped with one
note on stderr and does not count as a failure. A region that you name with
`-r` or `REFRESH_EKS_REGIONS` counts as a failure if it cannot be listed.
What a failed region does depends on the command:

- `status` marks the data incomplete and exits `4`.
- `nodegroup update --all-clusters` reports the region and exits `4`.
- `cluster list` prints one warning per failed region on stderr, prints the
  clusters it gathered, and exits `4`.

In all three, if no region answers, nothing was gathered and the command
fails with exit `1`. See [Exit codes](exit-codes.md#region-sweeps). When no
region answers because of the credentials, the error is the credential setup
help. An expired token names itself. Invalid keys make every region look
closed, so when a region was skipped or unavailable, `refresh` calls
`sts:GetCallerIdentity` once to tell the two apart.

## Timeouts

The global `--timeout` (default `60s`, or `REFRESH_TIMEOUT`) bounds the AWS
calls of list, describe, and check commands. Config loading and credential
resolution always run under it.

These commands bound long-running work with their own flags. `REFRESH_TIMEOUT`
does not set them:

| Command | Flag | Default | Scope |
|---|---|---|---|
| `nodegroup update` | `--wait-timeout` | `40m` | The whole run. With `--all-clusters`, each cluster (health gate, roll, verify) gets its own `--wait-timeout`. `0` means no limit |
| `cluster upgrade` | `--wait-timeout` | `4h` | The whole upgrade. `0` means no limit |
| `addon update` | `--timeout, -t` | `10m` | The update API calls. With `--wait`, each add-on (or each batch of 3 with `--parallel`) also gets `--wait-timeout` (default `5m`). `--wait-timeout 0` means no limit |
| `nodegroup scale --wait` | `--wait-timeout` | `5m` | Added to the global `--timeout`, plus one more `--timeout` with `--health-check`. `0` means no limit |

Before 0.11, the wait flag was `--timeout/-t` on `nodegroup update` and
`cluster upgrade`, and `--op-timeout` on `nodegroup scale`. These names still
work in 0.11 and print a deprecation warning. They go away in 0.12. If you
pass an old name together with `--wait-timeout`, the command fails.

`--poll-interval` on `nodegroup update` and `cluster upgrade` must be greater
than `0`. A zero or negative value fails before any AWS call.

## Environment variables

| Variable | Equivalent / effect |
|---|---|
| `AWS_PROFILE`, `AWS_DEFAULT_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION` | Standard AWS SDK resolution. A value that differs from the active context is an error (see [AWS credential & region resolution](#aws-credential-region-resolution)) |
| `REFRESH_TIMEOUT` | Default for the global `--timeout` (list, describe, and check commands). Not applied to `--wait-timeout` or to the `--timeout` of `addon update`, which bound long-running work |
| `REFRESH_MAX_CONCURRENCY` | Default for `--max-concurrency` |
| `REFRESH_LOG_LEVEL` | Default for `--log-level` |
| `REFRESH_EKS_REGIONS` | Comma-separated region set for multi-region sweeps: `status -A`, `cluster list -A`, and `nodegroup update --all-clusters`. `-r/--region` wins over it. When it is set, a region these credentials cannot use counts as failed instead of being skipped (see [Regions](#regions)) |
| `REFRESH_CONTEXT` | Name of a saved context to use for this shell, instead of the saved current context (see [Contexts](contexts.md)). If no saved context has that name, every command fails before any AWS call and lists the known names |
| `EKS_CLUSTER_NAME` | Default cluster for `nodegroup update` only. A cluster given with `--cluster`, or positionally with `--nodegroup` or a second positional, wins (see [Cluster resolution](#cluster-resolution)) |
| `NO_COLOR` | Any non-empty value disables colored output on stdout and stderr. `TERM=dumb` does the same |
| `REFRESH_NO_UPDATE_CHECK` | Disable the `refresh version` self-update check. Any non-empty value except `0`, `false`, or `no` disables it |
| `KUBECONFIG` | kubeconfig path(s) for the Kubernetes checks. A colon-separated list is merged with the usual `kubectl` rules |
| `REFRESH_IN_CLUSTER_NAME` | EKS cluster a pod runs in; lets in-cluster config be used for that cluster (see [below](#matching-the-kubeconfig-to-the-target-cluster)) |

## Kubeconfig (optional)

These features read the cluster's Kubernetes API:

- The Kubernetes-backed [pre-flight health checks](health-checks.md) in
  `nodegroup update` and `cluster upgrade` (PDB drain blockers, critical
  workloads, real node readiness).
- `nodegroup scale --check-pdbs`.
- `--check-readiness` on `cluster describe` and `nodegroup list`.
- `nodegroup describe --show-workloads`.
- The live node-roll panel and post-roll verification.

The kubeconfig comes from `--kubeconfig` (one file), then `$KUBECONFIG` (a
colon-separated list is merged with the usual `kubectl` rules), then
`~/.kube/config`. If the cluster is unreachable, the health checks that need
it are **skipped** (with a diagnostic), not failed. The
`nodegroup scale --check-pdbs` gate is the exception: it refuses the
scale-down.

### Matching the kubeconfig to the target cluster

refresh only runs Kubernetes checks against the cluster you target with
`--cluster`. It compares each kubeconfig context's server with the cluster's
API endpoint from `DescribeCluster`:

1. If the current context points at the target cluster, refresh uses it.
2. If another context points at the target cluster, refresh uses that context.
   If several contexts match, refresh tries them in order until one is reachable.
3. If no context matches, refresh skips the Kubernetes checks. A warning on
   stderr names both servers. Run `aws eks update-kubeconfig --name <cluster>
   --region <region>` to add a context.

Proxied or tunnelled API servers (Teleport, Rancher, an SSH tunnel to
`localhost`) never match the EKS endpoint. For these, name the context with
`--kube-context <name>`. refresh trusts a context that you name, and prints a
one-line note that it could not verify the target.

Inside a pod, the in-cluster config points at the Kubernetes service address,
so refresh cannot match it to an EKS endpoint. Set
`REFRESH_IN_CLUSTER_NAME=<eks cluster name>` in the pod to declare its
cluster. refresh uses the in-cluster config only when this value equals the
target cluster name.

## Required IAM permissions

`refresh` calls these AWS APIs. A read-only role needs the read actions. A
role that patches or upgrades also needs the write actions. When a call is
denied, the error names the operation, and the command either fails or marks
the data incomplete.

| Action | Used by |
|---|---|
| `sts:GetCallerIdentity` | Region sweeps in which no region answered (credential check) |
| `eks:ListClusters` | `status`, `cluster list`, `nodegroup update --all-clusters`, partial cluster names |
| `eks:DescribeCluster` | Every cluster command |
| `eks:ListNodegroups`, `eks:DescribeNodegroup` | `status`, `nodegroup *`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, health checks, the busy check of `addon update` |
| `eks:ListAddons` | `status`, `addon *` (also to resolve a partial add-on name), `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, the busy check of `nodegroup update`/`scale` |
| `eks:DescribeAddon`, `eks:DescribeAddonVersions` | `status`, `addon *`, `cluster upgrade-check`/`upgrade`/`rollback`; `eks:DescribeAddon` also in the busy check of `nodegroup update`/`scale` |
| `eks:DescribeClusterVersions` | `status`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback` (support calendar; `refresh` falls back to a built-in calendar) |
| `eks:ListInsights`, `eks:DescribeInsight` | `cluster upgrade-check`, `cluster upgrade`, `cluster rollback` |
| `eks:StartInsightsRefresh`, `eks:DescribeInsightsRefresh` | `cluster upgrade` (not with `--dry-run` or `--skip-insights-check`) |
| `eks:UpdateNodegroupVersion` | `nodegroup update`, `cluster upgrade`, `cluster rollback` |
| `eks:UpdateNodegroupConfig` | `nodegroup scale` |
| `eks:UpdateClusterVersion` | `cluster upgrade`, `cluster rollback` |
| `eks:UpdateAddon` | `addon update`, `cluster upgrade`, `cluster rollback` |
| `eks:DescribeUpdate` | `nodegroup update`, `nodegroup scale --wait`, `addon update --wait`, `cluster upgrade`, `cluster rollback`, `cluster upgrade-check` (rollback window) |
| `eks:ListUpdates` | `cluster rollback`, `cluster upgrade-check` (rollback window), `refresh ui` (watch a roll or upgrade started elsewhere) |
| `ssm:GetParameter` | Latest recommended AMI: `status`, `nodegroup list`/`describe`/`update` |
| `ec2:DescribeImages` | `status` (AMI age) |
| `ec2:DescribeInstances` | `status` (Karpenter detection), `nodegroup describe --show-instances`, current AMI lookup |
| `ec2:DescribeLaunchTemplateVersions` | Current AMI of a launch-template nodegroup (`nodegroup update --dry-run`) |
| `ec2:DescribeSubnets`, `ec2:DescribeInstanceTypeOfferings` | Instance-type availability warning in `nodegroup update`/`scale` |
| `ec2:DescribeVpcs` | `cluster describe --detailed` |
| `autoscaling:DescribeAutoScalingGroups` | Health checks, `nodegroup describe`, current AMI lookup |
| `cloudwatch:GetMetricData` | Health checks (CPU capacity, control plane, quota usage) |
| `servicequotas:GetServiceQuota` | Health checks (EC2 vCPU quota) |

Best-effort lookups (AMI age, instance-type availability, CloudWatch metrics,
quotas) degrade to "unknown" or "skipped" when denied. A denied
`ssm:GetParameter` marks the AMI status as `unknown (lookup failed)`, and
`status` exits `4`.

The Kubernetes side needs `list` on nodes, pods, namespaces, deployments,
events, and PodDisruptionBudgets, and read access to the metrics API. `watch`
on nodes and events lets the live roll panel stream changes instead of
polling.
