# Migrating from eksctl / aws-cli to refresh

`refresh` is the EKS **upgrade companion**: it focuses on the
status → readiness → patch → upgrade lifecycle. It is not a full eksctl
replacement (it does not create or delete clusters), but for the day-to-day
inspect-and-patch loop it replaces a pile of `eksctl` and `aws eks` incantations
with shorter, safer commands.

This cheat-sheet maps the workflows you already know to their `refresh`
equivalents. Every flag shown is a real, current flag.

> Tip: instead of repeating `--region`/`--profile` on every command, save a
> named context once (`refresh context add ...`) and switch with `refresh use`.
> See [Context switching](#context-switching) below.

## List clusters

| You used to run | Now run |
| --- | --- |
| `eksctl get cluster` | `refresh cluster list` |
| `aws eks list-clusters` | `refresh cluster list` |
| (list across every region) | `refresh cluster list -A` / `refresh cluster list --all-regions` |
| (specific regions) | `refresh cluster list -r us-east-1 -r us-west-2` |
| (filter by status/version) | `refresh cluster list --filter status=ACTIVE --filter version=1.32` |

`refresh cluster list` adds AMI/health awareness, multi-region fan-out, an
`-o tree` view, and `--watch` for a live, top-style refresh.

## Describe a cluster

| You used to run | Now run |
| --- | --- |
| `eksctl get cluster --name my-cluster -o yaml` | `refresh cluster describe my-cluster -o yaml` |
| `aws eks describe-cluster --name my-cluster` | `refresh cluster describe my-cluster` |
| (with networking/security detail) | `refresh cluster describe my-cluster --detailed` |

## List nodegroups

| You used to run | Now run |
| --- | --- |
| `eksctl get nodegroup --cluster my-cluster` | `refresh nodegroup list my-cluster` |
| `aws eks list-nodegroups --cluster-name my-cluster` | `refresh nodegroup list my-cluster` |
| (only stale AMIs) | `refresh nodegroup list my-cluster --filter amiStatus=outdated` |
| `aws eks describe-nodegroup --cluster-name my-cluster --nodegroup-name ng-1` | `refresh nodegroup describe my-cluster ng-1` |

`refresh nodegroup list` shows at a glance which nodegroups are on the latest
recommended AMI.

## Scale a nodegroup

| You used to run | Now run |
| --- | --- |
| `eksctl scale nodegroup --cluster my-cluster --name ng-1 --nodes 5` | `refresh nodegroup scale my-cluster -n ng-1 --desired 5` |
| `aws eks update-nodegroup-config --cluster-name my-cluster --nodegroup-name ng-1 --scaling-config desiredSize=5` | `refresh nodegroup scale my-cluster -n ng-1 --desired 5` |
| (also set min/max) | `refresh nodegroup scale my-cluster -n ng-1 --desired 5 --min 3 --max 8` |
| (safe scale-down) | `refresh nodegroup scale my-cluster -n ng-1 --desired 2 --check-pdbs --wait` |

`--check-pdbs` refuses a scale-down when the removed nodes could hold more of a
Pod Disruption Budget's pods than it allows (EKS does not honor PDBs when it
removes nodes for a scaling change); `--force` overrides it.
`--health-check` validates cluster health before and after.

## Update (roll) a nodegroup AMI

| You used to run | Now run |
| --- | --- |
| `eksctl upgrade nodegroup --cluster my-cluster --name ng-1` | `refresh nodegroup update my-cluster -n ng-1` |
| `aws eks update-nodegroup-version --cluster-name my-cluster --nodegroup-name ng-1` | `refresh nodegroup update my-cluster -n ng-1` |
| (all nodegroups in the cluster) | `refresh nodegroup update my-cluster` |
| (preview only) | `refresh nodegroup update my-cluster -n ng-1 --dry-run` |
| (across the fleet) | `refresh nodegroup update --all-clusters -r us-east-1 --yes` |

`refresh nodegroup update` runs pre-flight health gates, monitors progress live,
and skips custom-AMI nodegroups with guidance. It exits with a meaningful code:
`0` ok, `1` error or interrupt, `2` warn, `3` blocked, `4` failed to start,
`5` verification issues. See [Exit codes](concepts/exit-codes.md).

## Update add-ons

| You used to run | Now run |
| --- | --- |
| `aws eks update-addon --cluster-name my-cluster --addon-name vpc-cni --addon-version v1.18.0` | `refresh addon update my-cluster vpc-cni v1.18.0` |
| (latest version) | `refresh addon update my-cluster vpc-cni` |
| `aws eks list-addons --cluster-name my-cluster` | `refresh addon list my-cluster` |
| (update every add-on) | `refresh addon update my-cluster --all` |
| (dependency-safe, wait for each) | `refresh addon update my-cluster --all --dependency-order --wait` |

## Upgrade readiness

| You used to run | Now run |
| --- | --- |
| `aws eks list-insights --cluster-name my-cluster` | `refresh cluster upgrade-check my-cluster` |
| `aws eks describe-insight --cluster-name my-cluster --id <id>` | `refresh cluster upgrade-check my-cluster --id <id>` |

`refresh cluster upgrade-check` is a read-only report combining EKS Cluster
Insights with control-plane/nodegroup version-skew checks — run it before any
upgrade.

## Orchestrated cluster upgrade

