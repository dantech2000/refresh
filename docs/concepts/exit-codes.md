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
