## refresh nodegroup

Work with EKS managed nodegroups

### Synopsis

Work with the managed nodegroups of one cluster.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. A command that only reads also takes the cluster of the
current kubeconfig context; "nodegroup update" and "nodegroup scale" never
do.

```
refresh nodegroup [flags]
```

### Examples

```
  # The nodegroups of a cluster, with the state of their AMIs
  refresh nodegroup list -c prod

  # One nodegroup, with its instances
  refresh nodegroup describe web -c prod --show-instances

  # Roll the nodegroups that are behind onto the latest AMI
  refresh nodegroup update -c prod

  # Scale a nodegroup to four nodes
  refresh nodegroup scale web -c prod --desired 4
```

### Options

```
  -h, --help   help for nodegroup
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
* [refresh nodegroup describe](refresh_nodegroup_describe.md)	 - Describe one nodegroup of a cluster
* [refresh nodegroup list](refresh_nodegroup_list.md)	 - List the nodegroups of a cluster
* [refresh nodegroup scale](refresh_nodegroup_scale.md)	 - Change the sizes of a nodegroup
* [refresh nodegroup update](refresh_nodegroup_update.md)	 - Roll nodegroups onto the latest AMI

