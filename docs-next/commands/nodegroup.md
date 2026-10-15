# nodegroup

| Command | Use |
|---|---|
| [`nodegroup list`](#list) | List the managed nodegroups of a cluster. |
| [`nodegroup describe`](#describe) | Describe one nodegroup. |
| [`nodegroup update`](#update) | Roll nodegroups onto the latest AMI. |
| [`nodegroup scale`](#scale) | Change the sizes of a nodegroup. |

The cluster comes from `-c/--cluster`, then `REFRESH_CLUSTER`, then the active
context. `list` and `describe` also take the cluster of the current kubeconfig
context. `update` and `scale` never do.

## list

```bash
refresh nodegroup list -c <cluster> [flags]
```

```text
NODEGROUPS  prod-payments-00, 3

NAME      STATUS    INSTANCE    VERSION  AMI         NODES
ng-a      ● ACTIVE  m5.large    1.34     ▲ Outdated      4
ng-b      ● ACTIVE  m5.large    1.34     ● Latest        5
ng-c      ● ACTIVE  m5.large    1.34     ● Latest        4
```

The `AMI` column compares the AMI that the nodes run with the latest
recommended AMI for the version and the AMI type of the nodegroup:

| Value | Meaning |
|---|---|
| `Latest` | The nodes run the latest AMI. |
| `Outdated` | A newer AMI exists. |
| `Updating` | A roll is running. |
| `Custom` | A launch template sets the AMI. `refresh` does not roll it. |
| `Unknown` | The nodegroup has no instance. |

A version with a warning mark is behind the control plane.

| Flag | Meaning |
|---|---|
| `-f, --filter` | Select by `key=value`. Keys: `name`, `status`, `instanceType`, `amiStatus`, `version`. |
| `--sort` | `name` (default), `status`, `instanceType`, `nodes`, `version`, or `amiStatus`. |
| `--desc` | Sort in descending order. |
| `--check-readiness` | Read the `Ready` nodes of each nodegroup from the Kubernetes API. `NODES` then shows ready/desired. |
| `-w, --watch` | Read again and redraw every `--watch-interval`, until Ctrl+C. |

A nodegroup that could not be read, or whose AMI could not be looked up, is
listed under `INCOMPLETE DATA` or in `failures`, and the command exits `4`.

```bash
# The nodegroups with an outdated AMI
refresh nodegroup list -c prod -f amiStatus=Outdated

# The largest nodegroups first
refresh nodegroup list -c prod --sort nodes --desc
```

## describe

```bash
refresh nodegroup describe <nodegroup> -c <cluster> [flags]
```

```text
ng-a   ● ACTIVE   prod-payments-00 in us-east-1

OVERVIEW
  status          ● ACTIVE
  version         1.34
  instance types  m5.large
  capacity        ON_DEMAND

AMI
  type     BOTTLEROCKET_x86_64
  release  1.34.0-20260101
  status   ▲ Outdated
  current  ami-old134
  latest   ami-latest134

SCALING
  desired  4
  min      1
  max      12

INSTANCES  4
  NAME                       STATE      TYPE      LIFECYCLE  ZONE        AMI         LAUNCHED
  i-prod-payments-00-ng-a-1  ● running  m5.large  on-demand  us-east-1a  ami-old134  2026-09-18 01:18 UTC
  i-prod-payments-00-ng-a-2  ● running  m5.large  on-demand  us-east-1b  ami-old134  2026-09-04 01:18 UTC
```

| Flag | Meaning |
|---|---|
| `--show-instances` | List the EC2 instances of the nodegroup. |
| `--show-workloads` | Count the pods on the nodes of the nodegroup, from the Kubernetes API. |

If the AMI or the instances cannot be read, the command prints what it has,
names the failure, and exits `4`.

## update

```bash
refresh nodegroup update [nodegroup...] -c <cluster> [flags]
```

The command rolls managed nodegroups onto the latest recommended AMI of their
own Kubernetes version. EKS replaces the nodes: it drains each one and honours
PodDisruptionBudgets. The Kubernetes version of a nodegroup never changes
here. `cluster upgrade` does that.

### Targets

- With no name, every nodegroup of the cluster is a target.
- A name is exact.
- `-f` selects by pattern, such as `-f name=web-*`. The confirmation lists the
  matches.
- A nodegroup that is on the latest AMI is left alone. `--reroll` rolls it
  anyway.
- A nodegroup with a custom AMI is never rolled. Publish a new launch template
  version for it.

### What a run does

1. It reads the whole cluster and builds the plan.
2. It refuses while the cluster or anything else in it is changing. It leaves
   a target alone that is already changing: that target is listed in
   `failures`, the other targets go on, and the run exits `4`.
3. It runs the [health gate](../concepts/health-checks.md) for all the targets
   together, before the first roll starts.
4. It asks once. `--yes` skips the question.
5. It starts every roll and waits for all of them.
6. It verifies: the nodegroups are `ACTIVE`, the new nodes are `Ready`, and no
   pod is newly `Pending`.

```bash
refresh nodegroup update -c dev-orders-32 --yes
```

Progress, on stderr:

```text
PLAN  NodegroupUpdate, dev-orders-32 in us-east-1
  ng-a  skip, at 1.34 (AlreadyLatest)
  ng-b  skip, at 1.34 (AlreadyLatest)
  ng-c  roll onto the latest AMI of 1.34 (ami-latest134) (AMIOutdated)
◷ ng-c: Started (update 00000000-0000-4000-8000-000000000002)
● ng-c: Succeeded
```

The result, on stdout:

```text
Run   ● Succeeded   NodegroupUpdate

RESULTS  3
  NAME  KIND       STATUS       FROM                                            TO                        UPDATE
  ng-a  Nodegroup  • Skipped    1.34                                            1.34                      -
  ng-b  Nodegroup  • Skipped    1.34                                            1.34                      -
  ng-c  Nodegroup  ● Succeeded  1.34 (release 1.34.0-20260101, AMI ami-old134)  1.34 (AMI ami-latest134)  00000000-0000-4000-8000-000000000002

VERIFICATION  3
  NAME             OUTCOME  MESSAGE
  NodegroupActive  ● Pass   ng-c ACTIVE
  NodesReady       ● Pass   5 new nodes ready
  NewPendingPods   ● Pass   no new Pending pods
```

### If the gate blocks

```bash
refresh nodegroup update -c prod-payments-00 --dry-run
```

```text
dry run: a real run would be blocked
Plan   ✗ Blocked   NodegroupUpdate

HEALTH GATE
  decision  ✗ Block
  reasons   DrainBlockers: PDB app/web allows 0 disruptions on the target nodegroups; the roll would stall on eviction
```

The command exits `3` and changes nothing. Fix the blocker and run the command
again. `--force` goes past a drain blocker, with a warning, and tells EKS to
force the drain. It also goes past a vCPU quota surge that does not fit, with
a warning. `--force` does nothing else: it does not roll a nodegroup that is on
the latest AMI.

### Every cluster: `--all-clusters`

`--all-clusters` rolls every cluster of the regions of `-r`, one cluster after
another.

1. The command lists the clusters of the regions.
2. It plans every cluster. A dry run stops here.
3. It asks once, for all the clusters.
4. It runs the clusters one after another. Each cluster has its own
   `--wait-timeout`. The plan of a cluster is computed again when its turn
   comes.

A cluster that is blocked or fails does not stop the next one. An interrupt
does.

`--all-clusters` ignores `-c`, `REFRESH_CLUSTER`, and the active context. It
cannot be used with `-c`, with nodegroup names, or with `--kube-context`.

### Flags

| Flag | Meaning |
|---|---|
| `-f, --filter` | Select the targets by `key=value`. |
| `--all-clusters` | Roll every cluster of the regions of `-r`. |
| `--reroll` | Also roll a nodegroup that is on the latest AMI. |
| `--changelog` | With `--dry-run`, show the AMI release notes of each roll. |
| `--live` | Show the nodes of the roll as they drain and join, also without a terminal. |
| `--skip-verify` | Skip the verification after the rolls. |
| `--wait-timeout` | How long to wait for the rolls of a cluster. Default `40m`. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Done, or nothing to do. |
| `1` | An error, a question answered no, or an interrupt. Every roll that was sent failed. |
| `2` | With `--dry-run` only: a real run would ask about warnings. |
| `3` | Blocked: the cluster is busy, a gate stops the change, or `--require-healthy` and a warning. Nothing changed, or nothing more when the gate stops a later step. |
| `4` | Incomplete: a target was already changing, a wait timed out, something could not be read, or a roll failed beside one that was started. |
| `5` | The rolls were applied and the verification found an issue. |

### Examples

```bash
# See what would be rolled, with the AMI release notes
refresh nodegroup update -c prod --dry-run --changelog

# Roll two nodegroups without a prompt
refresh nodegroup update web api -c prod --yes

# Roll every cluster of two regions
refresh nodegroup update --all-clusters -r us-east-1,us-west-2 --yes

# In a nightly job: stop on any warning, and keep the document
refresh nodegroup update -c prod --yes --require-healthy -o json > run.json
```

## scale

```bash
refresh nodegroup scale <nodegroup> -c <cluster> [--desired N] [--min N] [--max N] [flags]
```

The command changes the desired, minimum, and maximum size of one managed
nodegroup. A size that is not given keeps its value.

### Size checks

The command checks the sizes before it sends anything: `0 <= min <= desired
<= max`, and `max` is at least 1. It checks against the current sizes of the
nodegroup for the sizes that were not given.

- If the sizes cannot hold, the command fails with exit `1` and sends nothing.
- If a lone `--min` or `--max` excludes the current desired size, the error
  says to pass `--desired` too.
- A scale to the sizes that the nodegroup already has sends nothing and exits
  `0`.

### The health gate

A scale that lowers the desired size removes nodes, and EKS does not wait for
PodDisruptionBudgets when it does. So the gate reads the nodes and the budgets
from the Kubernetes API, and stops a scale-down that can take a budget below
what it allows.

```bash
refresh nodegroup scale ng-b -c dev-orders-32 --desired 2 --dry-run
```

```text
dry run: a real run would be blocked
Plan   ✗ Blocked   NodegroupScale

STEPS  1
  NAME  KIND   ACTION  REASON  FROM                      TO                        NOTE
  ng-b  Scale  Submit  -       desired 4, min 1, max 12  desired 2, min 1, max 12  -

HEALTH GATE
  decision  ✗ Block
  reasons   DrainBlockers: removing 2 nodes from ng-b could evict more pods than PDB default/api allows
```

- `--force` goes past that, with a warning.
- Without Kubernetes access, the command asks before it goes on.
- Any other scale checks the EC2 vCPU quota, with the vCPUs of the nodes that
  it adds. If they do not fit, the scale is blocked, exit `3`. `--force` goes
  past that, with a warning. A scale-up warns when an availability zone of the nodegroup does not
  offer one of its instance types.

### What a run does

The command refuses while the cluster or anything else in it is changing. It
asks once. Then it waits until EKS reports the update done and the nodegroup is
`ACTIVE` at the new sizes. `--no-wait` returns once AWS accepts the change.

```text
PLAN  NodegroupScale, dev-orders-32 in us-east-1
  ng-b  scale from desired 4, min 1, max 12 to desired 6, min 1, max 12
◷ ng-b: Started (update 00000000-0000-4000-8000-000000000002)
● ng-b: Succeeded
```

### Flags

| Flag | Meaning |
|---|---|
| `--desired` | Desired number of nodes. |
| `--min` | Minimum number of nodes. |
| `--max` | Maximum number of nodes. |
| `--wait-timeout` | How long to wait for the scale. Default `5m`. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Done, or nothing to do. |
| `1` | An error, a question answered no, or an interrupt. The update failed, or the sizes cannot hold. |
| `2` | With `--dry-run` only: a real run would ask about warnings. |
| `3` | Blocked: the cluster is busy, a gate stops the change, or `--require-healthy` and a warning. Nothing changed, or nothing more when the gate stops a later step. |
| `4` | Incomplete: the nodegroup was already changing, the wait timed out, or something could not be read. |

### Examples

```bash
# Add a node
refresh nodegroup scale web -c prod --desired 4

# Raise the limit and the node count together, without a prompt
refresh nodegroup scale web -c prod --desired 8 --max 10 --yes

# Change only the bounds, and do not wait
refresh nodegroup scale web -c prod --min 2 --max 12 --yes --no-wait
```
