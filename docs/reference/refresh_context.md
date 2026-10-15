## refresh context

Manage saved contexts

### Synopsis

Manage the named contexts and choose the active one.

A context gives a name to a cluster, with an optional region and AWS profile.
Commands use the active context when you do not name a cluster.

The contexts are in a YAML file: $REFRESH_CONFIG_HOME/context.yaml when that
variable is set, otherwise $XDG_CONFIG_HOME/refresh/context.yaml, which is
~/.config/refresh/context.yaml by default.

```
refresh context [flags]
```

### Examples

```
  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
  refresh context list
  refresh context use prod
  refresh context current
  refresh context remove prod
```

### Options

```
  -h, --help   help for context
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

* [refresh](refresh.md)	 - EKS upgrade companion: status, readiness, patch, upgrade
* [refresh context add](refresh_context_add.md)	 - Add or update a saved context
* [refresh context current](refresh_context_current.md)	 - Print the active context
* [refresh context list](refresh_context_list.md)	 - List the saved contexts
* [refresh context remove](refresh_context_remove.md)	 - Remove a saved context
* [refresh context use](refresh_context_use.md)	 - Switch the active context

