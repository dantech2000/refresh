## refresh cluster describe

Describe one cluster with its nodegroups and add-ons

### Synopsis

Describe an EKS cluster: its version and support window, its network and
security configuration, its nodegroups, its add-ons, and its health.

The HEALTH section runs the checks that a change runs before it starts.
The checks that read the Kubernetes API run with --check-readiness;
without it they are skipped. The section shows health and not upgrade
readiness: it reads no upgrade insight and no version skew, so an insight
in ERROR shows in "cluster upgrade-check" and not here. The summary does
not change the exit code: describe reads and reports, and "status" and
"cluster upgrade-check" are the commands that judge.

Without an argument the cluster comes from REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context. The name is exact. For
a name that does not exist, the command lists the clusters whose names are
close.

When a part of the cluster cannot be read, the command prints what it has,
and names the part under INCOMPLETE DATA or in "failures". The support
dates come from EKS; when EKS does not report them, the command uses the
dates that refresh ships with and says so.

Exit codes:
  0  Done. The health summary does not change the code.
  1  An error, or an interrupt.
  4  Incomplete: a part of the cluster could not be read. The command
     prints what it has.

```
refresh cluster describe [cluster] [flags]
```

### Examples

```
  refresh cluster describe prod

  # The cluster of the active context
  refresh cluster describe

  # The add-on versions for a script
  refresh cluster describe prod -o json | jq -r '.cluster.addons[] | "\(.name) \(.version)"'
```

### Options

```
      --check-readiness   Read the Kubernetes API too: the Ready nodes of each nodegroup, and the health checks that need it
  -h, --help              help for describe
  -o, --output format     Output format: table, json, yaml, plain (default table)
```

### Options inherited from parent commands

```
      --kube-context string   Kubeconfig context, trusted even when its server is not the cluster endpoint (default: the context whose server is the cluster endpoint; for the cluster of a read command, the current context)
      --kubeconfig string     Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --log-level string      Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL) (default "warn")
      --max-concurrency int   Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY) (default 32)
      --no-color              Disable color (env: NO_COLOR)
      --profile string        AWS shared-config profile (env: AWS_PROFILE)
  -r, --region strings        AWS region; a command that sweeps regions takes several, repeated or separated by commas (env for a sweep with -A: REFRESH_EKS_REGIONS)
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters

