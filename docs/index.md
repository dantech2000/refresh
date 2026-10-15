# refresh

`refresh` is a command-line tool that keeps Amazon EKS clusters up to date. It
covers four steps:

| Step | Command | What it does |
|---|---|---|
| 1. Status | `refresh status` | Shows what is out of date in the fleet. |
| 2. Readiness | `refresh cluster upgrade-check` | Reports whether a cluster is ready for its next upgrade. |
| 3. Patch | `refresh nodegroup update`, `refresh addon update` | Rolls nodegroups onto the latest AMI and updates add-ons. |
| 4. Upgrade | `refresh cluster upgrade` | Upgrades the control plane, the add-ons, and the nodegroups, in order. |

`refresh cluster rollback` returns a cluster to the version before its upgrade.

## What you get

- **One view of the fleet.** `refresh status -A` reads every region and lists
  each cluster with its support window, its stale AMIs, and its add-ons.
- **A health gate before every change.** Each changing command checks the
  nodes, the capacity, the system workloads, and the PodDisruptionBudgets
  before it sends a request. See [Health checks](concepts/health-checks.md).
- **A dry run that tells the truth.** `--dry-run` prints the plan and the
  verdict of the health gate. Its exit code says what the real run would do.
- **Output for scripts.** Every command prints a table, JSON, YAML, or TSV.
  The exit codes have one meaning in every command. See
  [Output formats](concepts/output.md) and [Exit codes](concepts/exit-codes.md).
- **No state file.** Every plan comes from the live state of the cluster. If a
  run stops, run the same command again.

## Install

```bash
brew install dantech2000/tap/refresh
```

See [Installation](getting-started/installation.md) for the other ways.

## A short tour

```bash
# 1. What is out of date in every region?
refresh status -A

# 2. Is prod ready to upgrade?
refresh cluster upgrade-check prod

# 3. See what a nodegroup roll would do, then do it
refresh nodegroup update -c prod --dry-run
refresh nodegroup update -c prod

# 3. Update every add-on, in dependency order
refresh addon update --all -c prod

# 4. Upgrade the cluster
refresh cluster upgrade prod --to 1.35
```

Go to the [Quickstart](getting-started/quickstart.md) for the same steps with
their output.

## Coming from 0.16 or earlier

The release after 0.16 changed the command line, the exit codes of some
results, and the JSON documents. If you have scripts, read
[Migrating from 0.16](migrating.md).
