# Commands

Every command is `refresh <noun> <verb>`, except the four that have no noun:
`status`, `ui`, `version`, and `completion`.

```text
refresh status
refresh cluster   list | describe | upgrade-check | upgrade | rollback
refresh nodegroup list | describe | update | scale
refresh addon     list | describe | update
refresh context   list | add | remove | use | current
refresh ui
refresh version
refresh completion <bash|zsh|fish|powershell>
```

| Page | Commands |
|---|---|
| [status](status.md) | The fleet overview |
| [cluster](cluster.md) | `list`, `describe`, `upgrade-check`, `upgrade`, `rollback` |
| [nodegroup](nodegroup.md) | `list`, `describe`, `update`, `scale` |
| [addon](addon.md) | `list`, `describe`, `update` |
| [context](contexts.md) | `list`, `add`, `remove`, `use`, `current` |
| [ui](ui.md) | The terminal UI (experimental) |
| [version, completion, and help](utility.md) | `version`, `completion`, `help` |

These pages explain what each command does. The [reference](../reference/refresh.md)
is generated from the binary and lists every flag.

## Rules that hold for every command

- **The positional names the thing that the command acts on.** `-c/--cluster`
  names the cluster for the nodegroup and add-on commands. See
  [Cluster resolution](../concepts/configuration.md#cluster-resolution).
- **Names are exact.** Select by pattern with `-f/--filter`.
- **`-o/--output`** is `table`, `json`, `yaml`, or `plain`. See
  [Output formats](../concepts/output.md).
- **The exit codes** have one meaning everywhere. See
  [Exit codes](../concepts/exit-codes.md).
- **A changing command** runs the health gate, asks once, and waits for the
  change. It has `--dry-run`, `--yes`, and `--no-wait`. See
  [Flags of every changing command](../concepts/configuration.md#flags-of-every-changing-command).
