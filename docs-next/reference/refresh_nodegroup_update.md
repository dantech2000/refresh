## refresh nodegroup update

Roll nodegroups onto the latest AMI

### Synopsis

Roll managed nodegroups onto the latest recommended AMI of their own
Kubernetes version. EKS replaces the nodes: it drains each one and honours
PodDisruptionBudgets. The Kubernetes version of a nodegroup never changes
here; "cluster upgrade" does that.

With no name, every nodegroup of the cluster is a target. A name is exact.
-f selects by pattern, such as -f name=web-*, and the confirmation lists
the matches. A nodegroup that is on the latest AMI is left alone, unless
--reroll asks for a roll anyway. A nodegroup with a custom AMI is never
rolled: publish a new launch template version for it.

Before any roll the command reads the whole cluster. It refuses while the
cluster or anything else in it is changing, and it leaves a target alone
that is already changing. Then the health gate judges all the targets
together, before the first roll starts: the nodes, the capacity to take
the drained pods, the control plane, the system workloads, and the
PodDisruptionBudgets that would stop a drain. --force goes past a drain
blocker, with a warning, and tells EKS to force the drain. It does nothing
else: it does not roll a nodegroup that is on the latest AMI. Without
Kubernetes access the command asks before it goes on.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It starts every roll,
waits for all of them, and then verifies: the nodegroups are ACTIVE, the
new nodes are Ready, and no pod is newly Pending. --no-wait returns once
AWS accepted the rolls, and --skip-verify leaves the verification out.

--all-clusters rolls every cluster of the regions of -r, one cluster
after another, after one confirmation for all of them. A cluster that is
blocked or fails does not stop the next one.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig. --all-clusters
ignores all three.

Exit codes:
  0  Done, or nothing to do.
  1  An error, a question answered no, or an interrupt.
     Every roll that was sent failed.
  2  With --dry-run only: a real run would ask about warnings.
  3  Blocked: the cluster is busy, a gate stops the change, or
     --require-healthy and a warning. Nothing changed, or nothing more
     when the gate stops a later step.
  4  Incomplete: a target was already changing, a wait timed out, or
     something could not be read.
     A roll failed beside one that was started.
  5  The rolls were applied and the verification found an issue.

```
refresh nodegroup update [nodegroup...] [flags]
```

### Examples

```
  # Roll every nodegroup of a cluster that is behind
  refresh nodegroup update -c prod

  # See what would be rolled, with the AMI release notes
  refresh nodegroup update -c prod --dry-run --changelog

  # Roll two nodegroups without a prompt
  refresh nodegroup update web api -c prod --yes

  # Roll the nodegroups that match a pattern
  refresh nodegroup update -c prod -f name=web-*

  # Roll every cluster of two regions
  refresh nodegroup update --all-clusters -r us-east-1,us-west-2 --yes
```

### Options

```
      --all-clusters             Roll every cluster of the regions of -r, one after another
      --changelog                With --dry-run, show the AMI release notes of each roll
  -c, --cluster string           EKS cluster name (env: REFRESH_CLUSTER; default: the active context; never the kubeconfig)
  -d, --dry-run                  Print the plan and the verdict of the health gate, and change nothing
  -f, --filter stringArray       Select by key=value, where * matches any text; repeatable, and all must match. Keys: name, status, instanceType, amiStatus, version
      --force                    Proceed past drain blockers, with a warning
  -h, --help                     help for update
      --live                     Show the nodes of the roll as they drain and join, also without a terminal
      --no-wait                  Return once AWS accepted the change
  -o, --output format            Output format: table, json, yaml, plain (default table)
      --poll-interval duration   Time between two status polls (default 15s)
  -q, --quiet                    Print no progress; questions, warnings, and the result still show
      --require-healthy          Stop on any health warning, and do not ask
      --reroll                   Also roll a nodegroup that is on the latest AMI
      --skip-health-check        Skip the health gate before the change
      --skip-verify              Skip the verification after each nodegroup roll
      --wait-timeout duration    How long to wait for the rolls of a cluster; 0 means no limit (default 40m0s)
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
  -t, --timeout duration      Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh (env: REFRESH_TIMEOUT) (default 1m0s)
      --verbose               Same as --log-level debug
```

### SEE ALSO

* [refresh nodegroup](refresh_nodegroup.md)	 - Work with EKS managed nodegroups

