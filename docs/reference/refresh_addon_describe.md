## refresh addon describe

Describe one add-on of a cluster

### Synopsis

Describe an EKS add-on: its version, status, health issues, and
configuration values.

The name is exact. For a name that does not exist, the command lists the
add-ons whose names are close.

The command reads one add-on, so it has no partial result: an add-on that
cannot be read is an error.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context.

Exit codes:
  0  Done.
  1  An error, or an interrupt.

```
refresh addon describe <addon> [flags]
```

### Examples

```
  refresh addon describe vpc-cni -c prod

  # The configuration values alone
  refresh addon describe coredns -c prod -o json | jq -r .addon.configuration
```

### Options

```
  -c, --cluster string   EKS cluster name (env: REFRESH_CLUSTER; default: the active context, then the current kubeconfig context)
  -h, --help             help for describe
  -o, --output format    Output format: table, json, yaml, plain (default table)
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
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh; 0 means no limit (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh addon](refresh_addon.md)	 - Work with EKS add-ons

