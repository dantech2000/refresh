## refresh context add

Add or update a saved context

### Synopsis

Save a context that gives a name to a cluster, with an optional region and
AWS profile. A context with the same name is replaced, and the command says
"Updated" in place of "Saved".

The name has 1 to 63 letters, digits, hyphens, underscores, and dots. The
cluster name and the region are checked before the context is saved.

Exit codes:
  0  Done.
  1  An error.

```
refresh context add <name> [flags]
```

### Examples

```
  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod

  # Add the context and switch to it
  refresh context add stage --cluster stage-eks --use
```

### Options

```
  -c, --cluster string   EKS cluster name (required)
  -h, --help             help for add
      --profile string   AWS shared-config profile to save with the context
  -r, --region string    AWS region to save with the context
      --use              Switch to the context after it is saved
```

### Options inherited from parent commands

```
      --kube-context string   Kubeconfig context, trusted even when its server is not the cluster endpoint (default: the context whose server is the cluster endpoint; for the cluster of a read command, the current context)
      --kubeconfig string     Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --log-level string      Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL) (default "warn")
      --max-concurrency int   Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY) (default 32)
      --no-color              Disable color (env: NO_COLOR)
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh; 0 means no limit (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh context](refresh_context.md)	 - Manage saved contexts

