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
refresh cluster list -o json | jq '.[] | select(.status=="ACTIVE") .name'
refresh nodegroup list -c prod -o plain | awk -F'\t' '{print $1, $4}'
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

## Key consistency

`json` and `yaml` emit the **same** camelCase keys (e.g. `instanceType`,
`createdAt`), so `jq '.instanceType'` and `yq '.instanceType'` both work.

## `plain` is robust TSV

`plain` strips ANSI color and neutralizes any embedded tabs/newlines in a cell,
so one logical row is always exactly one well-formed TSV line — safe for
`awk -F'\t'`.

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
