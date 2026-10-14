## refresh addon

Work with EKS add-ons

### Synopsis

Work with the EKS add-ons of one cluster.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. A command that only reads also takes the cluster of the
current kubeconfig context; "addon update" never does.

```
refresh addon [flags]
```

### Examples

```
  # The add-ons of a cluster
  refresh addon list -c prod

  # One add-on, with its configuration
  refresh addon describe vpc-cni -c prod

  # Update every add-on, in dependency order
  refresh addon update --all -c prod
```

### Options

```
  -h, --help   help for addon
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
* [refresh addon describe](refresh_addon_describe.md)	 - Describe one add-on of a cluster
* [refresh addon list](refresh_addon_list.md)	 - List the add-ons of a cluster
* [refresh addon update](refresh_addon_update.md)	 - Update add-ons of a cluster

