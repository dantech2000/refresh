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

## Key consistency

`json` and `yaml` emit the **same** camelCase keys (e.g. `instanceType`,
`createdAt`), so `jq '.instanceType'` and `yq '.instanceType'` both work.

## `plain` is robust TSV

`plain` strips ANSI color and neutralizes any embedded tabs/newlines in a cell,
so one logical row is always exactly one well-formed TSV line — safe for
`awk -F'\t'`.

## Color

Color auto-disables when stdout is piped. Force it off with `--no-color` or the
`NO_COLOR` environment variable.

## Watch mode

`cluster list`, `nodegroup list`, and `addon list` support `--watch`
(with `--watch-interval`, default `10s`): top-style redraw on a terminal,
append-only when piped.
