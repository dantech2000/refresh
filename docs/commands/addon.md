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
With `--version`, it moves one add-on to that version. A version older than
the installed one is a downgrade: the command warns, and the step has the
reason `VersionAhead`.

### `--all`

`--all` updates every installed add-on, one after another, in dependency
order: `vpc-cni` before `coredns` and `kube-proxy`, and those before the
storage drivers. The order is not optional. `--skip-addon` leaves an add-on
out.

### Dependencies

An add-on is not updated when a dependency of it ran before it in the same
run and did not end clean. Clean means `Skipped`, or `Succeeded` and verified
with no issue: the read after the update worked and found the add-on healthy.
So when `vpc-cni` fails, times out, is already changing, ends `DEGRADED`, or
cannot be read after its update, `coredns` and `kube-proxy` are
`NotAttempted`. Their failure says why: "its dependency vpc-cni did not finish
(Failed)", "its dependency vpc-cni was updated and is not healthy (...)", or
"its dependency vpc-cni was updated and could not be read after it". The
add-ons that do not depend on it go on.

Named add-ons run in the order you give, and the same rule holds.
`refresh addon update vpc-cni coredns` does not update `coredns` after
`vpc-cni` failed. The command does not reorder named add-ons. In
`refresh addon update coredns vpc-cni`, `coredns` runs first, before its
dependency has run, so nothing stops it. The plan says so in a notice.

`--no-wait` is refused, with no request, when an add-on to update depends on
one that comes before it in the run: without a wait that order means nothing.
With `--all`, that is any dependent pair. Run without `--no-wait`, or update
the add-ons one at a time. A dependent named before its dependency has no
order to keep, so `--no-wait` goes on.

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
   in `failures`, and so are the add-ons that depend on it. If another add-on
   was updated, the run exits `4`; if not, nothing changed, and the run exits
   `3`. There is one exception: if the add-on
   is changing to the version that this run wants, the command waits for that
   update and sends none.
5. It asks once. `--yes` skips the question.
6. It sends each update and waits for it, for `--wait-timeout` each.
   `--no-wait` returns once AWS accepts the updates. An add-on whose
   dependency did not end clean is not updated (see
   [Dependencies](#dependencies)).
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
dry run: a real run would ask about health warnings
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
| `--version` | The add-on version to move to, with exactly one add-on, such as `v1.11.4-eksbuild.2`. A string that is not a version is an error before any request. A version that the add-on does not list for the Kubernetes version of the cluster is an error that lists the supported versions. |
| `--wait-timeout` | How long to wait for each add-on. Default `5m`. |

The [shared flags](../concepts/configuration.md#flags-of-every-changing-command)
apply, except `--force`: an add-on update drains nothing.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Done, or nothing to do. |
| `1` | An error, a question answered no, or an interrupt. Every update that was sent failed. |
| `2` | With `--dry-run` only: a real run would ask about warnings. |
| `3` | Blocked: the cluster is busy, every target is already changing, a gate stops the change, or `--require-healthy` and a warning. Nothing changed, or nothing more when the gate stops a later step. |
| `4` | Incomplete: a target was already changing beside targets that changed, a wait timed out, something could not be read, or an add-on failed and another was updated. |
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
