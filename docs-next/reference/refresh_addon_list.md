## refresh addon list

List the add-ons of a cluster

### Synopsis

List the EKS add-ons of a cluster with their version, status, and health.

The health is the status as one of four values: Healthy, Failed, InProgress,
or Unknown. A DEGRADED add-on is Failed.

An add-on that could not be read is listed under INCOMPLETE DATA, or in
"failures" with -o json or -o yaml.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context.

Exit codes:
  0  Done, or a --watch that Ctrl+C stopped.
  1  An error, or an interrupt.
  4  Incomplete: an add-on could not be read. The command prints what
     it has.

```
refresh addon list [flags]
```

### Examples

```
  # The add-ons of a cluster
  refresh addon list -c prod

  # The add-ons of the cluster of the active context
  refresh addon list

  # The names and versions for a script
  refresh addon list -c prod -o json | jq -r '.addons[] | "\(.name) \(.version)"'
```

### Options

```
  -c, --cluster string            EKS cluster name (env: REFRESH_CLUSTER; default: the active context, then the current kubeconfig context)
  -h, --help                      help for list
  -o, --output format             Output format: table, json, yaml, plain (default table)
  -w, --watch                     Read again and redraw every --watch-interval, until Ctrl+C
      --watch-interval duration   Time between two reads of --watch (default 10s)
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

* [refresh addon](refresh_addon.md)	 - Work with EKS add-ons

