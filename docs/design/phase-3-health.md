# Phase 3 design note: health

Status: draft 1, for review. It belongs to `REWRITE.md` and stays inside `CONTRACT.md`. Section 12 lists the contract edits it proposes. Section 13 is empty until code changes the design.

Scope: `internal/health` (checks, `Gate`, drain blockers, post-roll verification), the production `internal/kube` package, the Kubernetes groups of `fleet.Snapshot` that phase 2 left `NotRequested`, the command `cluster upgrade-check`, the `HEALTH` section of `cluster describe`, `cluster list --show-health`, `cluster describe --check-readiness`, `nodegroup list --check-readiness`, and `nodegroup describe --show-workloads`.

Out of scope: every changing command and the TUI. The gate API they call is fixed here, with the verdict that `--dry-run` reports. Phase 4 builds the commands.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | A check is a struct value with a `Run` function, listed in a table. No interface, no setters. |
| 2 | Five outcomes: `Pass`, `Warn`, `Fail`, `Skipped`, `Unreadable`. No score. "Could not read" has one form: `Unreadable` with a `Failure`. |
| 3 | The runner, not the check, decides `Skipped`: a check declares what it needs, and the runner skips it when the input lacks it. |
| 4 | One `Gate(results, policy) Verdict` for every changing command and the TUI. A front end calls its confirm function on `Ask` and nothing else. |
| 5 | A drain blocker is a blocking check that `--force` can override. Unreadable PDBs block as a blocker does. |
| 6 | Resource Balance is dropped. Its CPU numbers were the Capacity numbers counted again. |
| 7 | Insights and version skew are checks too, so `cluster upgrade-check` and the `cluster upgrade` readiness gate run one engine. |
| 8 | Reads are shared: one node list, one PDB list, one `GetMetricData` for every metric of a run. The gate is 2 round trips after the snapshot; `upgrade-check` is 2, or 3 with a rollback window. |
| 9 | `internal/kube` returns values and typed errors, writes nothing, and makes no probe call. |
| 10 | Post-roll verification is a check set with the same types. Node readiness is read. |

## 1. Types

### Check, Input, Result

```go
package health

// Check is one health check. The checks are a closed list in this package,
// so a struct with a function is enough: a table of values, as cobra and
// testing use, and nothing to vary between implementations. A test builds
// a Check with its own Run.
type Check struct {
	Name string
	// Blocking says that a Fail blocks the change. A Fail of a check that is
	// not blocking is a warning.
	Blocking bool
	// Forceable says that --force turns a Fail or an Unreadable of this
	// check into a Warn. Only DrainBlockers sets it.
	Forceable bool
	Needs     Needs
	Run       func(ctx context.Context, in *Input) Result
}

// Needs says what a check reads. The runner skips a check whose input lacks
// one of them, with the reason from the input, and the check never runs.
type Needs struct {
	Kube    bool // in.Kube
	Metrics bool // in.Metrics, the metrics.k8s.io client
}

// Input is everything the checks read. The command builds it once per run.
// No field is set after Run starts.
type Input struct {
	// Snapshot is the cluster as the command read it, with Need.Nodegroups
	// and Need.InstanceIDs for the Nodes and Capacity checks.
	Snapshot *fleet.Snapshot
	// Targets names the nodegroups about to roll. Empty means the whole
	// cluster: DrainBlockers then reports every blocker in the cluster.
	Targets []string
	// Clients are the AWS clients of the cluster's region.
	Clients *awsx.Clients
	// Kube is nil when there is no Kubernetes access. NoKube then says why,
	// in the words of the Skipped message, such as "no kubeconfig context
	// points at prod".
	Kube   kubernetes.Interface
	NoKube string
	// Metrics is nil without a metrics.k8s.io client.
	Metrics MetricsLister
	Now     time.Time

	reads *reads // the shared, memoized reads of this run; see section 3
}

// MetricsLister is the part of the metrics client the Utilization check
// uses. The metrics clientset's NodeMetricses() satisfies it.
type MetricsLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*metricsv1beta1.NodeMetricsList, error)
}

type Outcome string

const (
	Pass       Outcome = "Pass"
	Warn       Outcome = "Warn"
	Fail       Outcome = "Fail"
	Skipped    Outcome = "Skipped"    // a need is missing, or the check does not apply
	Unreadable Outcome = "Unreadable" // a read the verdict needs failed; Failures names it
)

// Result is what one check found. The runner fills Name and Blocking from
// the Check, so a check cannot contradict its own declaration.
type Result struct {
	Name     string
	Outcome  Outcome
	Blocking bool
	// Message is one line. For Skipped it is the reason. For Unreadable it
	// says what was read and what was not.
	Message string
	Details []string
	// Failures are the reads that failed, as apidoc.Failure. Non-empty for
	// Unreadable, and for a Fail whose reads partly failed.
	Failures []apidoc.Failure
}
```

Rules for the outcome of a check:

| Case | Outcome |
|---|---|
| Every read answered, the rule finds nothing | `Pass` |
| The rule finds a warning, or a Fail of a non-blocking check | `Warn` or `Fail` as the rule says; the gate treats both as a warning |
| The rule finds a failure | `Fail` |
| A need of the check is nil in the input | `Skipped`, by the runner |
| The check does not apply to the cluster (no managed nodegroup and no Kubernetes, a control plane that emits no metrics) | `Skipped`, by the check |
| A read failed and nothing of it was read | `Unreadable`, with the failure |
| A read partly failed and what was read gives `Pass` or `Warn` | `Unreadable`, with the failures; the message says what was read |
| A read partly failed and what was read gives `Fail` | `Fail`, with the failures |

The last three rows replace the old five forms of "could not read": a Warn with score 60, a Pass with a failure list, a Skipped with the error in the message, a nil summary, and "No critical pods found". A check never reports a count it did not read.

`Skipped` and `Unreadable` differ by cause, not by effect on one check: a missing client is `Skipped`, a client that exists and fails is `Unreadable`. The gate ignores the first and blocks on the second.

### Runner

```go
// Run runs checks against in, in parallel, and returns one Result per check
// in the order of checks. A check whose Needs the input lacks returns
// Skipped with in.NoKube or "no metrics client" as its message. A check
// whose context ends returns Unreadable with reason Interrupted or Timeout.
func Run(ctx context.Context, checks []Check, in *Input) []Result

// Preflight returns the checks a change runs before it starts, in display
// order: Nodes, Capacity, ControlPlane, Workloads, DrainBlockers,
// PDBCoverage, Utilization, Quotas.
func Preflight() []Check

// AWSOnly returns the checks of Preflight that need no Kubernetes client,
// for cluster list --show-health.
func AWSOnly() []Check
```

`Run` fans out with `fanout.ForEach` and no limit of its own. The request limit of `awsx` bounds the AWS calls, and the Kubernetes reads are at most two waves (section 3).

### Documents

```go
package apidoc

type HealthOutcome string // Pass, Warn, Fail, Skipped, Unreadable; Values() for the schema

type HealthResult struct {
	Name     string        `json:"name"`
	Outcome  HealthOutcome `json:"outcome"`
	Blocking bool          `json:"blocking"`
	Message  string        `json:"message"`
	Details  []string      `json:"details,omitzero"`
}

// Health is the health part of ClusterDescription. Summary is the worst
// outcome of the checks, in the order Unreadable, Fail, Warn, Pass; a
// Skipped check does not count, and a run of Skipped checks only is Pass.
type Health struct {
	Summary HealthOutcome  `json:"summary"`
	Checks  []HealthResult `json:"checks"`
}

type GateDecision string // Go, Ask, Block

// Gate is the verdict of the health gate in a Plan or a Run (phase 4).
type Gate struct {
	Decision GateDecision   `json:"decision"`
	Reasons  []string       `json:"reasons,omitzero"` // "Nodes: 2/6 nodes ready, ..."
	Checks   []HealthResult `json:"checks"`
}

// ClusterDescription gains:
Health *Health `json:"health,omitzero"`
```

