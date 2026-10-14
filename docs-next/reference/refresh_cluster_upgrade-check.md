## refresh cluster upgrade-check

Report whether a cluster is ready for its next upgrade

### Synopsis

Report whether a cluster is ready for its next Kubernetes upgrade: the EKS
cluster insights, the version skew of its nodegroups and add-ons, the
control-plane metrics, the support window, and the rollback window after a
recent upgrade. Nothing is changed.

The exit code is the verdict, for a CI gate: 0 ready, 2 review (warnings),
3 blocked, 4 incomplete (a check could not read what it needs, or a
nodegroup or an add-on could not be read). --exit-zero reports and exits 0.

--status narrows the insights that are shown, not the ones that are
judged: an ERROR insight blocks whatever is shown. --insight shows one
insight in full, by its ID or by the short ID of the table, and exits with
that insight's status.

```
refresh cluster upgrade-check [cluster] [flags]
```

### Examples

```
  refresh cluster upgrade-check prod

  # Every insight, passing ones too, as JSON for a gate
  refresh cluster upgrade-check prod --show-passing -o json

  # The report without a failing exit code
  refresh cluster upgrade-check prod --exit-zero

  # One insight in full
  refresh cluster upgrade-check prod --insight bc8b2f86
```

### Options

```
      --category string   Insight category: UPGRADE_READINESS, MISCONFIGURATION, ROLLBACK_READINESS (default "UPGRADE_READINESS")
      --exit-zero         Exit 0 whatever the verdict (report mode)
  -h, --help              help for upgrade-check
      --insight string    Show one insight in full, by its ID or the short ID of the table
  -o, --output format     Output format: table, json, yaml, plain (default table)
      --show-passing      Show the PASSING insights too
      --status strings    Show only the insights with these statuses: PASSING, WARNING, ERROR, UNKNOWN
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

* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters

