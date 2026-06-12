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

See the [contexts command reference](../commands/contexts.md) for every
subcommand and flag.
