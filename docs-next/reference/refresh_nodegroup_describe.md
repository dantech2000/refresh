## refresh nodegroup describe

Describe one nodegroup of a cluster

### Synopsis

Describe a managed nodegroup: its status, version, scaling, and the AMI
that its nodes run next to the latest recommended one. With --show-instances
the command also lists the EC2 instances of the nodegroup.

The name is exact. For a name that does not exist, the command lists the
nodegroups whose names are close.

When the AMI, the instances, or the workloads cannot be read, the command
prints what it has, and names the part under INCOMPLETE DATA or in
"failures".

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context.

Exit codes:
  0  Done.
  1  An error, or an interrupt.
  4  Incomplete: a part of the nodegroup could not be read. The command
     prints what it has.

```
refresh nodegroup describe <nodegroup> [flags]
```

### Examples

```
  refresh nodegroup describe web -c prod

  # With the EC2 instances
  refresh nodegroup describe web -c prod --show-instances
```

### Options

```
  -c, --cluster string   EKS cluster name (env: REFRESH_CLUSTER; default: the active context, then the current kubeconfig context)
  -h, --help             help for describe
  -o, --output format    Output format: table, json, yaml, plain (default table)
      --show-instances   List the EC2 instances of the nodegroup
      --show-workloads   Count the pods on the nodes of the nodegroup, from the Kubernetes API
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

* [refresh nodegroup](refresh_nodegroup.md)	 - Work with EKS managed nodegroups

