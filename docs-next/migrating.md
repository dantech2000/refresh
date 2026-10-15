# Migrating from 0.16

The release after 0.16 is a rewrite of `refresh`. The product is the same:
status, readiness, patch, upgrade. The command line, some exit codes, and the
JSON documents changed. This page lists every change that a user or a script
of 0.16 will notice.

The new release has no hidden aliases for the old flags. An old command line
fails with a usage error. It does not do something different in silence.

## Checklist for a script

1. Replace `--format` with `--output`. `-o` still works.
2. Move the cluster to `-c` or to the positional. See
   [Command lines](#command-lines).
3. Replace partial names with exact names, or with `-f name=...`.
4. Remove `--wait`. Every change waits. Add `--no-wait` where the script did
   not wait before.
5. Remove `--health-check` and `--check-pdbs`. The health gate is on by
   default.
6. Replace `EKS_CLUSTER_NAME` with `REFRESH_CLUSTER`.
7. Replace `--health-only` with `--dry-run`.
8. Add `--yes` to every change that runs without a terminal.
9. Review the [exit codes](#exit-codes) that the script tests.
10. Update the JSON paths. See [Documents](#documents).

## Command lines

| 0.16 | Now |
|---|---|
| `refresh use prod` | `refresh context use prod` |
| `refresh current` | `refresh context current` |
| `refresh -v` | `refresh --version`, or `refresh version` |
| `refresh install-man` | Removed. The Homebrew cask and the release archives carry the man pages. |
| `refresh status --format json` | `refresh status --output json` (`-o json` works in both) |
| `refresh status --sort cluster` | `refresh status --sort name` |
| `refresh cluster list -f name=prod` (substring) | `refresh cluster list -f name=*prod*` |
| `refresh cluster list -H` | `refresh cluster list --show-health` |
| `refresh cluster list --tree`, `-o tree` | Removed. Use `refresh status -A`. |
| `refresh cluster describe -c prod` | `refresh cluster describe prod` |
| `refresh cluster describe prod --detailed --show-security` | `refresh cluster describe prod` (every section always shows) |
| `refresh cluster describe prod -R` | `refresh cluster describe prod --check-readiness` |
| `refresh cluster upgrade-check -c prod` | `refresh cluster upgrade-check prod` |
| `refresh cluster upgrade-check prod --id <id or name>` | `refresh cluster upgrade-check prod --insight <id or short id>` |
| `refresh cluster upgrade -c prod --to 1.35` | `refresh cluster upgrade prod --to 1.35` |
| `refresh cluster upgrade prod --to 1.35 --skip coredns` | `refresh cluster upgrade prod --to 1.35 --skip-addon coredns` |
| `refresh cluster upgrade prod --to 1.35 --timeout 2h` | `refresh cluster upgrade prod --to 1.35 --wait-timeout 2h` |
| `refresh cluster rollback -c prod` | `refresh cluster rollback prod` |
| `refresh nodegroup list prod` | `refresh nodegroup list -c prod` |
| `refresh nodegroup describe prod web`, `-n web` | `refresh nodegroup describe web -c prod` |
| `refresh nodegroup describe ... -I -W` | `refresh nodegroup describe ... --show-instances --show-workloads` |
| `refresh nodegroup update -c prod -n web` | `refresh nodegroup update web -c prod` |
| `refresh nodegroup update -c prod -n web-` (pattern) | `refresh nodegroup update -c prod -f name=web-*` |
| `refresh nodegroup update -c prod --health-only` | `refresh nodegroup update -c prod --dry-run` |
| `refresh nodegroup update -c prod --force` (to re-roll) | `refresh nodegroup update -c prod --reroll` |
| `refresh nodegroup update -c prod --timeout 1h` | `refresh nodegroup update -c prod --wait-timeout 1h` |
| `EKS_CLUSTER_NAME=prod refresh nodegroup update` | `REFRESH_CLUSTER=prod refresh nodegroup update` |
| `refresh nodegroup scale prod web --desired 4 --wait` | `refresh nodegroup scale web -c prod --desired 4` |
| `refresh nodegroup scale ... --desired 2 --check-pdbs --health-check` | `refresh nodegroup scale ... --desired 2` |
| `refresh nodegroup scale ... --op-timeout 10m` | `refresh nodegroup scale ... --wait-timeout 10m` |
| `refresh addon list prod -H` | `refresh addon list -c prod` (the health column always shows) |
| `refresh addon describe prod vpc-cni`, `-a vpc-cni` | `refresh addon describe vpc-cni -c prod` |
| `refresh addon update prod vpc-cni v1.19.0-eksbuild.1` | `refresh addon update vpc-cni -c prod --version v1.19.0-eksbuild.1` |
| `refresh addon update prod vpc-cni --wait --health-check` | `refresh addon update vpc-cni -c prod` |
| `refresh addon update -c prod --all --dependency-order --wait` | `refresh addon update --all -c prod` |
| `refresh addon update -c prod --all --skip coredns` | `refresh addon update --all -c prod --skip-addon coredns` |
| `refresh addon update -c prod --all --parallel` | Removed. `--all` always uses dependency order. |
| `refresh --region us-east-1 status -A` | An error. `-A` with `-r` is a usage error. |
| `refresh cluster list -w -o json` | An error. `--watch` needs `-o table` and a terminal. |

## Removed flags and commands

| Removed | Use |
|---|---|
| `refresh use`, `refresh current` | `refresh context use`, `refresh context current` |
| `refresh install-man` | The man pages of the cask or of the release archive |
| `-v` | `--version` |
| `--format` | `--output` |
| `-o tree`, `--tree`, `-T` | `refresh status` |
| `-H`, `-R`, `-I`, `-W`, `-T`, `-C` | The long flags. `-A` is the only capital letter that stays. |
| `-n` on `nodegroup describe`, `scale`, `update`; `-a` on `addon describe`, `update` | The positional. No flag has the letter `-a` now. |
| `--wait` on `nodegroup scale` and `addon update` | Nothing: a change waits. `--no-wait` turns the wait off. |
| `--health-check` on `nodegroup scale` and `addon update`; `--check-pdbs` | Nothing: the gate is on. `--skip-health-check` turns it off. |
| `--health-only` | `--dry-run` |
| `--dependency-order`, `--parallel` on `addon update` | Nothing: dependency order always |
| `--skip` on `cluster upgrade` and `addon update` | `--skip-addon` |
| `--id` on `cluster upgrade-check` | `--insight` |
| `--detailed`, `--show-security`, `--no-addons`, `--no-health` on `cluster describe` | Nothing: every section shows |
| `--show-health` on `addon list` | Nothing: the column always shows |
| `--op-timeout`, and `--timeout` as an alias of `--wait-timeout` | `--wait-timeout` |
| `EKS_CLUSTER_NAME` | `REFRESH_CLUSTER`, which every command reads |
| The `NODES` column of `cluster list` | `refresh status`, `refresh cluster describe` |
| The third positional of `addon update` (the version) | `--version` |

## Changed behaviour

### Names are exact

A cluster, nodegroup, or add-on name was also a substring pattern. It is now
exact. A name that matches nothing is an error that lists the close matches.
Select by pattern with `-f/--filter`, where `*` matches any text. A filter
value without `*` must equal the whole value.

Skip names (`--skip-addon`, `--skip-nodegroup`) are exact too. A skip name
that matches nothing is an error.

### Every change waits, and every change has the health gate

| | 0.16 | Now |
|---|---|---|
| `nodegroup update` | Waits; gate on | Waits; gate on |
| `nodegroup scale` | Returns at once; gate with `--health-check`, `--check-pdbs` | Waits; gate on |
| `addon update` | Returns at once; gate with `--health-check` | Waits; gate on |

`--no-wait` returns once AWS accepts the change. `--skip-health-check` turns
the gate off.

### Every change asks, and needs `--yes` when unattended

- On a terminal, every changing command asks once before it starts.
  `nodegroup update` with an exact name used to start without a question.
- Without a terminal, or with `-o json` or `-o yaml`, every changing command
  refuses without `--yes`. `nodegroup update -o json` used to start the roll
  when health was clean.
- `--dry-run` never asks and never needs `--yes`.

### `--force` and `--reroll`

`--force` only goes past drain blockers. It no longer rolls a nodegroup that
is already on the latest AMI. Use `--reroll` for that.

### Dry runs report the health gate

`--dry-run` runs the health gate and prints its verdict in the plan. The exit
code says what the real run would do. This replaces `--health-only`.

### Kubernetes access

Without Kubernetes access, `nodegroup update` used to skip the Kubernetes
checks with a warning, and `nodegroup scale --check-pdbs` used to refuse. Now
every changing command asks before it goes on. `--yes` accepts, and the result
records it. `--require-healthy` blocks. A dry run exits `2`.

### A check that cannot read

Nodes, system workloads, and drain blockers block when they cannot be read.
Capacity and control-plane metrics ask: `--yes` accepts, `--require-healthy`
blocks. Either way the run exits `4`.

### A target that is already changing

`nodegroup update`, `nodegroup scale`, and `addon update` do not change a
target that is already changing, and do not attach to it. The target is listed
in `failures`, the other targets go on, and the run exits `4`. One exception:
`addon update` waits for an update in flight that is already to the requested
version.

### `cluster upgrade`

- It refuses while someone else changes the cluster. It had no busy check.
- It runs every gate once before any change. It used to read
  PodDisruptionBudgets only when it reached the nodegroup phase, after the
  control plane had moved.
- It verifies after each nodegroup roll, also for a roll that an earlier run
  started.
- When it attaches to an update in flight and that update fails, it says so
  before it continues.
- Before every change that it sends, it reads the cluster again.

### Verification after a roll

Verification now reads node readiness. New nodes that are not `Ready` are an
issue, exit `5`. A verification that cannot read is incomplete data, exit `4`.
It used to pass with exit `0` in both cases.

### `nodegroup scale`

- It validates the sizes before any request: `0 <= min <= desired <= max`, and
  `max` is at least 1. Invalid sizes are an error, and nothing is sent.
- It always prints one document with `-o json`. It used to print none when the
  wait failed.

### `cluster upgrade-check`

- `--status` filters what is shown, not what is judged. `--status PASSING` no
  longer reports "ready" while an `ERROR` insight exists.
- `--insight` takes the insight ID or the short ID of the table. The match on
  a part of the name is gone.
- `-o plain` prints every section as `FIELD` and `VALUE` rows.

### Other

- `--max-concurrency` limits the AWS requests in flight in one region. The
  default is `32`. It used to count clusters, with a default of `8`.
- `--timeout` with `--watch` applies to each refresh.
- `cluster rollback` that is interrupted before it starts reports
  `Interrupted`, not `Failed`.
- A `DEGRADED` add-on has one health value everywhere: `Failed`. Updating it
  to repair it is still allowed, after a question.
- The retry count of an AWS call dropped: one retry layer makes up to 5
  attempts.

## Exit codes

The six codes keep their meaning. See [Exit codes](concepts/exit-codes.md).
These results changed their code:

| Result | 0.16 | Now |
|---|---|---|
| A wait reaches `--wait-timeout` | `1` | `4` |
| AWS accepted a change that could not be followed | `4` in `nodegroup update`, `1` elsewhere | `4` |
| A drain blocker stops `cluster upgrade` or `cluster rollback` | `1` | `3` |
| `--require-healthy` stops a run over a warning | `2` | `3` |
| `nodegroup scale`: the PDB check cannot reach Kubernetes | `1` | `4` when it cannot read; the command asks when there is no access |
| `nodegroup update` with several targets, one roll failed and one succeeded | `1` | `4` |
| `nodegroup update`: EKS refuses the only roll | `4` | `1` |
| `nodegroup update`: a target is already updating | `0` (skipped) | `4` |
| `nodegroup scale`: the nodegroup is already updating | `3` | `4` |
| New nodes stay `NotReady` after a roll | `0` | `5` |
| The pod list fails after a roll | `0` | `4` |
| `nodegroup list`: an AMI lookup fails | `0` | `4` |
| `status`: incomplete data and an unsupported cluster together | `3` | `4` (the highest code wins) |
| `cluster upgrade-check`: blocked, and an item could not be read | `3` | `4` |

Exit `2` in a changing command now has one meaning: a dry run whose real run
would ask.

For independent targets there is one rule. If one target failed and another
succeeded, the run exits `4`. If every target failed, it exits `1`. A
`cluster upgrade` or `cluster rollback` whose phase fails exits `1`.

## Documents

Every document is now `apiVersion: refresh.drod.dev/v2`. Check `apiVersion` in
a script, and fail on a version that the script does not support.

### One shape per resource

A cluster, a nodegroup, and an add-on each have one shape, reused in every
document. In `v1`, a nodegroup had four shapes.

### No health score

A health result has an `outcome`: `Pass`, `Warn`, `Fail`, `Skipped`, or
`Unreadable`. The `score` key is gone. A skipped check was `Pass` with a flag,
and is now `Skipped`.

### `Plan` and `Run`

Two kinds replace eleven:

| `v1` kind | `v2` kind | `operation` |
|---|---|---|
| `NodegroupUpdatePlan`, `FleetUpdatePlan` | `Plan` | `NodegroupUpdate` |
| `NodegroupUpdate`, `FleetUpdate` | `Run` | `NodegroupUpdate` |
| `NodegroupScale` | `Plan` with `--dry-run`, else `Run` | `NodegroupScale` |
| `AddonUpdate`, `AddonUpdateAll` | `Plan` with `--dry-run`, else `Run` | `AddonUpdate` |
| `UpgradePlan` | `Plan` | `ClusterUpgrade` |
| `UpgradeRun` | `Run` | `ClusterUpgrade` |
| `RollbackPlan` | `Plan` | `ClusterRollback` |
| `RollbackRun` | `Run` | `ClusterRollback` |
| `HealthSummary` (`--health-only`) | `Plan`: `.clusters[].gate` | |

A run that a gate blocks before any change prints a `Plan`, not a `Run`.

Where to find things:

| To read | Path |
|---|---|
| What the run would do | `.clusters[].steps[]`, with `action` (`Submit`, `Skip`, ...) and `reason` |
| The verdict of the health gate | `.clusters[].gate.decision` (`Go`, `Ask`, `Block`) |
| Each health check | `.clusters[].gate.checks[]`, with `name`, `outcome`, `message` |
| The result of the whole run | `.status` |
| The result of each step | `.clusters[].results[]`, with `status` and `updateId` |
| The verification after a roll | `.clusters[].verification[]` |
| What failed or could not be read | `.failures[]` |

See [Plan and Run](concepts/output.md#plan-and-run) for every key.

### Other document changes

- `ClusterList` rows have no node count.
- `ClusterDescription` always has every section, and a `health` list.
- An add-on has one `health` value: `Healthy`, `Failed`, `InProgress`, or
  `Unknown`.
- `Version` is a new kind, from `refresh version -o json`.
- The schemas are at `https://drod.dev/refresh/schema/v2/<Kind>.json`. The
  `v1` schemas stay published.

## Other changes

- `go install` uses a new path:
  `go install github.com/dantech2000/refresh/cmd/refresh@latest`.
- The shell completion scripts are new. Install them again. PowerShell is
  supported now.
- The help has a new layout.
- `refresh ui` is read-only in this release. It does not start changes yet.
