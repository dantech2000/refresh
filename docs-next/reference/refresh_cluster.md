## refresh cluster

Work with EKS clusters

### Synopsis

Work with EKS clusters.

"refresh cluster list" reads one region, the regions you name with -r, or
every region with -A. "refresh cluster describe" reads one cluster, and
"refresh cluster upgrade-check" reports whether it is ready to upgrade.
"refresh cluster upgrade" moves a cluster to a Kubernetes version, and
"refresh cluster rollback" returns it to the version before its upgrade.

```
refresh cluster [flags]
```

### Examples

```
  # The clusters of the configured region
  refresh cluster list

  # Every region, as JSON
  refresh cluster list -A -o json
```

### Options

```
  -h, --help   help for cluster
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
* [refresh cluster describe](refresh_cluster_describe.md)	 - Describe one cluster with its nodegroups and add-ons
* [refresh cluster list](refresh_cluster_list.md)	 - List the EKS clusters of one or more regions
* [refresh cluster rollback](refresh_cluster_rollback.md)	 - Roll a cluster back to the Kubernetes version before its upgrade
* [refresh cluster upgrade](refresh_cluster_upgrade.md)	 - Upgrade a cluster to a Kubernetes version
* [refresh cluster upgrade-check](refresh_cluster_upgrade-check.md)	 - Report whether a cluster is ready for its next upgrade

