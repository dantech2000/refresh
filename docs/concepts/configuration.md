# Configuration & AWS auth

`refresh` uses your existing AWS credentials and (optionally) kubeconfig — there
is no separate config to bootstrap. This page covers how it resolves the target
account/region and the global flags and environment variables.

## AWS credential & region resolution

`refresh` resolves the **profile** and **region** in this order (first match
wins):

1. **Explicit CLI flags** — `--profile` / `--region` (global; work on every
   command).
2. **Standard AWS environment variables** — `AWS_PROFILE`, `AWS_REGION` /
   `AWS_DEFAULT_REGION` (resolved by the AWS SDK).
3. **The active `refresh` context** — see [Contexts](contexts.md).
4. **AWS SDK defaults** — `~/.aws/config`, `~/.aws/credentials`, SSO, IMDS, etc.

Flags always win, so you can override the active context for a single
invocation:

```bash
refresh status --profile prod --region us-east-1
```

Credentials themselves come from the standard SDK chain — `refresh` never stores
them.

## Cluster resolution

Every command that targets one cluster resolves it in this order (first match
wins):

1. The `--cluster, -c` flag.
2. The first positional argument.
3. The cluster of the active `refresh` context (`refresh use <name>`).
4. The cluster of the current kubeconfig context. Only read-only commands use
   this step.

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
| `--region` | — | AWS region (overrides the active context) |
| `--timeout, -t` | `60s` | Per-operation timeout for API calls (e.g. `60s`, `2m`). `cluster upgrade`, `nodegroup update`, and `addon update` have their own `--timeout` for the long-running wait |
| `--max-concurrency, -C` | `8` | Max concurrency for multi-region operations |

A global flag works the same before or after the subcommand:
`refresh -t 5s cluster list` and `refresh cluster list -t 5s` are equal. The
same is true for `refresh --region eu-west-1 status` and
`refresh status --region eu-west-1`. On `status` and `cluster list`, `-r` is
repeatable, and a local `-r` wins over the global `--region`.
| `--log-level` | `warn` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `--verbose` | off | Shortcut for `--log-level debug` |
| `--no-color` | off | Disable colored output (`NO_COLOR` is also honored) |

!!! note
    Logs go to **stderr**; data goes to **stdout**. Spinners auto-disable when
    output is piped, and `--log-level debug` surfaces service-level detail
    (cache hits, retries, fallbacks).

## Environment variables

| Variable | Equivalent / effect |
|---|---|
| `AWS_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION` | Standard AWS SDK resolution |
| `REFRESH_TIMEOUT` | Default for `--timeout` on API/read commands (list, describe, checks). Not applied to `cluster upgrade`, `addon update`, or `nodegroup update`, whose `--timeout` bounds a long-running wait |
| `REFRESH_MAX_CONCURRENCY` | Default for `--max-concurrency` |
| `REFRESH_LOG_LEVEL` | Default for `--log-level` |
| `REFRESH_EKS_REGIONS` | Comma-separated region set for multi-region sweeps: `status -A`, `cluster list -A`, and `nodegroup update --all-clusters`. `-r/--region` wins over it. When it is set, a region these credentials cannot use fails the sweep instead of being skipped |
| `REFRESH_CONTEXT` | Name of a saved context to use for this shell, instead of the saved current context (see [Contexts](contexts.md)). If no saved context has that name, every command fails before any AWS call and lists the known names |
| `EKS_CLUSTER_NAME` | Default cluster for `nodegroup update` only. A cluster given with `--cluster`, or positionally with `--nodegroup` or a second positional, wins (see [Cluster resolution](#cluster-resolution)) |
| `NO_COLOR` | Disable colored output |
| `REFRESH_NO_UPDATE_CHECK` | Disable the `refresh version` self-update check. Any value except `0`, `false`, or `no` disables it |
| `KUBECONFIG` | kubeconfig path for workload/PDB health checks |
| `REFRESH_IN_CLUSTER_NAME` | EKS cluster a pod runs in; lets in-cluster config be used for that cluster |

## Kubeconfig (optional)

Only the workload-aware pre-flight checks need Kubernetes access — the
PodDisruptionBudget and critical-workload checks used by `nodegroup update` and
`nodegroup scale --check-pdbs`. Resolution order is `--kubeconfig` →
`$KUBECONFIG` → `~/.kube/config`. If the cluster is unreachable, those checks are
**skipped** (with a diagnostic), not failed.

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
