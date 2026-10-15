# Output formats

Every command that prints data takes `-o/--output`:

| Format | Use |
|---|---|
| `table` | The default. For people. |
| `json` | One JSON document. For scripts. |
| `yaml` | The same document as YAML. |
| `plain` | Tab-separated values. For `cut`, `awk`, and spreadsheets. |

`refresh version` has `table`, `json`, and `yaml`.

These commands have no `-o` and print no document:

- `context list` and `context current` print for people. A script that needs
  the active context reads the first field of `refresh context current`, which
  is the name.
- `context add`, `context remove`, and `context use` print one line that says
  what changed.
- `ui` is a terminal app.
- `completion` prints a shell script, and `help` prints help.

## stdout and stderr

| Run | stdout | stderr |
|---|---|---|
| `-o table` | The table, and the result of a change | Progress, questions, warnings, errors |
| `-o json`, `-o yaml` | Exactly one document | Everything else |
| `-o plain` | The TSV rows | Everything else |

A run with `-o json` or `-o yaml` never prompts. A changing command then needs
`--yes` or `--dry-run`:

```text
Error: no terminal to confirm the change; pass --yes
```

A command that fails before it has a result prints nothing on stdout.

## Documents

Every document starts with `apiVersion` and `kind`:

```json
{
  "apiVersion": "refresh.drod.dev/v2",
  "kind": "Version",
  "version": "v0.16.0-158-gb62e59d",
  "commit": "b62e59d",
  "buildDate": "2026-10-14T01:17:44Z"
}
```

| Kind | Printed by |
|---|---|
| `FleetStatus` | `refresh status` |
| `ClusterList` | `refresh cluster list` |
| `ClusterDescription` | `refresh cluster describe` |
| `UpgradeCheck` | `refresh cluster upgrade-check` |
| `InsightDescription` | `refresh cluster upgrade-check --insight` |
| `NodegroupList` | `refresh nodegroup list` |
| `NodegroupDescription` | `refresh nodegroup describe` |
| `AddonList` | `refresh addon list` |
| `AddonDescription` | `refresh addon describe` |
| `Plan` | Every `--dry-run`, and a run that was blocked before any change |
| `Run` | Every other run of a changing command |
| `Version` | `refresh version` |

Each kind has a JSON Schema. See [JSON schemas](../reference/schemas.md).

### Rules

- A resource has one shape. A cluster, a nodegroup, or an add-on has the same
  keys in every document that contains it.
- No value is `null`. An empty list is `[]`. An empty map is `{}`. A key whose
  data was not collected is left out.
- Enum values of `refresh` are PascalCase, such as `Outdated` or `Succeeded`.
  Values of AWS pass through unchanged, such as `ACTIVE` or `ON_DEMAND`.
- A health result has an `outcome`: `Pass`, `Warn`, `Fail`, `Skipped`, or
  `Unreadable`.
- Timestamps are RFC 3339 in UTC.

### Compatibility

Within `apiVersion: refresh.drod.dev/v2`, changes are additive only. A release
can add a key to an object, add a value to an enum, or add a kind.

A script must ignore a key that it does not know, and handle an enum value
that it does not know. For a failure, treat an unknown `reason` like
`Unknown`. Check `apiVersion` before you parse, and fail on a version that the
script does not support.

`-o yaml` has the same keys, values, and structure as `-o json`. The order of
the keys carries no meaning.

