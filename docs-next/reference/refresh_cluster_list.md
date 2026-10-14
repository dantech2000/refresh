## refresh cluster list

List the EKS clusters of one or more regions

### Synopsis

List EKS clusters with their status and Kubernetes version.

Without a region flag the command reads the configured region. -r names one
or more regions, and -A reads every region: the list in REFRESH_EKS_REGIONS,
or else the regions that every account has enabled. In that last case a
region that these credentials cannot use is skipped with one note on stderr.
A region that you name is never skipped.

With -w the command reads again every --watch-interval and redraws the
table, until Ctrl+C. With --show-health the command runs the health checks
that need no Kubernetes access on every cluster and adds a HEALTH column;
the column does not change the exit code.

A cluster or a region that could not be read is listed under INCOMPLETE
DATA, or in "failures" with -o json or -o yaml.

Exit codes:
  0  Done, or a --watch that Ctrl+C stopped.
  1  An error, an interrupt, or no region answered.
  4  Incomplete: a cluster or a region could not be read. The command
     prints what it has.

```
refresh cluster list [flags]
```

### Examples

```
  # The clusters of the configured region
  refresh cluster list

  # Two regions, the newest version first
  refresh cluster list -r us-east-1 -r eu-west-1 --sort version --desc

  # Every region, only the production clusters that are being updated
  refresh cluster list -A -f name=prod-* -f status=UPDATING

  # Names and versions for a script
  refresh cluster list -o json | jq -r '.clusters[] | "\(.name) \(.version)"'
```

### Options

```
  -A, --all-regions               Read every region: the list in REFRESH_EKS_REGIONS, or the regions that every account has enabled
      --desc                      Sort in descending order
  -f, --filter stringArray        Select by key=value, where * matches any text; repeatable, and all must match. Keys: name, region, status, version
  -h, --help                      help for list
  -o, --output format             Output format: table, json, yaml, plain (default table)
      --show-health               Run the health checks that need no Kubernetes access on every cluster, and add a HEALTH column
      --sort key                  Sort by: name, region, status, version (default name)
  -w, --watch                     Read again and redraw every --watch-interval, until Ctrl+C
      --watch-interval duration   Time between two reads of --watch (default 10s)
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

* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters

