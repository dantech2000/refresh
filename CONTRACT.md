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
| 25 | `cluster upgrade-check --insight` takes the insight ID or the short ID the table shows | Names are exact; the old name-substring match is gone |
| 26 | `cluster upgrade-check --status` filters what is shown, not what is judged | `--status PASSING` must not report "ready" while an ERROR insight exists |
| 27 | `cluster upgrade-check -o plain` prints every section as `FIELD`/`VALUE` rows | The old form printed the insight rows and put the rest on stderr |
| 28 | A blocked `cluster upgrade-check` with an unread item exits 4 | The highest code wins, as for `status` |
| 29 | `-a` is not a reserved flag letter | No command has an add-on flag: the add-on is the positional. A letter that no flag uses is not reserved. |
| 30 | `context list` and `context current` print no document, and the contract lists every command without `-o` | They print for people. A script reads the name, the first field of `refresh context current`. |
| 31 | `completion` has help text in the voice of the other commands, with an `Exit codes:` block, and the command list names `help` | Cobra's stock text read like another tool |
| 32 | The message of a verdict, exit 2 or 3, prints without `Error:` | A warning or a block is the answer of the command, not a crash. Exit 1 and 4 keep `Error:`. |
| 33 | An `addon update` step that moves an add-on to an older version has the reason `VersionAhead`, as a rollback step does | The plan labelled a downgrade `VersionBehind`, the reason of an upgrade |
| 34 | The `HEALTH` column of `status` counts the health issues of the control plane, the nodegroups, and the add-ons, and a `DEGRADED` add-on with no issue listed counts as one. Any issue exits 2 at least. | It counted the control plane alone, so a cluster with a `DEGRADED` add-on showed `ok` and exited 0 |

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
refresh help [command]
```

Every command is `refresh <noun> <verb>`, except the five that have no noun: `status` (the fleet overview), `ui`, `version`, `completion`, and `help`. `completion` and `help` come from cobra. `help` keeps cobra's text; `completion` has help text in the voice of the other commands.

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

Step 4 reads the context that `--kube-context` names, or else the current context, of the kubeconfig that `--kubeconfig` names, or else `$KUBECONFIG`, then `~/.kube/config`. It counts only a context that names an EKS cluster and its region. The name and the region come from, in this order:

1. An EKS cluster ARN, as `aws eks update-kubeconfig` writes it: the cluster entry of the context, or the name of the context.
2. A cluster entry `<name>.<region>.eksctl.io`, as eksctl writes it.
3. The exec credential plugin of the context: the name is its `--cluster-name` or `-i` argument, and the region is the one in the host of the API server. For `aws eks get-token` with an API server that is not an EKS host, the region is the one that the plugin signs with: `--region`, then `AWS_REGION`, then `AWS_DEFAULT_REGION`, each from the plugin's own `env` before the environment of refresh. Another plugin, such as `aws-iam-authenticator`, names a cluster only with an EKS API server.

Any other context names no cluster: kind, a self-managed cluster, a plain cluster name with no credential plugin, or `aws eks get-token --cluster-id` (a cluster on Outposts). Step 4 then gives nothing, and the command fails as when nothing resolves. A missing kubeconfig is the same; a kubeconfig that cannot be parsed is an error.

When step 4 gives the cluster, the command reads the region of that cluster. `-r/--region` wins over it. It wins over the region of the active context and of the AWS configuration.

Names are exact. A name that matches nothing is an error that lists the close matches. A name that EKS cannot have (a cluster or add-on: 1 to 100 letters, digits, hyphens, and underscores, starting with a letter or digit; a nodegroup: the same, 1 to 63) is an error before any request, from any source. An empty argument or `-c ""` is an error, not the default. To select by pattern, use `-f`:

```
refresh cluster list -f name=prod-*
refresh nodegroup update -c prod -f name=web-*
```

A filter is `key=value`, repeatable, and all filters must match. The value may use `*`. A filter that does not parse is an error. When a filter selects the targets of a change, the command lists the matches and asks, or needs `--yes`.

## Global flags

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-o, --output` | | `table` | `table`, `json`, `yaml`, or `plain`. Each command registers it with the formats that command supports, so it goes after the command name. |
| `-r, --region` | `REFRESH_EKS_REGIONS` for sweeps | from the context or AWS config | Region. On sweep commands it takes several: repeat the flag or separate the values with commas. Every region name, from here, `REFRESH_EKS_REGIONS`, a context, or the AWS configuration, must have the shape of a region (`us-east-1`, `us-gov-west-1`); an empty or malformed name is an error before any request. |
| `--profile` | `AWS_PROFILE` | | AWS shared-config profile |
| `-t, --timeout` | `REFRESH_TIMEOUT` | `1m` | Budget for the API calls of a read. In a changing command, for the reads and the requests outside a wait. With `--watch`, for each refresh. `0` means no limit; a negative value is an error. |
| `--max-concurrency` | `REFRESH_MAX_CONCURRENCY` | `32` | Limit for AWS requests in flight in one region |
| `--no-color` | `NO_COLOR` | | Disable color |
| `--log-level` | `REFRESH_LOG_LEVEL` | `warn` | `debug`, `info`, `warn`, `error` |
| `--verbose` | | | Same as `--log-level debug` |
| `--kubeconfig` | `KUBECONFIG` | `~/.kube/config` | Kubeconfig file, for the cluster of a read command and for the Kubernetes checks |
| `--kube-context` | | | Kubeconfig context to use. It is trusted even when its server does not match the cluster endpoint. |
| `-h, --help` | | | Help |
| `--version` | | | Print the version, the commit, and the build date. On the root command only. |