The failures of the checks go in the document's top-level `failures`, as every other read does. A check's failure has `Kind: Cluster` or `Nodegroup`, `Operation` set to the IAM action for an AWS read and `""` for a Kubernetes read, and `Cluster` and `Region` set by the runner.

The `UpgradeCheck` and `InsightDescription` documents are in section 7.

## 2. The checks

Every old check, with its fate. Thresholds are the old ones. A threshold marked "arbitrary" has no source in the old code or the docs.

| Old check | Fate | Reason |
|---|---|---|
| Node Health | Keep as `Nodes` | The blocking signal for a roll |
| Cluster Capacity | Keep as `Capacity`; absorbs the spread line of Resource Balance | One CloudWatch read, one verdict |
| Resource Balance | Drop | Its `maxUtilization > 90 / > 80` rule (`balance.go:66,74`) is the peak rule of Capacity (`capacity.go:32-35`) with other numbers, over the same data. The CPU of the cluster was counted three times: the Capacity mean, the Capacity peak, and the Balance max. The std-dev line stays as a detail of Capacity, with no effect on the outcome. |
| Control Plane | Keep as `ControlPlane` | The etcd limit is a real blocker |
| Service Quotas | Keep as `Quotas` | A roll surges nodes against the vCPU quota |
| Critical Workloads | Keep as `Workloads` | |
| Pod Disruption Budgets | Split into `DrainBlockers` (blocking, forceable) and `PDBCoverage` (advisory) | The old check reported a drain blocker as a warning, and then the command ran `DrainBlockers` again to refuse. Two paths, two exit codes. |
| Node Utilization | Keep as `Utilization` | The only memory signal |
| (none) | Add `Insights` and `VersionSkew` | The readiness parts of `cluster upgrade-check` and the `cluster upgrade` gate, as checks, so one engine decides |

### Nodes (blocking)

Inputs: `Snapshot.Nodegroups` (no call; the command read them). With Kubernetes: one `list nodes`, no selector.

| Case | Rule | Outcome |
|---|---|---|
| Kubernetes read | `ready`, `joining`, `notReady` from the Ready condition. Joining: created less than 10 minutes ago (`nodes.go:302`) and the Ready reason is `KubeletNotReady` or `NodeStatusNeverUpdated`, or no Ready condition. | |
| | No node at all | `Fail`: "no nodes found" |
| | No problem node | `Pass` |
| | `ready == 0` and `settled > 0` (`settled = total - joining`) | `Fail` |
| | `ready == 0`, all others joining | `Warn` |
| | `ready * 100 < settled * 50` (`nodes.go:265`) | `Fail` |
| | Otherwise | `Warn`, naming the NotReady and joining nodes |
| No Kubernetes (estimate) | `ACTIVE` nodegroups count their desired size as ready; `CREATING` and `UPDATING` are left out; `DEGRADED` and any other status count as problems. The estimate never applies the 50 percent rule (`nodes.go:196`). | |
| | No managed capacity, no problem | `Skipped`: "no managed nodegroup capacity; node readiness needs Kubernetes access" |
| | Every nodegroup mid-change, nothing wrong | `Warn` |
| | A problem nodegroup, some ready | `Warn` |
| | No ready, a problem | `Fail` |
| | No problem | `Pass`, message ends in "(estimated)" |
| `Have.Nodegroups` is `Failed` or `Partial` | | `Unreadable`, with the snapshot's nodegroup failures |
| The node list fails with a client present | | `Unreadable`. The estimate is for "no access", not for a failed read. |

The old code warned on a transient EKS error and blocked on a permanent one (`nodes.go:180-190`). The retry layer handles transient errors before a check sees one, so the check has one rule. The 90 cap of the estimate (`nodes.go:270`) goes with the score.

### Capacity (blocking)

Inputs: `Nodegroup.InstanceIDs` of every nodegroup (section 3). One `GetMetricData` per 500 queries: `AWS/EC2 CPUUtilization` per instance, period 300 s, stat Average, over the last 10 minutes (`capacity.go:157-158,175`), paged with `awsx.ListAll`. The queries ride in the shared metrics call of the run.

| Case | Outcome |
|---|---|
| No instance ID in the snapshot (no managed nodegroup, or none has an Auto Scaling group) | `Skipped`: "no managed nodegroup instances" |
| `GetMetricData` fails | `Unreadable` |
| No instance has a datapoint | `Warn`: "no CPU metrics reported for the instances in the last 10 minutes". A read that answered with nothing is not a failure, and an unmeasured cluster deserves a question. The parity scenarios `nodegroup-update-warnings-unattended` and `-require-healthy` pin this. |
| Headroom `100 - mean >= 30` (`capacity.go:23`) | `Pass` |
| Headroom `>= 15` (`capacity.go:24`) | `Warn` |
| Else | `Fail` |
| Peak `>= 95` (`capacity.go:34`) | `Fail`, whatever the mean |
| Peak `>= 85` (`capacity.go:33`) and the mean passes | `Warn` |

Details: mean, peak, the number of instances with data, and the population standard deviation of per-instance CPU (the old Balance number, informational). The 30/15 and 95/85 lines are arbitrary; the docs state them, so they stay.

### ControlPlane (blocking)

One `GetMetricData` of five `AWS/EKS` metrics with dimension `ClusterName`, period 900 s over 15 minutes (`controlplane.go:35,85-93`): `etcd_mvcc_db_total_size_in_use_in_bytes` (Maximum), `apiserver_request_total`, `_5XX`, `_429` (Sum), `scheduler_pending_pods` (Maximum). The queries ride in the shared metrics call.

| Case | Outcome |
|---|---|
| The call fails | `Unreadable` (the old code skipped, `controlplane.go:77`) |
| No metric has a datapoint | `Skipped`: "control-plane metrics not emitted (EKS 1.28+ on a supported platform version)" |
| etcd `>= 95` percent of 8 GiB (`controlplane.go:20,26`) | `Fail` |
| etcd `>= 80` percent (`:27`) | `Warn` |
| `requests >= 1000` and 5xx `>= 2` percent (`:29,31`) | `Warn` |
| `requests >= 1000` and 429 `>= 1` percent (`:30`) | `Warn` |
| Else | `Pass` |

Pending pods are a detail line. The 2 and 1 percent lines and the 1000 floor are arbitrary; the 8 GiB limit is documented by AWS.

### Quotas (advisory)

One `GetServiceQuota` for `ec2` / `L-1216C47A` (`quotas.go:20-21`), the On-Demand Standard vCPU quota, and one `AWS/Usage ResourceCount` query (Service EC2, Type Resource, Resource vCPU, Class Standard/OnDemand), period 60 s, stat Maximum, newest minute first (`quotas.go:85-107`), in the shared metrics call. Both start in the same wave.

| Case | Outcome |
|---|---|
| `GetServiceQuota` or the metrics call fails | `Unreadable` (the old code skipped) |
| The quota has no value, or the usage metric has no datapoint | `Skipped`: "AWS/Usage vCPU metric not reported" |
| Usage `>= 95` percent (`quotas.go:23`) | `Fail` |
| Usage `>= 85` percent (`:22`) | `Warn` |
| Else | `Pass` |

### Workloads (blocking, needs Kubernetes)

Three `list pods` calls, one for each of `kube-system`, `kube-public`, `kube-node-lease` (`workloads.go:67-71`), in the first Kubernetes wave. `Succeeded` pods and stale `Failed` pods (`staleFailedPod`, `workloads.go:27-44`, kept as is) are left out.

| Case | Outcome |
|---|---|
| A list fails and no namespace was read | `Unreadable` |
| A list fails, the others give Pass or Warn | `Unreadable`, message "kube-system: 12/12 running; kube-public not read" |
| A list fails, the others give Fail | `Fail` with the failure |
| No pod in the three namespaces | `Warn`: "no pods in kube-system" |
| Every pod Running with every container ready | `Pass` |
| Running-and-ready `>= 90` percent (`workloads.go:157`) | `Warn` |
| Else | `Fail` |

