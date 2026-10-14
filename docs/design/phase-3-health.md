# Phase 3 design note: health

Status: draft 3. Draft 1 was reviewed against the old code and the parity traces: approve with changes, three blockers. Draft 2 applied every finding, and the owner's decisions on draft 1's open questions (section 12). Draft 3 adds section 13: what the code changed while steps 1 to 7 were built. Where a section above and section 13 differ, section 13 is right. It belongs to `REWRITE.md` and stays inside `CONTRACT.md`.

Scope: `internal/health` (checks, `Gate`, drain blockers, post-roll verification), the production `internal/kube` package, the Kubernetes groups of `fleet.Snapshot` that phase 2 left `NotRequested`, the command `cluster upgrade-check`, the `HEALTH` section of `cluster describe`, `cluster list --show-health`, `cluster describe --check-readiness`, `nodegroup list --check-readiness`, and `nodegroup describe --show-workloads`.

Out of scope: every changing command and the TUI. The gate API they call is fixed here, with the verdict that `--dry-run` reports. Phase 4 builds the commands.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | A check is a struct value with a `Run` function, listed in a table. No interface, no setters. |
| 2 | Five outcomes: `Pass`, `Warn`, `Fail`, `Skipped`, `Unreadable`. No score. "Could not read" has one form: `Unreadable` with a `Failure`. |
| 3 | The runner, not the check, decides `Skipped`: a check declares what it needs, and the runner skips it when the input lacks it. |
| 4 | One `Gate(results, policy) Verdict` for every changing command and the TUI. A front end calls its confirm function on `Ask` and nothing else. The exit code comes from the findings alone. |
| 5 | A drain blocker is a blocking check that `--force` can override. Unreadable PDBs, nodes, or system pods block; unreadable CloudWatch metrics ask. |
| 6 | No Kubernetes access is an `Ask` reason, never a silent go. |
| 7 | Resource Balance is dropped. Its CPU numbers were the Capacity numbers counted again. |
| 8 | Insights and version skew are checks too, so `cluster upgrade-check` and the `cluster upgrade` readiness gate run one engine. |
| 9 | Reads are shared within a run and keep their errors: one node list, one PDB list. Each metric check sends its own `GetMetricData`. The gate is 2 round trips after the snapshot; `upgrade-check` is 2, or 3 with a rollback window. |
| 10 | `internal/kube` returns values and typed errors, writes nothing, imports no refresh package but `apidoc`, and makes no probe call. |
| 11 | Post-roll verification is a check set with the same types, against a baseline of nodes and Pending pods. New nodes that are not Ready are an issue, after a bounded wait. |

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
	// FailClosed says that an Unreadable blocks too: the check reads what a
	// roll can break. Without it an Unreadable asks. Nodes, Workloads,
	// DrainBlockers, Insights, and VersionSkew set it; Capacity and
	// ControlPlane do not, so a role without cloudwatch:GetMetricData can
	// still roll after a prompt, with the PDB gate in place.
	FailClosed bool
	// Forceable says that --force turns a Fail or an Unreadable of this
	// check into a Warn. Only DrainBlockers sets it.
	Forceable bool
	Needs     Needs
	Run       func(ctx context.Context, in *Input) Result
}

// Needs says what a check reads. The runner skips a check whose input lacks
// one of them, and the check never runs.
type Needs struct {
	Kube    bool // in.Kube
	Metrics bool // in.Metrics, the metrics.k8s.io client
}

// Input is everything the checks read. The command builds it once per run.
// No field is set after Run starts.
type Input struct {
	// Snapshot is the whole cluster as the command read it, with
	// Need.Nodegroups and Need.InstanceIDs: every nodegroup, not only the
	// targets, so Nodes and Capacity judge the cluster that absorbs the
	// drain.
	Snapshot *fleet.Snapshot
	// Targets names the nodegroups about to roll. Empty means the whole
	// cluster. Only DrainBlockers, ScaleDownBlockers, and the target rule
	// of Nodes read it.
	Targets []string
	// Clients are the AWS clients of the cluster's region.
	Clients *awsx.Clients
	// Kube is nil when there is no Kubernetes access. NoKube then says why,
	// such as "no kubeconfig context points at prod".
	Kube   kubernetes.Interface
	NoKube string
	// Metrics is nil without a metrics.k8s.io client.
	Metrics metricsv1beta1.NodeMetricsInterface
	Now     time.Time

	reads *reads // the shared reads of this run; see section 3
}

type Outcome string

const (
	Pass       Outcome = "Pass"
	Warn       Outcome = "Warn"
	Fail       Outcome = "Fail"
	Skipped    Outcome = "Skipped"    // a need is missing, or the check does not apply
	Unreadable Outcome = "Unreadable" // a read the verdict needs failed; Failures names it
)

// Result is what one check found. The runner fills Name, Blocking, and
// FailClosed from the Check, so a check cannot contradict its declaration.
type Result struct {
	Name       string
	Outcome    Outcome
	Blocking   bool
	FailClosed bool
	// Message is one line. For Skipped it is the reason. For Unreadable it
	// says what was read and what was not.
	Message string
	Details []string
	// Findings are the named things behind a Warn or a Fail, sorted: node
	// names, "namespace/pod", "namespace/pdb", deployment names. Worse
	// compares them. A numeric check (Capacity, ControlPlane, Quotas,
	// Utilization) leaves them empty.
	Findings []string
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
| The check does not apply to the cluster (no managed nodegroup and no Kubernetes, a control plane that emits no metrics, metrics-server not installed) | `Skipped`, by the check |
| A read failed and nothing of it was read | `Unreadable`, with the failure |
| A read partly failed and what was read gives `Pass` or `Warn` | `Unreadable`, with the failures; the message says what was read |
| A read partly failed and what was read gives `Fail` | `Fail`, with the failures |

The last three rows replace the old five forms of "could not read": a Warn with score 60, a Pass with a failure list, a Skipped with the error in the message, a nil summary, and "No critical pods found". A check never reports a count it did not read.

`Skipped` and `Unreadable` differ by cause: a missing client or a check that does not apply is `Skipped`; a client that exists and answers 401, 403, or 5xx is `Unreadable`. A client that cannot connect at all is the third case, "no access", which section 4 treats as a question.

### Runner

```go
// Run runs checks against in, all at once, and returns one Result per
// check in the order of checks, followed by the Access result. A check
// whose Needs the input lacks returns Skipped with in.NoKube or "no
// metrics client" as its message. A check whose context ends returns
// Unreadable with reason Interrupted or Timeout.
func Run(ctx context.Context, checks []Check, in *Input) []Result

// Preflight returns the checks a change runs before it starts, in display
// order: Nodes, Capacity, ControlPlane, Workloads, DrainBlockers,
// PDBCoverage, Utilization, Quotas.
func Preflight() []Check

// AWSOnly returns the checks of Preflight that need no Kubernetes client,
// for cluster list --show-health.
func AWSOnly() []Check
```

`Run` uses `fanout.ForEach` with the limit `len(checks)`, so every check starts at once (`ForEach` turns a limit of 0 into 8, `foreach.go:18-21`, which would queue checks). The request limit of `awsx` bounds the AWS calls, and the Kubernetes reads are at most two waves (section 3).

The `Access` result is the runner's own, named `Kubernetes`, and it is never `Skipped` by Needs:

| Case | Outcome |
|---|---|
| `in.Kube` is nil | `Warn`: "no Kubernetes access: <NoKube>; the Workloads, DrainBlockers, PDBCoverage, and Utilization checks did not run" |
| A client is set and every Kubernetes read of the run failed to connect (a dial error or a timeout, `awsx.Classify` gives `NetworkError` or `Timeout`) | `Warn`: "Kubernetes API unreachable via context X: <error>". The runner turns the `Unreadable` results of the Kubernetes checks into `Skipped` with that message, so the unreachable API is one question, not a block. |
| A client is set and a read answered, or failed with an API answer | `Pass`, with the context in the message |

The Warn is advisory, so the gate asks (section 4). EKS enforces PodDisruptionBudgets during a managed drain and fails the update with `PodEvictionFailure` when one refuses an eviction, so a roll after an explicit accept is tolerable; a silent go is not, for a tool that drains nodes.

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
	Findings []string      `json:"findings,omitzero"`
}

// Health is the health part of ClusterDescription. Summary is the worst
// outcome of the checks, in the order Fail, Unreadable, Warn, Pass; a
// Skipped check does not count, and a run of Skipped checks only is Pass.
type Health struct {
	Summary HealthOutcome  `json:"summary"`
	Checks  []HealthResult `json:"checks"`
}

type GateDecision string // Go, Ask, Block

// Gate is the verdict of the health gate in a Plan or a Run (phase 4).
type Gate struct {
	Decision GateDecision   `json:"decision"`
	Reasons  []string       `json:"reasons"` // "Nodes: 2/6 nodes ready, ..."
	Accepted []string       `json:"accepted"` // the reasons --yes went past
	Forced   []string       `json:"forced"`   // the reasons --force went past
	Checks   []HealthResult `json:"checks"`
}

