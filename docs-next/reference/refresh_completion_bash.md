## refresh completion bash

Print the completion script for bash

### Synopsis

Print the completion script for bash.

The script needs the bash-completion package.
Start a new shell after you install the script.

Exit codes:
  0  Done.
  1  An error.

```
refresh completion bash
```

### Examples

```
  # Load the script into this session
  source <(refresh completion bash)

  # Install it for every new session, on Linux
  refresh completion bash > /etc/bash_completion.d/refresh

  # Install it for every new session, on macOS with Homebrew
  refresh completion bash > $(brew --prefix)/etc/bash_completion.d/refresh
```

### Options

```
  -h, --help              help for bash
      --no-descriptions   Leave the description of each command and flag out of the completions
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

* [refresh completion](refresh_completion.md)	 - Print a shell completion script

