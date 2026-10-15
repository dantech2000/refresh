# context

`refresh context` manages the saved contexts and chooses the active one. A
context gives a name to a cluster, with an optional region and AWS profile.
See [Contexts](../concepts/contexts.md) for how a context is used.

`refresh ctx` is an alias of `refresh context`.

| Command | Use |
|---|---|
| [`context add`](#add) | Add or replace a saved context. |
| [`context list`](#list) | List the saved contexts. |
| [`context use`](#use) | Switch the active context. |
| [`context current`](#current) | Print the active context. |
| [`context remove`](#remove) | Remove a saved context. |

These commands make no AWS call. They do not check that the cluster exists.

## add

```bash
refresh context add <name> --cluster <cluster> [--region <region>] [--profile <profile>] [--use]
```

| Flag | Meaning |
|---|---|
| `-c, --cluster` | EKS cluster name. Required. |
| `-r, --region` | AWS region to save with the context. |
| `--profile` | AWS shared-config profile to save with the context. |
| `--use` | Switch to the context after it is saved. |

A context with the same name is replaced, and the command prints
`Updated context "<name>"` in place of `Saved context "<name>"`.

The command checks the values before it saves them, and an error saves
nothing:

- The name has 1 to 63 letters, digits, hyphens (`-`), underscores (`_`), and
  dots (`.`). The name `-` alone is not allowed: `refresh context use -` means
  the previous context.
- The cluster name follows the EKS rules (see
  [Names are exact](../concepts/configuration.md#names-are-exact)).
- The region has the shape of a region name, such as `us-east-1` (see
  [Regions](../concepts/configuration.md#regions)).

```bash
refresh context add prod --cluster prod-payments-00 --region us-east-1 --profile prod
```

```text
Saved context "prod"
```

## list

```bash
refresh context list
```

```text
* prod                 cluster=prod-payments-00 region=us-east-1 profile=prod
  stage                cluster=staging-data-40 region=us-east-1 profile=-
```

## use

```bash
refresh context use [context|-]
```

- With a name, the command switches to that context.
- With `-`, it switches back to the context that was active before.
- Without an argument, it lists the saved contexts and asks for one.

```text
Switched to context "stage" (cluster=staging-data-40 region=us-east-1 profile=-)
```

## current

```bash
refresh context current
```

```text
prod  cluster=prod-payments-00  region=us-east-1  profile=prod
```

The `context` commands have no `-o`: they print text for people. A script
reads the first field of the line, which is the name of the active context:

```bash
refresh context current | cut -d' ' -f1
```

When no context is active, the command prints a hint on stderr and nothing on
stdout, and exits `0`. If `REFRESH_CONTEXT` names a context that is not saved,
the command fails with exit `1`.

## remove

```bash
refresh context remove <name>
```

If the removed context is the active one, no context is active afterwards.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Done. |
| `1` | An error: an unknown context, a missing `--cluster`, or a context file that cannot be read. |
