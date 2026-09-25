# Output formats

Every list/describe command (and most others) supports `-o` / `--format`:

| Format | Use it for |
|---|---|
| `table` *(default)* | Human-readable, colored terminal tables |
| `json` | Scripting; stable camelCase keys |
| `yaml` | Scripting; same keys as JSON |
| `plain` | Uncolored, tab-separated values for `grep`/`awk`/`cut` |
| `tree` | Hierarchical region → cluster view (**`cluster list` only**) |

```bash
refresh cluster list -o json | jq -r '.clusters[] | select(.status=="ACTIVE") | .name'
refresh nodegroup list -c prod -o plain | awk -F'\t' 'NR>1 {print $1, $5}'   # NAME, AMI
refresh cluster list -o tree
```

!!! warning "Unknown formats fail fast"
    An unrecognized `-o` value (a typo like `-o jsom`, or `-o xml`) is rejected
    with a clear error and a non-zero exit — it will **not** silently fall back
    to a table. This protects scripts that expect JSON.

## stdout and stderr

With `-o json` or `-o yaml`, stdout carries **exactly one document** and
nothing else, so `| jq` and `| yq` never choke on a stray line. Everything
meant for a person goes to stderr or is not printed:

- Spinners, progress, and the update monitor go to stderr, or are silent.
  Spinners also stop animating when stderr is not a terminal.
- Notices and warnings go to stderr. Examples: a skipped nodegroup, a failed
  start, an auto-accepted health warning, the cluster picked from
  `EKS_CLUSTER_NAME`.
- Errors, including the AWS credential setup help, print once, on stderr.
  The credential help appears only for a credential problem, not for a
  cancel, a timeout, or a network failure.
- Machine formats don't ask for confirmation, even on a terminal. A run that
  would ask a question fails with an error that names the missing flag,
  usually `--yes`. A partial cluster name resolves as it does without a
  terminal: a mutating command fails and names the candidate. Pass the exact
  name in scripts.
- A list is `[]` when empty, and a map is `{}`. A document never prints
  `null`. A key is left out when that data was not collected. For example,
  `cluster describe` leaves out `nodegroups` without `--detailed`, and
  prints `"nodegroups": []` with `--detailed` for a cluster that has none.

The exit code is the same as in the human view, and it applies after the
document is printed (see [Exit codes](exit-codes.md)). When a command fails before
it has a result (bad credentials, a missing `--yes`), stdout is empty and the
error is on stderr. A `nodegroup update` that the health gate stops is the
exception: stdout gets the run summary with nothing started and the `health`
verdict, the health report goes to stderr, and the error names the checks
that blocked or warned.

