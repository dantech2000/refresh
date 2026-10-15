## refresh nodegroup scale

Change the sizes of a nodegroup

### Synopsis

Change the desired, minimum, and maximum size of one managed nodegroup.
The name is exact. A size that is not given keeps its value.

The sizes are checked before anything is sent: against each other, and
against the current sizes of the nodegroup for the ones that were not
given. Sizes that cannot hold are an error. A lone --min or --max that
excludes the current desired size says to pass --desired too. A scale to
the sizes that the nodegroup already has sends nothing.

Before any change the command reads the whole cluster. It refuses while
the cluster or anything else in it is changing, and it leaves a nodegroup
alone that is already changing.

A scale that lowers the desired size removes nodes, and EKS does not wait
for PodDisruptionBudgets when it does. So the health gate reads the nodes
and the budgets from the Kubernetes API, and stops a scale-down that can
take a budget below what it allows. --force goes past that, with a
warning. Without Kubernetes access the command asks before it goes on.
Any other scale checks the EC2 vCPU quota: a scale-up whose new nodes do
not fit is blocked, and --force goes past that, with a warning. A scale-up
warns when an availability zone of the nodegroup does not offer one of its
instance types.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It waits until EKS
reports the update done and the nodegroup is ACTIVE at the new sizes;
--no-wait returns once AWS accepted the change.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig.

Exit codes:
  0  Done, or nothing to do.
  1  An error, a question answered no, or an interrupt.
     The update failed, the nodegroup is not ACTIVE at the new sizes
     after it, or the sizes cannot hold. A scale has no verification
     after it, so it never exits 5.
  2  With --dry-run only: a real run would ask about warnings.
  3  Blocked: the cluster is busy, a gate stops the change, or
     --require-healthy and a warning. Nothing changed, or nothing more
     when the gate stops a later step.
  4  Incomplete: a target was already changing, a wait timed out, or
     something could not be read.

```
refresh nodegroup scale <nodegroup> [flags]
```

### Examples

```
  # Add a node
  refresh nodegroup scale web -c prod --desired 4

  # See what would change, and what the health gate says
  refresh nodegroup scale web -c prod --desired 2 --dry-run

  # Raise the limit and the node count together, without a prompt
  refresh nodegroup scale web -c prod --desired 8 --max 10 --yes

  # Change only the bounds, and do not wait
  refresh nodegroup scale web -c prod --min 2 --max 12 --yes --no-wait
```

### Options

```
  -c, --cluster string           EKS cluster name (env: REFRESH_CLUSTER; default: the active context; never the kubeconfig)
      --desired int32            Desired number of nodes
  -d, --dry-run                  Print the plan and the verdict of the health gate, and change nothing
      --force                    Proceed past drain blockers and a vCPU quota surge, with a warning
  -h, --help                     help for scale
      --max int32                Maximum number of nodes
      --min int32                Minimum number of nodes
      --no-wait                  Return once AWS accepted the change
  -o, --output format            Output format: table, json, yaml, plain (default table)
      --poll-interval duration   Time between two status polls (default 15s)
  -q, --quiet                    Print no progress; questions, warnings, and the result still show
      --require-healthy          Stop on any health warning, and do not ask
      --skip-health-check        Skip the health gate before the change
      --wait-timeout duration    How long to wait for the scale; 0 means no limit (default 5m0s)
  -y, --yes                      Do not prompt; required with -o json|yaml and without a terminal
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

* [refresh nodegroup](refresh_nodegroup.md)	 - Work with EKS managed nodegroups

