## refresh ui

Open the terminal UI for the fleet (experimental)

### Synopsis

Open a full-screen terminal UI (experimental): the fleet with what is out of date, each
cluster with its nodegroups and add-ons, and the readiness checks of
"cluster upgrade-check" on demand.

The UI reads the fleet with the same code as "refresh status", every
--interval and on ctrl+r. It is read-only in this version: the screens that
start a change come in a later one, and --allow-changes has no effect yet.

The UI needs a terminal of at least 100 by 24 cells. While it runs, the
standard error of refresh and of the programs it starts (such as a
kubeconfig credential plugin) goes to the null device, so their messages
cannot write over the screen. Press ? in the UI for the keys.

```
refresh ui [flags]
```

### Examples

```
  # The configured region
  refresh ui

  # Every region, swept every 30 seconds
  refresh ui -A --interval 30s
```

### Options

```
  -A, --all-regions             Read every region: the list in REFRESH_EKS_REGIONS, or the regions that every account has enabled
      --allow-changes           Let the UI start changes after their dry runs (no effect yet: the change screens are not built)
  -h, --help                    help for ui
      --interval duration       Time between fleet sweeps, at least 10s; ctrl+r sweeps now (default 1m0s)
      --wait-timeout duration   How long the UI follows a change it starts; 0 means the default of the command that makes the change (no effect yet)
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

