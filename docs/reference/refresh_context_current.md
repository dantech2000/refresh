## refresh context current

Print the active context

### Synopsis

Print the active context: its name, cluster, region, and profile.

The active context is the one that REFRESH_CONTEXT names, or else the one
that "refresh context use" selected. A REFRESH_CONTEXT that names no saved context is
an error. When no context is active, the command prints a hint on stderr and
nothing on stdout.

Exit codes:
  0  Done, also when no context is active.
  1  An error.

```
refresh context current [flags]
```

### Examples

```
  refresh context current

  # Use another context for one command
  REFRESH_CONTEXT=stage refresh context current
```

### Options

```
  -h, --help   help for current
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

* [refresh context](refresh_context.md)	 - Manage saved contexts

