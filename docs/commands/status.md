# refresh status

Fleet patch posture across clusters and regions — the front door.

```bash
refresh status [name-pattern] [flags]
```

Reports, per cluster: Kubernetes version, EKS support window (standard vs.
extended support, with extended-support cost exposure), stale AMIs, and add-ons
behind their latest compatible version. Exits non-zero when something needs
attention, so it doubles as a CI gate.

## Flags

| Flag | Description |
|---|---|
| `--all-regions, -A` | Query all EKS-supported regions |
| `--region, -r` | Specific region(s) to query (repeatable) |
| `--max-concurrency, -C` | Max concurrent region requests |
| `--format, -o` | `table` (default), `json`, `yaml`, `plain` |
| `--timeout, -t` | Operation timeout |

With `--all-regions` and no `-r` or `REFRESH_EKS_REGIONS`, regions these
credentials can't use (an SCP denial, a region not enabled for the account)
are skipped with one note on stderr and don't count as failed regions. A
region you name with `-r` still fails if it's denied. Other region failures
print one warning line each.

## Examples

```bash
# Everything, everywhere
refresh status -A

# Only clusters whose name contains "prod", in two regions
refresh status prod -r us-east-1 -r us-west-2

# Machine-readable for a dashboard / CI gate
refresh status -A -o json
```