| You used to run | Now run |
| --- | --- |
| `eksctl upgrade cluster --name my-cluster` (then upgrade addons, then nodegroups, by hand) | `refresh cluster upgrade my-cluster --to 1.33` |
| (preview the full ordered plan) | `refresh cluster upgrade my-cluster --to 1.33 --dry-run` |

`refresh cluster upgrade` orchestrates the whole sequence — control plane →
nodegroups → add-ons — with per-phase health gates and confirmations. Unlike a
bare `UpdateClusterVersion`, it refreshes Cluster Insights before each hop and
blocks on problems. It is resumable: re-running re-derives the plan from live
cluster state.

## Fleet status

| You used to run | Now run |
| --- | --- |
| (loop `aws eks list-clusters` + `describe-cluster` per region) | `refresh status -A` |

`refresh status` is the front door: it summarizes patch posture (support
window, outdated nodegroup AMIs, nodegroups behind the control plane, add-on
drift, control-plane health) across your clusters and regions in one view.
Use `refresh status -A` for every region.

## Context switching

Instead of passing `--region`/`--profile` on every command (or juggling
`AWS_PROFILE` / `aws eks update-kubeconfig`), save named contexts once and switch
between them kubectx-style:

| You used to do | Now do |
| --- | --- |
| `export AWS_PROFILE=prod AWS_REGION=us-east-1` (then repeat flags) | `refresh context add prod --cluster prod-eks --region us-east-1 --profile prod` |
| (switch environments) | `refresh use prod` |
| (toggle back) | `refresh use -` |
| (see the active one) | `refresh current` |
| (list saved ones) | `refresh context list` |

Per-invocation `--region`/`--profile`/`--cluster` flags still override the
active context, and the `REFRESH_CONTEXT` env var overrides the saved current
pointer for a single shell.

## Output formats

Every list/describe command supports `-o table|json|yaml|plain` (plus `tree` for
`cluster list`). `plain` is uncolored TSV, ideal for `grep`/`awk` pipelines —
much like `aws ... --output text` but tab-separated and stable.

```bash
refresh nodegroup list my-cluster -o plain | awk -F'\t' 'NR>1 {print $1}'
refresh cluster list -A -o json | jq '.clusters[].name'
```

## Migrating to 0.11

0.11 gives every one-letter shorthand a single meaning across the CLI (see
[Flag shorthands](concepts/configuration.md#flag-shorthands)). Shorthands that
meant something else on one command were removed, and the wait timeouts got
one name. A removed shorthand fails with a message that names its
replacement:

```text
Error: -d was removed from 'cluster describe' in 0.11.0; use --detailed
```

### Removed shorthands

| Command | Removed | Use instead |
| --- | --- | --- |
| `cluster describe` | `-d` | `--detailed` |
| `cluster describe` | `-s` | `--show-security` |
| `cluster describe` | `-a` | nothing: add-ons are shown by default (`--no-addons` hides them) |
| `cluster upgrade` | `-s` | `--skip` |
| `cluster upgrade` | `-p` | `--poll-interval` |
| `nodegroup update` | `-f` | `--force` |
| `nodegroup update` | `-s` | `--skip-health-check` |
| `nodegroup update` | `-p` | `--poll-interval` |
| `addon update` | `-s` | `--skip` |
| `addon update` | `-p` | `--parallel` |
| `context add` | `-p` | `--profile` |

### Renamed and deprecated flags

These old names still work in 0.11. Each one prints a one-line warning on
stderr. They go away in 0.12.

| Command | Old | New |
| --- | --- | --- |
| `nodegroup update` | `--timeout`, `-t` (after the subcommand) | `--wait-timeout` |
| `cluster upgrade` | `--timeout`, `-t` (after the subcommand) | `--wait-timeout` |
| `nodegroup scale` | `--op-timeout` | `--wait-timeout` |
| `cluster describe` | `--show-health` | nothing: on by default |
| `cluster describe` | `--show-health=false` | `--no-health` |
| `cluster describe` | `--include-addons` | nothing: on by default |
| `cluster describe` | `--include-addons=false` | `--no-addons` |

The global `--timeout/-t` before the subcommand is still the API timeout:
`refresh -t 2m cluster upgrade ...`.

### New shorthands

| Command | Added |
| --- | --- |
| every command | `-r` for the global `--region` |
| `nodegroup scale` | `-d` (`--dry-run`), `-y` (`--yes`) |

### New confirmation prompts

`addon update` and `nodegroup scale` now ask before they change anything:

```text
Update coredns v1.11.1 → v1.11.4 on prod? [y/N]
Scale prod/ng-a desired 3 → 1? [y/N]
```

`cluster upgrade` also fails at once without a terminal unless you pass
`--yes` (before, its phase prompts read end-of-file and stopped the run).

To keep scripts working:

1. Add `--yes` (or `-y`) to every `addon update`, `nodegroup scale`, and
   `cluster upgrade` call in a script, cron job, or CI pipeline.
2. Replace `--timeout` after `nodegroup update` or `cluster upgrade` with
   `--wait-timeout`.
3. Replace `--op-timeout` with `--wait-timeout`.
4. Replace each removed shorthand with its long flag from the table above.

With `-o json`/`-o yaml`, or without a terminal, a run without `--yes` fails
before any AWS call and names `--yes`. `--dry-run` never prompts.