Other env vars, unchanged: `REFRESH_CONTEXT`, `REFRESH_CONFIG_HOME`, `REFRESH_NO_UPDATE_CHECK`, `REFRESH_IN_CLUSTER_NAME`.

A number flag or variable that is not a whole number, or that is out of range, is an error that says so in words: `--desired must be a whole number, not "abc"`, `--desired is too large (at most 2147483647)`.

`context add` checks what it saves: a name of 1 to 63 letters, digits, `-`, `_`, and `.` (not `-` alone), a cluster name by the EKS rules, and a region of the right shape. Replacing a context is allowed; the command then prints `Updated context "<name>"`.

Flag letters keep one meaning everywhere: `-c` cluster, `-n` nodegroup, `-o` output, `-r` region, `-t` timeout, `-d` dry-run, `-y` yes, `-q` quiet, `-w` watch, `-f` filter, `-A` all regions.

## Flags shared by every changing command

`nodegroup update`, `nodegroup scale`, `addon update`, `cluster upgrade`, `cluster rollback`:

| Flag | Meaning |
|---|---|
| `-d, --dry-run` | Print the plan and the verdict of the health gate. Change nothing. Never prompt. The exit code comes from the findings alone, whatever `--yes` says: 4 when a check could not read what it needs, else 3 when the real run would be blocked, else 2 when it would ask about warnings, else 0. |
| `-y, --yes` | Do not prompt. Required with `-o json|yaml` or without a terminal. A warning that `--yes` or an answer accepted does not change the exit code of a run that then succeeds; a dry run still reports it with exit 2. |
| `--no-wait` | Return once AWS accepts the change. Not available for `cluster upgrade` and `cluster rollback`, which have later steps. |
| `--wait-timeout` | How long to wait for the change. `0` means no limit. Defaults: 40m for a nodegroup update, 5m for a scale, 5m for each add-on, 4h for an upgrade or rollback. |
| | After a roll, the command also waits for the new nodes to become `Ready`: at most 2 minutes, and never past the time left of `--wait-timeout`. Nodes that are still not ready then are a verification issue, exit 5. |
| `--poll-interval` | How often to poll. Default 15s. |
| `--skip-health-check` | Skip the pre-flight health gate. |
| `--force` | Proceed past drain blockers and a vCPU quota surge, with a warning. Not on `addon update`, which drains nothing. |
| `-q, --quiet` | Print no progress. Questions, warnings, and the result still show. |

