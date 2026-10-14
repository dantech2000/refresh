## refresh cluster rollback

Roll a cluster back to the Kubernetes version before its upgrade

### Synopsis

Roll a cluster back one minor version, within 7 days of an in-place
upgrade. EKS rolls back only the control plane, so the command first rolls
the managed nodegroups back, one after another, then downgrades the add-ons
that the older version cannot run, and then rolls the control plane back.

The plan comes from the live state of the cluster and its update history.
Run the same command again after a run that stopped: it goes on with what
is left. A cluster that was created at its version, and an upgrade that is
more than 7 days old, cannot roll back.

Before any change the health gate runs for every nodegroup that the plan
would roll, and it runs again right before each roll. The control plane
does not roll back while a managed nodegroup is above the target: a
skipped nodegroup and one with a custom AMI are yours to move first.
Self-managed nodes and Fargate pods are not rolled back by EKS or by
refresh.

A phase that fails ends the run with exit code 1. The message names the
phases that completed and the command that goes on from there.

The command asks once, with the whole order in the question. --yes skips
the question, and is required with -o json|yaml and without a terminal.
When the cluster changed while the question was open, the command asks
again about what changed. It refuses, whatever the answer and the flags,
when the version to roll back to is no longer the one that was confirmed.

--skip-insights-check stops the rollback readiness insights from blocking,
and sends force to EKS, which then skips its own insight checks too.
--rollback-timeout is the time that EKS gives the control-plane rollback:
whole minutes from 2h to 168h. 0 leaves the default of EKS.

The cluster is the argument, then REFRESH_CLUSTER, then the active refresh
context. It never comes from the kubeconfig.

Exit codes:
  0  Done, or nothing to do.
  1  An error, a question answered no, or an interrupt.
     A phase failed. The phases before it stay done.
  2  With --dry-run only: a real run would ask about warnings.
  3  Blocked: the cluster is busy, a gate stops the change, or
     --require-healthy and a warning. Nothing changed, or nothing more
     when the gate stops a later step.
     The cluster cannot roll back: no upgrade in the last 7 days, a
     blocking insight, or a nodegroup above the target. When a gate
     stops a later phase, the message names the phases that completed.
  4  Incomplete: a target was already changing, a wait timed out, or
     something could not be read.
  5  A nodegroup was rolled and the verification found an issue, or
     an add-on was downgraded and is not healthy.

```
refresh cluster rollback [cluster] [flags]
```

### Examples

```
  # See what a rollback would do
  refresh cluster rollback prod --dry-run

  # Roll back, after one question
  refresh cluster rollback prod

  # Roll back without a prompt, and give EKS three hours
  refresh cluster rollback prod --yes --rollback-timeout 3h
```

### Options

```
  -d, --dry-run                     Print the plan and the verdict of the health gate, and change nothing
      --force                       Proceed past drain blockers, with a warning
  -h, --help                        help for rollback
  -o, --output format               Output format: table, json, yaml, plain (default table)
      --poll-interval duration      Time between two status polls (default 15s)
  -q, --quiet                       Print no progress; questions, warnings, and the result still show
      --require-healthy             Stop on any health warning, and do not ask
      --rollback-timeout duration   Time that EKS gives the control-plane rollback: whole minutes from 2h to 168h; 0 leaves the default of EKS
      --skip-health-check           Skip the health gate before the change
      --skip-insights-check         Roll back past blocking insights, and send force to EKS (not recommended)
      --skip-nodegroup strings      Leave these nodegroups alone, by exact name
      --skip-verify                 Skip the verification after each nodegroup roll
      --wait-timeout duration       How long to wait for the whole rollback; 0 means no limit (default 4h0m0s)
  -y, --yes                         Do not prompt; required with -o json|yaml and without a terminal
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

* [refresh cluster](refresh_cluster.md)	 - Work with EKS clusters

