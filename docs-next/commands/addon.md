# addon

| Command | Use |
|---|---|
| [`addon list`](#list) | List the EKS add-ons of a cluster. |
| [`addon describe`](#describe) | Describe one add-on. |
| [`addon update`](#update) | Update add-ons. |

The cluster comes from `-c/--cluster`, then `REFRESH_CLUSTER`, then the active
context. `list` and `describe` also take the cluster of the current kubeconfig
context. `update` never does.

## list

```bash
refresh addon list -c <cluster> [flags]
```

```text
ADD-ONS  dev-platform-05, 4

NAME                    VERSION             STATUS      HEALTH
coredns                 v1.35.2-eksbuild.1  ✗ DEGRADED  ✗ Failed
eks-pod-identity-agent  v1.35.2-eksbuild.1  ● ACTIVE    ● Healthy
kube-proxy              v1.35.2-eksbuild.1  ● ACTIVE    ● Healthy
vpc-cni                 v1.35.2-eksbuild.1  ● ACTIVE    ● Healthy
```

`HEALTH` is the status as one of four values: `Healthy`, `Failed`,
`InProgress`, or `Unknown`. A `DEGRADED` add-on is `Failed`. The value is the
same in every document.

| Flag | Meaning |
|---|---|
| `-w, --watch` | Read again and redraw every `--watch-interval`, until Ctrl+C. |

An add-on that could not be read is listed under `INCOMPLETE DATA` or in
`failures`, and the command exits `4`.

## describe

```bash
refresh addon describe <addon> -c <cluster>
```

```text
coredns   ✗ DEGRADED   dev-platform-05 in ap-northeast-1

OVERVIEW
  version   v1.35.2-eksbuild.1
  status    ✗ DEGRADED
  health    ✗ Failed

ISSUES  1
  NAME                          MESSAGE
  InsufficientNumberOfReplicas  The add-on is unhealthy because it doesn't have the desired number of replicas.
```

The name is exact. For a name that does not exist, the command lists the
add-ons whose names are close.

```bash
# The configuration values alone
refresh addon describe coredns -c prod -o json | jq -r .addon.configuration
```

## update

```bash
refresh addon update [addon...] -c <cluster> [flags]
refresh addon update --all -c <cluster> [flags]
```

The command updates add-ons to the latest version that the cluster can run.
With `--version`, it moves one add-on to that version.

### `--all`

`--all` updates every installed add-on, one after another, in dependency
order: `vpc-cni` before `coredns` and `kube-proxy`, and those before the
storage drivers. The order is not optional. A failed add-on does not stop the
others. `--skip-addon` leaves an add-on out.

```bash
refresh addon update --all -c staging-checkout-16 --dry-run
```

```text
Plan   ● Proceed   AddonUpdate

STEPS  4
  NAME                    KIND   ACTION  REASON           FROM                TO                  NOTE
  eks-pod-identity-agent  Addon  Skip    AlreadyAtTarget  v1.35.2-eksbuild.1  v1.35.2-eksbuild.1  -
  vpc-cni                 Addon  Submit  VersionBehind    v1.35.1-eksbuild.1  v1.35.2-eksbuild.1  -
  coredns                 Addon  Submit  VersionBehind    v1.35.1-eksbuild.1  v1.35.2-eksbuild.1  -
  kube-proxy              Addon  Skip    AlreadyAtTarget  v1.35.2-eksbuild.1  v1.35.2-eksbuild.1  -

HEALTH GATE
  decision  ● Go
```

### What a run does

1. It reads the whole cluster and builds the plan.
2. It refuses while the cluster or anything else in it is changing.
3. It leaves an add-on alone that is already at its target.
4. It leaves an add-on alone that is already changing. That add-on is listed
   in `failures`, and the run exits `4`. There is one exception: if the add-on
   is changing to the version that this run wants, the command waits for that
   update and sends none.
5. It asks once. `--yes` skips the question.
6. It sends each update and waits for it, for `--wait-timeout` each.
   `--no-wait` returns once AWS accepts the updates.
7. It checks that each updated add-on is `ACTIVE` at the new version.

```text
◷ vpc-cni: Started (update 00000000-0000-4000-8000-000000000002)
● vpc-cni: Succeeded
◷ coredns: Started (update 00000000-0000-4000-8000-000000000003)
● coredns: Succeeded
```

### A degraded add-on

An add-on that is `DEGRADED`, or whose last change failed, can be updated to
repair it. The health gate warns, so the command asks first:

```text
Error: dry run: a real run would ask about health warnings
Plan   ▲ Ask   AddonUpdate

STEPS  1
  NAME     KIND   ACTION  REASON  FROM                TO                  NOTE
  coredns  Addon  Submit  Repair  v1.35.2-eksbuild.1  v1.35.2-eksbuild.1  -

HEALTH GATE
  decision  ▲ Ask
  reasons   Addons: coredns is DEGRADED (InsufficientNumberOfReplicas); the update will try to repair it
```

### Flags

| Flag | Meaning |
|---|---|
| `--all` | Update every installed add-on, in dependency order. |
| `--skip-addon` | An add-on that `--all` leaves out. Repeat it, or separate the names with commas. |
| `--version` | The add-on version to move to, with exactly one add-on. |
| `--wait-timeout` | How long to wait for each add-on. Default `5m`. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply, except `--force`: an add-on update drains nothing.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Done, or nothing to do. |
| `1` | An error, a question answered no, or an interrupt. Every update that was sent failed. |
| `2` | With `--dry-run` only: a real run would ask about warnings. |
| `3` | Blocked, and nothing changed: the cluster is busy, a gate stops the change, or `--require-healthy` and a warning. |
| `4` | Incomplete: a target was already changing, a wait timed out, something could not be read, or an add-on failed and another was updated. |
| `5` | An add-on was updated and is not healthy. |

### Examples

```bash
# Update one add-on to the latest version
refresh addon update vpc-cni -c prod

# Update every add-on but one, without a prompt
refresh addon update --all --skip-addon coredns -c prod --yes

# Move one add-on to a version, and do not wait for it
refresh addon update coredns --version v1.11.4-eksbuild.2 -c prod --yes --no-wait
```