// ClusterDescription gains:
Health *Health `json:"health,omitzero"`
```

The encoder rejects a `null`, so the mappers allocate every list: `checks`, `details`, `findings`, `reasons`, `accepted`, `forced` are `[]` when empty, and so are `insights`, `nodegroups`, `addons`, `resources` of section 7. The failures of the checks go in the document's top-level `failures`, as every other read does. A check's failure has `Kind: Cluster` or `Nodegroup`, `Operation` set to the IAM action for an AWS read and `""` for a Kubernetes read, and `Cluster` and `Region` set by the runner.

The `UpgradeCheck` and `InsightDescription` documents are in section 7.

## 2. The checks

Every old check, with its fate. Thresholds are the old ones. A threshold marked "arbitrary" has no source in the old code or the docs.

| Old check | Fate | Reason |
|---|---|---|
| Node Health | Keep as `Nodes` | The blocking signal for a roll |
| Cluster Capacity | Keep as `Capacity`; absorbs the spread line of Resource Balance | One CloudWatch read, one verdict |
| Resource Balance | Drop | Its `max > 90` rule (`balance.go:66`) is the peak rule of Capacity (`capacity.go:32-35`) with other numbers, over the same data. The CPU of the cluster was counted three times: the Capacity mean, the Capacity peak, and the Balance max. Two of its warnings have no equivalent and are dropped on purpose: a peak between 80 and 85 (`balance.go:74`; Capacity warns from 85) and a CPU spread above 25 points (`:70,74`). Neither names an action a user can take before a roll, and the std-dev stays as a detail line of Capacity, with no effect on the outcome. |
| Control Plane | Keep as `ControlPlane` | The etcd limit is a real blocker |
| Service Quotas | Keep as `Quotas` | A roll surges nodes against the vCPU quota |
| Critical Workloads | Keep as `Workloads` | |
| Pod Disruption Budgets | Split into `DrainBlockers` (blocking, fail closed, forceable) and `PDBCoverage` (advisory) | The old check reported a drain blocker as a warning, and then the command ran `DrainBlockers` again to refuse. Two paths, two exit codes. |
| Node Utilization | Keep as `Utilization` | The only memory signal |
| (none) | Add `Insights` and `VersionSkew` | The readiness parts of `cluster upgrade-check` and the `cluster upgrade` gate, as checks, so one engine decides |

### Nodes (blocking, fail closed)

Inputs: `Snapshot.Nodegroups`, every nodegroup of the cluster (no call; the command read them). With Kubernetes: one `list nodes`, no selector, from the shared reads.

Nodegroup statuses count in both modes, as the old code did (`nodes.go:92-104` sets them, and `:109-119` keeps them beside the real counts): a `DEGRADED` or other non-`ACTIVE`, non-`CREATING`, non-`UPDATING` nodegroup is a finding, and a nodegroup in `Targets` with any status but `ACTIVE` is a `Fail` on its own, which is what the old upgrade engine refused outside the health check (trace `cluster-upgrade-nodegroup-degrades-mid-run`: "ng-b is DEGRADED, not ACTIVE").

| Case | Rule | Outcome |
|---|---|---|
| Any mode | A target nodegroup is not `ACTIVE` | `Fail` |
| Kubernetes read | `ready`, `joining`, `notReady` from the Ready condition. Joining: created less than 10 minutes ago (`nodes.go:302`) and the Ready reason is `KubeletNotReady` or `NodeStatusNeverUpdated`, or no Ready condition. Findings: the NotReady and joining nodes and the problem nodegroups. | |
| | No node at all | `Fail`: "no nodes found" |
| | No finding | `Pass` |
| | `ready == 0` and `settled > 0` (`settled = total - joining`) | `Fail` |
| | `ready == 0`, all others joining, no problem nodegroup | `Warn` |
| | `ready * 100 < settled * 50` (`nodes.go:265`) | `Fail` |
| | Otherwise | `Warn` |
| No Kubernetes (estimate) | `ACTIVE` nodegroups count their desired size as ready; `CREATING` and `UPDATING` are left out; the other statuses are problems. The estimate never applies the 50 percent rule (`nodes.go:196`). | |
| | No managed capacity, no problem | `Skipped`: "no managed nodegroup capacity; node readiness needs Kubernetes access" |
| | Every nodegroup mid-change, nothing wrong | `Warn` |
| | A problem nodegroup, some ready | `Warn` |
| | No ready, a problem | `Fail` |
| | No problem | `Pass`, message ends in "(estimated)" |
| `Have.Nodegroups` is `Failed` or `Partial` | | `Unreadable`, with the snapshot's nodegroup failures |
| The node list fails with a client present | | `Unreadable`. The estimate is for "no access", not for a failed read. |

The old code warned on a transient EKS error and blocked on a permanent one (`nodes.go:180-190`). The retry layer handles transient errors before a check sees one, so the check has one rule. The 90 cap of the estimate (`nodes.go:270`) goes with the score.

### Capacity (blocking)

Inputs: `Nodegroup.InstanceIDs` of every nodegroup (section 3). One `GetMetricData` per 500 queries, sent by this check: `AWS/EC2 CPUUtilization` per instance, period 300 s, stat Average, over the last 10 minutes (`capacity.go:157-158,175`), paged with `awsx.ListAll`.

| Case | Outcome |
|---|---|
| No instance ID in the snapshot (no managed nodegroup, or none has an Auto Scaling group) | `Skipped`: "no managed nodegroup instances" |
| `GetMetricData` fails | `Unreadable`; the message names the IAM action, `cloudwatch:GetMetricData` |
| No instance has a datapoint | `Warn`: "no CPU metrics reported for the instances in the last 10 minutes". A read that answered with nothing is not a failure, and an unmeasured cluster deserves a question. The parity scenarios `nodegroup-update-warnings-unattended` and `-require-healthy` pin this. |
| Headroom `100 - mean >= 30` (`capacity.go:23`) | `Pass` |
| Headroom `>= 15` (`capacity.go:24`) | `Warn` |
| Else | `Fail` |
| Peak `>= 95` (`capacity.go:34`) | `Fail`, whatever the mean |
| Peak `>= 85` (`capacity.go:33`) and the mean passes | `Warn` |

Details: mean, peak, the number of instances with data, and the population standard deviation of per-instance CPU (the old Balance number, informational). The 30/15 and 95/85 lines are arbitrary; the docs state them, so they stay.

### ControlPlane (blocking)

One `GetMetricData`, sent by this check, of five `AWS/EKS` metrics with dimension `ClusterName`, period 900 s over 15 minutes (`controlplane.go:35,85-93`): `etcd_mvcc_db_total_size_in_use_in_bytes` (Maximum), `apiserver_request_total`, `_5XX`, `_429` (Sum), `scheduler_pending_pods` (Maximum).

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

One `GetServiceQuota` for `ec2` / `L-1216C47A` (`quotas.go:20-21`), the On-Demand Standard vCPU quota, and one `GetMetricData` of `AWS/Usage ResourceCount` (Service EC2, Type Resource, Resource vCPU, Class Standard/OnDemand), period 60 s over 10 minutes, stat Maximum, `ScanBy` newest first (`quotas.go:85-107`). Both start in the same wave. The three metric checks cannot share one `GetMetricData`: a request has one time window and one `ScanBy`, and they need 10 minutes, 15 minutes, and 10 minutes newest-first.

| Case | Outcome |
|---|---|
| `GetServiceQuota` or the metrics call fails | `Unreadable` (the old code skipped) |
| The quota has no value, or the usage metric has no datapoint | `Skipped`: "AWS/Usage vCPU metric not reported" |
| Usage `>= 95` percent (`quotas.go:23`) | `Fail` |
| Usage `>= 85` percent (`:22`) | `Warn` |
| Else | `Pass` |

### Workloads (blocking, fail closed, needs Kubernetes)

Three `list pods` calls, one for each of `kube-system`, `kube-public`, `kube-node-lease` (`workloads.go:67-71`), in the first Kubernetes wave. `Succeeded` pods and stale `Failed` pods (`staleFailedPod`, `workloads.go:27-44`, kept as is) are left out. Findings: the problem pods as `namespace/name`.

| Case | Outcome |
|---|---|
| A list fails and no namespace was read | `Unreadable` |
| A list fails, the others give Pass or Warn | `Unreadable`, message "kube-system: 12/12 running; kube-public not read" |
| A list fails, the others give Fail | `Fail` with the failure |
| No pod in the three namespaces | `Warn`: "no pods in kube-system" |
| Every pod Running with every container ready | `Pass` |
| Running-and-ready `>= 90` percent (`workloads.go:157`) | `Warn` |
| Else | `Fail` |

### DrainBlockers (blocking, fail closed, forceable, needs Kubernetes)

Reads, from the shared reads of section 3: the PDB list, the node list, and the pods of each namespace that has an at-risk PDB or more than one PDB. With `Targets`, the target node set is the nodes whose `eks.amazonaws.com/nodegroup` label is in `Targets`, taken from the one node list.

The rules are the old `findDrainBlockers` and `findMultiPDBPods` (`pdbs.go:264-370`), behaviour-frozen:

- A PDB is at risk when `allowed <= 0 && (expected > 0 || unsynced)` (`pdbs.go:69`). Unsynced: `observedGeneration < generation`, or the `DisruptionAllowed` condition has reason `SyncFailed` (`:101-107`).
- A pod counts against an at-risk PDB when the eviction API would consult it: not `Succeeded`, `Failed`, `Pending`, or deleting; a not-Ready pod only under `IfHealthyBudget` while the budget is not met (`pdbs.go:602-631`).
- A pod that more than one PDB selects is a blocker on its own, and its readiness does not matter: the eviction API refuses it before it looks at any budget (`pdbs.go:322-370`, the `evictionIgnoresPDBs` test at `:354`).
- With `Targets`: a PDB counts when it gates a pod on a target node. When no node carries a target label, the check falls back to the cluster-wide list, unless the targets' desired size in the snapshot is 0, which is "nothing to drain" (`pdbs.go:264-293`). The desired size comes from the snapshot, not from a `DescribeNodegroup` call.
- Without `Targets`, every at-risk PDB and multi-PDB pod in the cluster counts, system namespaces included.

Findings: "namespace/pdb" for each blocker and "namespace/pod" for each multi-PDB pod.

| Case | Outcome |
|---|---|
| The PDB list fails | `Unreadable` |
| The node list fails | `Unreadable`. The old code fell back to cluster-wide; a failed read is now a failure. |
| A pod list of a namespace fails | `Unreadable`, unless another namespace already gives a blocker, then `Fail` with the failure |
| Targets have no nodes and desired 0 | `Pass`: "nothing to drain" |
| A blocker | `Fail`, one detail line per blocker in the old `DrainBlockerSummary` words |
| None | `Pass` |

`ScaleDownBlockers(nodegroup string, remove int32) Check` is the scale-down form, the old `pdbs.go:456-504` rule (worst-case loss over the `remove` nodes with the most covered pods), built for `nodegroup scale` in phase 4. It is a Check value with a closure, in this package, with the same flags and outcomes. `remove <= 0` is `Pass`.

### PDBCoverage (advisory, needs Kubernetes)

One `list deployments` across all namespaces and the shared PDB list. The old code listed namespaces and then deployments per namespace (`pdbs.go:146-213`): 1 + N calls in series. Deployments in `kube-system`, `kube-public`, and `kube-node-lease` are left out (`pdbs.go:75-79`); `default` counts. Findings: the uncovered deployments.

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

### Insights (blocking, fail closed, AWS only)

```go
type InsightsOptions struct {
	Category string // "" is UPGRADE_READINESS
	// ForVersion, when set, keeps the insights whose kubernetesVersion is
	// that minor: the cluster upgrade gate of phase 4.
	ForVersion fleet.Version
}
func Insights(opts InsightsOptions) Check
```

One paged `ListInsights` with the category filter and no status filter. The check keeps the raw list in the input's reads, so `upgrade-check` prints it without a second call. The verdict is over every insight of the category; `--status` of `upgrade-check` narrows what is shown, not what is judged (section 7). Findings: the names of the ERROR, UNKNOWN, and WARNING insights.

| Case | Outcome |
|---|---|
| The call fails | `Unreadable` |
| An `ERROR` insight | `Fail` |
| An `UNKNOWN` insight, or a status this build does not know | `Fail`: EKS could not evaluate it (`readiness.go:28-30`, `insights.go:194-205` of upgrade) |
| No insight at all in `UPGRADE_READINESS` | `Warn`: "EKS has not evaluated upgrade insights yet" in `upgrade-check`. The phase 4 gate, which refreshes first, makes it `Fail`, with `ForVersion` set. That is the one rule the two callers do not share, and it is a field, not a copy. |
| A `WARNING` insight | `Warn` |
| Else | `Pass` |

The insights refresh (`StartInsightsRefresh`, `DescribeInsightsRefresh`) belongs to phase 4's `change` package. `upgrade-check` reads the insights EKS has, as today.

### VersionSkew (blocking, fail closed, no call)

From the snapshot: each nodegroup's minor against the control plane, and each add-on against its latest compatible version (`Need.LatestAddons`). Findings: the nodegroups and add-ons behind.

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
// reads holds the Kubernetes lists of one Run, each made once. Every
// caller gets the same answer or the same error: a list that failed is not
// tried again within the run, so two checks that share a read cannot
// disagree. sync.OnceValues keeps the error; fanout.Memo does not, which is
// why it is not used here.
type reads struct {
	nodes       func(context.Context) (*corev1.NodeList, error)
	pdbs        func(context.Context) (*policyv1.PodDisruptionBudgetList, error)
	deployments func(context.Context) (*appsv1.DeploymentList, error)
	nodeMetrics func(context.Context) (*metricsv1beta1.NodeMetricsList, error)
	// pods lists one namespace, once per namespace.
	pods func(ctx context.Context, namespace string) (*corev1.PodList, error)
	// insights is the raw ListInsights answer of the Insights check.
	insights func(context.Context) ([]ekstypes.InsightSummary, error)
}
```

