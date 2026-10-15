# Exit codes

Every command uses the same six codes.

| Code | Meaning |
|---|---|
| `0` | OK. |
| `1` | An error, or an interrupt. |
| `2` | Needs attention: warnings, or items that are out of date. |
| `3` | Blocked or unsupported. A gate stopped the change. Nothing changed. When a gate stops a later phase of an upgrade or a rollback, nothing more changed, and the message names the phases that completed. |
| `4` | Incomplete data, or a partial failure. |
| `5` | The change was applied, and the check after it found issues. |

When one run has several results, the highest code wins. An interrupt is
always `1`. There is one exception to the interrupt rule: Ctrl+C in `--watch`
exits `0`, because that is the normal way to end a watch.

A command prints its document first and then exits with the code. So a script
can read the document of a run that exits `3`, `4`, or `5`.

The message on stderr of an exit `2` or `3` is a verdict, not an error, and
prints alone:

```text
upgrade needs review: 1 insight warns
dry run: a real run would be blocked
```

Every other message starts with `Error:`: an error or an interrupt (`1`),
incomplete data or a partial failure (`4`), and a failed verification (`5`).

## Codes per command

| Command | `0` | `1` | `2` | `3` | `4` | `5` |
|---|---|---|---|---|---|---|
| `status` | Everything current, in standard support | Error; no region answered | Something is out of date | A cluster in extended support, or unsupported | A part of the fleet could not be read | |
| `cluster list`, `cluster describe`, `nodegroup list`, `nodegroup describe`, `addon list`, `addon describe` | Read | Error; not found | | | A part could not be read | |
| `cluster upgrade-check` | Ready | Error | Review: warnings | Blocked | A check or an item could not be read | |
| `nodegroup update`, `addon update` | Done, or nothing to do | Error; answered no; interrupt; every change failed | Dry run only: the real run would ask | Blocked | Partial failure; busy target; wait timed out; unread data | The verification found an issue |
| `nodegroup scale` | Done, or nothing to do | Error; answered no; interrupt; the update failed; sizes that cannot hold | Dry run only | Blocked | Busy target; wait timed out; unread data | |
| `cluster upgrade`, `cluster rollback` | Done, or nothing to do | Error; answered no; interrupt; a phase failed | Dry run only | Blocked | Wait timed out; unread data | The verification found an issue |
| `context ...`, `version`, `completion` | Done | Error | | | | |
| `ui` | Quit | Error | | | | |

## `status`

| Result | Code |
|---|---|
| Every cluster is current and in standard support | `0` |
| A stale AMI, a nodegroup behind the control plane, an add-on behind, or a control-plane issue | `2` |
| A cluster in extended support, or unsupported | `3` |
| A part of the fleet could not be read | `4` |
| An error, or no region answered | `1` |

The highest code wins: incomplete data (`4`) outranks an unsupported cluster
(`3`).

## `cluster upgrade-check`

The exit code is the verdict.

| Verdict | Code |
|---|---|
| Ready | `0` |
| Review: a warning | `2` |
| Blocked: an `ERROR` or `UNKNOWN` insight, or a nodegroup too far behind | `3` |
| Incomplete: a check, a nodegroup, or an add-on could not be read | `4` |

- A blocked cluster with an unread item exits `4`.
- `--exit-zero` prints the report and exits `0`.
- `--status` narrows the insights that are shown. It does not change the
  verdict.
- `--insight` exits with the status of that one insight.

## Dry runs

The exit code of `--dry-run` comes from the findings alone. `--yes` does not
change it.

| Finding | Code |
|---|---|
| The real run would go | `0` |
| The real run would ask about warnings | `2` |
| The real run would be blocked | `3` |
| A check or the plan could not read what it needs | `4` |

Exit `2` is the code of a dry run whose real run would ask, and of nothing else
in a changing command.

## Real runs of a changing command

Each step of the run has a code. The run exits with the highest.

| Result of a step | Code |
|---|---|
| Succeeded, skipped, or started with `--no-wait` | `0` |
| The update failed, or EKS refused the request | `1` |
| The busy rule, the health gate, or `--require-healthy` over a warning stopped it | `3` |
| The gate or the plan could not read what it needs | `4` |
| The target was already changing | `4` |
| `--wait-timeout` passed before the change finished | `4` |
| AWS accepted the change, and it could not be followed | `4` |
| The verification found an issue; an add-on was updated and is not healthy | `5` |

Other results:

| Result | Code |
|---|---|
| A question answered no | `1` |
| Ctrl+C | `1`, whatever else happened |
| A read failed in the plan, the gate, a region, or the verification | `4` |
| A warning that `--yes` or an answer accepted, in a run that then succeeds | `0` |

### Partial failure

For independent targets, a failure beside a success is a partial failure. The
independent targets are the nodegroups of `nodegroup update`, the add-ons of
`addon update --all`, and the clusters of `nodegroup update --all-clusters`.

| Steps | Code |
|---|---|
| One failed, one succeeded or started | `4` |
| Every attempted step failed | `1` |
| One failed, one skipped as already current | `1` |
| One blocked, the rest succeeded | `3` |
| One verification issue, the rest succeeded | `5` |

`cluster upgrade` and `cluster rollback` are a sequence. A phase that fails
ends the run with exit `1`, whatever the earlier phases did. The message names
the phases that completed and the command that goes on from there.

### A wait that times out

If `--wait-timeout` passes, the command exits `4`. The change may still be
running in AWS. Nothing is cancelled. Run the command again: the plan comes
from the live state.

## Region sweeps

| Result | Code |
|---|---|
| A default sweep skipped a region | No effect |
| A region that you named could not be read | `4` |
| No region answered | `1` |

## In a script

```bash
refresh cluster upgrade-check prod -o json > check.json
case $? in
  0) echo "ready" ;;
  2) echo "review the warnings" ;;
  3) echo "blocked"; exit 1 ;;
  4) echo "incomplete: check the failures list"; exit 1 ;;
  *) echo "error"; exit 1 ;;
esac
```