The documents of releases up to 0.16 were `refresh.drod.dev/v1`. See
[Migrating from 0.16](../migrating.md#documents) for what changed.

## Plan and Run

The five changing commands print the same two kinds.

A `Plan` has these keys:

| Key | Content |
|---|---|
| `operation` | `NodegroupUpdate`, `NodegroupScale`, `AddonUpdate`, `ClusterUpgrade`, or `ClusterRollback` |
| `clusters` | One entry for each cluster. Only `nodegroup update --all-clusters` has more than one. |
| `skippedRegions` | The regions that a default sweep skipped |
| `failures` | What could not be read |

Each entry of `clusters`:

| Key | Content |
|---|---|
| `cluster`, `region`, `version`, `targetVersion` | The cluster, and the version to reach for an upgrade or a rollback |
| `decision` | `Proceed`, `Ask`, `Blocked`, or `NothingToDo` |
| `steps` | One entry for each thing that the run would do or leave alone |
| `gate` | The health gate: `decision` (`Go`, `Ask`, `Block`), `reasons`, `accepted`, `forced`, and `checks` |
| `blockers` | What stops the plan apart from the gate: `kind` (`Busy`, `NotReady`, `Unavailable`, `Unread`), `step`, `message` |
| `notices` | Lines for people |
| `rollback` | The rollback window, for `cluster rollback` |

Each step:

| Key | Content |
|---|---|
| `phase` | `ControlPlane`, `AddonsBefore`, `Nodegroups`, `AddonsAfter`, `Addons`, `Roll`, or `Scale` |
| `hop` | The minor version of this step, in an upgrade of several versions |
| `kind`, `name` | `ControlPlane`, `Addon`, `Nodegroup`, or `Scale`, and the name of the resource |
| `action` | `Submit`, `Attach`, `Verify`, `Skip`, `Manual`, or `Blocked` |
| `reason` | Why, such as `AMIOutdated`, `AlreadyLatest`, `CustomAMI`, `VersionBehind`. A step that moves a resource to an older version, in a rollback or an `addon update --version`, is `VersionAhead`. |
| `from`, `to` | The versions. `fromRelease`, `fromImage`, and `toImage` give the AMI of a roll. |
| `sizes` | `before` and `after` of a scale, each with `desired`, `min`, `max` |

A step is identified by `phase`, `hop`, `kind`, and `name` together.

An example, trimmed, from `refresh nodegroup update -c prod-payments-00 --dry-run -o json`:

```json
{
  "apiVersion": "refresh.drod.dev/v2",
  "kind": "Plan",
  "operation": "NodegroupUpdate",
  "clusters": [
    {
      "cluster": "prod-payments-00",
      "region": "us-east-1",
      "version": "1.34",
      "decision": "Blocked",
      "steps": [
        {
          "phase": "Roll",
          "kind": "Nodegroup",
          "name": "ng-a",
          "action": "Submit",
          "reason": "AMIOutdated",
          "from": "1.34",
          "to": "1.34",
          "fromRelease": "1.34.0-20260101",
          "fromImage": "ami-old134",
          "toImage": "ami-latest134"
        }
      ],
      "gate": {
        "decision": "Block",
        "reasons": [
          "DrainBlockers: PDB app/web allows 0 disruptions on the target nodegroups; the roll would stall on eviction"
        ],
        "accepted": [],
        "forced": [],
        "checks": [
          {
            "name": "Nodes",
            "outcome": "Pass",
            "blocking": true,
            "message": "13/13 nodes ready",
            "details": [],
            "findings": []
          }
        ]
      },
      "blockers": [],
      "notices": []
    }
  ],
  "failures": []
}
```

A `Run` has `operation`, `status`, `clusters`, and `failures`. Each entry of
`clusters` has the keys of a plan entry, and three more:

| Key | Content |
|---|---|
| `status` | The result for this cluster |
| `results` | One entry for each step of the plan, in plan order |
| `verification` | The checks after a roll, as health results |

`status` of a run and of a cluster is one of:

| Status | Meaning | Exit code |
|---|---|---|
| `Succeeded` | Every change finished. | `0` |
| `NothingToDo` | Nothing needed a change. | `0` |
| `Started` | `--no-wait`: AWS accepted the changes. | `0` |
| `Declined` | A question was answered no. | `1` |
| `Stopped` | The run was stopped between phases. | `1` |
| `Failed` | A change failed. | `1` |
| `Interrupted` | Ctrl+C. | `1` |
| `Refused`, `Blocked` | The busy rule or a gate stopped the change. | `3`, or `4` when something could not be read |
| `Incomplete` | A partial failure, a wait that timed out, or a read that failed. | `4` |
| `VerifyFailed` | The change was applied and the verification found an issue. | `5` |

Each result has the identity of its step and:

| Key | Content |
|---|---|
| `status` | `NotAttempted`, `Started`, `Succeeded`, `Failed`, `Cancelled`, `InProgress`, `Busy`, `Skipped`, or `Blocked` |
| `updateId` | The EKS update |
| `attached` | The run followed an update that it did not start |
| `inFlight` | The update that kept a `Busy` target busy |
| `startedAt`, `finishedAt` | Times |
| `failure` | The failure of this step |

```json
{
  "phase": "Roll",
  "kind": "Nodegroup",
  "name": "ng-c",
  "status": "Succeeded",
  "updateId": "00000000-0000-4000-8000-000000000002",
  "startedAt": "2026-10-14T01:18:39.938261Z",
  "finishedAt": "2026-10-14T01:18:42.150221Z"
}
```

## Failures

A command that could not read everything still prints what it read. It is not
a success:

- The document has a top-level `failures` list. It is `[]` when nothing failed.
- A row that is only partly read has `"incomplete": true`.
- The command exits `4`.

```bash
refresh cluster list -r us-east-1,me-central-1 -o json
```

```json
  "failures": [
    {
      "kind": "Region",
      "name": "me-central-1",
      "region": "me-central-1",
      "operation": "eks:ListClusters",
      "reason": "RegionUnavailable",
      "retryable": false,
      "error": "UnrecognizedClientException: The security token included in the request is invalid.",
      "awsErrorCode": "UnrecognizedClientException"
    }
  ]
```

With `-o json`, `-o yaml`, and `-o plain`, each failure is also one `warning:`
line on stderr:

```text
warning: region me-central-1: RegionUnavailable (eks:ListClusters): UnrecognizedClientException: The security token included in the request is invalid.
Error: incomplete data: 1 failure(s) (1 region)
```

The table lists the failures in a section, and writes no warning lines:

```text
INCOMPLETE DATA
○ region me-central-1: RegionUnavailable (eks:ListClusters): UnrecognizedClientException: The security token included in the request is invalid.
```

### The failure object

| Key | Content |
|---|---|
| `kind` | `Region`, `Cluster`, `Nodegroup`, `Addon`, `Insight`, `Update`, `PodDisruptionBudget`, or `Node` |
| `name` | The name of the item |
| `cluster`, `region` | Where the item is, when that applies |
| `operation` | The IAM action of the call that failed, such as `eks:DescribeNodegroup` |
| `reason` | One of the reasons below |
| `retryable` | Whether the same call can succeed later without a change |
| `error` | One line for people |
| `awsErrorCode` | The error code of AWS, when there is one |
| `updateId` | The EKS update, when the failure is about one |

### Reasons

| Reason | Meaning |
|---|---|
| `AccessDenied` | The identity lacks the IAM action in `operation`. |
| `CredentialError` | The credentials are missing, expired, or refused. |
| `Throttled` | AWS throttled the call, also after the retries. |
| `NotFound` | The item does not exist. |
| `RegionUnavailable` | The region is not enabled for the account, or does not answer. |
| `InvalidRequest` | AWS refused the request as invalid. |
| `ServiceError` | AWS answered with a server error. |
| `NetworkError` | The call did not reach AWS. |
| `Timeout` | `--timeout` or `--wait-timeout` passed. |
| `Interrupted` | Ctrl+C. |
| `UpdateFailed` | The EKS update ended as `Failed`. |
| `UpdateCancelled` | The EKS update ended as `Cancelled`. |
| `NotMonitored` | AWS accepted the change, and `refresh` could not follow it. |
| `NotAttempted` | The run ended before this item. |
| `Busy` | The target was already changing. |
| `Unknown` | None of the above. |

### Skipped regions

In a default sweep, a region that the credentials cannot use is skipped. It is
not a failure. The command prints one note on stderr, and the document names
the regions in `skippedRegions`.

## The `plain` format

`-o plain` is TSV, with nothing else on stdout.

- A list command prints one header row and one row for each item. The header
  names match the columns of the table.
- An empty cell is `-`.
- There is no title, no footer, and no blank line.

```bash
refresh cluster list -r us-east-1 -o plain
```

```text
CLUSTER	REGION	STATUS	VERSION
dev-orders-32	us-east-1	ACTIVE	1.34
dev-orders-56	us-east-1	ACTIVE	1.31
dev-platform-08	us-east-1	ACTIVE	1.34
```

A describe command, `cluster upgrade-check`, and the changing commands print
`FIELD` and `VALUE` rows. The fields are the sections of the table view:

```bash
refresh nodegroup describe ng-a -c prod-payments-00 -o plain
```

```text
FIELD	VALUE
overview.status	ACTIVE
overview.version	1.34
overview.instance_types	m5.large
overview.capacity	ON_DEMAND
ami.type	BOTTLEROCKET_x86_64
ami.release	1.34.0-20260101
ami.status	Outdated
ami.current	ami-old134
ami.latest	ami-latest134
```

## Color and marks

The table view marks a status with a glyph, a label, and a color. The glyph
and the label carry the meaning, so the output reads the same without color.

| Mark | Meaning |
|---|---|
| `●` | OK |
| `▲` | Needs attention |
| `✗` | Failed or blocked |
| `◷` | In progress |
| `○` | Not read |

Color is off when stdout is not a terminal, with `--no-color`, with a non-empty
`NO_COLOR`, and with `TERM=dumb`.

## Watch mode

`cluster list`, `nodegroup list`, and `addon list` take `-w/--watch`. The
command reads again every `--watch-interval` (default `10s`, at least `1s`)
and redraws the table.

- `--watch` needs `-o table` and a terminal.
- `--timeout` applies to each refresh.
- A refresh with a partial result draws what it has, and the watch goes on.
- A refresh that returns an error ends the watch with that error.
- Ctrl+C ends the watch with exit `0`.

## jq recipes

```bash
# Clusters with a stale AMI
refresh status -o json | jq -r '.clusters[] | select(any(.nodegroups[]?; .ami.status == "Outdated")) | .name'

# Names and versions
refresh cluster list -o json | jq -r '.clusters[] | "\(.name) \(.version)"'

# Outdated nodegroups of one cluster
refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.ami.status == "Outdated") | .name'

# Fail a job when anything could not be read
refresh status -A -o json | jq -e '.failures | length == 0'

# The checks that did not pass in a dry run
refresh nodegroup update -c prod --dry-run -o json \
  | jq -r '.clusters[].gate.checks[] | select(.outcome != "Pass" and .outcome != "Skipped") | "\(.name): \(.message)"'
```
