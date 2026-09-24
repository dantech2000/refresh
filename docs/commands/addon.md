# addon

Inspect and update the managed EKS add-ons (`vpc-cni`, `coredns`, `kube-proxy`,
and others) on a cluster. List shows installed versions and status, describe
drills into one add-on, and update rolls a single add-on or every add-on
(`--all`) to a compatible version with optional health gating and waiting.

```bash
refresh addon <list|describe|update> [args] [flags]
```

The cluster is a positional on each subcommand, or `--cluster/-c`, falling back
to the [active context](../concepts/contexts.md). `list` and `describe` also
fall back to the kubeconfig's current cluster; `update` never does. See
[Cluster resolution](../concepts/configuration.md#cluster-resolution).

---

## list

List the managed EKS add-ons installed on a cluster along with their current
version, status, and (with `--show-health`) a health badge.

```bash
refresh addon list [cluster] [flags]
```

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--show-health, -H` | Include each add-on's health: a badge in the table, and `health` (`Pass`, `Fail`, `InProgress`, or `Unknown`) with `-o json`. Without it, `HEALTH` shows `-` and `health` is left out |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--watch, -w` | Re-run and redraw every `--watch-interval` until interrupted (not with `-o json`/`-o yaml`) |
| `--watch-interval` | Refresh interval for `--watch` (default `10s`) |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

If some add-ons can't be described (for example, a missing
`eks:DescribeAddon` permission), the command prints the add-ons it did get
and names each failed add-on on stderr (or under `INCOMPLETE DATA` in the
table). `-o json|yaml` lists them under `failures`, which is `[]` when every
add-on was read. Then the command exits `4` (incomplete data), so a partial
list never looks complete.

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "AddonList",
  "cluster": "prod",
  "addons": [
    {"name": "coredns", "version": "v1.11.4-eksbuild.2", "status": "ACTIVE", "health": "Pass"}
  ],
  "count": 1,
  "failures": [
    {
      "kind": "Addon",
      "name": "vpc-cni",
      "cluster": "prod",
      "region": "us-east-1",
      "operation": "eks:DescribeAddon",
      "reason": "AccessDenied",
      "retryable": false,
      "error": "AccessDeniedException: ... not authorized to perform: eks:DescribeAddon",
      "awsErrorCode": "AccessDeniedException"
    }
  ]
}
```

!!! tip "Watch an update land"
    `refresh addon list my-cluster --watch` keeps the listing live, so you can
    watch an add-on update progress without re-running the command.

### Examples

```bash
refresh addon list my-cluster
refresh addon list my-cluster --show-health
refresh addon list my-cluster -o plain
refresh addon list my-cluster --watch --watch-interval 5s
```

---

## describe

Detailed information for one add-on: its version, status, and configuration.

```bash
refresh addon describe [cluster] [addon] [flags]
```

`describe` has the alias `get`. The add-on name may be the second positional or
`--addon/-a`, and a unique case-insensitive substring is resolved against the
installed add-ons. If a `refresh` context is active and you pass one positional
argument without `--cluster` or `--addon`, that argument is the add-on name, and
the cluster comes from the context.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--addon, -a` | Add-on name (e.g. `vpc-cni`); or pass as second positional |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Global operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
refresh addon describe my-cluster vpc-cni
refresh addon describe my-cluster coredns -o json
refresh addon describe vpc-cni        # in the active context's cluster
```

---

## update

Update a single managed add-on to a target version, or with `--all` update every
add-on in the cluster to its latest compatible version.

```bash
refresh addon update [cluster] [addon] [version] [flags]
```

For a single add-on, pass the add-on name and an optional version (the third
positional or `--version`, defaulting to `latest`). A failed single-add-on
update exits `1`. With `--all`, a failed add-on update exits `4`. With
`--all --parallel`, an add-on that was not started before the deadline or
Ctrl+C has the status `NotAttempted`. The command exits `4` after a deadline
and `1` after Ctrl+C. Every add-on with a failure is named once (see
[Failures](../concepts/output.md#failures)): under `INCOMPLETE DATA` in the
table view, or on a `warning:` line on stderr with `-o json`, `yaml`, or
`plain`. Health issues are named on stderr. The table and `-o plain` show
the status in the `STATUS` column.

`--all` can't be combined with an add-on name or version. The command rejects
that combination before it makes any AWS call.

### Version guard

- If the add-on is `ACTIVE` at the target version, the result is `UpToDate`
  and no update is sent. `UpToDate` doesn't count as a failure.
- If the add-on is at the target version but `DEGRADED` or `*_FAILED`, the
  update is sent again to repair it.
- If the add-on is already `UPDATING` or `CREATING` at the target version, no
  new update is sent. The result is `InProgress`, which is not a failure.
  With `--wait`, the command waits for that operation to finish.
- `latest` never downgrades. If the installed version is newer than every
  compatible version in the catalog, the result is `UpToDate`.
- If you pin a version older than the installed one, the update goes ahead
  and prints `warning: downgrading <addon> from <installed> to <target>` on
  stderr. The result also carries the text in its `warning` field.

### Confirmation

New in 0.11: before it sends an update, `addon update` asks for confirmation,
for example `Update coredns v1.11.1 → v1.11.4 on prod? [y/N]`. Only `y` or
`yes` continues. With `--all`, the command lists every add-on that would
change and asks once. An add-on that is already at the target, or already
updating to it, is not asked about. If the `--all` preview cannot read an
add-on, the command changes nothing and names that add-on under
`INCOMPLETE DATA`. Re-run, skip it with `--skip`, or add `--yes`.

- `--yes` skips the prompt.
- `--dry-run` never prompts.
- With `-o json`/`-o yaml`, or without a terminal, a run without `--yes` or
  `--dry-run` fails before any AWS call. Add `--yes` to scripts.

### Add-on names

An exact name, or an exact name in a different case, goes ahead. The names
come from `eks:ListAddons`. A name that only partially matches one installed
add-on (`cni` for `vpc-cni`) needs a confirmation. A name that matches several
add-ons fails and lists them. For a partial match:

- On a terminal, the command asks you to confirm the match.
- Without a terminal, or with `-o json`/`-o yaml`, the command fails and
  names the candidate. Pass the exact name, or pass `--yes` to accept the
  match.

### Waiting

With `--wait`, the command follows the EKS update by its update ID until the
update is `Successful`, `Failed`, or `Cancelled`. Then it checks that the
add-on reports the target version.

- A `Failed` or `Cancelled` update, or an add-on at a different version,
  gives `WaitFailed` and exit code `1` (`4` with `--all`). The result keeps
  the update ID and has a `failure` with the reason (`UpdateFailed`,
  `UpdateCancelled`, or `Timeout`).
- A throttling, server, or network error while polling is retried until
  `--wait-timeout`. A timeout error names the last poll error.
- A permanent API error while polling (for example, a missing
  `eks:DescribeUpdate` permission) fails at once.
- If the update lands but the post-update health check finds issues, the
  result is `CompletedWithIssues` and the command exits `5` (post-action
  verification failed).
- If the update lands but the post-update health check can't read the
  add-on, the result is `Unverified`, with a `failure`, and the command
  exits `4`: the add-on's health is unknown.

The result is printed in every output format, also when the wait fails. See
[exit codes](../concepts/exit-codes.md#addon-update).

| `status` | Meaning |
|---|---|
| `DryRun` | `--dry-run`: nothing was sent |
| `UpToDate` | The add-on is already at the target; no update was sent |
| `InProgress` | The add-on is already updating to the target; no new update was sent |
| `Started` | The update was sent, and the command did not wait (no `--wait`) |
| `Completed` | The update succeeded, and the post-update health check passed |
| `CompletedWithIssues` | The update landed, but the post-update health check found issues (`healthIssues`) |
| `Unverified` | The update landed, but the post-update health check could not read the add-on (`failure`) |
| `WaitFailed` | The update was sent but did not complete (`failure`) |
| `Failed` | The update could not be sent (`failure`) |
| `NotAttempted` | With `--all`: the run stopped before this add-on (`failure`) |

```json
{
  "apiVersion": "refresh.drod.dev/v1",
  "kind": "AddonUpdate",
  "addonName": "vpc-cni",
  "previousVersion": "v1.18.0-eksbuild.1",
  "newVersion": "v1.19.0-eksbuild.1",
  "updateId": "5e6f7a8b-...",
  "status": "WaitFailed",
  "failure": {"kind": "Update", "name": "vpc-cni", "cluster": "prod", "region": "us-east-1",
              "reason": "UpdateFailed", "retryable": false,
              "error": "addon vpc-cni update 5e6f7a8b-... Failed: ConfigurationConflict: conflicts found",
              "updateId": "5e6f7a8b-..."},
  "startedAt": "2026-09-24T10:00:00Z",
  "failures": [
    {"kind": "Update", "name": "vpc-cni", "cluster": "prod", "region": "us-east-1",
     "reason": "UpdateFailed", "retryable": false,
     "error": "addon vpc-cni update 5e6f7a8b-... Failed: ConfigurationConflict: conflicts found",
     "updateId": "5e6f7a8b-..."}
  ]
}
```

With `--all`, the document is `{"cluster", "dryRun", "results", "failures"}`:
one result per add-on, and every add-on's failure in `failures`.

| Code | Meaning |
|---|---|
| `0` | Success, including `UpToDate` and `InProgress` |
| `1` | An error or an interrupt. For a single add-on, also an update that could not be sent or whose wait failed (`WaitFailed`) |
| `4` | A failure: with `--all`, an add-on update failed, did not complete, or was not attempted; for any run, an add-on that could not be read after its update (`Unverified`) |
| `5` | `CompletedWithIssues`: the update landed, but the post-update health check found issues |

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--addon, -a` | Add-on name (or pass as second positional) |
| `--version` | Target version or `latest` (default; or pass as third positional) |
| `--all` | Update every add-on in the cluster to its latest version |
| `--health-check` | Verify the add-on is ACTIVE and version-compatible before updating |
| `--dry-run, -d` | Preview without applying changes. Never prompts |
| `--wait` | Wait for each update to complete |
| `--wait-timeout` | How long to wait for each add-on update to finish, with `--wait` (default `5m`; `0` = no limit) |
| `--parallel` | *(`--all` only)* Update add-ons in parallel |
| `--dependency-order` | *(`--all` only)* Update in dependency-safe order: `vpc-cni` → `coredns`/`kube-proxy` → others |
| `--skip` | *(`--all` only)* Skip specific add-ons (repeatable) |
| `--yes, -y` | Update without the confirmation prompt, and accept a partial add-on name match (required with `-o json`/`yaml` or without a terminal) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Timeout for the update API calls (default `10m`); with `--wait`, `--wait-timeout` per add-on is added on top. Not read from `REFRESH_TIMEOUT` |

!!! note "`--all`-only flags"
    `--parallel`, `--dependency-order`, and `--skip` apply only with `--all`. On
    a single-add-on update they're ignored with a warning. `--parallel` and
    `--dependency-order` are mutually exclusive (parallel defeats ordering).
    A single-add-on update honors `-o json|yaml` for a machine-readable result.

### Examples

```bash
# vpc-cni -> latest (asks for confirmation)
refresh addon update my-cluster vpc-cni

# The same in a script: no prompt
refresh addon update my-cluster vpc-cni --yes -o json

# Pin a version
refresh addon update my-cluster coredns v1.11.4-eksbuild.2

# Preview only
refresh addon update my-cluster vpc-cni --dry-run

# All add-ons, dependency-safe order, waiting for each to settle
refresh addon update my-cluster --all --dependency-order --wait

# All add-ons in parallel, skipping vpc-cni
refresh addon update my-cluster --all --skip vpc-cni --parallel
```
