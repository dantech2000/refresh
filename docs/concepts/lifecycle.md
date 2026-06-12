# The upgrade lifecycle

`refresh` is organized around a four-stage loop. Each stage has a command (or a
small group), and each builds on the previous one.

```
   status  ─►  upgrade-check  ─►  nodegroup/addon update  ─►  cluster upgrade
 (what's       (am I ready?)      (patch safely)              (orchestrate)
  stale?)
```

## 1. Status — what's stale?

[`refresh status`](../commands/status.md) is the front door. It fans out across
clusters and regions and summarizes patch posture: Kubernetes version, the EKS
support window (standard vs. extended support, with the extended-support cost
exposure), stale AMIs, and add-ons behind their latest compatible version.

It's designed to run in CI: a non-zero exit means "something needs attention."

## 2. Readiness — am I safe to upgrade?

[`refresh cluster upgrade-check`](../commands/cluster.md#upgrade-check) answers
"can I bump the control plane?" It combines **EKS Cluster Insights** (the
upstream-deprecation / config checks AWS surfaces in the console) with a local
**version-skew** analysis. It is strictly read-only.

## 3. Patch — roll it, safely

This is the heart of the tool:

- [`refresh nodegroup update`](../commands/nodegroup.md#update) rolls managed
  nodegroups to the latest recommended AMI.
- [`refresh addon update`](../commands/addon.md#update) updates EKS add-ons.

Both run **pre-flight health gates** first (capacity, node readiness,
PodDisruptionBudgets, critical workloads), support **dry-run** previews
(including the AMI changelog), stream **live progress**, and — for nodegroup
rolls — run **post-roll verification** that nodes returned Ready with no
newly-stuck pods.

The patch stage is also where the **safety story** lives:

- **Health gates** block a roll when the cluster isn't healthy enough.
- **`--dry-run`** shows exactly what would change.
- **Idempotency** — mutating calls carry a client request token, so a retried
  request can't trigger a second disruptive rollout.
- **Custom-AMI awareness** — nodegroups whose AMI is managed via a launch
  template are detected and skipped with guidance, not rolled blindly.

## 4. Upgrade — orchestrate the whole thing

[`refresh cluster upgrade`](../commands/cluster.md#upgrade) sequences a full
cluster upgrade: **control plane → add-ons → nodegroups**, with a health gate
after each phase. It's resumable by re-deriving the plan from live cluster
state (no state files to corrupt).

## Supporting surface

Around the loop sit the everyday read commands — `cluster list/describe`,
`nodegroup list/describe`, `addon list/describe` — plus `nodegroup scale` and
kubectx-style [contexts](contexts.md). These don't mutate the upgrade path; they
help you see and operate the fleet.
