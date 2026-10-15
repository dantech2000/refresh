## refresh version

Print the version of refresh

### Synopsis

Print the version, and the commit and build date of a release build.

On a terminal, the command also checks GitHub Releases for a newer version
and prints a one-line hint on stderr when there is one. The check runs at
most once a day, and a failed check prints nothing. It does not run when
stdout is not a terminal. To turn it off, use --no-update-check or set
REFRESH_NO_UPDATE_CHECK to any value other than 0, false, or no.

Exit codes:
  0  Done. A failed update check does not change the code.
  1  An error.

```
refresh version [flags]
```

### Examples

```
  refresh version

  # The version as a document, for a script
  refresh version -o json
```

### Options

```
  -h, --help              help for version
      --no-update-check   Do not check for a newer release (env: REFRESH_NO_UPDATE_CHECK)
  -o, --output format     Output format: table, json, yaml (default table)
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

