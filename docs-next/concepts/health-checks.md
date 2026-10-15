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
| [`Quotas`](#quotas) | The EC2 vCPU quota, its usage, and the nodes that the change launches | Blocking on a likely surge, `--force` goes past it; advisory on current use | No |
| `Kubernetes` | Whether the Kubernetes API was reachable | Asks when it was not | |

`cluster upgrade-check`, `cluster upgrade`, and `cluster rollback` also run
[`Insights` and `VersionSkew`](#insights-and-versionskew).

An example from `refresh nodegroup update -c prod-payments-00 --dry-run`:

```text
HEALTH GATE
  decision  ✗ Block
  reasons   DrainBlockers: PDB app/web allows 0 disruptions on the target nodegroups; the roll would stall on eviction

CHECKS  9
  NAME           OUTCOME  MESSAGE
  Nodes          ● Pass   13/13 nodes ready
  Capacity       ● Pass   CPU mean 30%, peak 38%, 13 instances
  ControlPlane   Skipped  control-plane metrics not emitted (EKS 1.28+ on a supported platform version)
  Workloads      ● Pass   28/28 pods running in the system namespaces
  DrainBlockers  ✗ Fail   PDB app/web allows 0 disruptions on the target nodegroups; the roll would stall on eviction
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
| `nodegroup scale`, otherwise | `Quotas`, with the nodes that the scale adds |
| `addon update` | The state of the target add-ons |

## The gate

The worst result decides.

| Worst result | Gate |
|---|---|
| A blocking check is `Fail` | Block |
| `Nodes`, `Workloads`, or `DrainBlockers` is `Unreadable` | Block |
| Any `Warn`; an advisory check is `Fail`; any other check is `Unreadable`; no Kubernetes access | Ask |
| Everything is `Pass` or `Skipped` | Go |

The flags change what an "ask", a drain blocker, and a quota surge do:

| Flag | Effect |
|---|---|
| none, on a terminal | The command asks. If you answer no, it exits `1` and changes nothing. |
| `--yes` | An "ask" becomes "go". The result records what was accepted. |
| `--require-healthy` | An "ask" becomes "block", exit `3`. It beats `--yes`. |
| `--force` | A `DrainBlockers` result that is `Fail` or `Unreadable`, and a `Quotas` result that is `Fail` because of a likely surge, become warnings. `--force` does nothing else. |
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
- If the API connects but answers no read in time, the checks that need it
  are `Unreadable`, and the `Kubernetes` result is `Warn`: the API did not
  answer.

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
limit. A roll starts new instances before it removes the old ones, so the
check also counts the nodes that the change launches.

The usage is the newest minute of the `AWS/Usage` CloudWatch metric. On a new
account, or in a region where the account has run few instances, the metric
has no data points for a while. Then the check reads the usage from EC2:

1. It lists the pending and running instances (`ec2:DescribeInstances`).
2. It keeps the On-Demand instances of the Standard families (A, C, D, H, I,
   M, R, T, Z). Spot instances and other families count against other quotas.
3. It adds up their vCPUs, from `ec2:DescribeInstanceTypes`.

The details say that the usage came from the instance list. The list stops
when the sum passes the quota, because the result is then `Fail`. The details
then give the sum as "at least".

CloudWatch publishes the metric late. Right after a roll, its newest data
point can still count a surge node that has stopped. So when the newest data
point is more than two minutes old, the check also sums the instance list, in
the same way, and uses the smaller of the two numbers. The metric only lags:
it never counts fewer vCPUs than the instances that run. The list stops when
its sum passes the metric, because the metric then decides. The details name
the source that decided. If the instance list cannot be read, the metric
decides, and the details say so.

For a roll, EKS raises the Auto Scaling group of each nodegroup by the larger
of two numbers: the number of its Availability Zones, and `maxUnavailable`. It
documents a maximum of twice the number of zones, or `maxUnavailable` when that
is larger. See [Understand each phase of node
updates](https://docs.aws.amazon.com/eks/latest/userguide/managed-node-update-behavior.html).
The check counts this rule for each target nodegroup:

- The zones are the Availability Zones of the nodegroup's Auto Scaling groups.
  When they are not known, each subnet of the nodegroup counts as one zone.
- `maxUnavailable` is the count of the update config. A percentage is a share
  of the desired size, rounded up. Without an update config it is 1, as EKS
  sets it.
- Each node counts with the vCPUs of its instance type. A nodegroup with
  several instance types counts the largest, and says so.
- A nodegroup with the `MINIMAL` update strategy launches no extra node: it
  removes old nodes before it starts new ones.
- Spot nodegroups, and instance types outside the Standard families (A, C, D,
  H, I, M, R, T, Z), count against other quotas, and the check leaves them out.

`nodegroup update` rolls its targets at once, so their nodes add up.
`cluster upgrade` and `cluster rollback` roll one nodegroup after another, so
the largest roll counts. For `nodegroup scale`, the new nodes are the new
desired size minus the current one.

| Case | Outcome |
|---|---|
| Usage plus the likely surge is over the quota | `Fail` (blocking: exit `3`, nothing is sent; `--force` goes past it with a warning) |
| Usage plus the documented maximum is over the quota | `Warn` |
| Usage at 95 percent or more | `Fail` (advisory: the gate asks) |
| Usage at 85 percent or more | `Warn` |
| The quota is not reported | `Skipped` |
| The usage metric has no data points, and the instance list cannot be read | `Skipped` |
| The quota, the usage metric, or the vCPUs of an instance type cannot be read | `Unreadable` |

The worse of the two rules decides. Only a surge blocks: a roll or a scale-up
that does not fit the quota would stall on `VcpuLimitExceeded`. The rules of
current use only ask. A failed roll names the nodegroup, the numbers, and the
fix:

```text
Quotas  ✗ Fail  rolling ng-a may launch 3 to 6 t3.medium nodes (6 to 12 vCPU) and 4 of 8 vCPU are in use: request a higher EC2 On-Demand vCPU quota, or set the update strategy to MINIMAL
```

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