Each function is built with `sync.OnceValues` over the real call (the pods function keeps a map of them under a mutex). A caller's context cancellation returns its own error without touching the shared result.

### The gate

The command already holds a snapshot of the whole cluster with `Need{Nodegroups: true, InstanceIDs: true}` (2 to 3 waves, phase 2). The gate adds:

| Wave | AWS | Kubernetes |
|---|---|---|
| 1 | `GetMetricData` × 3 (Capacity, one per 500 instances; ControlPlane; Quotas usage), `GetServiceQuota` | `list nodes`, `list poddisruptionbudgets`, `list deployments`, `list nodes` (metrics), `list pods` × 3 system namespaces |
| 2 | | `list pods` for each namespace that has an at-risk PDB or more than one PDB and is not a system namespace |

So the gate is 2 sequential round trips, 1 when no user namespace has such a PDB. The AWS wave and the Kubernetes wave 1 run together.

The old gate, traced through `preflightHealthCheck` and `drainGate` (`actions_update.go:273,506-571`, `drain_gate.go:64-92`): `DescribeCluster` for the kube target, a probe `list namespaces`, then the drain gate (`list poddisruptionbudgets`, `list nodes`, `list pods` per namespace, `DescribeNodegroup` per target), then the checks in parallel, of which the PDB check is the deepest: `list poddisruptionbudgets`, `list nodes`, `list pods` per namespace, `list namespaces`, `list deployments` per namespace, and Capacity: `ListNodegroups`, `DescribeNodegroup` × N, `DescribeAutoScalingGroups`, `GetMetricData`. About 9 sequential round trips, and `ListNodegroups` + `DescribeNodegroup` × N three times over (nodes, capacity, drain gate). Unsure by one: the drain gate's per-nodegroup loop runs in series.

### `cluster upgrade-check`

| Wave | Calls |
|---|---|
| 1 | `DescribeCluster`, `ListNodegroups`, `ListAddons`, `DescribeClusterVersions`, `ListInsights` (the category), `ListUpdates`, `GetMetricData` (control plane) |
| 2 | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` per add-on, `DescribeUpdate` × K (the cluster's updates) |
| 3 | `ListInsights` (`ROLLBACK_READINESS`), only when wave 2 found an open rollback window |

2 round trips, 3 with a rollback window. The old command (`insights.go:328-431`, `upgradecheck.go:126-157`): `DescribeCluster`, `ListInsights`, `ListNodegroups`, `DescribeNodegroup` one after another, `ListAddons`, then for each add-on `DescribeAddon` and `DescribeAddonVersions`, then `DescribeClusterVersions`, `ListUpdates` with its describes, `ListInsights` for rollback, and `GetMetricData`. That is 8 + N + 2M round trips (`insights.go:371-427`): 14 for the fake cluster with 2 nodegroups and 2 add-ons, which fits the measured 769 ms at a 50 ms mean. Unsure: how many `DescribeUpdate` calls `versionHistory` makes; the new design reads every update ID in one wave, which is the same count in one round trip.

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

`cluster describe` always has a `HEALTH` section (contract change 14; owner decision 3). It runs `Preflight()` with no targets; the Kubernetes checks are `Skipped` unless `--check-readiness` connects, and the `Kubernetes` result then reads `Warn` in the section, which is the hint to pass the flag. The `Need.InstanceIDs` read is one more call in wave 2 and the metric calls are wave 3, so the command goes from 2 to 3 round trips.

`cluster list --show-health` runs `AWSOnly()` for each cluster (owner decision 4): `ListNodegroups`, `DescribeNodegroup` × N, `DescribeAutoScalingGroups`, then the metric calls and `GetServiceQuota`: 3 waves and 6 + N calls per cluster beyond the list. The `HEALTH` column shows `Health.Summary`.

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
	Force          bool // --force: a Fail or Unreadable of a Forceable check becomes a warning
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
	// Exit is the code of the findings, whatever the policy: 4 when a
	// check is Unreadable, else 3 when a blocking check fails and is not
	// forced, else 2 when there is any warning, else 0. A dry run exits
	// with it. A blocked run exits with it.
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

// Worse returns the checks of cur that are worse than in prev: a worse
// outcome, a finding that prev did not have, a check that ran in prev and
// is Skipped in cur, or a check that prev does not have and that is not a
// pass (added in phase 4: nobody has seen such a check).
func Worse(prev, cur []Result) []Reason
```

