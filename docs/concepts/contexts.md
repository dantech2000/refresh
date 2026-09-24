# Contexts

`refresh` has a kubectx-style context system so you don't have to repeat
`--cluster` / `--region` / `--profile` on every command. Contexts are stored in
a YAML file at `~/.config/refresh/context.yaml`.

A context bundles a **cluster**, and optionally a **region** and **profile**.
The *active* context fills in those values as a unit. A flag overrides a value
for one command. An AWS environment variable (`AWS_PROFILE`,
`AWS_DEFAULT_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION`) that disagrees with
the context is an error, so the context's cluster never runs with another
account or region. See
[Configuration & AWS auth](configuration.md#aws-credential-region-resolution).

If the context file exists but cannot be read or parsed, every command that
uses it fails and names the file. Fix the file, or remove it to start again.

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

`REFRESH_CONTEXT` wins over `refresh use`. If you run `refresh use prod` while
`REFRESH_CONTEXT` names another context, refresh saves `prod` as the current
context and prints a warning: commands in this shell still use the
`REFRESH_CONTEXT` context. The `refresh use` picker and `refresh context list`
mark the context that commands use now.

See the [contexts command reference](../commands/contexts.md) for every
subcommand and flag.