### DrainBlockers (blocking, forceable, needs Kubernetes)

Reads, from the shared reads of section 3: the PDB list, the node list, and the pods of each namespace that has an at-risk PDB or more than one PDB. With `Targets`, the target node set is the nodes whose `eks.amazonaws.com/nodegroup` label is in `Targets`, taken from the one node list.

The rules are the old `findDrainBlockers` and `findMultiPDBPods` (`pdbs.go:264-370`), behaviour-frozen:

- A PDB is at risk when `disruptionsAllowed <= 0` and `expectedPods > 0`, or its status is not synced (`pdbs.go:68`, `:101-107`).
- A pod counts when the eviction API would consult the PDB: not `Succeeded`, `Failed`, `Pending`, or deleting; a not-Ready pod only under `IfHealthyBudget` while the budget is not met (`pdbs.go:602-631`).
- A pod that more than one PDB selects is a blocker on its own (`pdbs.go:322-370`).
- With `Targets`: a PDB counts when it gates a pod on a target node. When no node carries a target label, the check falls back to the cluster-wide list, unless the targets' desired size in the snapshot is 0, which is "nothing to drain" (`pdbs.go:264-293`). The desired size comes from the snapshot, not from a `DescribeNodegroup` call.
- Without `Targets`, every at-risk PDB and multi-PDB pod in the cluster counts, system namespaces included.

| Case | Outcome |
|---|---|
| The PDB list fails | `Unreadable` |
| The node list fails | `Unreadable`. The old code fell back to cluster-wide; a failed read is now a failure. |
| A pod list of a namespace fails | `Unreadable`, unless another namespace already gives a blocker, then `Fail` with the failure |
| Targets have no nodes and desired 0 | `Pass`: "nothing to drain" |
| A blocker | `Fail`, one detail line per blocker in the old `DrainBlockerSummary` words |
| None | `Pass` |

`ScaleDownBlockers(nodegroup string, remove int32) Check` is the scale-down form, the old `pdbs.go:456-504` rule (worst-case loss over the `remove` nodes with the most covered pods), built for `nodegroup scale` in phase 4. It is a Check value with a closure, in this package, with the same outcomes. `remove <= 0` is `Pass`.

### PDBCoverage (advisory, needs Kubernetes)

One `list deployments` across all namespaces and the shared PDB list. The old code listed namespaces and then deployments per namespace (`pdbs.go:146-213`): 1 + N calls in series. Deployments in `kube-system`, `kube-public`, and `kube-node-lease` are left out (`pdbs.go:75-79`); `default` counts.

| Case | Outcome |
|---|---|
| The deployment list or the PDB list fails | `Unreadable` |
| No deployment | `Pass`: "no user deployments" |
| Every deployment's pod template matches a PDB selector in its namespace | `Pass` |
| Else | `Warn`, up to five names in a detail line |

### Utilization (advisory, needs Kubernetes and metrics)

The shared node list and one `list nodes` of `metrics.k8s.io/v1beta1`. Only nodes in both lists count (`utilization.go:50-87`).

| Case | Outcome |
|---|---|
| The metrics API answers 404 (not installed) | `Skipped`: "metrics-server is not installed" |
| Another error | `Unreadable` |
| No comparable node | `Skipped` |
| CPU headroom `< 15` or memory headroom `< 10` (`utilization.go:14-15`, `capacity.go:24`) | `Fail` (advisory, so a warning for the gate) |
| CPU `< 30` or memory `< 20` | `Warn` |
| Else | `Pass` |

### Insights (blocking, AWS only)

```go
type InsightsOptions struct {
	Category string   // "" is UPGRADE_READINESS
	Statuses []string // a filter; empty means every status
	// ForVersion, when set, keeps the insights whose kubernetesVersion is
	// that minor: the cluster upgrade gate of phase 4.
	ForVersion fleet.Version
}
func Insights(opts InsightsOptions) Check
```

One paged `ListInsights` with the category filter. The check keeps the raw list in the input's reads, so `upgrade-check` prints it without a second call.

| Case | Outcome |
|---|---|
| The call fails | `Unreadable` |
| An `ERROR` insight | `Fail` |
| An `UNKNOWN` insight, or a status this build does not know | `Fail`: EKS could not evaluate it (`readiness.go:28-30`, `insights.go:194-205` of upgrade) |
| No insight at all in `UPGRADE_READINESS` with no status filter | `Warn`: "EKS has not evaluated upgrade insights yet" in `upgrade-check`. The phase 4 gate, which refreshes first, makes it `Fail`, with `InsightsOptions.ForVersion` set. That is the one rule the two callers do not share, and it is a field, not a copy. |
| A `WARNING` insight | `Warn` |
| Else | `Pass` |

The insights refresh (`StartInsightsRefresh`, `DescribeInsightsRefresh`) belongs to phase 4's `change` package. `upgrade-check` reads the insights EKS has, as today.

### VersionSkew (blocking, no call)

From the snapshot: each nodegroup's minor against the control plane, and each add-on against its latest compatible version (`Need.LatestAddons`).