### Ranks

`--force` is applied first: for each `Forceable` result that is `Fail` or `Unreadable`, the outcome becomes `Warn`, the reason goes in `Forced`, and an `Unreadable` keeps its failures. Then every result gets a rank; `Skipped` has no rank and never changes the decision.

| Rank | Result | Decision |
|---|---|---|
| 1 | Blocking `Fail` | `Block` |
| 2 | `Unreadable` of a `FailClosed` check | `Block` |
| 3 | `Unreadable` of any other check, `Fail` of a non-blocking check, any `Warn` (the `Kubernetes` result included) | `Ask` |
| 4 | `Pass`, `Skipped` | `Go` |

The worst rank decides. Then the policy: `Ask` with `--require-healthy` is `Block`; `Ask` with `--yes` is `Go` with the rank 3 reasons in `Accepted`; `--require-healthy` beats `--yes`. `Block` is `Block` under every policy.

### The exit code

`--yes` does not change the code, so a dry run with `--yes` and warnings exits 2, as the real run would ask. `--require-healthy` does: a `Block` exits 3 at least, whatever made it one.

1. 4 when any result is `Unreadable`, forced or not: something a gate needed was not read.
2. Else 3 when the decision is `Block`: a rank 1 or rank 2 result is left, or `--require-healthy` is set and a rank 3 result is left.
3. Else 2 when any rank 3 result is left, which includes a forced blocker and an accepted warning.
4. Else 0.

Exit 2 is therefore the code of a dry run whose real run would ask, and of nothing else.

### The table

One row per case; the test copies it. "B Fail" is a blocking Fail, "FC Unr" an Unreadable of a FailClosed check, "Adv" an advisory check, "Drain" the DrainBlockers check.

| # | Worst results | Policy | Decision | Exit |
|---|---|---|---|---|
| 1 | all Pass or Skipped | any | Go | 0 |
| 2 | no check ran | `Skip` | Go, `Skipped` | 0 |
| 3 | a Warn | default | Ask | 2 |
| 4 | a Warn | `--yes` | Go, in `Accepted` | 2 |
| 5 | a Warn | `--require-healthy` | Block |3 |
| 6 | a Warn | `--yes --require-healthy` | Block |3 |
| 7 | Adv Fail | default | Ask | 2 |
| 8 | Adv Fail | `--yes` | Go | 2 |
| 9 | Adv Fail | `--require-healthy` | Block |3 |
| 10 | Adv Unreadable (Quotas, PDBCoverage, Utilization) | default | Ask | 4 |
| 11 | Adv Unreadable | `--yes` | Go, failures kept | 4 |
| 12 | Adv Unreadable | `--require-healthy` | Block | 4 |
| 13 | Capacity or ControlPlane Unreadable | default | Ask | 4 |
| 14 | Capacity or ControlPlane Unreadable | `--yes` | Go, failures kept | 4 |
| 15 | Capacity or ControlPlane Unreadable | `--require-healthy` | Block | 4 |
| 16 | `Kubernetes` Warn (no access or unreachable) | default | Ask | 2 |
| 17 | `Kubernetes` Warn | `--yes` | Go, in `Accepted` | 2 |
| 18 | `Kubernetes` Warn | `--require-healthy` | Block |3 |
| 19 | B Fail (Nodes, Capacity, ControlPlane, Workloads) | any | Block | 3 |
| 20 | B Fail and an Adv Unreadable | any | Block | 4 |
| 21 | FC Unr (Nodes, Workloads) | any | Block | 4 |
| 22 | Drain Fail | default or `--yes` | Block | 3 |
| 23 | Drain Fail | `--force` | Ask, in `Forced` | 2 |
| 24 | Drain Fail | `--force --yes` | Go, in `Forced` and `Accepted` | 2 |
| 25 | Drain Fail | `--force --require-healthy` | Block |3 |
| 26 | Drain Unreadable | default or `--yes` | Block | 4 |
| 27 | Drain Unreadable | `--force` | Ask, in `Forced`, failures kept | 4 |
| 28 | Drain Unreadable | `--force --yes` | Go, failures kept | 4 |
| 29 | Drain Unreadable | `--force --require-healthy` | Block | 4 |
| 30 | Drain Fail and a B Fail | `--force` | Block | 3 |
| 31 | a Warn and a Capacity Unreadable | `--yes` | Go | 4 |
| 32 | `--force` with no Forceable finding | `--force` | as rows 1 to 21 | |

Row 25 is the old binary's behaviour: `--require-healthy` makes every warning a stop, and a forced blocker is a warning. The stop exits 3, as every `Block` does (owner decision during phase 4; draft rows 5, 6, 9, 18, and 25 said 2). Rows 11, 14, 27, and 28 are why a run that goes on carries `Verdict.Failures` and exits 4 at its end (`exit-codes.md`, `nodegroup update` row 4).

Two differences from the old gate. An unreadable read of nodes, system pods, or PDBs blocks in every command, where the old Node Health warned on a transient error and the old upgrade gate exited 1. And a drain blocker is a blocking check in the summary, not a warning plus a second refusal. The old Capacity check warned on any CloudWatch error; the new one asks, which is the same decision with the failure recorded (owner, finding 5 of the draft 1 review).

### How a front end uses it

```go
results := health.Run(ctx, health.Preflight(), in)
v := health.Gate(results, policy)
// print the report (section 8) on stdout for a human run, stderr for a machine run
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

`cmdutil.Confirm(ctx, ios, question) (bool, error)` and `cmdutil.RequireYes` do not exist yet; `cmdutil.ReadLine` does. Phase 4 adds both with its first changing command, as `REWRITE.md` describes them. The gate needs only a `func(context.Context, string) (bool, error)`. `RequireYes` has already refused a machine run or a run without a terminal that has no `--yes`, so `confirm` is only ever called where a prompt can show. `--quiet` does not change the gate: the question still shows. The old rule "quiet on a TTY never auto-accepts" (`gates_test.go:21`) holds because quiet never accepts anything.

`--dry-run` runs the same checks and the same `Gate` with the real policy, prints the verdict in the plan, and exits with `v.Exit`. Nothing in the gate knows about dry runs.

### When the gate runs

1. In the plan step, before any change, once per cluster with `Targets` set to every nodegroup the plan would roll. For `cluster upgrade` this is before the control plane moves, so a drain blocker is known up front (CONTRACT.md, "Behaviour the new CLI does not copy").
2. Again right before each phase that rolls nodes, with `Targets` set to that phase's nodegroups. "Again" means a new `Input`: a fresh snapshot of the whole cluster with `Need{Nodegroups: true, InstanceIDs: true}`, as the plan step read it, and a new `reads`, so every Kubernetes list and every metric is read anew. Only `Targets` narrows, and only the drain and target rules read it, so `Worse(plan, now)` compares the same population. The old gate did the same: `RunAllChecks` is cluster-wide before each roll (`upgrade_gate.go:162`).
3. The second run is skipped when nothing could have changed between the two: the plan step and the phase are the same step of a single-batch run (`nodegroup update`, `nodegroup scale`, `addon update`), and no prompt, wait, or other phase came between the gate and the submit. `cluster upgrade` and `cluster rollback` always have a phase between.

The pre-phase policy:

| Gate before the phase | `Worse(plan, now)` | Default (a prompt can show) | `--yes` | `--require-healthy` |
|---|---|---|---|---|
| `Block` | any | Block, the phase fails, exit by the table | Block | Block |
| `Go` | empty | proceed | proceed | proceed |
| `Ask` | empty | proceed: the user already answered these reasons at the plan step | proceed | cannot happen: `Ask` is `Block` |
| `Ask` or `Go` | not empty | ask again, with the `Worse` reasons as the question | proceed, the reasons go in `Accepted` (old gate: `--yes` proceeds, `upgrade_gate.go:170-174`) | Block, exit 3 |

A run whose prompt cannot show (the TUI without a question channel, a machine run) treats "ask again" as a failure of the phase, exit 1, as the old gate did when `confirm` was nil (`upgrade_gate.go:176-178`).

Exit codes of a run that went past the gate are phase 4's: the gate contributes `Verdict.Failures` and nothing else.

## 5. Post-roll verification

```go
// Baseline is what verification compares against, taken before the roll.
type Baseline struct {
	// Nodes is the set of node names in the cluster.
	Nodes map[string]struct{}
	// Pending is the set of "namespace/name" of the Pending pods, less the
	// pods pinned to a node that is not in Nodes (verify.go:38-56).
	Pending map[string]struct{}
	// Failures are the lists that failed. NewPendingPods and NodesReady are
	// then Unreadable, not Skipped: the client was there and did not
	// answer.
	Failures []apidoc.Failure
	// Taken is false when there was no Kubernetes client.
	Taken bool
}

// TakeBaseline lists the nodes (no selector) and the Pending pods, in one
// wave. With a nil client it returns Taken false and no failure.
func TakeBaseline(ctx context.Context, kube kubernetes.Interface) Baseline

// Verify returns the checks that run after a roll of nodegroups:
// NodegroupActive, NodesReady, NewPendingPods. wait bounds NodesReady.
func Verify(nodegroups []string, base Baseline, wait WaitOptions) []Check

