## refresh cluster upgrade

Upgrade a cluster to a Kubernetes version

### Synopsis

Upgrade a cluster to a Kubernetes version, one minor version at a time.
For each minor version the command moves the control plane, then updates
the add-ons that the new control plane cannot run, then rolls every managed
nodegroup, one after another, and then updates the other add-ons.
Self-managed nodes, hybrid nodes, and Fargate pods are not upgraded by EKS
or by refresh: move them yourself after the control plane.

The plan comes from the live state of the cluster, and there is no state
file. Run the same command again after a run that stopped: it goes on with
what is left. --to with the version that the control plane already runs
only catches up the add-ons and the nodegroups.

Before any change the command reads the whole cluster and judges it. It
asks EKS to refresh the cluster insights and waits for that, up to 5
minutes: an ERROR or an UNKNOWN insight for the next version blocks, and
so does a nodegroup that the new version would put past the kubelet skew.
That refresh is the one request that a blocked run sends. A dry run sends
none. The health gate then runs for every nodegroup that the plan would
roll, so a PodDisruptionBudget that would stop a drain is known before the
control plane moves. Each gate runs again right before its own phase.

A phase that fails ends the run with exit code 1. The message names the
phases that completed and the command that goes on from there. The command
refuses while someone else changes the cluster, and it waits for a change
of its own that is still running.

On a terminal the command asks before each phase. --yes skips the
questions, and is required with -o json|yaml and without a terminal.
--wait-timeout bounds the whole run, and a question does not count.

--only limits the run to the parts that it names: control-plane, addons,
nodegroups. -n rolls only the nodegroups that it names. --skip-addon and
--skip-nodegroup leave something alone. A name is exact.

The cluster is the argument, then REFRESH_CLUSTER, then the active refresh
context. It never comes from the kubeconfig.

Exit codes:
  0  Done, or nothing to do.
  1  An error, a question answered no, or an interrupt.
     A phase failed. The phases before it stay done.
  2  With --dry-run only: a real run would ask about warnings.
  3  Blocked: the cluster is busy, every target is already changing, a
     gate stops the change, or --require-healthy and a warning. Nothing
     changed, or nothing more when the gate stops a later step.
     The insights or the kubelet skew block the next version. When a
     gate stops a later phase, the message names the phases that
     completed.
  4  Incomplete: a target was already changing beside targets that
     changed, a wait timed out, or something could not be read.
  5  A nodegroup was rolled and the verification found an issue, or
     an add-on was updated and is not healthy.

```
refresh cluster upgrade [cluster] --to <version> [flags]
```

### Examples

```
  # See what an upgrade would do
  refresh cluster upgrade prod --to 1.33 --dry-run

  # Upgrade, with a question before each phase
  refresh cluster upgrade prod --to 1.33

  # Upgrade two minor versions without a prompt
  refresh cluster upgrade prod --to 1.34 --yes

  # Move only the control plane
  refresh cluster upgrade prod --to 1.33 --only control-plane

  # Roll the nodegroups later, and leave one alone
  refresh cluster upgrade prod --to 1.33 --only nodegroups --skip-nodegroup gpu
```

### Options

```
  -d, --dry-run                  Print the plan and the verdict of the health gate, and change nothing
      --force                    Proceed past drain blockers and a vCPU quota surge, with a warning
  -h, --help                     help for upgrade
  -n, --nodegroup strings        Roll only these nodegroups, by exact name
      --only strings             Limit the run to these parts: control-plane, addons, nodegroups
  -o, --output format            Output format: table, json, yaml, plain (default table)
      --poll-interval duration   Time between two status polls (default 15s)
  -q, --quiet                    Print no progress; questions, warnings, and the result still show
      --require-healthy          Stop on any health warning, and do not ask
      --skip-addon strings       Leave these add-ons alone, by exact name
      --skip-health-check        Skip the health gate before the change
      --skip-insights-check      Do not refresh or read the cluster insights (not recommended)
      --skip-nodegroup strings   Leave these nodegroups alone, by exact name
      --skip-verify              Skip the verification after each nodegroup roll
      --to string                Kubernetes version to reach, such as 1.33 (required)
      --wait-timeout duration    How long to wait for the whole upgrade; 0 means no limit (default 4h0m0s)
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

* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters

