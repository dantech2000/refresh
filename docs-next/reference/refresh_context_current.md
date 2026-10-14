## refresh context current

Print the active context

### Synopsis

Print the active context: its name, cluster, region, and profile.

The active context is the one that REFRESH_CONTEXT names, or else the one
that "refresh context use" selected. A REFRESH_CONTEXT that names no saved context is
an error. When no context is active, the command prints a hint on stderr and
nothing on stdout.

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

* [refresh context](refresh_context.md)	 - Manage saved contexts