// WaitOptions bound the wait of NodesReady. Interval is --poll-interval.
// Deadline is the remaining --wait-timeout, or zero for none. After and
// Now come from the Factory, so a test drives the wait with a handshake.
type WaitOptions struct {
	Interval time.Duration
	Deadline time.Time
	After    func(time.Duration) <-chan time.Time
	Now      func() time.Time
}

// VerifyExit maps the results to the exit code of the verification: 5 when
// a check is Fail, else 4 when one is Unreadable, else 0.
func VerifyExit(results []Result) int
```

| Check | Reads | `Fail` (exit 5) | `Unreadable` (exit 4) | `Skipped` |
|---|---|---|---|---|
| `NodegroupActive` | The snapshot of the rolled nodegroups, which the command re-reads after the wait | A nodegroup whose status is not `ACTIVE` | A nodegroup with `State: Failed` | never |
| `NodesReady` | One `list nodes`, no selector, shared with `NewPendingPods`; the check keeps the nodes that carry the label of a rolled nodegroup and are not in `Baseline.Nodes`: the new nodes | A new node that is not Ready at the end of the wait | The list fails, or `Baseline.Failures` names the node list | No Kubernetes client (`Taken` false) |
| `NewPendingPods` | One `list pods` with the field selector `status.phase=Pending`, and the node list above for the pinned-node rule, which filters by node name in code over the unselected list, as the old code did (`verify.go:45`) | A Pending pod that is not in the baseline, up to five named | The list fails, or `Baseline.Failures` names the pod list | No Kubernetes client |

The wait of `NodesReady`: a new node that is not Ready is polled every `Interval` until it is Ready or the bound passes. The bound is 2 minutes from the first list, and never later than `Deadline`; with `--wait-timeout 0` the bound is 2 minutes. EKS marks a managed update successful only after the new nodes joined and were Ready, so a NotReady new node after the update is a node that lost readiness or a lingering object; the 2 minutes absorb a kubelet that is late by seconds and no more. The 2 minutes are arbitrary. The old code never read the nodes. Old Node objects that EKS has not yet deleted, and nodes of other nodegroups or of an autoscaler, are in the baseline or carry another label, so they cannot fail the check. The pod list runs once, in the first wave, with the first node list.

`NodesReady` is new (CONTRACT.md row "Post-roll verification does not read node readiness"). `NewPendingPods` with a failed list is `Unreadable` (row "If the pod list fails after a roll"). The old `VerifyPostRoll` kept its skip reasons in the `checks` list of the document (`verify.go:83-96`); the new document has the results of this check set under `verification`, with their outcomes, through the same `HealthResult` shape.

## 6. Kubernetes client resolution

`internal/kube` imports client-go and `apidoc` only. It does not import `health`: `cmdutil` imports `kube` for the cluster of a read-only command, and the `cmdutil` import test forbids `fleet`, which `health` imports.

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
	// Metrics is the metrics.k8s.io node lister of the same config, the
	// client-go type, so this package needs nothing of health.
	Metrics   metricsv1beta1.NodeMetricsInterface
	Selection Selection
}

// Connect returns a client for target. It makes no request.
func Connect(target Target, opts Options) (*Client, error)

// MismatchError says that no config points at the target. A command turns
// it into the NoKube reason of the health input and goes on.
type MismatchError struct {
	Target    Target
	Context   string // the current context that was considered
	Server    string
	InCluster bool
}

func (e *MismatchError) Error() string
func (e *MismatchError) Hint() string // "aws eks update-kubeconfig --name prod --region us-east-1, or pass --kube-context"
```

The matching rules are the old `targetCandidates` (`kubetarget.go:180-263`), ported:

1. Locate the kubeconfig: `--kubeconfig`, else `$KUBECONFIG` (a list; used when one file exists), else `~/.kube/config`. An explicit path that does not exist is an error. No file at all falls through to in-cluster config, except that `--kube-context` with no kubeconfig is an error (`kubetarget.go:188-190`).
2. With `--kube-context`: that context, or an error when it is not in the file. `Unverified` is set when its server does not match the endpoint. Nothing else is tried.
3. Else the contexts whose server matches the endpoint, by `host:port` after normalising (`NormalizeEndpoint`, `kubetarget.go:104-129`): the current context first, then the rest by name. The first one that builds a config wins. No endpoint, or no match, is a `*MismatchError`.
4. In-cluster config is used only when its host matches the endpoint or `InClusterName` equals the target name. Else a `*MismatchError` with `InCluster` set.

Differences from the old code:

- **No probe** (owner decision 5). The old code listed namespaces with a 5 s timeout on each candidate and tried the next on failure (`kubeclient.go:129-139`). That is one round trip before any check. One candidate is chosen, the first match. A context that cannot be reached shows on the `Kubernetes` result of the run (section 1), which asks; a context that answers 403 makes the checks `Unreadable`.
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

A command turns the error into `Input.NoKube`, and the runner's `Kubernetes` result carries it. The report shows it as a warning line, and the gate asks (section 4). The one stderr line a read command prints:

```text
warning: no Kubernetes access to prod: kubeconfig context "staging" (server https://...) does not point at it
  add one with: aws eks update-kubeconfig --name prod --region us-east-1, or pass --kube-context
```

Tests: `kubetest.NewServer(world, region, cluster)` sets the cluster's endpoint in the world to the server's URL, so the fake EKS reports it and `Connect` matches by `host:port`. `srv.WriteKubeconfig(path)` writes the file that `Options.Kubeconfig` names. An in-process test can also set `Factory.Kube` to return `kubernetes.NewForConfig(srv.Config())`. The first form tests `Connect` and the whole-command goldens; the second form is for the check tables, which still go through the server (section 10).

## 7. `cluster upgrade-check`

```text
Use:   upgrade-check [cluster]
Short: Report whether the cluster is ready for its next upgrade
Args:  at most 1
Flags: --category, --status, --show-passing, --insight, --exit-zero, -o (table, json, yaml, plain)
```

The cluster resolves as a read-only command does. `--category` is `UPGRADE_READINESS` by default, or `MISCONFIGURATION` or `ROLLBACK_READINESS`. `--status` is repeatable and filters the insights shown; `PASSING` in it implies `--show-passing`. The verdict is judged on every insight of the category, so `--status PASSING` cannot read `Ready` while an `ERROR` insight exists. The old command judged the filtered list (`upgradecheck_exit_test.go:87` expects exit 2 for `--status WARNING` with an `ERROR` present); contract edit 7 records the change.

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
	Reasons      []string       `json:"reasons"`
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
	Cluster        string            `json:"cluster"`
	Region         string            `json:"region"`
	Insight        Insight           `json:"insight"`
	Recommendation string            `json:"recommendation,omitzero"`
	Resources      []string          `json:"resources"`
	AdditionalInfo map[string]string `json:"additionalInfo,omitzero"`
	Deprecations   []Deprecation     `json:"deprecations"` // Usage, ReplacedWith, StopServingVersion, StartServingReplacementVersion, ClientStats
	Failures       Failures          `json:"failures"`
}
```

Every list above is allocated by the mapper, so it encodes as `[]` when empty.

Readiness and its precedence, from the gate verdict of the three checks:

| Verdict | Readiness | Exit |
|---|---|---|
| `Block` from a `Fail` | `Blocked` | 3, or 4 when a failure is in the document |
| `Block` from an `Unreadable` only | `Incomplete` | 4 |
| `Ask` | `Review` | 2, or 4 with a failure |
| `Go` | `Ready` | 0 |

The label follows the old precedence, blocked before incomplete before review (`readiness.go:100-109`), so the human reads the strongest finding. The exit code follows `CONTRACT.md`: the highest code wins, so a blocked check with an unread nodegroup exits 4 (owner decision 2; the old command exited 3 there). `--exit-zero` turns 2, 3, and 4 into 0; an error or an interrupt stays 1. A support posture of `Extended` or `Unsupported` is a reason for `Review` and shows in the header; it does not change the code (the old command did not either).

`--insight <id>`: `DescribeInsight` with the ID, or with the full ID whose first 8 characters the table printed. The command lists the insights of `--category` only when the value is not a full ID, so an ID costs one call and a short ID two. A name substring is no longer accepted (contract edit 1). The exit code is that one insight's: 0 for `PASSING`, 2 for `WARNING`, 3 for `ERROR` or `UNKNOWN`.

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

The `READINESS` section is `Fields` with a `Status` each, the `INSIGHTS` and `VERSION SKEW` sections are `Items`, so plain output is `readiness.blocked`, `insights.bc8b2f86.status`, `version_skew.ng-legacy.behind`, and so on, from the same data. The old plain form put only the insight rows on stdout and the rest on stderr; the new one is the `FIELD`/`VALUE` form of every section, as every describe view (phase 2, section 6; contract edit 6).

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
  ✓ Kubernetes     context "prod" (https://...)

Verdict: BLOCKED  DrainBlockers: PDB default/web allows 0 disruptions
```

The verdict line reads `would proceed`, `would ask` (with the question), or `would be blocked` in a dry run, and `proceeding`, the question itself, or `blocked` in a run. The `--force` and `--yes` reasons print as `! --force: ...` and `! --yes: ...` lines under the verdict. The machine form is `apidoc.Gate` in the plan.