| Case | Outcome |
|---|---|
| `Have.Cluster` not `Collected` | `Unreadable` |
| A nodegroup or an add-on with `State` or `LatestState` not `Collected` | `Unreadable` with the snapshot's failures, unless another item already gives `Fail` |
| A nodegroup 3 or more minors behind (`insights.go:38` of cluster; the upgrade planner's `> 3` against the hop target, `plan.go:408`, is the same line) | `Fail`: the next hop would exceed the kubelet skew |
| A nodegroup behind, or an add-on behind | `Warn` |
| Else | `Pass` |

## 3. Calls and waves

### Shared reads

```go
// reads memoizes the lists and metrics of one Run, so two checks that want
// the nodes make one call. Each method returns what the call returned, or
// its error, to every caller. A failed read is not retried by the memo.
type reads struct {
	nodes       fanout.Memo[struct{}, *corev1.NodeList]
	pdbs        fanout.Memo[struct{}, *policyv1.PodDisruptionBudgetList]
	pods        fanout.Memo[string, *corev1.PodList] // by namespace
	deployments fanout.Memo[struct{}, *appsv1.DeploymentList]
	nodeMetrics fanout.Memo[struct{}, *metricsv1beta1.NodeMetricsList]
	metrics     *metricsBatch // every CloudWatch query of the run
}
```

`metricsBatch` collects the queries of Capacity, ControlPlane, and Quotas and sends one `GetMetricData` per 500 queries. `Run` starts the checks together; each check registers its queries first thing, and the batch sends when every check that declared metrics has registered or finished. A cluster with up to 494 instances makes one call.

### The gate

The command already holds a snapshot with `Need{Nodegroups: true, InstanceIDs: true}` (2 to 3 waves, phase 2). The gate adds:

| Wave | AWS | Kubernetes |
|---|---|---|
| 1 | `GetMetricData` (CPU, control plane, usage; 1 call per 500 queries), `GetServiceQuota` | `list nodes`, `list poddisruptionbudgets`, `list deployments`, `list nodes` (metrics), `list pods` × 3 system namespaces |
| 2 | | `list pods` for each namespace that has an at-risk PDB or more than one PDB and is not a system namespace |

So the gate is 2 sequential round trips, 1 when no user namespace has such a PDB. The AWS wave and the Kubernetes wave 1 run together.

The old gate, traced through `preflightHealthCheck` and `drainGate` (`actions_update.go:273,506-571`, `drain_gate.go:64-92`): `DescribeCluster` for the kube target, a probe `list namespaces`, then the drain gate (`list poddisruptionbudgets`, `list nodes`, `list pods` per namespace, `DescribeNodegroup` per target), then the checks in parallel, of which the PDB check is the deepest: `list poddisruptionbudgets`, `list nodes`, `list pods` per namespace, `list namespaces`, `list deployments` per namespace, and Capacity: `ListNodegroups`, `DescribeNodegroup` × N, `DescribeAutoScalingGroups`, `GetMetricData`. About 9 sequential round trips, and `ListNodegroups` + `DescribeNodegroup` × N three times over (nodes, capacity, drain gate). Unsure by one: the drain gate's per-nodegroup loop runs in series.

### `cluster upgrade-check`

| Wave | Calls |
|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons`, `DescribeClusterVersions`, `ListInsights` (the category), `ListUpdates`, `GetMetricData` (control plane) |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` per add-on, `DescribeUpdate` × K (the cluster's updates) |
| 3 | `ListInsights` (`ROLLBACK_READINESS`), only when wave 2 found an open rollback window |

2 round trips, 3 with a rollback window. The old command (`insights.go:328-431`, `upgradecheck.go:126-157`): `DescribeCluster`, `ListInsights`, `ListNodegroups`, `DescribeNodegroup` one after another, `ListAddons`, then for each add-on `DescribeAddon` and `DescribeAddonVersions`, then `DescribeClusterVersions`, `ListUpdates` with its describes, `ListInsights` for rollback, and `GetMetricData`. That is 8 + 2N + 2M round trips: 16 for the fake cluster with 2 nodegroups and 2 add-ons, which matches the measured 769 ms at a 50 ms mean. Unsure: how many `DescribeUpdate` calls `versionHistory` makes; the new design reads every update ID in one wave, which is the same count in one round trip.

New in `fleet` for this: `Need.InstanceIDs` keeps every instance ID that the `DescribeAutoScalingGroups` call of the AMI read already returns, so it costs no call when `Need.AMI` is set and one chunked call otherwise. `Need.Updates` reads `ListUpdates` and `DescribeUpdate` for each ID, into `Snapshot.Updates []Update` (ID, type, status, params, created, errors). `fleet.RollbackWindow(snap, now) (Rollback, bool)` is the pure derivation of the old `RollbackAvailability` (`rollback.go:283-306`): an in-place version update to the current version, started less than 7 days ago, with no rollback since.

### `--check-readiness`, `--show-workloads`

```go
package fleet

// KubeNeed says what FetchKube reads from the Kubernetes API.
type KubeNeed struct {
	// Ready counts the Ready nodes of each managed nodegroup: one node list
	// with the selector "eks.amazonaws.com/nodegroup".
	Ready bool
	// Workloads counts the pods on the nodes of the named nodegroup: one
	// pod list per node, with the field selector "spec.nodeName", in one
	// wave. A cluster-wide pod list would read every pod of the cluster.
	Workloads string
}

// FetchKube fills the Kubernetes groups of snap: ReadyNodes with
// ReadyState, and Workloads with WorkloadsState on the named nodegroup.
// It sets snap.KubeAt. A failed list sets the state Failed and adds a
// Nodegroup failure for each nodegroup it covers.
func (f *Fetcher) FetchKube(ctx context.Context, kube kubernetes.Interface, snap *Snapshot, need KubeNeed)
```

`FetchKube` starts as soon as the snapshot's nodegroup list is known, so the node list runs in wave 2 of the command, next to the `DescribeNodegroup` calls. A nodegroup absent from the node list has `ReadyNodes: 0` and `ReadyState: Collected`. `nodegroup describe --show-workloads` adds `Workloads{Pods, SystemPods int}` to `apidoc.Nodegroup` under `workloads`, with `omitzero`. The old `PodDisruption` summary string of the workloads view (`types.go:85`) is not kept: the HEALTH section says the same with the DrainBlockers check.

### `cluster describe` and `cluster list --show-health`

`cluster describe` always has a `HEALTH` section (contract change 14). It runs `Preflight()` with no targets; the Kubernetes checks are `Skipped` unless `--check-readiness` connects. The `Need.InstanceIDs` read is one more call in wave 2 and the metrics call is wave 3, so the command goes from 2 to 3 round trips. Section 12 proposes the alternative.

`cluster list --show-health` runs `AWSOnly()` for each cluster: `ListNodegroups`, `DescribeNodegroup` × N, `DescribeAutoScalingGroups`, then the metrics call and `GetServiceQuota`: 3 waves and 4 + N calls per cluster beyond the list. The `HEALTH` column shows `Health.Summary`. It is what the old flag did, without a Kubernetes client, and it is the most expensive column in the product; section 12 asks whether to keep it.

## 4. The gate

```go
package health

type Decision string

const (
	Go    Decision = "Go"
	Ask   Decision = "Ask"
	Block Decision = "Block"
)

// Policy is what the flags of a changing command say to the gate.
type Policy struct {
	Yes            bool // --yes: an Ask becomes Go, and the reasons go in Verdict.Accepted
	RequireHealthy bool // --require-healthy: an Ask becomes Block
	Force          bool // --force: a Fail or Unreadable of a Forceable check becomes Warn
	Skip           bool // --skip-health-check: no check ran; Verdict is Go with Skipped set
}

// Reason is one check behind a verdict.
type Reason struct {
	Check   string
	Outcome Outcome
	Message string
}

type Verdict struct {
	Decision Decision
	// Exit is the code a dry run exits with, and a blocked run: 0 for Go, 2
	// for Ask, 2 for a Block that --require-healthy made from warnings, 3
	// for a Block, and 4 when a check is Unreadable, because the highest
	// code wins (CONTRACT.md, exit codes).
	Exit int
	// Reasons are the results that decided, worst first. Accepted are the
	// reasons --yes went past; Forced the ones --force went past. Skipped
	// is set when no check ran.
	Reasons  []Reason
	Accepted []Reason
	Forced   []Reason
	Skipped  bool
	// Failures are the reads the checks could not make. A run that goes on
	// keeps them and exits 4 at its end.
	Failures []apidoc.Failure
}

func Gate(results []Result, p Policy) Verdict

// Question is the prompt for an Ask: "proceed despite 2 warnings (Nodes:
// 5/6 nodes ready; Quotas: 91% of the vCPU quota in use)?"
func (v Verdict) Question() string

// Summary is the one-line error of a Block.
func (v Verdict) Summary() string

// Worse returns the checks of cur whose outcome is worse than in prev, or
// that ran in prev and are Skipped in cur. The TUI refuses a start when it
// is not empty; cluster upgrade asks.
func Worse(prev, cur []Result) []Reason
```

The decision. Rank each result; `Skipped` has no rank and never changes the decision. `--force` is applied first, to Forceable checks only, and turns their `Fail` and `Unreadable` into `Warn` with the reason kept in `Forced`. Then the worst rank decides:

| Worst result | Default | `--yes` | `--require-healthy` | Exit (dry run or blocked run) |
|---|---|---|---|---|
| Blocking `Fail` | `Block` | `Block` | `Block` | 3 |
| Blocking `Unreadable` | `Block` | `Block` | `Block` | 4 |
| Non-blocking `Fail` or `Warn` | `Ask` | `Go`, in `Accepted` | `Block` | 2 |
| Non-blocking `Unreadable` | `Ask` | `Go`, in `Accepted` | `Block` | 4 |
| `Pass`, or only `Skipped` | `Go` | `Go` | `Go` | 0 |
| No check ran (`Skip`) | `Go`, `Skipped` | `Go` | `Go` | 0 |

`--yes` and `--require-healthy` together: `--require-healthy` wins. `--force` with a non-Forceable check changes nothing. An `Unreadable` result always puts its failures in `Verdict.Failures`, so a run that goes past one with `--yes` or `--force` exits 4 at its end, which is the old behaviour of `--force` past unreadable PDBs (`exit-codes.md`, `nodegroup update` row 4).

Two differences from the old gate, both from the drift table of `REWRITE.md`: an unreadable blocking check blocks in every command (the old Node Health warned on a transient error, the old Capacity warned on any CloudWatch error), and a drain blocker is a blocking check in the summary, not a warning plus a second refusal. The CONTRACT row "a gate that cannot read exits 4" holds literally: `Block` with exit 4.

How a front end uses it, the whole of it:

```go
results := health.Run(ctx, health.Preflight(), in)
v := health.Gate(results, policy)
// print the report (section 8) on stderr for a machine format, stdout otherwise
switch v.Decision {
case health.Block:
	return &cmdutil.ExitError{Code: v.Exit, Err: errors.New(v.Summary())}
case health.Ask:
	ok, err := confirm(ctx, v.Question()) // cmdutil.Confirm in the CLI, Backend.Answer in the TUI
	if err != nil || !ok {
		return declined // exit 1, "declined after health warnings"
	}
}
// go on; v.Failures join the run's failures
```

`cmdutil.RequireYes` has already refused a machine run or a run without a terminal that has no `--yes`, so `confirm` is only ever called where a prompt can show. `--quiet` does not change the gate: the question still shows. The old rule "quiet on a TTY never auto-accepts" (`gates_test.go:21`) holds because quiet never accepts anything.

`--dry-run` runs the same checks and the same `Gate` with the real policy, prints the verdict in the plan, and exits with `v.Exit`. Nothing in the gate knows about dry runs.

When the gate runs:

1. In the plan step, before any change, once per cluster with `Targets` set to every nodegroup the plan would roll. For `cluster upgrade` this is before the control plane moves, so a drain blocker is known up front (CONTRACT.md, "Behaviour the new CLI does not copy").
2. Again right before each phase that rolls nodes, with `Targets` set to that phase's nodegroups. "Again" means a new `Input`: a fresh snapshot with `Need{OnlyNodegroups: targets, InstanceIDs: true}` and a new `reads`, so every Kubernetes list and every metric is read anew. Nothing from the plan step is reused but the client. The phase then asks when `Worse(plan, now)` is not empty, as the TUI does today.

Exit codes of a run that went past the gate are phase 4's: the gate contributes `Verdict.Failures` and nothing else.

## 5. Post-roll verification

```go
// Baseline is what verification compares against, taken before the roll.
type Baseline struct {
	// Pending is the set of "namespace/name" of the Pending pods, less the
	// pods pinned to a node that no longer exists (verify.go:38-56).
	Pending map[string]struct{}
	// Read is false when the lists failed; NewPendingPods is then Skipped.
	Read bool
}

func TakeBaseline(ctx context.Context, kube kubernetes.Interface) Baseline

// Verify returns the checks that run after a roll of nodegroups:
// NodegroupActive, NodesReady, NewPendingPods.
func Verify(nodegroups []string, base Baseline) []Check

// VerifyExit maps the results to the exit code of the verification: 5 when
// a check is Fail, else 4 when one is Unreadable, else 0.
func VerifyExit(results []Result) int
```

| Check | Reads | `Fail` (exit 5) | `Unreadable` (exit 4) | `Skipped` |
|---|---|---|---|---|
| `NodegroupActive` | The snapshot of the rolled nodegroups, which the command re-reads after the wait | A nodegroup whose status is not `ACTIVE` | A nodegroup with `State: Failed` | never |
| `NodesReady` | One `list nodes` with the selector `eks.amazonaws.com/nodegroup in (targets)` | A node that is not Ready. There is no join grace: the roll is over. | The list fails | No Kubernetes |
| `NewPendingPods` | One `list pods` with the field selector `status.phase=Pending`, and the node list above for the pinned-node rule | A Pending pod that is not in the baseline, up to five named | The list fails | No Kubernetes, or `Baseline.Read` false |

`NodesReady` is new (CONTRACT.md row "Post-roll verification does not read node readiness"). `NewPendingPods` with a failed list is `Unreadable` (row "If the pod list fails after a roll"). The old `VerifyPostRoll` kept its skip reasons in the `checks` list of the document (`verify.go:83-96`); the new document has the results of this check set under `verification`, with their outcomes, through the same `HealthResult` shape. Both lists run in one wave.

## 6. Kubernetes client resolution

```go
package kube

// Target is the EKS cluster a client must point at.
type Target struct {
	Name, Region string
	Endpoint     string // from DescribeCluster; "" when the cluster has none
}

type Options struct {
	Kubeconfig string // --kubeconfig; "" means $KUBECONFIG, then ~/.kube/config
	Context    string // --kube-context; trusted as named
	// InClusterName is $REFRESH_IN_CLUSTER_NAME. The command reads the
	// environment; this package does not.
	InClusterName string
	// RequestTimeout bounds each request. Zero means 30 seconds.
	RequestTimeout time.Duration
}

type Source string // Kubeconfig, NamedContext, InCluster

// Selection says which config was chosen, for the one notice a command
// prints.
type Selection struct {
	Source       Source
	Path         string // the kubeconfig file or list
	Context      string
	Server       string
	SwitchedFrom string // the current context, when another one was chosen
	Unverified   bool   // --kube-context whose server does not match the endpoint
}

// Notice is the line a command prints once, or "" when there is nothing
// to say: the Unverified note, or the switch of context.
func (s Selection) Notice() string

type Client struct {
	Clientset kubernetes.Interface
	Metrics   health.MetricsLister // the metrics.k8s.io node lister of the same config
	Selection Selection
}

// Connect returns a client for target. It makes no request.
func Connect(target Target, opts Options) (*Client, error)

// MismatchError says that no config points at the target. A command prints
// it as a warning with Hint and goes on without Kubernetes.
type MismatchError struct {
	Target  Target
	Context string // the current context that was considered
	Server  string
	InCluster bool
}

func (e *MismatchError) Error() string
func (e *MismatchError) Hint() string // "aws eks update-kubeconfig --name prod --region us-east-1, or pass --kube-context"
```

The matching rules are the old `targetCandidates` (`kubetarget.go:180-263`), ported:

1. Locate the kubeconfig: `--kubeconfig`, else `$KUBECONFIG` (a list; used when one file exists), else `~/.kube/config`. An explicit path that does not exist is an error. No file at all falls through to in-cluster config.
2. With `--kube-context`: that context, or an error when it is not in the file. `Unverified` is set when its server does not match the endpoint. Nothing else is tried.
3. Else the contexts whose server matches the endpoint, by `host:port` after normalising (`NormalizeEndpoint`, `kubetarget.go:104-129`): the current context first, then the rest by name. The first one that builds a config wins. No endpoint, or no match, is a `*MismatchError`.
4. In-cluster config is used only when its host matches the endpoint or `InClusterName` equals the target name. Else a `*MismatchError` with `InCluster` set.

Differences from the old code:

- **No probe.** The old code listed namespaces with a 5 s timeout on each candidate and tried the next on failure (`kubeclient.go:129-139`). That is one round trip before any check, and the checks tell the same thing: a context that cannot be reached makes every Kubernetes check `Unreadable`, and the failure names the context and the error. One candidate is chosen, the first match.
- **No writes.** The package writes nothing. `Selection.Notice()` and `MismatchError` are values the command prints, once, through `IOStreams`. The old dedupe through a context value (`kube.go:85-104`) is not needed: a run resolves its client once, in the factory, and keeps it.
- **Exec plugins.** `Connect` sets `rest.Config.ExecProvider.StdinUnavailable = true` and `InteractiveMode = Never`, so `aws eks get-token` and `aws-iam-authenticator` never read the terminal, and a prompt of the command cannot race a plugin for stdin. client-go hands a plugin's stderr to `os.Stderr` and offers no field for it, so a plugin's own warning lines reach stderr; in the CLI that is where notices go. The TUI (phase 5) keeps the stderr file-descriptor redirect of the old `detachStdio` for that one reason, and no longer needs the stdin part.
- **Timeouts.** `rest.Config.Timeout` is `RequestTimeout`, and the dial timeout is 10 s. Every list also carries the command's context, so `--timeout` bounds the whole run.
- **Metrics.** `Client.Metrics` comes from the same `rest.Config`, so both clients talk to the same server. Building it cannot fail apart from the clientset.

The factory:

```go
// On cmdutil.Factory:
// Kube returns the Kubernetes client for target, once per target and run.
// A *kube.MismatchError or any other error is returned, not printed.
Kube func(ctx context.Context, target kube.Target) (*kube.Client, error)
```

A command turns the error into `Input.NoKube` and one stderr warning:

```text
warning: no Kubernetes access to prod: kubeconfig context "staging" (server https://...) does not point at it
  add one with: aws eks update-kubeconfig --name prod --region us-east-1, or pass --kube-context
  the Workloads, DrainBlockers, PDBCoverage, and Utilization checks are skipped
```

A Kubernetes API that is configured and does not answer is not this case: the checks run and come back `Unreadable`.

Tests: `kubetest.NewServer(world, region, cluster)` sets the cluster's endpoint in the world to the server's URL, so the fake EKS reports it and `Connect` matches by `host:port`. `srv.WriteKubeconfig(path)` writes the file that `Options.Kubeconfig` names. An in-process test can also set `Factory.Kube` to return `kubernetes.NewForConfig(srv.Config())`. Both forms are used: the first for `Connect` itself and the whole-command goldens, the second for the check tables.

## 7. `cluster upgrade-check`

```text
Use:   upgrade-check [cluster]
Short: Report whether the cluster is ready for its next upgrade
Args:  at most 1
Flags: --category, --status, --show-passing, --insight, --exit-zero, -o (table, json, yaml, plain)
```

The cluster resolves as a read-only command does. `--category` is `UPGRADE_READINESS` by default, or `MISCONFIGURATION` or `ROLLBACK_READINESS`. `--status` is repeatable and filters the insights shown and judged; `PASSING` in it implies `--show-passing`.

The report and where each part comes from:

| Part | Source |
|---|---|
| Version, support posture | `Snapshot.Cluster`, `Support` (`Need.Support`) |
| Insights | The `Insights` check and its raw list, filtered by `--status`, `PASSING` hidden unless asked |
| Version skew | The `VersionSkew` check and the snapshot (`Need{Nodegroups, Addons, LatestAddons}`) |
| Control plane | The `ControlPlane` check |
| Rollback window | `fleet.RollbackWindow` (`Need.Updates`); the `ROLLBACK_READINESS` counts from one `ListInsights` when the window is open |
| Readiness | `Gate(results, Policy{})` over the three checks |

```go
package apidoc

type Readiness string // Ready, Review, Blocked, Incomplete

type UpgradeCheck struct {
	Cluster      string         `json:"cluster"`
	Region       string         `json:"region"`
	Version      string         `json:"version,omitzero"`
	Readiness    Readiness      `json:"readiness"`
	Reasons      []string       `json:"reasons,omitzero"`
	Support      *Support       `json:"support,omitzero"`
	Insights     []Insight      `json:"insights"`
	// InsightsEvaluated is false when EKS returned no insight of the
	// category, passing ones included.
	InsightsEvaluated bool          `json:"insightsEvaluated"`
	Skew              *Skew         `json:"skew,omitzero"`
	ControlPlane      *HealthResult `json:"controlPlane,omitzero"`
	Rollback          *Rollback     `json:"rollback,omitzero"`
	Failures          Failures      `json:"failures"`
}
type Insight struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Category          string    `json:"category"`
	Status            string    `json:"status"` // the EKS value
	StatusReason      string    `json:"statusReason,omitzero"`
	KubernetesVersion string    `json:"kubernetesVersion,omitzero"`
	LastRefreshTime   time.Time `json:"lastRefreshTime,omitzero"`
	Description       string    `json:"description,omitzero"`
}
type Skew struct {
	Nodegroups []NodegroupSkew `json:"nodegroups"`
	Addons     []AddonSkew     `json:"addons"`
}
type NodegroupSkew struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitzero"`
	MinorsBehind int    `json:"minorsBehind"`
	AtSkewLimit  bool   `json:"atSkewLimit"`
	Incomplete   bool   `json:"incomplete,omitzero"`
}
type AddonSkew struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitzero"`
	Latest     string `json:"latest,omitzero"`
	Behind     bool   `json:"behind"`
	Incomplete bool   `json:"incomplete,omitzero"`
}
type Rollback struct {
	PreviousVersion string         `json:"previousVersion"`
	UpgradedAt      time.Time      `json:"upgradedAt"`
	AvailableUntil  time.Time      `json:"availableUntil"`
	Insights        *InsightCounts `json:"insights,omitzero"` // Passing, Warning, Error, Unknown
}

