# Contexts

A context gives a name to a cluster, with an optional region and AWS profile.
When a context is active, a command that needs a cluster uses the cluster of
the context, and you do not repeat `-c`, `-r`, and `--profile`.

## Save and switch

```bash
refresh context add prod --cluster prod-payments-00 --region us-east-1 --profile prod --use
refresh context add stage --cluster staging-data-40 --region us-east-1
refresh context list
```

```text
* prod                 cluster=prod-payments-00 region=us-east-1 profile=prod
  stage                cluster=staging-data-40 region=us-east-1 profile=-
```

The `*` marks the active context.

```bash
refresh context use stage     # switch
refresh context use -         # switch back to the context before
refresh context current       # print the active context
```

See [the `context` commands](../commands/contexts.md) for each command.

## Where a context ranks

A command that needs a cluster looks for it in this order:

1. The positional argument or `-c/--cluster`.
2. The `REFRESH_CLUSTER` environment variable.
3. The active context.
4. For a command that only reads: the cluster of the current kubeconfig
   context.

A command that changes something never uses step 4, with or without
`--dry-run`. See [Cluster resolution](configuration.md#cluster-resolution).

## One shell, one context: `REFRESH_CONTEXT`

`REFRESH_CONTEXT` names the active context for one shell. It beats the context
that `refresh context use` saved.

```bash
REFRESH_CONTEXT=stage refresh nodegroup list
```

If `REFRESH_CONTEXT` names a context that is not saved, the command fails.

## A context applies as a unit

The cluster, the region, and the profile of a context belong together. An
environment variable never replaces only one of them.

- If an environment variable sets a value that the context leaves empty, the
  environment variable applies.
- If an environment variable has the same value as the context, nothing
  changes.
- If an environment variable has a different value from the context, the
  command fails before any AWS call.

```text
Error: AWS_PROFILE=staging conflicts with profile "prod" of the active context "prod"; pass --profile for this command, unset AWS_PROFILE, or switch contexts
```

To fix a conflict, do one of these:

1. Pass `--profile` or `--region` for the one command.
2. Unset the environment variable that the error names.
3. Switch to a context that matches.

## The file

The contexts are in a YAML file. `refresh` looks for it in this order:

1. `$REFRESH_CONFIG_HOME/context.yaml`
2. `$XDG_CONFIG_HOME/refresh/context.yaml`
3. `~/.config/refresh/context.yaml`

```yaml
current: prod
previous: stage
contexts:
    prod:
        cluster: prod-payments-00
        region: us-east-1
        profile: prod
    stage:
        cluster: staging-data-40
        region: us-east-1
```

A file that exists and cannot be read or parsed is an error. Only a missing
file means "no context".