## 9. Performance

Targets, in sequential round trips, with the request limit at 32:

| Command | Old | New |
|---|---|---|
| The gate, after the snapshot | about 9 | 2, or 1 without a user PDB at risk |
| `cluster upgrade-check` | 8 + N + 2M (14 on the fake) | 2, or 3 with a rollback window |
| `cluster describe` | 2 (phase 2) | 3 |
| `cluster describe --check-readiness` | | 3, the node list in wave 2 |
| `nodegroup list --check-readiness` | | 3 or 4 (phase 2) with the node list in wave 2 |
| `nodegroup describe --show-workloads` | | 3 (phase 2) with the node list in wave 2 and the pod lists in wave 3 |
| `cluster list --show-health`, per cluster | | 4 |
| Post-roll verification | | 1, plus the polls of `NodesReady` |

Model for `upgrade-check` with the 0 to 100 ms latency of the fake: two waves whose slowest call is near 100 ms, so about 200 ms against the measured 769 ms. The measurement goes in section 13, with the old binary in the same run, as phase 2 did.

How a test asserts the waves: the `awsxtest.Gate` for AWS, and the same shape over `kubetest.Handler.OnRequest` for Kubernetes. The kube gate is a few lines in `kubetest`: `NewGate(t, handler)` with `Wave(ops...)` over `fleettest.Operation`, so one test holds both:

```go
aws := awsxtest.NewGate(t, srv.Handler)
k8s := kubetest.NewGate(t, ksrv.Handler)
go run()
aws.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups)
aws.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.AutoScalingDescribeAutoScalingGroups)
aws.Wave(fleettest.CloudWatchGetMetricData, fleettest.CloudWatchGetMetricData, fleettest.CloudWatchGetMetricData, fleettest.ServiceQuotasGetServiceQuota)
k8s.Wave(fleettest.KubeListNodes, fleettest.KubeListPDBs, fleettest.KubeListDeployments, fleettest.KubeListNodeMetrics,
	fleettest.KubeListPods, fleettest.KubeListPods, fleettest.KubeListPods)
k8s.Wave(fleettest.KubeListPods) // the namespace of the tight PDB
aws.Done()
k8s.Done()
```

The two gates are independent, so the AWS waves and the Kubernetes waves may interleave in any order; each gate proves its own depth.

Benchmarks: `Gate` over 10 results and `HealthLines` over 10 results with `testing.AllocsPerRun` on every PR; the PDB selector cross-product (`REWRITE.md` names `pdbs.go:350-358`) nightly with benchstat, 200 PDBs over 2,000 pods.

## 10. Test plan

**Table tests for each check.** One test per check with one row per outcome: `Pass`, `Warn`, `Fail`, `Skipped`, `Unreadable`, and the partial-read rows (`Unreadable` from a Pass, `Fail` kept). The Kubernetes checks run with a real clientset against `kubetest.Server`, never `kubernetes/fake`, which ignores field selectors (`status.phase=Pending`, `spec.nodeName`) and would pass a list that the real server would not. A failed read is `World.SetScopedFault` on the operation (`Deny` for 403, `Code` for another reason, `Reset` for a dial failure, `Hang` for a timeout). The AWS checks run against `awsxtest` with the same faults. The threshold rows of each check are the old tests' numbers.

**What the fakes must gain.** `fleettest.Node`: a Ready reason (`KubeletNotReady`, `NodeStatusNeverUpdated`, `NodeStatusUnknown`) and a `ProviderID`. `fleettest.Pod`: a deletion timestamp, container readiness apart from the pod condition, a status reason and the `DisruptionTarget` condition for the stale-pod rule. `fleettest.PDB`: `MaxUnavailable`, `UnhealthyPodEvictionPolicy`, `ObservedGeneration` behind `Generation`, a `SyncFailed` condition, a nil and an empty selector. `fleettest.Cluster`: `NoMetricsServer`, so the metrics API answers 404. `awsxtest` CloudWatch: refuse more than 500 queries or a repeated `Id` with a validation error, as CloudWatch does. `kubetest`: a request `Gate` with `Wave` and `Done`, and exported builders of the objects it serves (`NodeObject`, `PodObject`, `PDBObject`, `DeploymentObject`, `NodeMetricsObject`), so a test can assert what the server would hand a client.

**Gate.** One table test over the 32 rows of section 4, with `Decision`, `Exit`, `Accepted`, `Forced`, and `Failures` asserted. One test for the pre-phase table. One for `Worse`: a worse outcome, a new finding, a numeric check with the same outcome and other numbers (not worse), a check Skipped now that ran before. One for `Question` and `Summary` wording.

**Runner.** Needs → `Skipped` with the input's reason; a cancelled context → `Unreadable` with `Interrupted`; `Name`, `Blocking`, and `FailClosed` filled from the check; the order kept; every check started at once; the shared node list made once for three checks, and its error shared when it fails; the `Kubernetes` result for no client, for a `Reset` on every read, and for a 403.

**Kube.** `Connect` against kubeconfig files in a temp dir: each rule of section 6 as a row, the `$KUBECONFIG` list, the explicit missing path, `--kube-context` with no kubeconfig, the exec fields set, the metrics client built from the same host.

**Whole-command tests** through `cmdtest.NewFake` plus a `kubetest.Server`, with goldens in the four formats for: `cluster upgrade-check` on a ready, a review, a blocked, an incomplete, and a not-evaluated cluster, with `--status`, and with `--insight`; `cluster describe` with the health section, with and without `--check-readiness`, with no kubeconfig match (the one warning), and with a Kubernetes API that answers 403 (Unreadable, exit 4); `nodegroup list --check-readiness`; `nodegroup describe --show-workloads`; `cluster list --show-health`. Each asserts the request counts and the waves.

**Verification.** The `Verify` set over the world's roll scripts, with the wait driven by `Factory.After`: a clean roll, `NodesNeverReady` (Fail, 5, after the bound), a node Ready on the second poll (Pass), `PendingPods` (Fail, 5), a Pending pod pinned to a live node of another nodegroup (Pass), a denied pod list after the roll (Unreadable, 4), a denied baseline (Unreadable, 4), a denied `DescribeNodegroup` (Unreadable, 4), and a Fail with an Unreadable (5).

**Parity.** The scenarios in `test/parity/scenarios_kube.go` and `scenarios.go` already pin the old gate: `nodegroup-update-pdb-blocked`, `-pdb-forced`, `-pdb-elsewhere`, `-pdbs-unreadable`, `-pdbs-unreadable-forced`, `-workload-failing`, `-node-not-ready`, `-node-not-ready-require-healthy`, `-warnings-unattended`, `-require-healthy`, `-prompt-yes|no|interrupt`, `-blocked`, `-degraded-neighbour`, `-pdb-tightens-after-dry-run`, `-stuck-drain`, the `nodegroup-scale-pdb-*` set, and the `cluster-upgrade-pdb-*` and `cluster-rollback-pdb-*` sets. Phase 4 runs them. The intended differences that must enter `allowlist.json`, each with its CONTRACT.md row as `source`:

| Scenario | Difference | CONTRACT.md row |
|---|---|---|
| `nodegroup-update-verify-node-not-ready` | exit 5, not 0 | "Post-roll verification does not read node readiness" |
| `nodegroup-update-verify-pods-unreadable` | exit 4, not 0 | "If the pod list fails after a roll" |
| `cluster-upgrade-pdb-blocked`, `cluster-rollback-pdb-blocked`, `cluster-upgrade-pdb-blocks-second-nodegroup`, `cluster-rollback-pdb-blocks-second-nodegroup`, `cluster-upgrade-pdbs-unreadable`, `cluster-rollback-pdbs-unreadable` | no mutation, exit 3 (or 4 for unreadable), where the old binary moved the control plane and the add-ons first and then exited 1 | "`cluster upgrade` reads PDBs only when it reaches the nodegroup phase" and "A drain-gate refusal exits 3" |
| `nodegroup-scale-pdbs-unreadable` | exit 4 with a document, not 1 without | "`nodegroup scale -o json` prints no document" |
| `nodegroup-scale-check-pdbs-no-kube` | the scale asks (fails without `--yes` in the trace), where the old `--check-pdbs` refused with exit 1 | the new row of contract edit 4 |
| `nodegroup-update-no-yes` and every `Kube: false` scenario of a changing command that proceeds with `--yes` | the trace gains an accepted `Kubernetes` reason; no change in mutations | the new row of contract edit 4; `expect` pins the mutations |

**Carried over.** These old tests name a regression and are ported into the tables above, or into a named whole-command case:

