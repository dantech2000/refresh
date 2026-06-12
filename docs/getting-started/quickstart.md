# Quickstart

This walks through one full pass of the upgrade loop against a real account.
Everything here is read-only or dry-run until you opt in.

!!! note "Prerequisites"
    Working AWS credentials (`aws sts get-caller-identity` succeeds) and — for
    the workload/PDB health checks — a kubeconfig pointing at the cluster.

## 1. See what's stale

```bash
refresh status -A
```

`status` fans out across regions and reports, per cluster: Kubernetes version,
EKS support window (and extended-support cost exposure), stale AMIs, and add-ons
that are behind. It exits non-zero when something needs attention, so it doubles
as a CI gate.

## 2. Check upgrade readiness

```bash
refresh cluster upgrade-check -c prod
```

This pulls **EKS Cluster Insights** (the same checks the AWS console runs) plus a
local version-skew analysis, and tells you whether the control plane is safe to
bump. It's read-only.

## 3. Preview a nodegroup patch

```bash
# What would change? Include the AMI release notes between current and target.
refresh nodegroup update -c prod --dry-run --changelog
```

Dry-run shows the planned action per nodegroup (update / skip-if-latest /
custom-AMI-skip) without touching anything.

## 4. Patch with health gates

```bash
refresh nodegroup update -c prod
```

Before rolling, `refresh` runs pre-flight health checks (capacity, node
readiness, PDBs, critical workloads). It rolls the managed nodegroups to the
latest recommended AMI, streams live progress, and then verifies nodes came back
Ready with no newly-stuck pods.

## 5. Patch the add-ons

```bash
refresh addon update --all --dependency-order --wait
```

Updates every add-on to its latest compatible version, in a dependency-safe
order (vpc-cni → coredns/kube-proxy → others).

## Where to go next

- [The upgrade lifecycle](../concepts/lifecycle.md) — how the four stages fit together.
- [Configuration & AWS auth](../concepts/configuration.md) — profiles, regions, contexts, env vars.
- [Cookbook](../usage/cookbook.md) — fleet mode, unattended/CI runs, scaling, and more.
- [Command reference](../commands/index.md) — every command and flag.
