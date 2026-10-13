# refresh v2 contract

Status: approved by the owner on 2026-10-12, then revised the same day after the owner said backwards compatibility is not a goal: "I rather just have a sane command structure." Changes 12 to 17 below come from that. Frozen until cutover.

This is the public interface of the rewritten CLI: commands, flags, env vars, output, and exit codes. After approval it is frozen until cutover. `REWRITE.md` is the plan this belongs to.

The rule for every choice here: do what `kubectl`, `gh`, `eksctl`, and the AWS CLI do, unless refresh has a safety reason to differ. Compatibility with the old CLI is not a reason to keep anything.

## Summary of changes from today

Say no to any row and it reverts to today's behaviour.

| # | Change | Why |
|---|---|---|
| 1 | `-o/--output` replaces `-o/--format` | `kubectl`, `eksctl`, and the AWS CLI all call it `--output` |
| 2 | The positional names what the command acts on; `-c/--cluster` names the cluster for nodegroup and add-on commands | Today every argument is both a flag and a positional, and one positional changes meaning when a context is active |
| 3 | Resource names are exact; patterns go through `-f/--filter` | A typo or a short name can no longer select the wrong cluster for a change |
| 4 | Every change waits by default; `--no-wait` returns once AWS accepts it | Today `nodegroup update` waits, and `nodegroup scale` and `addon update` need `--wait` |
| 5 | The health gate is on by default for every change; `--skip-health-check` turns it off | Today it is opt-out in two commands and opt-in in two others |
| 6 | `REFRESH_CLUSTER` replaces `EKS_CLUSTER_NAME`, and every command reads it | Today one command reads it |
| 7 | `addon update --all` always uses dependency order; `--dependency-order` and `--parallel` are removed | The safe order should not be optional |
| 8 | Documents move to `refresh.drod.dev/v2` with one shape per resource and no health score | Today a nodegroup has four shapes and the score decides nothing |
| 9 | Deprecated flags and aliases are removed: `--op-timeout`, the per-command `--timeout` aliases, `--tree`, the removed-shorthand errors | A clean start |
| 10 | Capital-letter shorthands are removed, except `-A` | `-H`, `-R`, `-I`, `-W`, `-T`, `-C` are unusual and easy to forget; `-A` matches `kubectl` |
| 11 | `-v` no longer prints the version; use `--version` or `refresh version` | `-v` means verbose in most tools, so it is left unused |
| 12 | Every command is `refresh <noun> <verb>`. `refresh use` and `refresh current` become `refresh context use` and `refresh context current` | One shape for the whole tree, as `kubectl config` and `gh auth` do it |
| 13 | `--health-only` is removed. `--dry-run` runs the read-only health gate and reports its verdict in the plan | A flag that turns a changing command into a read-only one is confusing; a dry run should show whether the real run would be allowed |
| 14 | `cluster describe` always shows every section. `--detailed`, `--show-security`, `--no-addons`, and `--no-health` are removed | Four flags for one view, some opt-in and some opt-out |
| 15 | `-o tree` is removed | One command had it, and it drew a second status vocabulary |
| 16 | `install-man` is removed. Man pages ship in the release archives and the Homebrew cask | A CLI should not write into system directories; packaging does that |
| 17 | `cluster upgrade-check --id` becomes `--insight` | It names what the value is |
| 18 | `cluster list` has no NODES column | The column cost two calls plus one per nodegroup for every cluster. `status` and `cluster describe` show nodegroups. |
| 19 | `--max-concurrency` limits AWS requests in flight in one region; default 32 | The old default of 8 nested, so the real number was far higher. The default drops to 16 if the throttle test or the live check fails at 32. |
| 20 | `addon list --show-health` is removed | Health comes from the status the command already reads, so the column is always shown |
| 21 | `-A` with `-r` is a usage error | They contradict each other |
| 22 | `--watch` needs `-o table` and a terminal; `--timeout` applies to each refresh | A watch of a machine format is a stream of documents, which breaks "one document" |
| 23 | `status --sort cluster` becomes `--sort name` | The other list commands call it `name` |
| 24 | A failed AMI lookup makes `nodegroup list` exit 4 | One rule for a row that is only partly read |

