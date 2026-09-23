# addon

Inspect and update the managed EKS add-ons (`vpc-cni`, `coredns`, `kube-proxy`,
and others) on a cluster. List shows installed versions and status, describe
drills into one add-on, and update rolls a single add-on or every add-on
(`--all`) to a compatible version with optional health gating and waiting.

```bash
refresh addon <list|describe|update> [args] [flags]
```

The cluster is a positional on each subcommand, or `--cluster/-c`, falling back
to the [active context](../concepts/contexts.md).

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
| `--show-health, -H` | Include a health mapping/badge in table output |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--watch, -w` | Re-run and redraw every `--watch-interval` until interrupted |
| `--watch-interval` | Refresh interval for `--watch` (default `10s`) |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

If some add-ons can't be described (for example, a missing
`eks:DescribeAddon` permission), the command prints the add-ons it did get and
names each failed add-on on stderr. `-o json|yaml` lists them under
`failures`. Then the command exits `1`, so a partial list never looks
complete.

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
installed add-ons.

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--addon, -a` | Add-on name (e.g. `vpc-cni`); or pass as second positional |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Operation timeout (env `REFRESH_TIMEOUT`) |

### Examples

```bash
refresh addon describe my-cluster vpc-cni
refresh addon describe my-cluster coredns -o json
```

---

## update

Update a single managed add-on to a target version, or with `--all` update every
add-on in the cluster to its latest compatible version.

```bash
refresh addon update [cluster] [addon] [version] [flags]
```

For a single add-on, pass the add-on name and an optional version (the third
positional or `--version`, defaulting to `latest`). The command exits non-zero
if any add-on update fails.

`--all` can't be combined with an add-on name or version. The command rejects
that combination before it makes any AWS call.

### Version guard

- If the add-on is `ACTIVE` at the target version, the result is `UP_TO_DATE`
  and no update is sent. `UP_TO_DATE` doesn't count as a failure.
- If the add-on is at the target version but `DEGRADED` or `*_FAILED`, the
  update is sent again to repair it.
- If the add-on is already `UPDATING` or `CREATING` at the target version, no
  new update is sent. The result is `IN_PROGRESS`, which is not a failure.
  With `--wait`, the command waits for that operation to finish.
- `latest` never downgrades. If the installed version is newer than every
  compatible version in the catalog, the result is `UP_TO_DATE`.
- If you pin a version older than the installed one, the update goes ahead
  and prints `warning: downgrading <addon> from <installed> to <target>` on
  stderr. The result also carries the text in its `warning` field.

### Add-on names

An exact name, or an exact name in a different case, goes ahead. A name that
only partially matches one installed add-on (`cni` for `vpc-cni`) needs a
confirmation:

- On a terminal, the command asks you to confirm the match.
- Without a terminal, the command fails and names the candidate. Pass the
  exact name, or pass `--yes` to accept the match.

### Waiting

With `--wait`, the command follows the EKS update by its update ID until the
update is `Successful`, `Failed`, or `Cancelled`. Then it checks that the
add-on reports the target version.

- A `Failed` or `Cancelled` update, or an add-on at a different version,
  gives `WAIT_FAILED` and exit code `1`. The result keeps the update ID and
  puts the reason in its `error` field.
- A throttling, server, or network error while polling is retried until
  `--wait-timeout`. A timeout error names the last poll error.
- A permanent API error while polling (for example, a missing
  `eks:DescribeUpdate` permission) fails at once.
- If the update lands but the post-update health check finds issues, the
  result is `COMPLETED_WITH_ISSUES` and the command exits `2`.

The result is printed in every output format, also when the wait fails. See
[exit codes](../concepts/exit-codes.md#addon-update).

### Flags

| Flag | Description |
|---|---|
| `--cluster, -c` | EKS cluster name or pattern (or pass as positional) |
| `--addon, -a` | Add-on name (or pass as second positional) |
| `--version` | Target version or `latest` (default; or pass as third positional) |
| `--all` | Update every add-on in the cluster to its latest version |
| `--health-check` | Verify the add-on is ACTIVE and version-compatible before updating |
| `--dry-run, -d` | Preview without applying changes |
| `--wait` | Wait for each update to complete |
| `--wait-timeout` | Per-add-on wait timeout, with `--wait` (default `5m`) |
| `--parallel, -p` | *(`--all` only)* Update add-ons in parallel |
| `--dependency-order` | *(`--all` only)* Update in dependency-safe order: `vpc-cni` → `coredns`/`kube-proxy` → others |
| `--skip, -s` | *(`--all` only)* Skip specific add-ons (repeatable) |
| `--yes, -y` | Accept a partial add-on name match without a prompt (for unattended/CI use) |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Timeout for the update API calls (default `10m`); with `--wait`, `--wait-timeout` per add-on is added on top. Not read from `REFRESH_TIMEOUT` |

!!! note "`--all`-only flags"
    `--parallel`, `--dependency-order`, and `--skip` apply only with `--all`. On
    a single-add-on update they're ignored with a warning. `--parallel` and
    `--dependency-order` are mutually exclusive (parallel defeats ordering).
    A single-add-on update honors `-o json|yaml` for a machine-readable result.

### Examples

```bash
# vpc-cni -> latest
refresh addon update my-cluster vpc-cni

# Pin a version
refresh addon update my-cluster coredns v1.11.1

# Preview only
refresh addon update my-cluster vpc-cni --dry-run

# All add-ons, dependency-safe order, waiting for each to settle
refresh addon update my-cluster --all --dependency-order --wait

# All add-ons in parallel, skipping vpc-cni
refresh addon update my-cluster --all --skip vpc-cni --parallel
```
