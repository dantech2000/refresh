## refresh addon update

Update add-ons of a cluster

### Synopsis

Update EKS add-ons to the latest version that the cluster can run, or one
add-on to the version of --version. Names are exact.

With --all, every installed add-on is updated, one after another in
dependency order: vpc-cni before coredns and kube-proxy, and those before
the storage drivers. A failed add-on does not stop the others.

Before any change the command reads the whole cluster. It refuses while
the cluster or anything else in it is changing. An add-on that is DEGRADED,
or whose last change failed, is updated to repair it, after a question. An
add-on that is already at its target is left alone, and so is one that is
already changing, unless it is changing to the version this run wants: the
command then waits for that update and sends none.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It waits for each
update, for --wait-timeout each; --no-wait returns once AWS accepted them.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig.

Exit codes:
  0  Done, or nothing to do.
  1  An error, a question answered no, or an interrupt.
     Every update that was sent failed.
  2  With --dry-run only: a real run would ask about warnings.
  3  Blocked, and nothing changed: the cluster is busy, a gate stops the
     change, or --require-healthy and a warning.
  4  Incomplete: a target was already changing, a wait timed out, or
     something could not be read.
     An add-on failed and another was updated.
  5  An add-on was updated and is not healthy.

```
refresh addon update [addon...] [flags]
```

### Examples

```
  # Update one add-on to the latest version
  refresh addon update vpc-cni -c prod

  # See what would change
  refresh addon update --all -c prod --dry-run

  # Update every add-on but one, without a prompt
  refresh addon update --all --skip-addon coredns -c prod --yes

  # Move one add-on to a version, and do not wait for it
  refresh addon update coredns --version v1.11.4-eksbuild.2 -c prod --yes --no-wait
```

### Options

```
      --all                      Update every installed add-on, in dependency order
  -c, --cluster string           EKS cluster name (env: REFRESH_CLUSTER; default: the active context; never the kubeconfig)
  -d, --dry-run                  Print the plan and the verdict of the health gate, and change nothing
  -h, --help                     help for update
      --no-wait                  Return once AWS accepted the change
  -o, --output format            Output format: table, json, yaml, plain (default table)
      --poll-interval duration   Time between two status polls (default 15s)
  -q, --quiet                    Print no progress; questions, warnings, and the result still show
      --require-healthy          Stop on any health warning, and do not ask
      --skip-addon strings       Add-on that --all leaves out; repeat it or separate the names with commas
      --skip-health-check        Skip the health gate before the change
      --version string           Add-on version to move to, with exactly one add-on (default: the latest that the cluster can run)
      --wait-timeout duration    How long to wait for each add-on; 0 means no limit (default 5m0s)
  -y, --yes                      Do not prompt; required with -o json|yaml and without a terminal
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

* [refresh addon](refresh_addon.md)	 - Work with EKS add-ons