type InsightDescription struct {
	Cluster        string              `json:"cluster"`
	Region         string              `json:"region"`
	Insight        Insight             `json:"insight"`
	Recommendation string              `json:"recommendation,omitzero"`
	Resources      []string            `json:"resources"`
	AdditionalInfo map[string]string   `json:"additionalInfo,omitzero"`
	Deprecations   []Deprecation       `json:"deprecations,omitzero"` // Usage, ReplacedWith, StopServingVersion, StartServingReplacementVersion, ClientStats
	Failures       Failures            `json:"failures"`
}
```

Readiness and its precedence, from the gate verdict of the three checks:

| Verdict | Readiness | Exit |
|---|---|---|
| `Block` from a `Fail` | `Blocked` | 3, or 4 when a failure is in the document |
| `Block` from an `Unreadable` only | `Incomplete` | 4 |
| `Ask` | `Review` | 2, or 4 with a failure |
| `Go` | `Ready` | 0 |

The label follows the old precedence, blocked before incomplete before review (`readiness.go:100-109`), so the human reads the strongest finding. The exit code follows `CONTRACT.md`: the highest code wins, so a blocked check with an unread nodegroup exits 4. The old command exited 3 there. Section 12 records it. `--exit-zero` turns 2, 3, and 4 into 0; an error or an interrupt stays 1. A support posture of `Extended` or `Unsupported` is a reason for `Review` and shows in the header; it does not change the code (the old command did not either).

`--insight <id>`: `DescribeInsight` with the ID, or with the full ID whose first 8 characters the table printed. The command lists the insights of `--category` only when the value is not a full ID, so an ID costs one call and a short ID two. A name substring is no longer accepted (section 12). The exit code is that one insight's: 0 for `PASSING`, 2 for `WARNING`, 3 for `ERROR` or `UNKNOWN`.

The table view, a `view.Description`:

```text
prod  ◆ NOT READY   us-east-1 · 1.32 · standard support until 2027-03-23 (164 days)

