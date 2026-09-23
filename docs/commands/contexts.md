# Contexts (use / current / context)

A kubectx-style workflow for EKS. Save named contexts that bind a cluster to an
optional region and AWS profile, then switch between them by name instead of
repeating `--cluster/--region/--profile` on every command.

```bash
refresh use <name>          # switch the active context
refresh current             # print the active context
refresh context add|list|remove [args] [flags]
```

The active context fills in cluster/region/profile defaults for every command.
Per-invocation flags still override it. A mutating command that takes its
cluster from the context prints `Using cluster <cluster> (from context <name>)`
to stderr. For the conceptual overview see
[Contexts](../concepts/contexts.md); this page is the command-reference detail.

!!! note "Where contexts are stored"
    Contexts are saved as YAML under `$XDG_CONFIG_HOME/refresh/context.yaml`
    (default `~/.config/refresh/context.yaml`). The `REFRESH_CONTEXT` env var
    overrides the saved "current" pointer for a single shell. It must name a
    saved context; otherwise every command fails and lists the known names.

---

## refresh use

Switch the active context so subsequent commands inherit its cluster, region,
and profile.

```bash
refresh use [context-name|-]
```

- `refresh use prod` — make `prod` active.
- `refresh use -` — toggle back to the previously active context.
- `refresh use` — no name: pick interactively from the saved list. The
  answer can also come from a pipe: `printf prod | refresh use`.

Per-invocation `--region/--profile/--cluster` flags still override the active
context.

```bash
refresh use prod
refresh use -
```

---

## refresh current

Print the name and cluster/region/profile of the currently active context. Honors
the `REFRESH_CONTEXT` override (an unknown name is an error), and prints a hint
to stderr when no context is active.

```bash
refresh current
```

---

## refresh context

Manage the named contexts that `refresh use` switches between. The group has the
alias `ctx`.

```bash
refresh context <list|add|remove> [args] [flags]
```

### context list

List every saved context with its cluster, region, and profile. The active
context is marked with a `*`. Aliased as `ls`. Hints go to stderr. If
`REFRESH_CONTEXT` names an unknown context, `context list` prints a warning
instead of failing, so you can find the right name.

```bash
refresh context list
```

### context add

Create or overwrite a named context. Re-running with the same name updates it in
place.

```bash
refresh context add <name> --cluster <cluster> [flags]
```

| Flag | Description |
|---|---|
| `--cluster, -c` | **Required.** EKS cluster name |
| `--region, -r` | AWS region (optional) |
| `--profile` | AWS shared-config profile (optional). `-p` was removed in 0.11 |
| `--use` | Switch to this context immediately after saving |

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
refresh context add stage --cluster stage-eks --region us-west-2 --profile stage

# Add and switch in one step
refresh context add prod --cluster prod-eks --use
```

### context remove

Delete a saved context by name. If the removed context was the active or
previous one, those pointers are cleared. Aliased as `rm` / `delete`.

```bash
refresh context remove prod
```

---

## Typical workflow

```bash
# Define your environments once
refresh context add prod  --cluster prod-eks  --region us-east-1 --profile prod
refresh context add stage --cluster stage-eks --region us-west-2 --profile stage

# Switch, then run commands with no repeated flags
refresh use prod
refresh nodegroup list          # targets prod-eks / us-east-1 / prod
refresh cluster upgrade-check   # same context

refresh use stage               # flip the whole environment
```
