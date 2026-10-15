## refresh context use

Switch the active context

### Synopsis

Switch the active context. Later commands use its cluster, region, and
profile when you do not give them.

With "-", switch back to the context that was active before. Without an
argument, list the saved contexts and ask for one.

The flags and the positional of a command still beat the active context.
REFRESH_CONTEXT selects a context for one shell and beats the context that
this command saves.

Exit codes:
  0  Done.
  1  An error, or an interrupt.

```
refresh context use [context|-] [flags]
```

### Examples

```
  refresh context use prod

  # Switch back to the context before
  refresh context use -

  # Choose from the list
  refresh context use
```

### Options

```
  -h, --help   help for use
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