| Old test file | What it protects |
|---|---|
| `health/health_test.go` | A PDB whose selector does not match is not coverage (`:264`); `minAvailable == replicas` is a drain blocker (`:345`); a target scaled to 0 has nothing to drain (`:731`); `default` deployments count (`:805`); the eviction rules, the sync-failed PDBs, the scoping fallbacks |
| `health/accuracy_test.go` | Graceful-shutdown pods are skipped and bare `Terminated` is not; multi-PDB pods, scoped and unscoped, readiness ignored; scale-down arithmetic and fail-closed; joining nodes warn, broken nodes still fail; nil and empty selectors |
| `health/scoring_test.go` | A denied describe blocks; updating nodegroups are not half ready; all-updating warns; no managed nodegroups without Kubernetes is skipped and with Kubernetes counts nodes. The two "throttled does not block" tests become "a throttled read is retried", at the `awsx` layer. |
| `health/workloads_unread_test.go` | A failed pod list cannot pass; a partial failure keeps a Fail; the unread namespace is named |
| `health/checker_test.go` | A skipped check changes neither the verdict nor the summary (REF-146); a non-blocking Fail warns; blocking beats warn; all skipped does not panic |
| `health/aws_calls_test.go` | A 100-name `DescribeAutoScalingGroups` chunk returns every group (`MaxRecords`), now a `fleet` test; retried throttling; summarised AWS errors |
| `health/pdb_fuzz_test.go` | The drain-blocker report on fuzzed clusters (REF-4) |
| `health/quotas_test.go` | The newest minute, not the maximum over the window, is the usage; skip without the metric |
| `health/controlplane_test.go`, `utilization_test.go` | The parsing and each threshold; no data is skipped; metrics-server absent is skipped |
| `health/kubetarget_test.go`, `kubeclient_test.go`, `kubectl_test.go` | Endpoint normalising; current context first; explicit context trusted; `$KUBECONFIG` list; in-cluster rules; explicit missing path is an error; exec region and `--cluster-name` parsing (the last is already in `kube` from phase 2) |
| `health/readiness_test.go`, `services/nodegroup/readiness_test.go` | Ready counts by nodegroup; a count is never invented |
| `commands/nodegroup/verify_test.go`, `services/nodegroup/verify_rename_test.go` | A throttled describe is retried (REF-173); pods for a removed node are not stuck; a failed baseline is reported; a failed describe is a failure, not an issue |
| `commands/nodegroup/drain_gate_test.go`, `commands/cluster/upgrade_gate_test.go`, `upgrade_gate_failures_test.go` | Refuse, force, no kube asks, read error refuses, skip-health-check, dry run shows blockers, unread PDBs listed, a gate failure does not hide a failed roll, a cancelled check stops despite `--force` |
| `commands/nodegroup/gates_test.go`, `unattended_test.go`, `health_decision_test.go` | `--require-healthy` blocks a warn; `--yes` proceeds and prints the accepted warnings; quiet never accepts; block exits 3; fleet verdict codes |
| `commands/cluster/upgradecheck_exit_test.go`, `services/cluster/readiness_test.go`, `insights_test.go` | The exit table (REF-165); `--status PASSING` shows passing (REF-168); `--insight` uses the category; not-evaluated insights; the skew report; the deprecation detail. `TestUpgradeCheckExitCode_StatusFilter` flips: `--status WARNING` with an `ERROR` present now exits 3. |
| `commands/runner/kube_test.go`, `commands/cluster/readiness_test.go` | Mismatch warns once and skips; the matching context is selected; the explicit context is noted; a describe failure or an unreachable API leaves readiness unknown |
| `commands/nodegroup/scale_health_failures_test.go` | Unread pods of a scale's health check are failures |

`legacy/PORTING.md` records the result for each file.

## 11. Build order

Each step ends with `task dev:full` green and is one reviewable change.

1. **Types, the gate, and the first check in a command.** `Check`, `Input`, `Result`, `Outcome`, `Run` with Needs, the `Kubernetes` result and cancellation, `Gate`, `Verdict`, `Worse`; `apidoc.HealthResult`, `Health`, `Gate`, and their schemas; the `ControlPlane` check; `view.HealthLines`; the `HEALTH` section of `cluster describe` with that one check, its goldens, and its wave test. The gate's decision-table test runs here, so phase 4 can code against the API, and the slice is end to end.
2. **`internal/kube`.** `Connect`, `Selection`, `MismatchError`, `Factory.Kube`, the `kubetest` gate and builders, the fake additions of section 10.
3. **The other AWS-only checks.** `Need.InstanceIDs`, `Capacity`, `Quotas`, in the `HEALTH` section, goldens.
4. **The Kubernetes checks and the read flags.** `Nodes`, `Workloads`, `DrainBlockers`, `ScaleDownBlockers`, `PDBCoverage`, `Utilization`, the shared reads, `fleet.FetchKube`, `--check-readiness` on both commands, `--show-workloads`, goldens.
5. **`cluster upgrade-check`.** `Insights`, `VersionSkew`, `Need.Updates`, `fleet.RollbackWindow`, the two documents and schemas, the command, `--insight`, goldens, the wave test, the measurement for section 13.
6. **Verification.** `Baseline`, `Verify`, `VerifyExit`, the roll-script tests. No command calls it until phase 4.
7. **Close-out.** `cluster list --show-health`, the benchmarks, the livecheck rows (the AWS/EKS metric names and the quota code against a real account), `gen-docs`, the `PORTING.md` rows.

## 12. Decisions and contract edits

The owner decided these on draft 1. Nothing in this note is open.

| # | Question | Decision |
|---|---|---|
| 1 | What does an unreadable blocking check do? | Replaced by the review: `Unreadable` blocks for Nodes, Workloads, and DrainBlockers, the checks that read what a roll can break; for Capacity and ControlPlane it asks, `--yes` accepts, `--require-healthy` blocks, and the run exits 4 at its end. The message names the missing IAM action. |
| 2 | Exit code of a blocked `upgrade-check` with an unread item? | 4, by "the highest code wins". The label still reads `NOT READY`. |
| 3 | Does `cluster describe` run the AWS-only checks always? | Yes, as contract change 14 says every section shows. One more round trip. |
| 4 | Keep `cluster list --show-health`? | Yes, for this phase; revisit after the measurement. |
| 5 | A connection probe before the checks? | None. An unreachable API is the `Kubernetes` warning, which asks. |
| 6 | Drop Resource Balance? | Yes; its spread stays as a detail line, and section 2 names the two warnings that go with it. |
| 7 | No Kubernetes access? | Replaced by the review: an `Ask` reason, never a silent go. A TTY asks; `--yes` accepts and the result records it; `--require-healthy` blocks; a dry run exits 2. A dial failure or timeout on every Kubernetes read is the same case; a 401, 403, or 5xx is `Unreadable` under decision 1. No new flag. |

Contract edits, accepted by the owner unless marked:

1. `cluster upgrade-check --insight` takes the insight ID or the short ID the table shows. The name-substring match of the old `--id` is removed, by the rule that names are exact.
2. Exit codes, the row "`nodegroup scale -o json` prints no document ... A gate that cannot read exits 4": add the words "and blocks, or asks, by the check", so the row and the gate table agree; `--force` goes past unreadable PDBs and the run exits 4 at its end.
3. The `--dry-run` row of the shared flags: "The exit code comes from the findings alone: 0 when the real run would be allowed, 2 when it would ask about warnings, also with `--yes`, 3 when it would be blocked, and 4 when a check could not read what it needs."
4. Replaced by the review. New row under "Behaviour the new CLI does not copy": "`nodegroup scale --check-pdbs` without Kubernetes access refused the scale-down (exit 1); `nodegroup update` and `cluster upgrade` rolled with a warning. Every changing command now treats missing Kubernetes access as a warning the gate asks about: on a terminal the command asks, `--yes` accepts and the result records it, `--require-healthy` blocks, and a dry run exits 2. A Kubernetes API that is configured and does not connect is the same case. One that answers 401, 403, or 5xx makes the checks that need it unreadable, which blocks for nodes, system pods, and PDBs."
5. Documents: `ClusterDescription` gains `health`; `Nodegroup` gains `readyNodes` (phase 2 reserved it) and `workloads`; `UpgradeCheck` and `InsightDescription` have the fields of section 7; `Plan` and `Run` carry `gate` and `verification` as lists of `HealthResult`.
6. `cluster upgrade-check -o plain` is the `FIELD`/`VALUE` form of every section, not the insight rows alone with the rest on stderr.
7. Proposed in this draft: `cluster upgrade-check --status` filters the insights shown, not the insights judged. The old command judged the filtered list, so `--status PASSING` read `Ready` beside an `ERROR` insight.

Two points of `REWRITE.md` change in this note and need no contract edit: `Check` is a struct with a `Run` function, not an interface, and `Gate` returns a `Verdict` value with its exit code rather than a bare `Go`, `Ask`, `Block`.

## 13. Changes made while building

The code is the reference where it and the sections above differ.

