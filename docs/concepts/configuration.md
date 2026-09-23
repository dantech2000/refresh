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
4. The cluster of the current kubeconfig context.

If none of these gives a cluster, the command fails with a non-zero exit.
Read-only commands (`describe`, `list`, `upgrade-check`) also print the
available clusters to stderr. Mutating commands (`cluster upgrade`,
`addon update`, `nodegroup update`, `nodegroup scale`) print only the error.

The name can be a partial pattern:

- An exact name always wins. `-c prod` selects `prod`, not `prod-legacy`.
- If only one cluster contains the pattern, `refresh` asks you to confirm it.
  Without a terminal, mutating commands fail and name the candidate. Read-only
  commands use the candidate and print a note to stderr.
- If several clusters contain the pattern, `refresh` asks you to pick one.
  Without a terminal, the command fails and lists the candidates.

## Global flags

These are accepted on every command:

| Flag | Default | Description |
|---|---|---|
| `--profile` | — | AWS shared-config profile (overrides the active context) |
| `--region` | — | AWS region (overrides the active context) |
| `--timeout, -t` | varies | Per-operation timeout for API calls (e.g. `60s`, `2m`) |
| `--max-concurrency, -C` | — | Max concurrency for multi-region operations |
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
| `REFRESH_TIMEOUT` | Default for `--timeout` |
| `REFRESH_MAX_CONCURRENCY` | Default for `--max-concurrency` |
| `REFRESH_LOG_LEVEL` | Default for `--log-level` |
| `REFRESH_EKS_REGIONS` | Region set for fleet discovery (`nodegroup update --all-clusters`) |
| `EKS_CLUSTER_NAME` | Default cluster for `nodegroup update` |
| `NO_COLOR` | Disable colored output |
| `REFRESH_NO_UPDATE_CHECK` | Disable the `refresh version` self-update check |
| `KUBECONFIG` | kubeconfig path for workload/PDB health checks |

## Kubeconfig (optional)

Only the workload-aware pre-flight checks need Kubernetes access — the
PodDisruptionBudget and critical-workload checks used by `nodegroup update` and
`nodegroup scale --check-pdbs`. Resolution order is `--kubeconfig` →
`$KUBECONFIG` → `~/.kube/config`. If the cluster is unreachable, those checks are
**skipped** (with a diagnostic), not failed.
