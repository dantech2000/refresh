## refresh addon list

List the add-ons of a cluster

### Synopsis

List the EKS add-ons of a cluster with their version, status, and health.

The health is the status as one of four values: Healthy, Failed, InProgress,
or Unknown. A DEGRADED add-on is Failed.

An add-on that could not be read is listed under INCOMPLETE DATA, or in
"failures" with -o json or -o yaml, and the command exits 4.

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
      --kube-context string   Kubeconfig context (default: the current context)
      --kubeconfig string     Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --log-level string      Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL) (default "warn")
      --max-concurrency int   Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY) (default 32)
      --no-color              Disable color (env: NO_COLOR)
      --profile string        AWS shared-config profile
  -r, --region strings        AWS region; a command that sweeps regions takes several, repeated or separated by commas
  -t, --timeout duration      Budget for the API calls of a read command (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh addon](refresh_addon.md)	 - Work with EKS add-ons