## Command flags

Only flags beyond the global and shared sets.

| Command | Flags |
|---|---|
| `status` | `-A/--all-regions`, `-f/--filter`, `--sort`, `--desc` |
| `cluster list` | `-A`, `-f`, `--sort`, `--desc`, `--show-health`, `-w/--watch`, `--watch-interval` |
| `cluster describe` | `--check-readiness` |
| `cluster upgrade-check` | `--category`, `--status`, `--show-passing`, `--insight`, `--exit-zero` |
| `cluster upgrade` | `--to`, `--only`, `-n/--nodegroup`, `--skip-addon`, `--skip-nodegroup`, `--skip-insights-check`, `--skip-verify`, `--require-healthy` |
| `cluster rollback` | `--skip-nodegroup`, `--skip-insights-check`, `--rollback-timeout`, `--skip-verify`, `--require-healthy` |
| `nodegroup list` | `-c`, `-f`, `--sort`, `--desc`, `--check-readiness`, `-w`, `--watch-interval` |
| `nodegroup describe` | `-c`, `--show-instances`, `--show-workloads` |
| `nodegroup update` | `-c`, `-f`, `--all-clusters`, `--reroll`, `--require-healthy`, `--skip-verify`, `--changelog`, `--live` |
| `nodegroup scale` | `-c`, `--desired`, `--min`, `--max`, `--require-healthy` |
| `addon list` | `-c`, `-w`, `--watch-interval` |
| `addon describe` | `-c` |
| `addon update` | `-c`, `--version`, `--all`, `--skip-addon`, `--require-healthy` |
| `context add` | `-c`, `-r`, `--profile`, `--use` |
| `ui` | `-A`, `-r`, `--interval`, `--allow-changes`, `--wait-timeout` |
| `version` | `--no-update-check` |

Renames inside this table: `cluster upgrade --skip` becomes `--skip-addon`, and `addon update --skip` becomes `--skip-addon`. `addon update` takes its version from `--version` only, not from a third positional. `nodegroup scale` loses `--health-check` and `--check-pdbs`, because the gate is on by default (change 5).

## Output

Unchanged rules:

- `-o json|yaml` prints exactly one document on stdout. Everything human goes to stderr.
- A document starts with `apiVersion` and `kind`.
- No `null`. An empty list is `[]`, an empty map is `{}`, and a key with no collected data is left out.
- A partial result has a top-level `failures` list and exits 4. This holds for a describe command too: it prints what it read.
- `-o plain` is TSV: one header row, then one row per item, `-` for an empty cell.
- A run with `-o json|yaml` never prompts.
- refresh's own enum values are PascalCase. AWS values pass through unchanged.

### Commands that print no document

These commands have no `-o`. Every other command has it.

- `context list`, `context current`: they print for people. A script that needs the active context reads the first field of `refresh context current`, which is the name.
- `context add`, `context remove`, `context use`: they print one line that says what changed.
- `ui`: it is a terminal app.
- `completion`: it prints a shell script.
- `help`: it prints help.

### Documents

`apiVersion: refresh.drod.dev/v2`. Each resource has one shape, reused in every document that contains it.

| Shape | Used in |
|---|---|
| `Cluster` | `ClusterList`, `ClusterDescription`, `FleetStatus`, every plan and run |
| `Nodegroup` | `NodegroupList`, `NodegroupDescription`, `ClusterDescription`, `FleetStatus` |
| `Addon` | `AddonList`, `AddonDescription`, `ClusterDescription`, `FleetStatus` |
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

`Plan` and `Run` replace eleven kinds: `NodegroupUpdatePlan`, `NodegroupUpdate`, `FleetUpdatePlan`, `FleetUpdate`, `NodegroupScale`, `AddonUpdate`, `AddonUpdateAll`, `UpgradePlan`, `UpgradeRun`, `RollbackPlan`, `RollbackRun`.

`Plan` has `operation` (`AddonUpdate`, `NodegroupScale`, `NodegroupUpdate`, `ClusterUpgrade`, `ClusterRollback`), `clusters`, `skippedRegions`, and `failures`.

