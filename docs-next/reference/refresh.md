## refresh

EKS upgrade companion: status, readiness, patch, upgrade

### Synopsis

refresh keeps EKS clusters up to date in four steps:

  1. refresh status                 Find what is out of date in the fleet.
  2. refresh cluster upgrade-check  Check if a cluster is ready to upgrade.
  3. refresh nodegroup update       Patch nodegroups and add-ons.
     refresh addon update
  4. refresh cluster upgrade        Upgrade control plane, nodegroups, add-ons.

Commands that change a cluster support --dry-run. Use --yes in scripts to
skip the confirmation prompts.

```
refresh [flags]
```

### Examples

```
  # Save a context for a cluster and make it active
  refresh context add prod --cluster prod-eks --region us-east-1 --use

  # Show the active context
  refresh context current
```

### Options

```
  -h, --help                  help for refresh
      --kube-context string   Kubeconfig context, trusted even when its server is not the cluster endpoint (default: the context whose server is the cluster endpoint; for the cluster of a read command, the current context)
      --kubeconfig string     Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --log-level string      Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL) (default "warn")
      --max-concurrency int   Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY) (default 32)
      --no-color              Disable color (env: NO_COLOR)
      --profile string        AWS shared-config profile (env: AWS_PROFILE)
  -r, --region strings        AWS region; a command that sweeps regions takes several, repeated or separated by commas (env for a sweep with -A: REFRESH_EKS_REGIONS)
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh; 0 means no limit (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
      --version               Print the version, the commit, and the build date
```

### SEE ALSO

* [refresh addon](refresh_addon.md)	 - Work with EKS add-ons
* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters
* [refresh completion](refresh_completion.md)	 - Print a shell completion script
* [refresh context](refresh_context.md)	 - Manage saved contexts
* [refresh nodegroup](refresh_nodegroup.md)	 - Work with EKS managed nodegroups
* [refresh status](refresh_status.md)	 - Show what is out of date in the fleet
* [refresh ui](refresh_ui.md)	 - Open the terminal UI for the fleet (experimental)
* [refresh version](refresh_version.md)	 - Print the version of refresh

