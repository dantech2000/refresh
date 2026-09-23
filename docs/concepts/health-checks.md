# Pre-flight health checks

Before `refresh` rolls nodes, it checks that the cluster can absorb the roll.
The same checks run in these places:

- `nodegroup update`, before each roll (`--health-only` runs only the checks,
  and `--skip-health-check` turns them off).
- `cluster upgrade`, before each nodegroup roll (`--skip-health-check` turns
  them off).
- `nodegroup scale --health-check`, before and after the scaling change.

`nodegroup scale --check-pdbs` uses a separate PDB gate for scale-downs. See
[Scale-down PDB gate](#scale-down-pdb-gate) below.

## The checks

| Check | Needs | Level | What fails it |
|---|---|---|---|
| Node Health | EKS (Kubernetes API for real counts) | Blocking | No Ready nodes, or fewer than 50% Ready (see below) |
| Cluster Capacity | CloudWatch EC2 CPU metrics | Blocking | Too little CPU headroom, or a node near CPU saturation |
| Control Plane | CloudWatch control-plane metrics (EKS 1.28+) | Blocking | etcd near its 8 GiB limit |
| Critical Workloads | Kubernetes API | Blocking | Pods in `kube-system`, `kube-public`, or `kube-node-lease` that are not Running and Ready |
| Pod Disruption Budgets | Kubernetes API | Warning | PDBs or pods that would block a node drain (see below) |
| Node Utilization | Kubernetes metrics API | Warning | Too little CPU or memory headroom to absorb a drain |
| Service Quotas | Service Quotas, CloudWatch usage metrics | Warning | On-Demand EC2 vCPU quota close to its limit |
| Resource Balance | CloudWatch EC2 CPU metrics | Warning | Uneven CPU load across nodes |

The checks give one decision:

- `BLOCK`: a blocking check failed. `nodegroup update` exits `3` and rolls
  nothing. `cluster upgrade` stops the nodegroup phase.
- `WARN`: a check warned, or a non-blocking check failed. On a terminal,
  `refresh` asks you to confirm. Without a terminal, with `-o json`/`-o yaml`,
  or with `nodegroup update --quiet`, the run stops unless you pass `--yes`.
  With `nodegroup update --require-healthy`, a warning is a hard stop
  (exit `2`).
- `PROCEED`: nothing to report.

A check that cannot run (no Kubernetes access, no metrics) is marked as
skipped. A skipped check does not change the score or the decision. The
error for `BLOCK` or `WARN` names the checks that caused it.

## Node Health

When `refresh` can read the cluster's Kubernetes API, it counts Ready nodes:

- Fewer than 50% of the nodes Ready fails the check, so the roll is blocked.
- Some NotReady nodes, but at least 50% Ready, is a warning.
- A NotReady node that was created in the last 10 minutes, and whose kubelet
  is still starting, counts as joining. Joining nodes only warn, and they are
  left out of the 50% rule. So the check after a scale-up does not fail
  before the new nodes report Ready.

Without Kubernetes access, `refresh` estimates readiness from the desired
size of the `ACTIVE` nodegroups. The estimate is a warning at most. It fails
only when no node is Ready.

## Critical Workloads

A `Failed` pod that eviction or a graceful node shutdown left behind does not
fail the check. Such pods stay until garbage collection and say nothing about
their replacements. For the `Terminated` reason (kubelet 1.22 and later),
`refresh` skips the pod only when the `DisruptionTarget` condition or the
kubelet shutdown message confirms the shutdown.

## Pod Disruption Budgets

A node roll drains each old node through the eviction API. The check reports
what the eviction API would refuse:

- A PDB that allows 0 disruptions and covers pods.
- A PDB whose status is not synced (`SyncFailed`, or an
  `observedGeneration` older than its `generation`) and that allows 0
  disruptions. The controller then reports 0 expected pods, but eviction is
  still refused.
- A pod that more than one PDB selects. The eviction API refuses such a pod,
  whatever each PDB allows.

Pods that eviction does not gate are left out: `Succeeded`, `Failed`,
`Pending`, and deleting pods, and not-Ready pods under
`unhealthyPodEvictionPolicy: AlwaysAllow` (or `IfHealthyBudget` while the
budget is met). `kube-system` PDBs count too: a stuck `coredns` PDB blocks a
drain like any other.

The check is scoped to the nodegroups that will roll. A PDB is reported only
if one of its pods runs on a node with the label
`eks.amazonaws.com/nodegroup=<target>`. If `refresh` cannot find those nodes
(the node list fails, or no node has the label), it reports every such PDB
in the cluster as one that "may block a drain". If the target nodegroups are
scaled to 0, nothing is drained, so nothing blocks.

In `nodegroup update` a drain blocker is a warning. In `cluster upgrade` a
drain blocker stops the nodegroup roll before EKS is asked to roll anything.
With `--force` it is only a warning, because `--force` tells EKS to evict
through PDBs.

## Scale-down PDB gate

EKS does not honor PDBs when a scaling change removes nodes. The Auto Scaling
group picks the nodes and terminates them. With `--check-pdbs`,
`nodegroup scale` refuses a scale-down (exit `1`, before any change) when the
removed nodes could hold more of a PDB's pods than the PDB allows. The gate
assumes the worst case: the removed nodes are the ones that hold the most of
the PDB's pods. The error shows the numbers for each PDB. Only `--desired`
changes the node count: `nodegroup scale` refuses a `--max` below, or a
`--min` above, the current desired size unless you also pass `--desired`.

The gate fails closed. If it cannot read PDBs or pods (no Kubernetes access,
a failed list), it refuses the scale-down. `--force` scales down anyway and
prints the blockers to stderr. `--dry-run --check-pdbs` prints the verdict and
the PDBs that affect this nodegroup, including system namespaces.

## Kubernetes access

The Kubernetes-backed checks use the kubeconfig context whose API server
matches the target cluster's endpoint. See
[Matching the kubeconfig to the target cluster](configuration.md#matching-the-kubeconfig-to-the-target-cluster).
The checks need `list` on nodes, pods, namespaces, deployments, and
PodDisruptionBudgets, and the metrics API for Node Utilization.
