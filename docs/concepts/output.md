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
refresh nodegroup list -c prod -o plain | awk -F'\t' 'NR>1 {print $1, $4}'
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
- Machine formats never prompt. A run that would ask a question fails with
  an error that names the missing flag, usually `--yes`.

The exit code is the same as in the human view. When a command fails before
it has a result (bad credentials, a blocked health gate, a missing `--yes`),
stdout is empty and the error is on stderr.

The documents for the mutating commands:

| Command | Document on stdout |
|---|---|
| `nodegroup update` | The run summary: `cluster`, `started`, `skipped`, `customUnmanaged`, `failed`, and `verification` |
| `nodegroup update --dry-run` | The preview: `cluster`, `dryRun`, `force`, and one `nodegroups` entry per nodegroup with its `action` (`update`, `force-update`, `skip-updating`, `skip-latest`) |
| `nodegroup update --health-only` | The health verdict. The exit code is `0`, `2`, or `3` |
| `nodegroup update --all-clusters` | `clusters` (one result per cluster, or one preview with `--dry-run`), plus `discoveryErrors` and `skippedRegions`. With no clusters found, `clusters` is an empty list |
| `cluster upgrade --dry-run` | The plan |
| `cluster upgrade --yes` | `{plan, report}`: the plan the run started from and what it did (`completed`, `failedAt`, `remaining`). A blocked plan prints the plan alone and exits `1` |

`cluster upgrade -o json|yaml` without `--dry-run` needs `--yes`, because it
can't confirm each phase. `nodegroup update -o json|yaml` needs `--yes` when a
pattern matches more than one nodegroup or the health checks warn.

`--watch` can't be combined with `-o json` or `-o yaml`, because it would
print one document per interval. To poll from a script, run the command in a
loop.

```bash
refresh cluster upgrade -c prod --to 1.33 --yes -o json 2>upgrade.log | jq '.report'
```

## JSON envelopes

List commands wrap their rows in an object, so a jq filter starts from the
array key, not from `.[]`:

| Command | Top-level shape |
|---|---|
| `cluster list` | `{"clusters": [...], "count": N}` |
| `nodegroup list` | `{"cluster": "...", "nodegroups": [...], "count": N}`, plus `"failures"` when some nodegroups could not be described |
| `addon list` | `{"cluster": "...", "addons": [...], "count": N}` |
| `status` | `{"clusters": [...]}` |

Describe commands (`cluster describe`, `nodegroup describe`, `addon describe`)
print the object itself, with no envelope.

```bash
refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.amiStatus == "Outdated") | .name'
refresh addon list prod -o json | jq -r '.addons[] | "\(.name) \(.version)"'
refresh status -o json | jq -r '.clusters[] | select(.staleAmi.behind > 0) | .name'
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
surface — `status`, `cluster`, `nodegroup`, `addon` — reads consistently:

- **Status tokens** pair a glyph with a label and color: `●` healthy/current,
  `▲` warn/stale, `✗` failed/unsupported, `◷` in-progress, `○` unknown. The
  glyph and label always carry the meaning, so **color is purely additive** —
  output stays fully legible with `--no-color`, when piped, or on a non‑UTF‑8
  terminal (where glyphs fall back to `[OK] [!] [X] [~] [?]`).
- **Color depth adapts to the terminal:** 24‑bit truecolor when the terminal
  advertises it (`COLORTERM=truecolor`), a 256‑color approximation otherwise,
  and no color when piped / `NO_COLOR` / `--no-color`.

This styling applies only to the human view. **`-o json`, `-o yaml`, and
`-o plain` are unaffected** — their bytes are identical regardless of terminal
or color settings, so scripts are never surprised.

## Color

Color auto-disables when stdout is piped. Force it off with `--no-color` or the
`NO_COLOR` environment variable.

## Live node-roll view

When you patch a nodegroup's AMI, `refresh nodegroup update --live` shows a
**real-time, per-node view** of the roll — nodes draining (with pod-eviction
progress), terminating, and coming online — instead of a single spinner. It is
line-oriented (it redraws in place on a terminal and appends snapshots when
piped); it is **not** a full-screen TUI.

```bash
refresh nodegroup update -c prod -n web --live
```

`--live` requires cluster (kubeconfig) access and a single nodegroup; it falls
back to standard monitoring otherwise, and the EKS update status stays
authoritative for the result regardless. Old-vs-new is determined from a
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
