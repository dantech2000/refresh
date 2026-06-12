# refresh — the EKS upgrade companion

`refresh` is a Go CLI for the **EKS cluster upgrade and patching lifecycle**.
It turns "I think we're behind on patches" into a safe, repeatable loop:

<div class="grid cards" markdown>

- :material-radar: **Status** — *what's stale across the fleet?*

    `refresh status` reports patch posture across every cluster and region:
    version skew, stale AMIs, add-ons behind, and extended-support exposure.

- :material-clipboard-check: **Readiness** — *am I safe to upgrade?*

    `refresh cluster upgrade-check` reads EKS Cluster Insights and the local
    version-skew picture. Read-only; it mutates nothing.

- :material-update: **Patch** — *roll it, safely*

    `refresh nodegroup update` / `refresh addon update` patch with pre-flight
    health gates, dry-run previews, live monitoring, and post-roll verification.

- :material-layers-triple: **Upgrade** — *orchestrate the whole thing*

    `refresh cluster upgrade` sequences control plane → add-ons → nodegroups,
    with a health gate after every phase.

</div>

## Why refresh

The product is the **upgrade workflow**, with safety first:

- **Pre-flight health gates** — capacity, node readiness, PodDisruptionBudgets,
  and critical-workload checks run *before* anything mutates.
- **Dry-run everything** — preview exactly what would change (including the AMI
  changelog and which PDBs would constrain a scale-down) before you commit.
- **Live monitoring + verification** — watch rollouts in real time, then confirm
  nodes came back Ready with no newly-stuck pods.
- **Built for CI/cron** — unattended flags, machine-readable output, and a
  documented exit-code contract.

It intentionally does **not** try to be a general EKS browser or a cost tool —
that's `k9s`/the console/Kubecost territory. `refresh` does the upgrade loop well.

## Install

=== "Homebrew"

    ```bash
    brew install dantech2000/tap/refresh
    ```

=== "go install"

    ```bash
    go install github.com/dantech2000/refresh@latest
    ```

See the [installation guide](getting-started/installation.md) for binary
downloads (with signature verification), shell completion, and the man page.

## A 30-second tour

```bash
# What's stale across all regions?
refresh status -A

# Am I ready to upgrade prod?
refresh cluster upgrade-check -c prod

# Preview a nodegroup AMI roll (with release notes), then do it
refresh nodegroup update -c prod --dry-run --changelog
refresh nodegroup update -c prod --yes --require-healthy

# Patch every add-on, in dependency-safe order
refresh addon update --all --dependency-order --wait
```

Next: the [quickstart](getting-started/quickstart.md) walks through a first run
end to end, and [the upgrade lifecycle](concepts/lifecycle.md) explains how the
four stages fit together.
