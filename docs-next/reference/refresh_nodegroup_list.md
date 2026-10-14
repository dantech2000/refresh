## refresh nodegroup list

List the nodegroups of a cluster

### Synopsis

List the managed nodegroups of a cluster with their status, Kubernetes
version, and the state of their AMI.

The AMI column compares the AMI that the nodes run with the latest
recommended AMI for the version and the AMI type of the nodegroup: Latest,
Outdated, Updating, Custom (a launch template sets the AMI), or Unknown (a
nodegroup with no instance). A version with a warning mark is behind the
control plane.

A nodegroup that could not be read, or whose AMI could not be looked up, is
listed under INCOMPLETE DATA, or in "failures" with -o json or -o yaml, and
the command exits 4.

```
refresh nodegroup list [flags]
```

### Examples

```
  refresh nodegroup list -c prod

  # The nodegroups with an outdated AMI
  refresh nodegroup list -c prod -f amiStatus=Outdated

  # The largest nodegroups first
  refresh nodegroup list -c prod --sort nodes --desc

  # The names of the outdated nodegroups for a script
  refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.ami.status == "Outdated") | .name'
```

### Options

```
      --check-readiness           Read the Ready nodes of each nodegroup from the Kubernetes API; NODES then shows ready/desired
  -c, --cluster string            EKS cluster name (env: REFRESH_CLUSTER; default: the active context, then the current kubeconfig context)
      --desc                      Sort in descending order
  -f, --filter stringArray        Select by key=value, where * matches any text; repeatable, and all must match. Keys: name, status, instanceType, amiStatus, version
  -h, --help                      help for list
  -o, --output format             Output format: table, json, yaml, plain (default table)
      --sort key                  Sort by: name, status, instanceType, nodes, version, amiStatus (default name)
  -w, --watch                     Read again and redraw every --watch-interval, until Ctrl+C
      --watch-interval duration   Time between two reads of --watch (default 10s)
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

* [refresh nodegroup](refresh_nodegroup.md)	 - Work with EKS managed nodegroups