Each entry of `clusters` has `cluster`, `region`, `version`, `targetVersion`, `decision` (`Proceed`, `Ask`, `Blocked`, `NothingToDo`), `steps`, `gate`, `blockers` (each with `kind`, `step`, `message`), `notices`, and `rollback`.

Each step has `phase` (`ControlPlane`, `AddonsBefore`, `Nodegroups`, `AddonsAfter`, `Addons`, `Roll`, `Scale`), `hop`, `kind` (`ControlPlane`, `Addon`, `Nodegroup`, `Scale`), `name`, `action` (`Submit`, `Attach`, `Verify`, `Skip`, `Manual`, `Blocked`), `reason`, `from`, `to`, `fromRelease`, `fromImage`, `toImage`, `force`, `inFlight`, and `sizes` (`before` and `after`, each with `desired`, `min`, `max`).

A step is identified by `region`, `cluster`, `phase`, `hop`, `kind`, and `name` together. No field is a composite ID to parse.

`Run` has `operation`, `status`, `clusters`, and `failures`. Each entry of `clusters` has the fields of a plan cluster, and `status`, `results`, and `verification` (a list of `HealthResult`). `results` has one entry for each step of the plan, in plan order, with the six identity fields and `status` (`NotAttempted`, `Started`, `Succeeded`, `Failed`, `Cancelled`, `InProgress`, `Busy`, `Skipped`, `Blocked`), `attached`, `updateId`, `inFlight`, `startedAt`, `finishedAt`, `failure`, and `note`.

`status` of a run and of a cluster is one of `Succeeded`, `NothingToDo`, `Started`, `Refused`, `Declined`, `Stopped`, `Blocked`, `Failed`, `VerifyFailed`, `Incomplete`, `Interrupted`. A run that did every step of its own is `Succeeded` also when its plan left steps to the operator (`action` `Manual`); the table view names those steps.

A health result has an `outcome`: `Pass`, `Warn`, `Fail`, `Skipped`, or `Unreadable`. There is no score.

The field lists are generated as JSON Schemas in `api/schema/v2` in phase 1 and reviewed then.

## Exit codes

Unchanged.

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | Error or interrupt |
| `2` | Needs attention: warnings or stale items |
| `3` | Blocked or unsupported: a gate stopped the change. Nothing changed, or, when a gate stops a later phase of an upgrade or rollback, nothing more changed and the message names the phases that completed. |
| `4` | Incomplete data or partial failure |
| `5` | The change was applied and the check after it found issues |

Exit 5 needs a check after the change: the verification after a nodegroup roll, and the health of an add-on after its update. `nodegroup scale` has no such check, so it never exits 5. It waits until EKS reports the update done and the nodegroup is `ACTIVE` at the new sizes. A nodegroup that is not `ACTIVE` then, such as `DEGRADED`, is a failed change, exit 1.

