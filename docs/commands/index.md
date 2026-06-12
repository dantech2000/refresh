# Command reference

`refresh` is organized into a few command groups. Pick a page for the full
flag-by-flag detail and examples.

!!! note "Generated reference coming"
    A complete, always-in-sync command/flag reference is generated directly from
    the CLI (tracked as REF-108). Until that lands, these hand-written pages
    cover every command and the flags you'll actually use. You can also always
    run `refresh <command> --help` or `man refresh`.

| Group | What it does |
|---|---|
| [`status`](status.md) | Fleet patch posture across clusters/regions |
| [`cluster`](cluster.md) | `list`, `describe`, `upgrade-check`, `upgrade` |
| [`nodegroup`](nodegroup.md) | `list`, `describe`, `scale`, `update` (AMI roll) |
| [`addon`](addon.md) | `list`, `describe`, `update` (incl. `--all`) |
| [Contexts](contexts.md) | `use`, `current`, `context add/list/remove` |
| [Utility](utility.md) | `version`, `install-man`, `completion` |

## Global flags

Accepted on every command — see [Configuration & AWS auth](../concepts/configuration.md):

`--profile`, `--region`, `--timeout/-t`, `--max-concurrency/-C`,
`--log-level`, `--verbose`, `--no-color`.

## Conventions

- **Cluster argument** — most commands take the cluster as a positional
  (`refresh nodegroup list my-cluster`) or via `--cluster/-c`, falling back to
  the [active context](../concepts/contexts.md).
- **Output** — `-o table|json|yaml|plain` (and `tree` for `cluster list`); see
  [Output formats](../concepts/output.md).
