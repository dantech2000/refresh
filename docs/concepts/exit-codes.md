# Exit codes

`refresh` uses meaningful exit codes so it slots into CI/cron pipelines.

## General

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | A general error (bad flags, AWS error, not found, etc.) |

`refresh status` exits non-zero when the fleet has items needing attention, so
it works as a gate (e.g. fail a pipeline if anything is on extended support or
badly behind).

## `status`

| Code | Meaning |
|---|---|
| `0` | Every cluster is current and in standard support |
| `2` | Something is stale (nodegroup AMI or addon behind latest) |
| `3` | A cluster is on extended support or unsupported |
| `4` | **Incomplete data**: a cluster row has errors (a failed AWS call, or a sweep that timed out before it reached the cluster), or a region could not be listed |

When more than one applies, the highest-priority code wins: `3`, then `2`,
then `4`. Incomplete data never exits `0`. Rows with errors show an unknown
marker and their error text in the table, an `ERRORS` column in `-o plain`, and
an `errors` field in `-o json`/`-o yaml`.

## `addon update`

| Code | Meaning |
|---|---|
| `0` | Success: updates started, completed, or were already `UP_TO_DATE` |
| `1` | An update failed: the API call failed, or with `--wait` the EKS update was `Failed`/`Cancelled`, the add-on ended at another version, or the wait timed out (`WAIT_FAILED`) |
| `2` | **Needs attention**: every update landed, but a post-update health check found issues (`COMPLETED_WITH_ISSUES`) |

With `--all`, a failure (`1`) wins over health issues (`2`). The result is
printed before the command exits, so `-o json` output always has the update
ID and status.

```bash
refresh addon update -c prod vpc-cni --wait -o json
case $? in
  0) echo "updated" ;;
  2) echo "updated, but check the add-on's health" ;;
  *) echo "update failed" ;;
esac
```

## `nodegroup update`

The patch command has a richer contract so unattended runs can branch on the
outcome:

| Code | Meaning |
|---|---|
| `0` | Success — updates started/completed as expected |
| `2` | Health **warnings** (with `--health-only` or `--require-healthy`) |
| `3` | Health **blocked** — a pre-flight check failed; nothing was rolled |
| `4` | One or more nodegroup updates **failed to start** |
| `5` | Post-roll **verification** found issues (nodes not Ready / newly-stuck pods) |

An interrupt (Ctrl+C) or a monitoring timeout exits `1`. The EKS update keeps
running in AWS; check it with `refresh nodegroup list <cluster>`.

In fleet mode (`--all-clusters`) the run exits with the worst code across
clusters (`5`, then `4`, then `3`, then `2`, then `1`). A cluster stopped
by health warnings (`--health-only` or `--require-healthy`) counts as `2`. A region whose clusters could
not be listed also counts as `4`. The exception is a region in the default
sweep that your credentials can't use (SCP-denied or not enabled). That
region is skipped with a note on stderr and doesn't count.

Example CI usage:

```bash
refresh nodegroup update -c prod --yes --require-healthy -o json
case $? in
  0) echo "patched cleanly" ;;
  2) echo "health warnings — review" ;;
  3) echo "blocked by health — do not proceed" ;;
  4) echo "some updates failed to start" ;;
  5) echo "rolled, but verification flagged issues" ;;
esac
```

See [`nodegroup update`](../commands/nodegroup.md#update) for the flags that
drive these (`--health-only`, `--require-healthy`, `--skip-verify`).