| Section | Note | Built |
|---|---|---|
| 1 | `Result` has `Name`, `Blocking`, `FailClosed` from the check | It has `Forceable` too, filled by the runner, so `Gate` reads one value and never the check list. |
| 1 | The runner adds the `Kubernetes` result to every run | Only to a run with a check that needs Kubernetes. The three readiness checks of `upgrade-check` need none, so its verdict has no access line. |
| 1 | `Input.reads` memoizes under the context of each caller | `Run` builds the reads under the context of the run, with `sync.OnceValues` over the real call. A check that starts a shared read gives it that context, and every check sees the same answer or error. A check that needs several reads starts them together (`together`), so its reads are one round trip. |
| 1 | An unreachable API turns the Kubernetes checks into `Skipped` | Also the `Nodes` check, which needs no client but read one: a client that is there and does not answer gives no estimate. The rule is on the failures of the result: every one a Kubernetes read with a network error or a timeout. |
| 2 | `Nodes` and `Capacity` report `Unreadable` with the snapshot's failures | With no failure of their own: the snapshot's failures are in the document already, and a second copy would print twice. The outcome still decides the exit code. |
| 2 | `Insights(opts)` reads the list itself; `--status` is an option | `health.ListInsights` reads the list once, the command prints it, and `Insights(InsightsInput{Insights, Err, Category, ForVersion})` judges it. `--status` is the command's own filter on what is shown, as contract change 26 says. |
| 2 | `Quotas` is `Unreadable` on a failed `GetServiceQuota` | A quota that Service Quotas does not know (`NoSuchResourceException`) is `Skipped`: there is nothing to compare with. `awsx.IsNotFound` learned the code. |
| 2 | `Quotas` judges the usage alone | With targets it counts the nodes that a roll launches too, which a real roll exposed: 4 of 8 vCPUs in use passed at 50%, and the roll raised the Auto Scaling group by 3 t3.medium nodes and stalled on `VcpuLimitExceeded`. The surge is the larger of the zones of the Auto Scaling groups and `maxUnavailable`, up to twice the zones (the EKS rule); use plus the likely surge fails, use plus the maximum warns. `MINIMAL`, Spot, and non-Standard types add nothing; a mixed nodegroup counts its largest type. `nodegroup update` adds the surges of its targets, which roll at once; the engine counts the largest, as it rolls one at a time. `nodegroup scale` counts the nodes that it adds (`ScaleUpQuotas`). The vCPUs come from one `ec2:DescribeInstanceTypes`, in the same wave as `GetServiceQuota`, and only with targets; the zones come from the Auto Scaling group read of the snapshot. The check stays advisory. |
| 2 | `Quotas` is `Skipped` when `AWS/Usage` has no data points | A real cluster 20 minutes old still showed `Skipped`, so the surge rule never ran on the accounts with the smallest quota. With no data points the check sums the vCPUs of the pending and running On-Demand instances of the Standard families (`ec2:DescribeInstances`, then `ec2:DescribeInstanceTypes` for the types that the surge read lacks, 100 a call), and stops the list once the sum passes the quota. The details name the source. A failed metric stays `Unreadable` with no fallback; a failed fallback stays `Skipped` and names the error. |
| 2 | Resource Balance's spread is a detail line | Built: "CPU mean, peak, spread, over N of M instances". |
| 3 | `Need.InstanceIDs` keeps the IDs of the group that the AMI read reads | It reads every Auto Scaling group of a nodegroup, so a nodegroup with several groups is measured whole. One chunked call either way. |
| 3 | `Need.Updates` is a read like any other | It is advisory, as the old `RollbackAvailability` was: a failed read leaves the `rollback` key out and adds no failure. `fleet.RollbackWindowOf(snap, now)` is the pure rule. |
| 3 | `upgrade-check` wave 1 has `GetMetricData` | Built: the `ControlPlane` check runs in wave 1 on a stub snapshot that holds the name and the region of the cluster, which is all it reads. |
| 3 | `cluster describe` wave 3 is one metrics call | Three calls: `GetMetricData` for the control plane and for the quota usage, and `GetServiceQuota`. The capacity check's call joins them when the cluster has an Auto Scaling group. |
| 3 | `cluster list --show-health` is 4 + N calls per cluster | 6 + N: the two `GetMetricData` calls and `GetServiceQuota` beside `ListNodegroups`, `DescribeNodegroup` × N, and `DescribeAutoScalingGroups`. |
| 5 | `TakeBaseline(ctx, kube)` | It takes the cluster name too, for the failures it records. The first node list of `NodesReady` is the shared list of the run; the polls after it list again through the client. |
| 6 | `Factory.Kube(ctx, target)` | `Factory.Kube(target)`: `Connect` makes no request, so it needs no context. `cmdutil.NewKube` memoizes a failed resolution too. |
| 6 | The command turns a `MismatchError` into `NoKube` and one warning | `fleetcmd.ConnectKube(f.Kube, snap)` and `fleetcmd.ReportKube(ios, access)` do it for the read commands, so the three flags share the words. |
| 6 | `Client.Metrics` is `health.MetricsLister` | It is client-go's `NodeMetricsInterface`, and `Input.Metrics` is the same type: `kube` imports no refresh package but `apidoc`, and `cmdutil` can import it. |
| 7 | `--insight` with a full ID costs one call | One `ListInsights` always, then `DescribeInsight`: a full ID is matched in the list, and a short ID is the start of exactly one. A value shorter than the 8 characters of the table is refused. `--exit-zero` applies to the insight's exit code too. |
| 7 | The view has one `VERSION SKEW` section with two tables | A section draws one item table, so the view has `NODEGROUP SKEW` and `ADD-ON SKEW`, and a `CHECKS` section with the three readiness checks in place of `CONTROL PLANE`. |
| 7 | `Readiness` follows the gate verdict | Built so, with one addition: a cluster in extended support or unsupported is a `Review` reason, as the old `SupportPosture` line was a finding. |
| 8 | `HealthLines` draws the gate's report | Built with its test and benchmark; the first command that draws it is phase 4's dry run. The `HEALTH` section of `cluster describe` is an item table, as section 8 says. |
| 10 | The fakes gain a CloudWatch validation of the 500-query limit and unique IDs, `MaxUnavailable`, and exported `kubetest` builders | Not built. The capacity check chunks at 500 and names its queries by index, which the tests of the check exercise; the drain rules read the status counts of a budget and never `maxUnavailable`; the check tests run against `kubetest.Server`, so the builders stay private. The rest of the list is built: `Node.ReadyReason`, `Pod.Reason`, `Pod.DisruptionTarget`, `Pod.Deleting`, `PDB.Unsynced`, `PDB.SyncFailed`, `PDB.AlwaysAllow`, `PDB.NoSelector`, `Cluster.NoMetricsServer`, and `kubetest.Gate`. |
| 10 | The allowlist gains the rows of the table | No row was added: every one names a changing command, and the paired comparison runs only once a v2 changing command exists. Phase 4 adds them with its commands. |
| 11 | Step 6 builds verification with no caller | Built as `health.Verify`, `TakeBaseline`, `WaitOptions`, and `VerifyExit`, with a test-only driver (`driver_test.go`) that runs the plan gate, the gate before a phase with `Worse`, and the verification against the fakes, with the three moving-world cases of the parity suite: a budget tightened after the dry run, a nodegroup that degrades between two phases, and an add-on updated elsewhere. |

The `HEALTH` summary of `cluster describe` and the `HEALTH` column of `cluster list --show-health` do not change the exit code of those commands: they are read commands, and `status` and `cluster upgrade-check` are the commands that judge. A failed read of a check is a failure and exits 4, as every other read.

The review of the built phase changed four rules, each in its own commit: `Worse` ranks `Unreadable` above `Fail`; an API is "unreachable" only when the run's context has not ended, no read got an answer, and every failed read could not connect (a timeout, a 5xx, or a read that failed after another answered keeps the checks `Unreadable`); unscoped drain blockers need no pod read, and a scale-down drops the multi-budget rule; a nodegroup that is still coming up warns, as the old check warned. A budget whose selector does not parse matches no pod, as in the eviction API; the old check skipped such a budget for the coverage count, which is the one rule difference not listed in section 2.

The read commands that phase 2 built keep their shape: `--check-readiness` on `cluster describe` and `nodegroup list` reads the nodes once through `fleet.FetchKube` and shows `ready/desired` in the `NODES` column, and `nodegroup describe --show-workloads` lists the pods of each node of the nodegroup in one wave and shows a `WORKLOADS` section. The `health` key moved from `ClusterDescription` to the `Cluster` shape, so `cluster list --show-health` and `cluster describe` print it the same way.

Measured on the fake fleet of 200 clusters in 8 regions, old binary and new binary in the same run, 8 runs after one warm-up, `-o json`:

| Command | Old, no latency | New, no latency | Old, 0 to 100 ms | New, 0 to 100 ms | Old memory | New memory |
|---|---|---|---|---|---|---|
| `cluster upgrade-check` | 40 ms | 30 ms | 892 ms | 176 ms | 45 MB | 39 MB |
| `cluster describe`, with its health section | 39 ms | 31 ms | 484 ms | 255 ms | 47 MB | 38 MB |
| `cluster list -A --show-health` | 115 ms | 89 ms | 1,310 ms | 645 ms | 107 MB | 85 MB |

The runs went through `/usr/bin/time -l` for the peak RSS, which adds about 20 ms of process start to every no-latency number; the earlier read-side table did not. The fake fleet ran with `-throttle 0` and no slow region. `upgrade-check` is the two waves of section 3, and the old binary's 892 ms is its 8 + N + 2M round trips. `cluster describe` gained a third wave for the health checks, so it is slower than its phase 2 reading (172 ms) and still half the old binary. `cluster list -A --show-health` is four waves per region, all regions at once.