READINESS
  ✗ 1 ERROR insight; 1 nodegroup at the kubelet skew limit
  ! 2 addons behind latest

INSIGHTS                                            UPGRADE_READINESS
  ID        NAME                              STATUS   VERSION
  bc8b2f86  Deprecated APIs removed in 1.33   ERROR    1.33
  1a9f0c21  Kubelet version skew              WARNING  1.33

VERSION SKEW                                        control plane 1.32
  NODEGROUP   VERSION   BEHIND
  ng-legacy   1.29      3 (at the limit)
  ng-a        1.32      0

  ADD-ON      INSTALLED              LATEST
  coredns     v1.11.3-eksbuild.1     v1.11.4-eksbuild.2   behind

CONTROL PLANE
  ✓ etcd 12% of 8 GiB; 5xx 0.01% of 48,210 requests

ROLLBACK
  to 1.31 until 2026-10-18 (6 days); rollback insights 3 passing
```

The `READINESS` section is `Fields` with a `Status` each, the `INSIGHTS` and `VERSION SKEW` sections are `Items`, so plain output is `readiness.blocked`, `insights.bc8b2f86.status`, `version_skew.ng-legacy.behind`, and so on, from the same data. The old plain form put only the insight rows on stdout and the rest on stderr; the new one is the `FIELD`/`VALUE` form of every section, as every describe view (phase 2, section 6).

## 8. Views for health results

One function renders results everywhere:

```go
package view

