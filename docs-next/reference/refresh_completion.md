## refresh completion

Generate the autocompletion script for the specified shell

### Synopsis

Generate the autocompletion script for refresh for the specified shell.
See each sub-command's help for details on how to use the generated script.


```
refresh completion [flags]
```

### Options

```
  -h, --help   help for completion
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

* [refresh](refresh.md)	 - EKS upgrade companion: status, readiness, patch, upgrade
* [refresh completion bash](refresh_completion_bash.md)	 - Generate the autocompletion script for bash
* [refresh completion fish](refresh_completion_fish.md)	 - Generate the autocompletion script for fish
* [refresh completion powershell](refresh_completion_powershell.md)	 - Generate the autocompletion script for powershell
* [refresh completion zsh](refresh_completion_zsh.md)	 - Generate the autocompletion script for zsh

