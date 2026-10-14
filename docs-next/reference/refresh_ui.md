## refresh ui

Open the terminal UI for the fleet (experimental)

### Synopsis

Open a full-screen terminal UI (experimental): the fleet with what is out
of date, each cluster with its nodegroups and add-ons, the readiness checks
of "cluster upgrade-check" on demand, and the changes of the cluster
commands with their dry runs, health gates, and live progress.

The UI reads the fleet with the same code as "refresh status", every
--interval and on ctrl+r. The change keys (p, a, U, B) run the dry run of
"nodegroup update", "addon update", "cluster upgrade", and "cluster
rollback" and show the plan with the equivalent command. Without
--allow-changes the UI is read-only: nothing starts from it. With the
flag, y in a dry run starts that plan and no other; a plan that changed
since, or a health gate that warns, asks again before anything runs.
A change started elsewhere (the CLI, the console) shows on the Change
screen and can be watched, never stopped or answered, from the UI.

The UI needs a terminal of at least 100 by 24 cells. While it runs, the
standard error of refresh and of the programs it starts (such as a
kubeconfig credential plugin) goes to the null device, so their messages
cannot write over the screen. Press ? in the UI for the keys.

Exit codes:
  0  The UI was closed.
  1  An error, or no interactive terminal.

```
refresh ui [flags]
```

### Examples

```
  # The configured region
  refresh ui

  # Every region, swept every 30 seconds
  refresh ui -A --interval 30s

  # Let the UI start changes after their dry runs
  refresh ui --allow-changes
```

### Options

```
  -A, --all-regions             Read every region: the list in REFRESH_EKS_REGIONS, or the regions that every account has enabled
      --allow-changes           Let the UI start changes after their dry runs; without it the UI is read-only
  -h, --help                    help for ui
      --interval duration       Time between fleet sweeps, at least 10s; ctrl+r sweeps now (default 1m0s)
      --wait-timeout duration   How long the UI follows a change it starts or watches; unset means the default of the command that makes the change, 0 means no limit
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
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh](refresh.md)	 - EKS upgrade companion: status, readiness, patch, upgrade