// HealthLines draws one line per result: the glyph of its outcome, the
// name padded to the widest, and the message. A Skipped result is dim with
// no glyph. An Unreadable result has the Unknown glyph.
func HealthLines(th Theme, results []apidoc.HealthResult) []string
```

| Outcome | `view.Status` | Glyph |
|---|---|---|
| `Pass` | `Healthy` | `✓` |
| `Warn` | `Warn` | `!` |
| `Fail` | `Fail` | `✗` |
| `Unreadable` | `Unknown` | `?` |
| `Skipped` | `NoStatus`, dim | none |

`cluster describe` adds a `HEALTH` section with `Items`, one per check, fields `outcome` (with the status) and `message`; plain output has `health.nodes.outcome` and `health.nodes.message`. The section's subtitle is the summary.

The gate's report, before a prompt and in a dry run, on stdout for a human run and stderr for a machine run:

```text
PRE-FLIGHT CHECKS  prod · ng-a
  ✓ Nodes          6/6 nodes ready
  ✓ Capacity       CPU mean 31%, peak 58%, 6 instances
  ✓ ControlPlane   etcd 12% of 8 GiB
  ✓ Workloads      14/14 pods running in kube-system
  ✗ DrainBlockers  PDB default/web allows 0 disruptions (2/2 pods healthy)
  ! PDBCoverage    1 deployment without a PDB: default/batch
    Utilization    metrics-server is not installed
  ? Quotas         could not read the vCPU quota: AccessDenied on servicequotas:GetServiceQuota

Verdict: BLOCKED  DrainBlockers: PDB default/web allows 0 disruptions
```

The verdict line reads `would proceed`, `would ask` (with the question), or `would be blocked` in a dry run, and `proceeding`, the question itself, or `blocked` in a run. The `--force` and `--yes` reasons print as `! --force: ...` lines under the verdict. The machine form is `apidoc.Gate` in the plan.

## 9. Performance

Targets, in sequential round trips, with the request limit at 32:

| Command | Old | New |
|---|---|---|
| The gate, after the snapshot | about 9 | 2, or 1 without a user PDB at risk |
| `cluster upgrade-check` | 8 + 2N + 2M (16 on the fake) | 2, or 3 with a rollback window |
| `cluster describe` | 2 (phase 2) | 3 |
| `cluster describe --check-readiness` | | 3, the node list in wave 2 |
| `nodegroup list --check-readiness` | | 3 or 4 (phase 2) with the node list in wave 2 |
| `nodegroup describe --show-workloads` | | 3 (phase 2) with the node list in wave 2 and the pod lists in wave 3 |
| `cluster list --show-health`, per cluster | | 4 |

Model for `upgrade-check` with the 0 to 100 ms latency of the fake: two waves whose slowest call is near 100 ms, so about 200 ms against the measured 769 ms. The measurement goes in section 13, with the old binary in the same run, as phase 2 did.

How a test asserts the waves: the `awsxtest.Gate` for AWS, and the same shape over `kubetest.Handler.OnRequest` for Kubernetes. The kube gate is a few lines in `kubetest`: `NewGate(t, handler)` with `Wave(ops...)` over `fleettest.Operation`, so one test holds both:

```go
aws := awsxtest.NewGate(t, srv.Handler)
k8s := kubetest.NewGate(t, ksrv.Handler)
go run()
aws.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups)
aws.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.AutoScalingDescribeAutoScalingGroups)
aws.Wave(fleettest.CloudWatchGetMetricData, fleettest.ServiceQuotasGetServiceQuota)
k8s.Wave(fleettest.KubeListNodes, fleettest.KubeListPDBs, fleettest.KubeListDeployments, fleettest.KubeListNodeMetrics,
	fleettest.KubeListPods, fleettest.KubeListPods, fleettest.KubeListPods)
