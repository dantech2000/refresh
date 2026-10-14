## refresh status

Show what is out of date in the fleet

### Synopsis

Show every cluster with what is out of date: the Kubernetes version and its
EKS support window, the nodegroups whose AMI is not the latest, and the
add-ons that have a newer version.

Without a region flag the command reads the configured region. -r names one
or more regions, and -A reads every region: the list in REFRESH_EKS_REGIONS,
or else the regions that every account has enabled.

Columns:
  SUPPORT    standard, extended, or unsupported, with the days left. A "*"
             marks dates that refresh ships with, because EKS did not
             report them.
  COMPUTE    the managed nodegroups, Auto Mode, Karpenter, or none
  STALE AMI  the nodegroups that do not run the latest AMI for their own
             Kubernetes version, of all nodegroups, with the age of the
             oldest such AMI; and the nodegroups on an older version than
             the control plane
  ADDONS     the add-ons with a newer compatible version
  HEALTH     the control-plane issues that EKS reports

When several exit codes apply, the highest wins.

Exit codes:
  0  Every cluster is current and in standard support.
  1  An error, an interrupt, or no region answered.
  2  Something is out of date: an AMI, an add-on, a nodegroup behind the
     control plane, or a control-plane issue.
  3  A cluster is in extended support, or unsupported.
  4  Incomplete: a part of the fleet could not be read.

```
refresh status [flags]
```

### Examples

```
  # The configured region
  refresh status

  # Every region, the clusters with the most to do first
  refresh status -A --sort stale --desc

  # The clusters that left standard support
  refresh status -A -f support=extended

  # The names of the clusters with a stale AMI, for a script
  refresh status -o json | jq -r '.clusters[] | select(any(.nodegroups[]?; .ami.status == "Outdated")) | .name'
```

### Options

```
  -A, --all-regions          Read every region: the list in REFRESH_EKS_REGIONS, or the regions that every account has enabled
      --desc                 Sort in descending order
  -f, --filter stringArray   Select by key=value, where * matches any text; repeatable, and all must match. Keys: name, region, version, support
  -h, --help                 help for status
  -o, --output format        Output format: table, json, yaml, plain (default table)
      --sort key             Sort by: name, region, version, support, stale (default name)
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

