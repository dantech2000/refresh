## refresh completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(refresh completion zsh)

To load completions for every new session, execute once:

#### Linux:

	refresh completion zsh > "${fpath[1]}/_refresh"

#### macOS:

	refresh completion zsh > $(brew --prefix)/share/zsh/site-functions/_refresh

You will need to start a new shell for this setup to take effect.


```
refresh completion zsh [flags]
```

### Options

```
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
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

* [refresh completion](refresh_completion.md)	 - Generate the autocompletion script for the specified shell