## Commands

```
refresh status
refresh cluster   list | describe | upgrade-check | upgrade | rollback
refresh nodegroup list | describe | update | scale
refresh addon     list | describe | update
refresh context   list | add | remove | use | current
refresh ui
refresh version
refresh completion <bash|zsh|fish|powershell>
```

Every command is `refresh <noun> <verb>`, except the four that have no noun: `status` (the fleet overview), `ui`, `version`, and `completion`.

## Targets

| Command | Positional | Cluster comes from |
|---|---|---|
| `cluster describe`, `upgrade-check`, `upgrade`, `rollback` | `[cluster]` | The positional, then the resolution order below |
| `nodegroup list`, `addon list` | none | `-c`, then the resolution order |
| `nodegroup describe`, `nodegroup scale` | `<nodegroup>` | `-c`, then the resolution order |
| `nodegroup update` | `[nodegroup...]`; none means all | `-c`, then the resolution order |
| `addon describe` | `<addon>` | `-c`, then the resolution order |
| `addon update` | `[addon...]`, or `--all` | `-c`, then the resolution order |
| `status`, `cluster list`, `ui` | none | A sweep: the regions from `-r`, `-A`, or the default |

Cluster resolution order:

1. The explicit value: the positional or `-c`.
2. `REFRESH_CLUSTER`.
3. The active refresh context.
4. For read-only commands only: the cluster of the current kubeconfig context.

A command that changes something never uses step 4, with or without `--dry-run`. When nothing resolves, the command fails and names the three ways to set a cluster. Fleet mode (`nodegroup update --all-clusters`) ignores steps 2 to 4.

Names are exact. A name that matches nothing is an error that lists the close matches. To select by pattern, use `-f`:

```
refresh cluster list -f name=prod-*
refresh nodegroup update -c prod -f name=web-*
```

A filter is `key=value`, repeatable, and all filters must match. The value may use `*`. A filter that does not parse is an error. When a filter selects the targets of a change, the command lists the matches and asks, or needs `--yes`.

