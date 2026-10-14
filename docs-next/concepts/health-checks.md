# Health checks

Every changing command runs a set of health checks before it sends a request.
The checks give one verdict, the health gate: go, ask, or block. The gate is on
by default. `--skip-health-check` turns it off.

`cluster describe` and `cluster list --show-health` run the same checks to
report health. They do not change their exit code for it.

## Outcomes

Each check has one outcome. There is no score.

| Outcome | Meaning |
|---|---|
| `Pass` | Nothing to report. |
| `Warn` | Something to look at. The gate asks. |
| `Fail` | A problem. A blocking check blocks; an advisory check asks. |
| `Skipped` | The check did not apply, or its data source does not exist. It never changes the verdict. |
| `Unreadable` | The check could not read what it needs, for example because an IAM action is denied. The run exits `4`. |

## The checks

| Check | Reads | Kind | Needs Kubernetes |
|---|---|---|---|
| [`Nodes`](#nodes) | Nodegroup statuses, node readiness | Blocking | For the real counts |
| [`Capacity`](#capacity) | CloudWatch CPU of the instances | Blocking | No |
| [`ControlPlane`](#controlplane) | CloudWatch metrics of the EKS control plane | Blocking | No |
| [`Workloads`](#workloads) | Pods of the system namespaces | Blocking | Yes |
| [`DrainBlockers`](#drainblockers) | PodDisruptionBudgets, nodes, pods | Blocking, `--force` goes past it | Yes |
| [`PDBCoverage`](#pdbcoverage) | Deployments and PodDisruptionBudgets | Advisory | Yes |
| [`Utilization`](#utilization) | Node metrics from metrics-server | Advisory | Yes |
| [`Quotas`](#quotas) | The EC2 vCPU quota and its usage | Advisory | No |
| `Kubernetes` | Whether the Kubernetes API was reachable | Asks when it was not | |

`cluster upgrade-check`, `cluster upgrade`, and `cluster rollback` also run
[`Insights` and `VersionSkew`](#insights-and-versionskew).

An example from `refresh nodegroup update -c prod-payments-00 --dry-run`:

```text
HEALTH GATE
  decision  ✗ Block
  reasons   DrainBlockers: 1 PDB allow 0 disruptions on the target nodegroups; the roll would stall on eviction

CHECKS  9
  NAME           OUTCOME  MESSAGE
  Nodes          ● Pass   13/13 nodes ready
  Capacity       ● Pass   CPU mean 30%, peak 38%, 13 instances
  ControlPlane   Skipped  control-plane metrics not emitted (EKS 1.28+ on a supported platform version)
  Workloads      ● Pass   28/28 pods running in the system namespaces
  DrainBlockers  ✗ Fail   1 PDB allow 0 disruptions on the target nodegroups; the roll would stall on eviction
  PDBCoverage    ● Pass   every deployment has a PodDisruptionBudget (1)
  Utilization    ● Pass   CPU 38%, memory 33% in use
  Quotas         ● Pass   57% of the vCPU quota in use (146 of 256)
  Kubernetes     ● Pass   Kubernetes API reachable
```

### Which command runs which checks

| Command | Checks |
|---|---|
| `nodegroup update`; each nodegroup roll of `cluster upgrade` and `cluster rollback` | All nine in the table above |
| `nodegroup scale`, when the desired size goes down | `Nodes`, and the scale-down form of `DrainBlockers` |
| `nodegroup scale`, otherwise | `Quotas` |
| `addon update` | The state of the target add-ons |

## The gate

The worst result decides.

| Worst result | Gate |
|---|---|
| A blocking check is `Fail` | Block |
| `Nodes`, `Workloads`, or `DrainBlockers` is `Unreadable` | Block |
| Any `Warn`; an advisory check is `Fail`; any other check is `Unreadable`; no Kubernetes access | Ask |
| Everything is `Pass` or `Skipped` | Go |

The flags change what an "ask" and a drain blocker do:

| Flag | Effect |
|---|---|
| none, on a terminal | The command asks. If you answer no, it exits `1` and changes nothing. |
| `--yes` | An "ask" becomes "go". The result records what was accepted. |
| `--require-healthy` | An "ask" becomes "block", exit `3`. It beats `--yes`. |
| `--force` | A `DrainBlockers` result that is `Fail` or `Unreadable` becomes a warning. `--force` does nothing else. |
| `--skip-health-check` | No check runs. |

A block exits `3`, or `4` when a check was `Unreadable`. Nothing changed.

### When the gate runs

1. Once in the plan, before any change, for every nodegroup that the plan
   would roll. For `cluster upgrade` this is before the control plane moves, so
   a drain blocker is known up front.
2. Again right before each phase that rolls nodes, in `cluster upgrade` and
   `cluster rollback`. If a check got worse since the plan, the command asks
   again. With `--yes` it goes on and records the reasons. With
   `--require-healthy` it stops, exit `3`.

### Dry runs

`--dry-run` runs the same checks and prints the verdict in the plan. Its exit
code says what the real run would do: `0` go, `2` ask, `3` block, `4` a check
could not read.

## Kubernetes access

Four checks read the Kubernetes API, and `Nodes` reads it for the real node
counts. See [Kubeconfig](configuration.md#kubeconfig) for how `refresh` finds
the cluster.

- If no kubeconfig context points at the cluster, or the API does not connect,
  the `Kubernetes` result is `Warn`. The gate asks. `--yes` accepts and the
  result records it. `--require-healthy` blocks. A dry run exits `2`.
- If the API answers 401, 403, or 5xx, the checks that need it are
  `Unreadable`. That blocks for `Nodes`, `Workloads`, and `DrainBlockers`.

EKS still honours PodDisruptionBudgets during a managed nodegroup roll, with
or without the checks.

## Nodes

With Kubernetes access, the check counts the nodes by their `Ready` condition.
A node that is less than 10 minutes old and not yet ready counts as joining.

| Case | Outcome |
|---|---|
| A nodegroup that the change targets is not `ACTIVE` | `Fail` |
| Every node is ready and every nodegroup is healthy | `Pass` |
| No node is ready | `Fail` |
| Fewer than half of the settled nodes are ready | `Fail` |
| Some nodes are not ready, or a nodegroup is degraded | `Warn` |
| The node list fails | `Unreadable` |

Without Kubernetes access, the check estimates: an `ACTIVE` nodegroup counts
its desired size as ready. The message ends in "(estimated)".

## Capacity

The check reads the CPU utilization of the nodegroup instances from CloudWatch
over the last 10 minutes. A roll drains nodes, so the other nodes need room.

| Case | Outcome |
|---|---|
| Mean CPU headroom of 30 percent or more | `Pass` |
| Headroom from 15 to 30 percent, or a peak of 85 percent or more | `Warn` |
| Headroom under 15 percent, or a peak of 95 percent or more | `Fail` |
| No instance reported CPU metrics | `Warn` |
| No managed nodegroup instance | `Skipped` |
| `cloudwatch:GetMetricData` is denied | `Unreadable`; the gate asks |

## ControlPlane

The check reads the EKS control-plane metrics from CloudWatch over the last 15
minutes.

| Case | Outcome |
|---|---|
| etcd database at 95 percent or more of 8 GiB | `Fail` |
| etcd database at 80 percent or more | `Warn` |
| 1,000 or more API requests, with 2 percent or more 5xx or 1 percent or more 429 | `Warn` |
| The cluster emits no control-plane metrics | `Skipped` |
| The read fails | `Unreadable`; the gate asks |

## Workloads

The check lists the pods of `kube-system`, `kube-public`, and
`kube-node-lease`.

| Case | Outcome |
|---|---|
| Every pod is running with every container ready | `Pass` |
| 90 percent or more are running and ready | `Warn` |
| Fewer than 90 percent | `Fail` |
| No pod at all | `Warn` |
| A pod list fails | `Unreadable`; the gate blocks |

## DrainBlockers

A drain evicts pods. The eviction API refuses an eviction that would take a
PodDisruptionBudget (PDB) below what it allows. The roll then stalls. This
check finds those cases before the roll starts.

A PDB is a blocker when it allows zero disruptions and it covers a pod on a
node that the change would drain. A pod that more than one PDB selects is also
a blocker, because the eviction API refuses it.

| Case | Outcome |
|---|---|
| A blocker | `Fail`, with one line for each blocker |
| No blocker | `Pass` |
| The target nodegroups have no nodes and a desired size of 0 | `Pass`: nothing to drain |
| The PDB list, the node list, or a pod list fails | `Unreadable`; the gate blocks |

If the check blocks:

1. Read the blockers in the `CHECKS` section.
2. Fix the PDB or scale the workload so that the PDB allows a disruption.
3. Run the command again.

To go past the blockers, pass `--force`. The command prints a warning and
tells EKS to force the drain.

### Scale-down

EKS does not wait for PDBs when `nodegroup scale` lowers the desired size. So
the check uses a stricter rule for a scale-down: it takes the nodes with the
most covered pods, as many as the scale removes, and fails if a PDB could lose
more pods than it allows.

```text
HEALTH GATE
  decision  ✗ Block
  reasons   DrainBlockers: removing 2 nodes from ng-b could evict more pods than PDB default/api allows
```

## PDBCoverage

The check warns when a deployment outside the system namespaces has no PDB. It
names up to five. It never blocks.

## Utilization

The check reads node CPU and memory from metrics-server.

| Case | Outcome |
|---|---|
| CPU headroom under 15 percent, or memory headroom under 10 percent | `Fail` (advisory: the gate asks) |
| CPU headroom under 30 percent, or memory headroom under 20 percent | `Warn` |
| metrics-server is not installed | `Skipped` |

## Quotas

The check compares the usage of the EC2 On-Demand Standard vCPU quota with its
limit. A roll starts new instances before it removes the old ones.

| Case | Outcome |
|---|---|
| Usage at 95 percent or more | `Fail` (advisory: the gate asks) |
| Usage at 85 percent or more | `Warn` |
| The usage metric is not reported | `Skipped` |

## Insights and VersionSkew

These two checks decide upgrade readiness. `cluster upgrade-check` reports
them, and `cluster upgrade` blocks on them.

| Check | Case | Outcome |
|---|---|---|
| `Insights` | An EKS insight is `ERROR` or `UNKNOWN` | `Fail` |
| | An insight is `WARNING` | `Warn` |
| | EKS has not evaluated the insights yet | `Warn` in `upgrade-check`; `Fail` in `cluster upgrade` |
| `VersionSkew` | A nodegroup is 3 or more minor versions behind the control plane | `Fail` |
| | A nodegroup or an add-on is behind | `Warn` |

## Verification after a roll

After a roll, the command verifies the result. `--skip-verify` leaves this out.

| Check | Issue (exit `5`) | Could not read (exit `4`) |
|---|---|---|
| `NodegroupActive` | A rolled nodegroup is not `ACTIVE` | The nodegroup could not be read |
| `NodesReady` | A new node is not `Ready` after the wait | The node list fails |
| `NewPendingPods` | A pod is `Pending` that was not `Pending` before the roll | The pod list fails |

`NodesReady` waits for the new nodes: at most 2 minutes, and never past the
time left of `--wait-timeout`. Without Kubernetes access, `NodesReady` and
`NewPendingPods` are `Skipped`.

```text
VERIFICATION  3
  NAME             OUTCOME  MESSAGE
  NodegroupActive  ● Pass   ng-c ACTIVE
  NodesReady       ● Pass   5 new nodes ready
  NewPendingPods   ● Pass   no new Pending pods
```