k8s.Wave(fleettest.KubeListPods) // the namespace of the tight PDB
aws.Done()
k8s.Done()
```

The two gates are independent, so the AWS waves and the Kubernetes waves may interleave in any order; each gate proves its own depth. A metrics-batch test asserts one `GetMetricData` for a cluster of 20 instances and two for 600.

Benchmarks: `Gate` over 10 results and `HealthLines` over 10 results with `testing.AllocsPerRun` on every PR; the PDB selector cross-product (`REWRITE.md` names `pdbs.go:350-358`) nightly with benchstat, 200 PDBs over 2,000 pods.

## 10. Test plan

**Table tests for each check.** One test per check with one row per outcome: `Pass`, `Warn`, `Fail`, `Skipped`, `Unreadable`, and the partial-read rows (`Unreadable` from a Pass, `Fail` kept). The Kubernetes checks run against `kubernetes/fake` clientsets built from a `fleettest.World` through `kubetest`'s object builders, with a reactor for the failed list. The AWS checks run against `awsxtest` with `World.SetScopedFault`. The threshold rows of each check are the old tests' numbers.

**Gate.** One table test over the decision table of section 4: every row by every policy, with `Exit`, `Accepted`, `Forced`, and `Failures` asserted. One test for `Worse`. One for `Question` and `Summary` wording.

**Runner.** Needs → `Skipped` with the input's reason; a cancelled context → `Unreadable` with `Interrupted`; `Name` and `Blocking` filled from the check; the order kept; the memo makes one node list for three checks that want it.

**Kube.** `Connect` against kubeconfig files in a temp dir: each rule of section 6 as a row, the `$KUBECONFIG` list, the explicit missing path, the exec fields set, the metrics client built from the same host.

**Whole-command tests** through `cmdtest.NewFake` plus a `kubetest.Server`, with goldens in the four formats for: `cluster upgrade-check` on a ready, a review, a blocked, an incomplete, and a not-evaluated cluster, and with `--insight`; `cluster describe` with the health section, with and without `--check-readiness`, with no kubeconfig match (the one warning), and with a Kubernetes API that answers 403 (Unreadable, exit 4); `nodegroup list --check-readiness`; `nodegroup describe --show-workloads`; `cluster list --show-health`. Each asserts the request counts and the waves.

**Verification.** The `Verify` set over the world's roll scripts: a clean roll, `NodesNeverReady` (Fail, 5), `PendingPods` (Fail, 5), a denied pod list after the roll (Unreadable, 4), a denied `DescribeNodegroup` (Unreadable, 4), and a Fail with an Unreadable (5).

**Parity.** The scenarios in `test/parity/scenarios_kube.go` and `scenarios.go` already pin the old gate: `nodegroup-update-pdb-blocked`, `-pdb-forced`, `-pdb-elsewhere`, `-pdbs-unreadable`, `-pdbs-unreadable-forced`, `-workload-failing`, `-node-not-ready`, `-node-not-ready-require-healthy`, `-warnings-unattended`, `-require-healthy`, `-prompt-yes|no|interrupt`, `-blocked`, `-degraded-neighbour`, `-pdb-tightens-after-dry-run`, `-stuck-drain`, the `nodegroup-scale-pdb-*` set, and the `cluster-upgrade-pdb-*` and `cluster-rollback-pdb-*` sets. Phase 4 runs them. The intended differences that must enter `allowlist.json`, each with its CONTRACT.md row as `source`:

| Scenario | Difference | CONTRACT.md row |
|---|---|---|
| `nodegroup-update-verify-node-not-ready` | exit 5, not 0 | "Post-roll verification does not read node readiness" |
| `nodegroup-update-verify-pods-unreadable` | exit 4, not 0 | "If the pod list fails after a roll" |
| `cluster-upgrade-pdb-blocked`, `cluster-rollback-pdb-blocked`, `cluster-upgrade-pdb-blocks-second-nodegroup`, `cluster-rollback-pdb-blocks-second-nodegroup`, `cluster-upgrade-pdbs-unreadable`, `cluster-rollback-pdbs-unreadable` | no mutation, exit 3 (or 4 for unreadable), where the old binary moved the control plane and the add-ons first and then exited 1 | "`cluster upgrade` reads PDBs only when it reaches the nodegroup phase" and "A drain-gate refusal exits 3" |
| `nodegroup-scale-pdbs-unreadable` | exit 4 with a document, not 1 without | "`nodegroup scale -o json` prints no document" |
| `nodegroup-scale-check-pdbs-no-kube` | the scale proceeds with a warning, where the old `--check-pdbs` refused | needs the new row of section 12, edit 4 |

**Carried over.** These old tests name a regression and are ported into the tables above, or into a named whole-command case:

| Old test file | What it protects |
|---|---|
| `health/health_test.go` | A PDB whose selector does not match is not coverage (`:264`); `minAvailable == replicas` is a drain blocker (`:345`); a target scaled to 0 has nothing to drain (`:731`); `default` deployments count (`:805`); the eviction rules, the sync-failed PDBs, the scoping fallbacks |
| `health/accuracy_test.go` | Graceful-shutdown pods are skipped and bare `Terminated` is not; multi-PDB pods, scoped and unscoped; scale-down arithmetic and fail-closed; joining nodes warn, broken nodes still fail; nil and empty selectors |
| `health/scoring_test.go` | A denied describe blocks; updating nodegroups are not half ready; all-updating warns; no managed nodegroups without Kubernetes is skipped and with Kubernetes counts nodes. The two "throttled does not block" tests become "a throttled read is retried", at the `awsx` layer. |
| `health/workloads_unread_test.go` | A failed pod list cannot pass; a partial failure keeps a Fail; the unread namespace is named |
| `health/checker_test.go` | A skipped check changes neither the verdict nor the summary (REF-146); a non-blocking Fail warns; blocking beats warn; all skipped does not panic |
| `health/aws_calls_test.go` | A 100-name `DescribeAutoScalingGroups` chunk returns every group (`MaxRecords`), now a `fleet` test; retried throttling; summarised AWS errors |
| `health/pdb_fuzz_test.go` | The drain-blocker report on fuzzed clusters (REF-4) |
| `health/quotas_test.go` | The newest minute, not the maximum over the window, is the usage; skip without the metric |
| `health/controlplane_test.go`, `utilization_test.go` | The parsing and each threshold; no data is skipped; metrics-server absent is skipped |
| `health/kubetarget_test.go`, `kubeclient_test.go`, `kubectl_test.go` | Endpoint normalising; current context first; explicit context trusted; `$KUBECONFIG` list; in-cluster rules; explicit missing path is an error; exec region and `--cluster-name` parsing (the last is already in `kube` from phase 2) |
| `health/readiness_test.go`, `services/nodegroup/readiness_test.go` | Ready counts by nodegroup; a count is never invented |
| `commands/nodegroup/verify_test.go`, `services/nodegroup/verify_rename_test.go` | A throttled describe is retried (REF-173); pods for a removed node are not stuck; a failed baseline skips the pod check; a failed describe is a failure, not an issue |
| `commands/nodegroup/drain_gate_test.go`, `commands/cluster/upgrade_gate_test.go`, `upgrade_gate_failures_test.go` | Refuse, force, no kube, read error refuses, skip-health-check, dry run shows blockers, unread PDBs listed, a gate failure does not hide a failed roll, a cancelled check stops despite `--force` |
| `commands/nodegroup/gates_test.go`, `unattended_test.go`, `health_decision_test.go` | `--require-healthy` blocks a warn; `--yes` proceeds and prints the accepted warnings; quiet never accepts; block exits 3; fleet verdict codes |
| `commands/cluster/upgradecheck_exit_test.go`, `services/cluster/readiness_test.go`, `insights_test.go` | The exit table (REF-165); `--status PASSING` shows passing (REF-168); `--insight` uses the category; not-evaluated insights; the skew report; the deprecation detail |
| `commands/runner/kube_test.go`, `commands/cluster/readiness_test.go` | Mismatch warns once and skips; the matching context is selected; the explicit context is noted; a describe failure or an unreachable API leaves readiness unknown |
| `commands/nodegroup/scale_health_failures_test.go` | Unread pods of a scale's health check are failures |

`legacy/PORTING.md` records the result for each file.

## 11. Build order

Each step ends with `task dev:full` green and is one reviewable change.

1. **Types and the gate.** `Check`, `Input`, `Result`, `Outcome`, `Run` with Needs and cancellation, `Gate`, `Verdict`, `Worse`; `apidoc.HealthResult`, `Health`, `Gate`, and their schemas. Pure, with the decision-table test. The smallest slice that phase 4 can already code against.
2. **`internal/kube`.** `Connect`, `Selection`, `MismatchError`, `Factory.Kube`, the `kubetest` gate. No command changes yet.
3. **The AWS-only checks and `cluster describe`.** `Need.InstanceIDs`, the metrics batch, `Capacity`, `ControlPlane`, `Quotas`, `view.HealthLines`, the `HEALTH` section always on, goldens, the wave test.
4. **The Kubernetes checks and the read flags.** `Nodes`, `Workloads`, `DrainBlockers`, `ScaleDownBlockers`, `PDBCoverage`, `Utilization`, the shared reads, `fleet.FetchKube`, `--check-readiness` on both commands, `--show-workloads`, goldens.
5. **`cluster upgrade-check`.** `Insights`, `VersionSkew`, `Need.Updates`, `fleet.RollbackWindow`, the two documents and schemas, the command, `--insight`, goldens, the wave test, the measurement for section 13.
6. **Verification.** `Baseline`, `Verify`, `VerifyExit`, the roll-script tests. No command calls it until phase 4.
7. **Close-out.** `cluster list --show-health`, the benchmarks, the livecheck rows (the AWS/EKS metric names and the quota code against a real account), `gen-docs`, the `PORTING.md` rows.

## 12. Decisions for the owner, and contract edits

Decisions, each with the recommended answer:

1. **An unreadable blocking check blocks** (exit 4), also for a CloudWatch read that a narrow IAM policy denies. The old Capacity check warned there. Recommended: block, as the drift table decided for PDBs; `--skip-health-check` is the way past, and the message names the IAM action.
2. **Exit code of a blocked `upgrade-check` with an unread item**: 4 by the contract's "highest code wins", 3 by the old precedence. Recommended: 4, the rule the owner chose for `status` in phase 2. The label still reads `NOT READY`.
3. **`cluster describe` runs the AWS-only health checks always**, one more round trip and 2 more calls. The alternative is a `HEALTH` section only with `--check-readiness`. Recommended: always, as contract change 14 says every section shows.
4. **`cluster list --show-health`** costs 3 round trips and 4 + N calls per cluster. Recommended: keep it this phase as the contract lists it, and revisit after the measurement.
5. **No connection probe** before the checks. Recommended: none; an unreachable API shows as `Unreadable` on the checks that need it, one round trip sooner.
6. **Resource Balance is dropped.** Recommended: yes; its spread stays as a detail line.

Proposed contract edits, as a list; nothing changes until the owner says so:

1. `cluster upgrade-check --insight` takes the insight ID or the short ID the table shows. The name-substring match of the old `--id` is removed, by the rule that names are exact.
2. Exit codes, the row "`nodegroup scale -o json` prints no document ... A gate that cannot read exits 4": add the words "and blocks", so the row and the drift table agree: a gate that cannot read blocks and exits 4; `--force` goes past unreadable PDBs and the run exits 4 at its end.
3. The `--dry-run` row of the shared flags: "The exit code is 0 when the real run would be allowed, 2 when it would ask about warnings, 3 when it would be blocked, and 4 when a check could not read what it needs."
4. New row under "Behaviour the new CLI does not copy": "`nodegroup scale --check-pdbs` without Kubernetes access refused the scale-down (exit 1). Every changing command treats a cluster with no Kubernetes configuration the same way: the Kubernetes checks are skipped with one warning, and the change goes on. A Kubernetes API that is configured and does not answer makes the checks unreadable, which blocks (exit 4)."
5. Documents: `ClusterDescription` gains `health`; `Nodegroup` gains `readyNodes` (phase 2 reserved it) and `workloads`; `UpgradeCheck` and `InsightDescription` have the fields of section 7; `Plan` and `Run` carry `gate` and `verification` as lists of `HealthResult`.
6. `cluster upgrade-check -o plain` is the `FIELD`/`VALUE` form of every section, not the insight rows alone with the rest on stderr.

Two points of `REWRITE.md` change in this note and need no contract edit: `Check` is a struct with a `Run` function, not an interface, and `Gate` returns a `Verdict` value with its exit code rather than a bare `Go`, `Ask`, `Block`.

## 13. Changes made while building

Empty until code changes the design.
