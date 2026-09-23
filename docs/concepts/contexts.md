# Contexts

`refresh` has a kubectx-style context system so you don't have to repeat
`--cluster` / `--region` / `--profile` on every command. Contexts are stored in
a YAML file at `~/.config/refresh/context.yaml`.

A context bundles a **cluster**, and optionally a **region** and **profile**.
The *active* context fills in those values (unless a flag or AWS env var
overrides it — see [Configuration & AWS auth](configuration.md)).

## Saving and switching

```bash
# save a named context
refresh context add prod --cluster prod-use1 --region us-east-1 --profile prod

# list saved contexts (the active one is marked)
refresh context list

# switch the active context
refresh use prod

# show the active context
refresh current
```

After `refresh use prod`, commands that take a cluster will default to
`prod-use1` in `us-east-1` under the `prod` profile:

```bash
refresh nodegroup list          # uses the active context
refresh nodegroup list -c other # one-off override, active context untouched
```

## Per-shell override: `REFRESH_CONTEXT`

Set `REFRESH_CONTEXT` to the name of a saved context to use that context in
one shell. The saved current context does not change:

```bash
export REFRESH_CONTEXT=staging
refresh nodegroup list          # uses staging, whatever `refresh use` saved
```

The name must match a saved context. If it does not (for example, a typo such
as `stagee`), every command fails before any AWS call. The error lists the
known names. `refresh context list` shows a warning instead, so you can find
the right name. Unset the variable to go back to the saved current context.

See the [contexts command reference](../commands/contexts.md) for every
subcommand and flag.