The documents for the mutating commands. Each one has a top-level
`failures` list (see [Failures](#failures)). Each item of a run has a
`status`, and a `failure` when the item failed; the same failure is in the
top-level list.

| Command | Document on stdout |
|---|---|
| `nodegroup update` | `cluster`, `nodegroups` (one entry per selected nodegroup: `name`, `status`, `updateId` once the update started, `reason` for a skip, `failure`), `verification`, `health` (the pre-flight verdict, when a check ran), and `failures` |
| `nodegroup update --dry-run` | `cluster`, `dryRun`, `force`, `reroll`, one `nodegroups` entry per nodegroup with its `action` (`Update`, `ForceUpdate`, `SkipUpdating`, `SkipLatest`, `SkipCustom`, or `Unknown` with a `failure` when the nodegroup could not be read), and `failures` |
| `nodegroup update --health-only` | The health verdict, with its own `failures` (the reads the checks could not make). The exit code is `0`, `2`, `3`, or `4` for a pass whose checks could not read everything |
| `nodegroup update --all-clusters` | `clusters` (one entry per cluster: `cluster`, `region`, `status`, `nodegroups`, `verification`, `health`, and `failure` when the cluster itself failed; with `--dry-run`, `status` and a `plan` instead), `skippedRegions` (a notice: default-sweep regions these credentials can't use), and `failures` (every cluster's failures, and a `Region` failure for each region that could not be listed). With no clusters found, `clusters` is an empty list. `--health-only` needs no `--yes` |
| `addon update` | The result (`addonName`, `previousVersion`, `newVersion`, `updateId`, `status`, `healthIssues`, `warning`, `failure`, `startedAt`) and `failures` |
| `addon update --all` | `cluster`, `dryRun`, `results` (one result per add-on), and `failures` |
| `cluster upgrade --dry-run` | The plan: `hops`, `notices` (advisory lines that never change the exit code), and `failures` (the reads the planner could not make) |
| `cluster upgrade --yes` | `{plan, report, failures}`: the plan the run started from, and what the run did: `status`, `completed`, `stoppedAt`, `remaining`, and `failure` (why it stopped). A blocked plan prints the plan alone and exits `3` |

The status values:

| Document | `status` values |
|---|---|
| `nodegroup update`, each nodegroup | `Started`, `Succeeded`, `Skipped`, `Failed`, `Cancelled`, `InProgress` (the run stopped watching an update that may still be running), `NotAttempted` |
| `nodegroup update --all-clusters`, each cluster | `Succeeded`, `Incomplete`, `Failed`, `HealthBlocked`, `HealthWarned`, `VerifyFailed`, `Interrupted`, `TimedOut`, `NotAttempted`; with `--dry-run`: `Planned`, `Incomplete`, `Failed` |
| `addon update`, each add-on | `DryRun`, `UpToDate`, `InProgress`, `Started`, `Completed`, `CompletedWithIssues`, `Unverified`, `WaitFailed`, `Failed`, `NotAttempted` |
| `cluster upgrade`, the report | `Succeeded`, `Failed`, `Blocked`, `Interrupted`, `TimedOut`, `Aborted` |

The command pages describe each value:
[`nodegroup update`](../commands/nodegroup.md#json-document),
[`addon update`](../commands/addon.md#waiting), and
[`cluster upgrade`](../commands/cluster.md#json-document). New versions can
add values; treat an unknown value as a failure you don't know how to handle.

`cluster upgrade -o json|yaml` without `--dry-run` needs `--yes`, because it
can't confirm each phase. It fails before any AWS call without it.
`nodegroup update -o json|yaml` needs `--yes` when the nodegroup pattern is
not an exact name or the health checks warn.

`--watch` can't be combined with `-o json` or `-o yaml`, because it would
print one document per interval. To poll from a script, run the command in a
loop.

```bash
refresh cluster upgrade -c prod --to 1.33 --yes -o json 2>upgrade.log | jq '.report'
```

## Document versions and schemas

Every `-o json` and `-o yaml` document starts with two keys:

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "NodegroupList",
  "cluster": "prod",
  "...": "..."
}
```

`apiVersion` is the version of the contract on this page. `kind` names the
document type, in PascalCase: `FleetStatus`, `ClusterList`,
`ClusterDescription`, `UpgradeCheck`, `InsightDescription`, `UpgradePlan`,
`UpgradeRun`, `NodegroupList`, `NodegroupDescription`, `NodegroupUpdate`,
`NodegroupUpdatePlan`, `FleetUpdate`, `FleetUpdatePlan`, `HealthSummary`,
`AddonList`, `AddonDescription`, `AddonUpdate`, or `AddonUpdateAll`. A
document nested in another one, such as the plan inside an `UpgradeRun` or
the health verdict inside a `NodegroupUpdate`, has no `apiVersion` or `kind`
of its own.

Each kind has a JSON Schema (draft 2020-12) at
`https://drod.dev/refresh/schema/v1/<kind>.json`. The
[JSON schemas](../reference/schemas.md) page lists them. `refresh`
generates the schemas from its Go types, and CI fails when a committed
schema no longer matches the code. The schemas list required keys, the
values of every enum, and no field allows `null`. They allow keys
they do not list, so a consumer on an older schema keeps working when a
newer release adds a field.

```bash
refresh cluster list -o json > clusters.json
check-jsonschema --schemafile https://drod.dev/refresh/schema/v1/ClusterList.json clusters.json
```

### Enum values

`refresh`'s own enums are PascalCase: every `status`, `reason`, `kind`,
`decision`, `action`, `type`, `tier`, `compute`, and `health` value that
`refresh` defines, such as `Succeeded`, `SkipLatest`, `ControlPlane`, or
`Proceed`. The schema of each kind lists them.

AWS values pass through unchanged, in AWS's own casing. Examples are EKS
statuses (`ACTIVE`, `UPDATING`), AMI and capacity types (`AL2_x86_64`,
`ON_DEMAND`), the upgrade policy (`supportType`: `STANDARD`), insight
statuses and categories (`PASSING`, `UPGRADE_READINESS`), and the EC2
instance `lifecycle` (`on-demand`, `spot`). The schemas describe these as
plain strings, because AWS can add values at any time.

The table view and `-o plain` keep their own words. For example, the health
decision prints as `PROCEED` there, and the plan step type as
`control-plane`. Scripts that parse `-o plain` see the same text as
before.

### Compatibility

Within `apiVersion: refresh.drod.dev/v1`, changes are additive only. A
release can:

- add a key to any object,
- add a value to any enum,
- add a new `kind`.

A consumer must ignore keys it does not know and handle an enum value it
does not know. For a failure, treat an unknown `reason` like `Unknown`.
Removing or renaming a key, changing its type, or changing what a value
means needs a new `apiVersion` (`v2`). Check `apiVersion` before you
parse, and fail loudly on a version you do not support.

Some rules hold for every document:

- `-o yaml` has the same keys, values, and structure as `-o json`.
- Key order carries no meaning. `refresh` prints `apiVersion` and `kind`
  first, then the keys in a stable order: JSON in field order, YAML
  sorted.
- The top-level `failures` is the complete list of the run's failures. A
  nested `failure` or `failures` is a subset of it. The list is sorted by
  `kind`, `region`, `cluster`, `name`, and `operation` (see
  [Failures](#failures)).

## JSON envelopes

List commands wrap their rows in an object, so a jq filter starts from the
array key, not from `.[]`:

| Command | Top-level shape |
|---|---|
| `cluster list` | `{"clusters": [...], "count": N, "failures": [...]}`. `failures` has the regions that could not be listed (`Region`), the clusters and nodegroups that could not be read, and with `--show-health`, the reads the health checks could not make. A row that could not be fully read has `"incomplete": true` |
| `nodegroup list` | `{"cluster": "...", "nodegroups": [...], "count": N, "failures": [...]}`. `failures` has the nodegroups that could not be described; the list leaves them out. A row whose latest-AMI lookup failed has an advisory `amiLookupFailure` (see [Advisory AMI lookup](#advisory-ami-lookup)) |
| `addon list` | `{"cluster": "...", "addons": [...], "count": N, "failures": [...]}`. `failures` has the add-ons that could not be described; the list leaves them out |
| `status` | `{"clusters": [...], "failures": [...]}`. `failures` has the regions that could not be listed and every part of a cluster row that could not be read. A row that could not be fully read has `"incomplete": true` |

Describe commands (`cluster describe`, `nodegroup describe`, `addon describe`)
and `cluster upgrade-check` print one object with no envelope. The object
also has a top-level `failures` list: the parts of a `cluster describe` that
could not be read (the add-on or nodegroup list, one add-on or
nodegroup, and with `--show-health`, the reads the health checks could
not make), and the nodegroups and add-ons whose version skew
`cluster upgrade-check` could not read. `nodegroup describe`, `addon
describe`, and `upgrade-check --id` read one item or fail with exit `1`, so
their `failures` is always `[]`.

```bash
refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.amiStatus == "Outdated") | .name'
refresh addon list prod -o json | jq -r '.addons[] | "\(.name) \(.version)"'
refresh status -o json | jq -r '.clusters[] | select(.staleAmi.behind > 0) | .name'
refresh status -o json | jq -r '.clusters[] | select(.incomplete) | .name'
```

## Failures

A failure is an item `refresh` could not read, or an action it tried that
did not complete. A command reports its failures in three places, and all
three come from the same list, so they always agree:

- **The document.** With `-o json` or `-o yaml`, the top-level `failures`
  list has one entry per failure. The list is always present. It is `[]`
  when there are no failures.
- **stderr.** One line per failure, in the same order as the document:

    ```text
    warning: nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded
    warning: region ap-east-1: RegionUnavailable: OptInRequired: not subscribed
    ```

    The line is `warning: <kind> <cluster>/<name> (<region>): <reason>: <error>`.
    Empty parts are left out. `-o plain` keeps stdout pure TSV, so failures
    appear only on stderr. The table view of a read command (`status`,
    `cluster list`, `cluster describe`, `cluster upgrade-check`, `nodegroup
    list`, `addon list`) lists the same lines at the end of its output,
    under `INCOMPLETE DATA`, and does not repeat them on stderr. The table
    view of a mutating command (`nodegroup update`, `nodegroup scale`,
    `addon update`, `cluster upgrade`) does the same.
- **The exit code.** A run with failures exits `4` (incomplete data), unless
  a code that wins over `4` also applies. Each command's order is in
  [Exit codes](exit-codes.md). The error message counts the failures by kind, for
  example `incomplete data: 3 failure(s) (1 cluster, 2 nodegroup)`.

Failures are not findings. A stale AMI, a blocked upgrade, a health warning,
or version skew is a finding: it stays in its own field and drives exit `2`,
`3`, or `5`. The `warnings` and `errors` of a health verdict are health
findings, not failures.

A row that was only partly read keeps what was read and gets
`"incomplete": true`, so a script can filter rows. The row carries no error
text of its own: its failures are in the top-level `failures`, where
`cluster` (or, for the cluster itself, `name`) identifies the row. A skipped
region of a default sweep (see [Skipped regions](#skipped-regions)) is not a
failure.

### Advisory AMI lookup

`nodegroup list` and `nodegroup describe` look up the latest recommended AMI
in SSM (`ssm:GetParameter`). When that lookup fails, the nodegroup's
`amiStatus` is `Unknown` and the row (or the describe object) has an
`amiLookupFailure` object with the same keys as a failure. It is advisory:
the nodegroup itself was read, so it is not in `failures` and does not change
the exit code. One `warning:` line on stderr names the reason and the
action. `status` counts the same lookup as a failure (exit `4`), because an
unknown AMI status makes its STALE AMI count incomplete.

### Skipped regions

A default region sweep (`-A` with no `-r` and no `REFRESH_EKS_REGIONS`)
skips the regions these credentials cannot use, such as an SCP denial or a
region that is not enabled. It writes one notice line to stderr and leaves
them out of `failures`:

```text
Skipped 2 region(s) not accessible to these credentials: ap-east-1, me-south-1 (scope with -r or REFRESH_EKS_REGIONS)
```

`nodegroup update --all-clusters` also lists them in its document, under
`skippedRegions`: a list of region names, left out when no region was
skipped. It is a notice, like the stderr line, so it never changes the exit
code. `status` and `cluster list` print only the stderr line.

When every region is skipped, the command exits `1`: nothing could be read.
A region you name with `-r` or `REFRESH_EKS_REGIONS` is never skipped; if it
cannot be listed, it is a `Region` failure.

### The failure object

```json
{
  "kind": "Nodegroup",
  "name": "web",
  "cluster": "prod",
  "region": "us-east-1",
  "operation": "eks:DescribeNodegroup",
  "reason": "Throttled",
  "retryable": true,
  "error": "ThrottlingException: Rate exceeded",
  "awsErrorCode": "ThrottlingException"
}
```

| Key | Always present | Meaning |
|---|---|---|
| `kind` | yes | The type of item: `Region`, `Cluster`, `Nodegroup`, `Addon`, `Insight`, `Update`, `PodDisruptionBudget`, or `Node` |
| `name` | yes | The item's name. For a `Region` failure, the region |
| `cluster` | no | The cluster the item belongs to |
| `region` | no | The AWS region. A `Region` failure always sets it, so a filter on `region` also finds the region's own failure |
| `operation` | no | The IAM action that failed, such as `eks:DescribeNodegroup`. Every action is in the [IAM permissions table](configuration.md#required-iam-permissions) |
| `reason` | yes | Why it failed. One value from the table below |
| `retryable` | yes | `true` when running the same command again, with no other change, may succeed |
| `error` | yes | One line of text for people. Do not parse it; use `reason` |
| `awsErrorCode` | no | The raw AWS error code, such as `AccessDeniedException`, when AWS answered |
| `updateId` | no | The EKS update ID, for a failure of a started update |

Entries are sorted by `kind`, then `region`, `cluster`, `name`, and
`operation`, so the order is the same on every run.

Branch on `reason`, not on `error`:

```bash
refresh status -o json | jq -r '.failures[] | select(.retryable) | "\(.kind) \(.name)"'
```

### Reasons

| Reason | Meaning | Retryable | Typical fix |
|---|---|---|---|
| `AccessDenied` | IAM or an SCP denied the call | no | Grant the action in `operation` (see the IAM permissions table) |
| `CredentialError` | The credentials are missing, expired, or invalid | no | Log in again (`aws sso login`) or fix the profile |
| `Throttled` | AWS rate-limited the call, and the retries ran out | yes | Run again later, or lower `--max-concurrency` |
| `NotFound` | The resource does not exist | no | Check the name. It may have been deleted during the run |
| `RegionUnavailable` | The region is not enabled for the account, or it cannot be reached | no | Enable the region, or leave it out with `-r` |
| `InvalidRequest` | AWS rejected the request as malformed | no | Check the flag values. If they are correct, report a bug |
| `ServiceError` | AWS failed on its side (a 5xx response) | yes | Run again later |
| `NetworkError` | The request got no response from AWS | yes | Check the network, proxy, and VPC endpoints, then run again |
| `Timeout` | The deadline (`--timeout` or `--wait-timeout`) passed | yes | Run again, or increase the timeout |
| `Interrupted` | The run was stopped (Ctrl+C or SIGTERM) | yes | Run again |
| `UpdateFailed` | An EKS update ended with status `Failed` | no | Read the update's errors (`aws eks describe-update`), fix the cause, then run again |
| `UpdateCancelled` | An EKS update ended with status `Cancelled` | no | Find out why it was cancelled, then run again |
| `NotMonitored` | Status polling stopped. The EKS update may still be running | yes | Check the update in EKS, or run again to pick up where it is |
| `NotAttempted` | The run stopped before this item started | yes | Run again |
| `Unknown` | Any other error | no | Read `error` and `awsErrorCode` |

New versions can add keys, kinds, and reasons. A consumer must ignore keys
it does not know and treat an unknown `reason` like `Unknown`.

## jq cookbook

The regions that could not be listed:

```bash
refresh status -o json | jq -r '.failures[] | select(.kind == "Region") | "\(.name) \(.reason)"'
```

The failures that a second run may fix:

```bash
refresh cluster list -o json | jq -r '.failures[] | select(.retryable) | "\(.kind) \(.cluster // "-")/\(.name): \(.reason)"'
```

Failures counted by reason:

```bash
refresh status -o json | jq '.failures | group_by(.reason) | map({reason: .[0].reason, count: length})'
```

The nodegroups a `nodegroup update` did not roll, with the reason:

```bash
refresh nodegroup update prod --yes -o json |
  jq -r '.nodegroups[] | select(.status != "Succeeded") | "\(.name) \(.status) \(.reason // .failure.reason // "")"'
```

Refuse a document from a contract version the script does not know:

```bash
refresh addon list -c prod -o json |
  jq -e '.apiVersion == "refresh.drod.dev/v1"' > /dev/null || { echo "unsupported refresh output" >&2; exit 1; }
```

A CI step that branches on the exit code and keeps the document:

```bash
set +e
refresh cluster upgrade-check -c prod -o json > readiness.json 2> readiness.log
code=$?
set -e
case "$code" in
  0) echo "ready" ;;
  2) echo "ready, with findings to review" ;;
  3) echo "blocked:"; jq -r '.insights[] | select(.status == "ERROR") | .name' readiness.json; exit 1 ;;
  4) echo "incomplete data:"; jq -r '.failures[] | "\(.kind) \(.name): \(.reason)"' readiness.json; exit 1 ;;
  *) cat readiness.log; exit 1 ;;
esac
```

## Key consistency

`json` and `yaml` emit the **same** camelCase keys (e.g. `instanceType`,
`createdAt`), so `refresh nodegroup describe prod -n ng-a -o json | jq '.instanceType'`
and the same filter through `yq` on `-o yaml` both work.

## The `plain` contract

`-o plain` writes pure TSV to stdout, and nothing else:

- The first line is a header row. The column names are the same as the
  `table` view's headers, in the same order.
- Each following line is one item (a cluster, a nodegroup, an add-on, an
  insight, an update result).
- There is no title, no "Retrieved in" line, no blank line, no footer, no
  glyph, and no color.
- A cell with an embedded tab or newline gets a space in its place, so one
  item is always exactly one line.
- An empty cell is written as `-`, so every line has the same number of
  fields, even with `IFS=$'\t' read`.
- Values are never truncated (the `table` view shortens some, such as long
  endpoints and insight IDs).

Messages that are not data go to stderr. Examples are "No nodegroups found",
warnings, and the parts of the `cluster upgrade-check` report that are not
insight rows (the readiness verdict, support, control plane, and version
skew). An empty list prints the header row only.

`cluster upgrade -o plain` writes one row per plan step (`HOP`, `STEP`,
`TYPE`, `TARGET`, `VERSION`, `STATUS`, `DESCRIPTION`, `REASON`). The plan's
summary line, its notices and failures, and everything after the plan
(prompts, progress, the report) go to stderr.

Skip the header with `NR>1` in awk or `tail -n +2`:

```bash
refresh cluster list -o plain | awk -F'\t' 'NR>1 {print $1}'
refresh addon list -c prod -o plain | tail -n +2 | cut -f1,2
```

### Describe commands: `FIELD` / `VALUE`

`cluster describe`, `nodegroup describe`, `addon describe`, and
`cluster upgrade-check --id` write two columns: a `FIELD`/`VALUE` header, then
one row per attribute. Field names are lowercase (`status`, `endpoint`,
`ami status`). Repeated items use a `<kind>/<name>` field, and their value
is a list of `key=value` pairs:

```text
FIELD	VALUE
name	prod
status	ACTIVE
endpoint	https://ABC123.gr7.us-east-1.eks.amazonaws.com
nodegroup/web	instance=m5.large nodes=3 status=ACTIVE
addon/vpc-cni	version=v1.18.3-eksbuild.1 status=ACTIVE health=Healthy
```

```bash
refresh cluster describe -c prod -o plain | awk -F'\t' '$1=="endpoint" {print $2}'
```

`plain` carries at least the data the `table` view shows. For example,
`cluster describe` also lists the subnet and security group IDs and deletion
protection.

## The human display (design system)

The default `table` output is rendered by a small design system so every
surface reads consistently: the list and describe views, the pre-flight
health report, dry-run previews, the update and upgrade progress and
results, and the context commands.

- **Status tokens** pair a glyph with a label and color: `●` healthy/current,
  `▲` warn/stale, `✗` failed/unsupported, `◷` in-progress, `○` unknown, and
  `•` neutral (for example a check that was skipped, so it never reads as
  passed). The glyph and label always carry the meaning, so **color is purely
  additive**: output stays fully legible with `--no-color`, when piped, or on
  a non‑UTF‑8 terminal (where glyphs fall back to `[OK] [!] [X] [~] [?] -`).
- A resource in transition (`UPDATING`, `CREATING`, `DELETING`, `SCALING`)
  shows the in-progress token, the same as an EKS update that is
  `InProgress`.
- **Color depth adapts to the terminal:** 24‑bit truecolor when the terminal
  advertises it (`COLORTERM=truecolor`), a 256‑color approximation otherwise,
  and no color when piped / `NO_COLOR` / `--no-color`.

This styling applies only to the human view. **`-o json`, `-o yaml`, and
`-o plain` are unaffected** — their bytes are identical regardless of terminal
or color settings, so scripts are never surprised.

## Color

Each stream decides on color for itself. stdout is colored only when stdout
is a color terminal, and stderr only when stderr is one. So with `2>log`,
the log file gets no escape codes, and warnings on a terminal stay colored
when stdout is piped. `--no-color`, a non-empty `NO_COLOR` (any value), or
`TERM=dumb` turns color off on both streams, also in `--help` output.

## Live node-roll view

When you roll one nodegroup on a color terminal, `refresh nodegroup update`
shows a **real-time, per-node view** of the roll — nodes draining (with
pod-eviction progress), terminating, and coming online — instead of a single
spinner. `cluster upgrade` shows the same panel for its nodegroup rolls. It
is line-oriented (it redraws in place); it is **not** a full-screen TUI.

When stdout is piped, in CI, or with `NO_COLOR`, you get the standard
progress lines instead. `--live` forces the panel there too: it appends a
snapshot at most every 15s, and only when something changed.

```bash
refresh nodegroup update -c prod -n web --live
```

The panel needs cluster (kubeconfig) access and a single nodegroup, and it is
off with `--quiet`. Without them, `refresh` falls back to standard monitoring.
The EKS update status stays authoritative for the result regardless: the panel
stops when the EKS update ends, also when it fails. A failed Kubernetes read
keeps the last frame with a retry notice. Old-vs-new is determined from a
roll-start baseline, so it works for any roll.

To preview the view with **no AWS and no cluster** (demos, or just to see the
shape of it), a hidden flag drives the panel from a scripted roll of 3 old → 3
new nodes:

```bash
refresh nodegroup update --simulate
```

## Watch mode

`cluster list`, `nodegroup list`, and `addon list` support `--watch`
(with `--watch-interval`, default `10s`): top-style redraw on a terminal,
append-only when piped.