The message on stderr of an exit 2 or 3 is a verdict and prints alone, such as `upgrade needs review: ...`. Every other message starts with `Error:`: exit 1, 4, and 5.

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
| A check that cannot read its data is a warning in some checks and a block in others | Nodes, critical workloads, and drain blockers block when they cannot read. Capacity and control-plane metrics ask: `--yes` accepts, `--require-healthy` blocks. Either way the run exits 4. |
| Without Kubernetes access, `nodegroup update` skips the Kubernetes checks with a warning and `nodegroup scale --check-pdbs` refuses | Every changing command asks before it goes on without the Kubernetes checks. `--yes` accepts and the result records it, `--require-healthy` blocks, and a dry run exits 2. EKS still enforces PDBs during a managed drain. |
| With Kubernetes present, a degraded nodegroup is only a count of nodes that are not ready | A nodegroup that is not `ACTIVE` is a problem in every mode, and one that is a target blocks |
| `cluster upgrade` skips a nodegroup whose roll, started by an earlier run, finished before its phase, and never verifies it | The run verifies that nodegroup |
| The upgrade engine checks the cluster before a phase and not again after it attached to a change, asked a question, or waited | Before every change it sends, the engine reads the cluster again, checks that the change is still allowed (the rollback window, the control plane version a roll needs), applies the busy rule, and runs the gate |
| A wait that reaches `--wait-timeout` exits 1 | It exits 4 in every changing command. The change may still be running in AWS, so the result is incomplete, and the output says so. |
| A change that AWS accepted but that could not be watched exits 4 in `nodegroup update` and 1 elsewhere | It exits 4 in every command. This includes a wait whose AWS credentials expire or can no longer be resolved: it stops at the first poll that fails, says that the change may still be running, and names its update ID. |
| `nodegroup update` skips a nodegroup that is already updating, or submits and lets EKS refuse, depending on timing | A target that the last read before a change shows as already updating is not changed and not attached to in `nodegroup update`, `nodegroup scale`, and `addon update`. One exception: `addon update` attaches when the update in flight is already to the requested version. Its row names the update in flight, it is listed in `failures`, the other targets go on, and the run exits 4; when it was the run's only work, the run exits 3 (see the next row). |
| A target that is already changing exits 0 in `nodegroup update` and 3 in `nodegroup scale`, and the new CLI first exited 4 for it, or 3 when the plan found it | One rule in every command, whether the plan or the read right before the submit finds it: a busy target that is the run's only work exits 3, since nothing changed. A run that changed some targets and found others busy is a partial failure, exit 4. A dry run exits as the real run would. In `cluster upgrade` and `cluster rollback`, a busy target blocks its phase, exit 3. |
| `nodegroup scale` does no size check of its own beyond refusing a lone `--min` or `--max` that excludes the current desired size. It sends every other combination to EKS, which refuses sizes that cannot hold, and it sends a request that changes nothing. | `nodegroup scale` checks the sizes before it sends anything: against each other, and against the nodegroup's current sizes for the ones that were not given. The rule is `0 <= min <= desired <= max`, and `max` is at least 1, as EKS requires. Sizes that cannot hold are an error, exit 1, with no request. A lone `--min` or `--max` that excludes the current desired size says to pass `--desired` too. A scale to the current sizes sends nothing and exits 0. |
| The vCPU quota check is advisory, and counts current use only: a roll or a scale-up whose new nodes do not fit the quota starts and stalls on `VcpuLimitExceeded` | When current use plus the likely surge (the larger of the zones and `maxUnavailable`, in nodes) or the known nodes of a scale-up exceed the quota, the gate blocks: exit 3, nothing sent. `--force` goes past it with a warning, as for drain blockers. The documented maximum surge over the quota, and the thresholds of current use, only ask. |
| The vCPU quota check reads the CloudWatch `AWS/Usage` metric alone, which lags: right after a roll it still counts the surge nodes that have stopped, and the check warns when nothing is wrong | When the newest data point of the metric is more than two minutes old, the check also sums the vCPUs of the instance list and uses the smaller number. The details name the source that decided. |
| A run that `--require-healthy` stops over a warning exits 2 | It exits 3: a gate stopped the change and nothing changed. Exit 2 is for a dry run whose real run would ask. |
| `--force` also re-rolls a nodegroup that is already on the latest AMI | `--force` only goes past drain blockers and a vCPU quota surge. `--reroll` re-rolls. |
| `nodegroup update` on a terminal starts an exact-name roll without asking | Every changing command asks once on a terminal, and needs `--yes` when unattended |
| Mixed results take the numeric maximum in fleet mode and other rules elsewhere | One rule. For independent targets (clusters in fleet mode, the targets of `nodegroup update`, the add-ons of `addon update --all`): if one failed and another succeeded, the run exits 4; if every one failed, it exits 1. Then the highest code wins, and an interrupt is always 1. A `cluster upgrade` or `cluster rollback` whose phase fails exits 1, whatever earlier phases did. |

One old behaviour is kept on purpose: `cluster upgrade` starts an EKS insights refresh before it decides whether the run is blocked. The decision needs fresh insights. It is the one request a blocked run may send.
