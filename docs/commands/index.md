# Command reference

`refresh` is organized into a few command groups. Pick a page for the full
flag-by-flag detail and examples.

!!! tip "Looking for the exhaustive flag list?"
    These pages are the **curated guide** — the flags you'll actually use, with
    examples. For the complete, always-in-sync list of *every* command and flag,
    see the **[generated Reference](../reference/index.md)** (produced directly
    from the CLI, so it can't drift). You can also run `refresh <command> --help`
    or `man refresh`.

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
`--log-level`, `--verbose`, `--no-color`. They work before or after the
subcommand (`refresh -t 5s cluster list` equals `refresh cluster list -t 5s`).

## Conventions

- **Cluster argument** — most commands take the cluster as a positional
  (`refresh nodegroup list my-cluster`) or via `--cluster/-c`, falling back to
  the [active context](../concepts/contexts.md). Read-only commands also fall
  back to the kubeconfig's current cluster. See
  [Cluster resolution](../concepts/configuration.md#cluster-resolution).
- **Output** — `-o table|json|yaml|plain` (and `tree` for `cluster list`); see
  [Output formats](../concepts/output.md). With `-o json`/`-o yaml`, stdout
  holds exactly one document.
- **Exit codes** — every command follows one contract: `0` ok, `1` error or
  interrupt, `2` needs attention, `3` blocked, `4` incomplete data, `5`
  post-action verification failed. Each command's `--help` lists its codes;
  see [Exit codes](../concepts/exit-codes.md).
