## refresh completion

Print a shell completion script

### Synopsis

Print the completion script of refresh for bash, zsh, fish, or PowerShell.
The script completes commands, flags, and the names of clusters,
nodegroups, and add-ons from your account.

Run "refresh completion <shell> --help" for the steps for one shell.

```
refresh completion [flags]
```

### Examples

```
  # Load the script into this zsh session
  source <(refresh completion zsh)

  # Install it for every new fish session
  refresh completion fish > ~/.config/fish/completions/refresh.fish
```

### Options

```
  -h, --help   help for completion
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
* [refresh completion bash](refresh_completion_bash.md)	 - Print the completion script for bash
* [refresh completion fish](refresh_completion_fish.md)	 - Print the completion script for fish
* [refresh completion powershell](refresh_completion_powershell.md)	 - Print the completion script for PowerShell
* [refresh completion zsh](refresh_completion_zsh.md)	 - Print the completion script for zsh