## Global flags

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-o, --output` | | `table` | `table`, `json`, `yaml`, or `plain`. Each command registers it with the formats that command supports, so it goes after the command name. |
| `-r, --region` | `REFRESH_EKS_REGIONS` for sweeps | from the context or AWS config | Region. On sweep commands it takes several: repeat the flag or separate the values with commas. |
| `--profile` | `AWS_PROFILE` | | AWS shared-config profile |
| `-t, --timeout` | `REFRESH_TIMEOUT` | `1m` | Budget for the API calls of a read command |
| `--max-concurrency` | `REFRESH_MAX_CONCURRENCY` | `32` | Limit for AWS requests in flight in one region |
| `--no-color` | `NO_COLOR` | | Disable color |
| `--log-level` | `REFRESH_LOG_LEVEL` | `warn` | `debug`, `info`, `warn`, `error` |
| `--verbose` | | | Same as `--log-level debug` |
| `-h, --help` | | | Help |
| `--version` | | | Print the version. On the root command only. |

Other env vars, unchanged: `REFRESH_CONTEXT`, `REFRESH_CONFIG_HOME`, `REFRESH_NO_UPDATE_CHECK`, `REFRESH_IN_CLUSTER_NAME`.

Flag letters keep one meaning everywhere: `-c` cluster, `-n` nodegroup, `-a` add-on, `-o` output, `-r` region, `-t` timeout, `-d` dry-run, `-y` yes, `-q` quiet, `-w` watch, `-f` filter, `-A` all regions.

## Flags shared by every changing command

`nodegroup update`, `nodegroup scale`, `addon update`, `cluster upgrade`, `cluster rollback`:

| Flag | Meaning |
|---|---|
| `-d, --dry-run` | Print the plan and the verdict of the health gate. Change nothing. Never prompt. The exit code is 0 when the real run would be allowed, 2 when it would ask about warnings, and 3 when it would be blocked. |
| `-y, --yes` | Do not prompt. Required with `-o json|yaml` or without a terminal. |
| `--no-wait` | Return once AWS accepts the change. Not available for `cluster upgrade` and `cluster rollback`, which have later steps. |
| `--wait-timeout` | How long to wait for the change. `0` means no limit. Defaults: 40m for a nodegroup update, 5m for a scale, 5m for each add-on, 4h for an upgrade or rollback. |
| `--poll-interval` | How often to poll. Default 15s. |
| `--skip-health-check` | Skip the pre-flight health gate. |
| `--force` | Proceed past drain blockers, with a warning. |
| `-q, --quiet` | Minimal progress output. |
| `--kubeconfig`, `--kube-context` | Kubernetes access for the workload checks and the live node view. |

## Command flags

Only flags beyond the global and shared sets.

| Command | Flags |
|---|---|
| `status` | `-A/--all-regions`, `-f/--filter`, `--sort`, `--desc` |
| `cluster list` | `-A`, `-f`, `--sort`, `--desc`, `--show-health`, `-w/--watch`, `--watch-interval` |
| `cluster describe` | `--check-readiness`, `--kubeconfig`, `--kube-context` |
| `cluster upgrade-check` | `--category`, `--status`, `--show-passing`, `--insight`, `--exit-zero` |
| `cluster upgrade` | `--to`, `--only`, `-n/--nodegroup`, `--skip-addon`, `--skip-nodegroup`, `--skip-insights-check` |
| `cluster rollback` | `--skip-nodegroup`, `--skip-insights-check`, `--rollback-timeout` |
| `nodegroup list` | `-c`, `-f`, `--sort`, `--desc`, `--check-readiness`, `-w`, `--watch-interval`, `--kubeconfig`, `--kube-context` |
| `nodegroup describe` | `-c`, `--show-instances`, `--show-workloads`, `--kubeconfig`, `--kube-context` |
| `nodegroup update` | `-c`, `-f`, `--all-clusters`, `--reroll`, `--require-healthy`, `--skip-verify`, `--changelog`, `--live` |
| `nodegroup scale` | `-c`, `--desired`, `--min`, `--max` |
| `addon list` | `-c`, `-w`, `--watch-interval` |
| `addon describe` | `-c` |
| `addon update` | `-c`, `--version`, `--all`, `--skip-addon` |
| `context add` | `-c`, `-r`, `--profile`, `--use` |
| `ui` | `-A`, `-r`, `--interval`, `--allow-changes`, `--wait-timeout`, `--kubeconfig`, `--kube-context` |
| `version` | `--no-update-check` |

Renames inside this table: `cluster upgrade --skip` becomes `--skip-addon`, and `addon update --skip` becomes `--skip-addon`. `addon update` takes its version from `--version` only, not from a third positional. `nodegroup scale` loses `--health-check` and `--check-pdbs`, because the gate is on by default (change 5).

## Output

Unchanged rules:

- `-o json|yaml` prints exactly one document on stdout. Everything human goes to stderr.
- A document starts with `apiVersion` and `kind`.
- No `null`. An empty list is `[]`, an empty map is `{}`, and a key with no collected data is left out.
- A partial result has a top-level `failures` list and exits 4.
- `-o plain` is TSV: one header row, then one row per item, `-` for an empty cell.
- A run with `-o json|yaml` never prompts.
- refresh's own enum values are PascalCase. AWS values pass through unchanged.

### Documents

`apiVersion: refresh.drod.dev/v2`. Each resource has one shape, reused in every document that contains it.

| Shape | Used in |
|---|---|
| `Cluster` | `ClusterList`, `ClusterDescription`, `FleetStatus`, every plan and run |
| `Nodegroup` | `NodegroupList`, `NodegroupDescription`, `ClusterDescription`, `FleetStatus`, plans and runs |
| `Addon` | `AddonList`, `AddonDescription`, `ClusterDescription`, `FleetStatus`, plans and runs |
| `HealthResult` | `ClusterDescription`, plans and runs |
| `Failure` | Every document's `failures` |

| Kind | Command |
|---|---|
| `FleetStatus` | `status` |
| `ClusterList`, `ClusterDescription` | `cluster list`, `cluster describe` |
| `NodegroupList`, `NodegroupDescription` | `nodegroup list`, `nodegroup describe` |
| `AddonList`, `AddonDescription` | `addon list`, `addon describe` |
| `UpgradeCheck`, `InsightDescription` | `cluster upgrade-check`, with `--insight` |
| `Plan` | Every `--dry-run`, and a run that a gate blocked |
| `Run` | Every finished change: the plan, the result of each step, and failures |
| `Version` | `version -o json` |

`Plan` and `Run` replace eleven kinds: `NodegroupUpdatePlan`, `NodegroupUpdate`, `FleetUpdatePlan`, `FleetUpdate`, `NodegroupScale`, `AddonUpdate`, `AddonUpdateAll`, `UpgradePlan`, `UpgradeRun`, `RollbackPlan`, `RollbackRun`. A plan has an `operation` field and a list of steps per cluster.

A health result has an `outcome`: `Pass`, `Warn`, `Fail`, `Skipped`, or `Unreadable`. There is no score.

The field lists are generated as JSON Schemas in `api/schema/v2` in phase 1 and reviewed then.

## Exit codes

Unchanged.

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | Error or interrupt |
| `2` | Needs attention: warnings or stale items |
| `3` | Blocked or unsupported: a gate stopped the change and nothing changed |
| `4` | Incomplete data or partial failure |
| `5` | The change was applied and the check after it found issues |

When one run has several results, the highest code wins, except that an interrupt is always `1`. This holds for `status` too: incomplete data (4) outranks an unsupported cluster (3). One exception to the interrupt rule: stopping `--watch` with Ctrl+C exits `0`, because that is the normal way to end a watch.

## Behaviour the new CLI does not copy

The parity suite found these in the old binary. The new CLI does the stated thing instead.

| Old behaviour | New behaviour |
|---|---|
| `nodegroup update -o json` without `--yes` and without a terminal starts the roll when health is clean | Every changing command refuses without `--yes` in that case |
| `nodegroup scale -o json` prints no document when the wait fails, or when the PDB check cannot reach Kubernetes (which also exits 1) | One document always. A gate that refuses exits 3. A gate that cannot read exits 4. |
| `cluster rollback` interrupted before it starts reports `Failed` | It reports `Interrupted`, as `cluster upgrade` does |
| `addon update` on a `DEGRADED` add-on prints a raw log line | Updating a degraded add-on to repair it stays allowed, and the notice is a normal stderr line |
| A `DEGRADED` add-on is "Issues" in one document and "Fail" in another | One add-on health value everywhere |
| `cluster upgrade` has no busy check | Each phase states which in-flight changes it accepts and refuses the others |
| Post-roll verification does not read node readiness, so new nodes that stay `NotReady` pass with exit 0 | Verification reads node readiness. New nodes that are not `Ready` are an issue, exit 5. |
| If the pod list fails after a roll, the pod check is skipped and the run exits 0 | A verification that cannot read is incomplete data, exit 4 |
| A drain-gate refusal exits 3 in `nodegroup update` and `nodegroup scale`, and 1 in `cluster upgrade` and `cluster rollback` | A gate that refuses exits 3 in every command |
| `cluster upgrade` reads PDBs only when it reaches the nodegroup phase, after the control plane and add-ons are upgraded | The plan step runs every gate once before any change, so a blocker is known up front. Each gate runs again right before its own phase. |
| `cluster upgrade` attaches to an in-flight update, and when that update fails it starts its own without a word | It says that the attached update failed before it continues |

One old behaviour is kept on purpose: `cluster upgrade` starts an EKS insights refresh before it decides whether the run is blocked. The decision needs fresh insights. It is the one request a blocked run may send.
