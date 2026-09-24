# Exit codes

Every `refresh` command follows one exit-code contract, so a CI job or a cron
script can branch on the result without reading the output.

## The contract

| Code | Meaning |
|---|---|
| `0` | OK. The command did what you asked and found nothing to report. |
| `1` | Error or interrupt: bad flags, an AWS error, not found, a missing `--yes`, Ctrl+C, or SIGTERM. |
| `2` | Needs attention. The command finished, but it found warnings or stale items. |
| `3` | Blocked or unsupported. A gate stopped the operation and nothing changed, or the target is on extended support or unsupported. |
| `4` | Incomplete data or partial failure. The command printed what it gathered, but some of it is missing or failed. If nothing could be gathered, the command fails with `1` instead. |
| `5` | Post-action verification failed. The change was applied, but the check after it found issues. |

Rules that apply to every command:

- With `-o json` or `-o yaml`, the command prints its document first. Then the
  exit code applies. A non-zero code never means the document is missing,
  unless the code is `1`.
- A partial result is never a success. The command names what it could not
  read on stderr, adds a `failures` or `errors` key to the JSON/YAML
  document, and exits `4`.
- An interrupt exits `1`. A second Ctrl+C ends the process at once. Data
  cut short by Ctrl+C is an interrupted run, so it exits `1`, not `4`. Data
  cut short by the `--timeout` deadline is a partial result and exits `4`.
- With `--watch`, a partial result (`4`) prints a warning and the watch
  continues. Any other error ends the watch.
- `--help` for every command lists the codes that command can return and
  links to this page.

## Codes per command

| Command | Codes it can return |
|---|---|
| [`status`](#status) | `0`, `1`, `2` stale or needs attention, `3` extended support or unsupported, `4` incomplete data |
| `cluster list` | `0`, `1` (also when no region answered), `4` a region failed, or a cluster could not be fully read |
| `cluster describe` | `0`, `1`, `4` some add-ons or nodegroups could not be read |
| [`cluster upgrade-check`](#cluster-upgrade-check) | `0` ready, `1`, `2` warnings only, `3` blocked, `4` a nodegroup or add-on could not be read |
| [`cluster upgrade`](#cluster-upgrade) | `0`, `1` error, failed phase, interrupt, or timeout, `3` the plan has a blocker |
| `nodegroup list` | `0`, `1`, `4` a nodegroup could not be described |
| `nodegroup describe` | `0`, `1` |
| [`nodegroup scale`](#nodegroup-scale) | `0`, `1`, `3` blocked by `--check-pdbs` or the pre-scaling health check, `5` post-scaling health check failed |
| [`nodegroup update`](#nodegroup-update) | `0`, `1`, `2`, `3`, `4`, `5` |
| `addon list` | `0`, `1`, `4` an add-on could not be described |
| `addon describe` | `0`, `1` |
| [`addon update`](#addon-update) | `0`, `1`, `4` (with `--all`), `5` |
| `use`, `current`, `context list/add/remove` | `0`, `1` |
| `version`, `install-man`, `completion` | `0`, `1` |

A failed latest-AMI lookup (for example, a missing `ssm:GetParameter`
permission) is a warning in `nodegroup list` and `nodegroup describe`. The
AMI column shows `unknown (lookup failed)` and the exit code does not change.
`status` counts the same failure as incomplete data (`4`), because AMI
staleness is part of its verdict.

## `status`

| Code | Meaning |
|---|---|
| `0` | Every cluster is current and in standard support |
| `2` | Something needs attention: a stale nodegroup AMI, an addon behind latest, a nodegroup behind the control-plane version, or an AWS-reported control-plane health issue (the `HEALTH` column) |
| `3` | A cluster is on extended support or unsupported |
| `4` | Incomplete data: a cluster row has errors (a failed AWS call, or a sweep that timed out before it reached the cluster), or a region could not be listed |
| `1` | An error, or nothing could be gathered: every region failed or was skipped |

When more than one applies, the highest-priority code wins: `3`, then `2`,
then `4`. Incomplete data never exits `0`. Rows with errors show an unknown
marker and their error text in the table, an `ERRORS` column in `-o plain`,
and an `errors` field in `-o json`/`-o yaml`.

## Region sweeps

`status`, `cluster list -A`, and `nodegroup update --all-clusters` share one
rule for regions: `4` means some data is missing; if nothing could be
gathered, the command fails with `1`. The default sweep (no `-r` and no `REFRESH_EKS_REGIONS`)
skips regions your credentials can't use, such as an SCP denial or a region
that is not enabled. It prints one note on stderr, and a skipped region does
not count as a failure. When you name the regions with `-r` or
`REFRESH_EKS_REGIONS`, a denied region is a failure.

- A region that failed while at least one other region answered (even with
  no clusters) makes the run exit `4`. `cluster list` and `status` print what
  the answering regions returned first.
- When no region answered (every region failed, or every region was
  skipped), nothing was gathered. The command prints an error and exits `1`.
  For `nodegroup update --all-clusters`, a discovery that does not finish
  within `--wait-timeout` also exits `1`.

## `cluster upgrade-check`

`upgrade-check` is a CI gate. The exit code follows the readiness verdict in
the table header (`READY`, `REVIEW`, `NOT READY`). The verdict covers the
insights in the report, the version skew, and the control-plane health check:

| Code | Meaning |
|---|---|
| `0` | Ready: no finding |
| `2` | Needs attention (`REVIEW`): a `WARNING` insight, a nodegroup behind the control plane but inside the kubelet skew limit, an addon behind its latest compatible version, or a control-plane health warning |
| `3` | Blocked (`NOT READY`): an `ERROR` or `UNKNOWN` insight, a nodegroup at the kubelet skew limit (3 minors behind), or a failed control-plane health check (for example, etcd near its size limit) |
| `4` | Incomplete (`INCOMPLETE`): nothing blocks, but a nodegroup or add-on could not be read. The document lists them under `incomplete` |
| `1` | An error, such as an AWS error, a cluster that does not exist, or an interrupt |

`UNKNOWN` blocks because `cluster upgrade` refuses a hop on an `UNKNOWN`
insight too: EKS could not evaluate it, so nothing says the upgrade is safe.

Precedence is `3`, then `4`, then `2`. A known blocker wins over missing
data. Missing data wins over warnings, because the item that could not be
read (for example, a throttled `DescribeNodegroup` on the one nodegroup 3
minors behind) could be a blocker.

`--category` and `--status` narrow the insights, and the gate looks only at
the insights in the report. With `--id`, the exit code reflects that one
insight: `0` for `PASSING`, `2` for `WARNING`, `3` for `ERROR` or `UNKNOWN`.

`--exit-zero` turns the gate off, for `2`, `3`, and `4`. The command prints
the same report and exits `0` unless an error or an interrupt stops it. Use it where you want the report without
failing the job.

```bash
refresh cluster upgrade-check -c prod -o json > readiness.json
case $? in
  0) echo "ready to upgrade" ;;
  2) echo "warnings: review readiness.json" ;;
  3) echo "blocked: do not upgrade" ;;
  4) echo "incomplete: see .incomplete in readiness.json" ;;
  *) echo "check failed" ;;
esac
```

## `cluster upgrade`

| Code | Meaning |
|---|---|
| `0` | The plan finished, or there was nothing to do. A `--dry-run` with no blocker |
| `1` | A phase failed, the run was interrupted, or it timed out |
| `3` | The plan has a blocker, also with `--dry-run`. Nothing changed |

A `--wait-timeout` that runs out is a timeout, not an interrupt: the error
says `timed out` and names `--wait-timeout`. After a failure, an interrupt,
or a timeout, `refresh` prints the command that resumes the upgrade, with the `--wait-timeout`, `--yes`, and other flags you gave. See
[`cluster upgrade`](../commands/cluster.md#upgrade).

## `nodegroup scale`

| Code | Meaning |
|---|---|
| `0` | The scaling request was accepted (and, with `--wait`, it settled) |
| `1` | An error, including a `--check-pdbs` check that could not read the PDBs, a declined confirmation, or a missing `--yes` without a terminal |
| `3` | Blocked: `--check-pdbs` refused a scale-down, or the pre-scaling health check (`--health-check`) blocked it. Nothing changed. A `--dry-run` with `--check-pdbs` exits `3` or `1` where the real run would |
| `5` | The scale was applied, but the post-scaling health check found blocking issues |

See [Scale-down PDB gate](health-checks.md#scale-down-pdb-gate).

## `nodegroup update`

| Code | Meaning |
|---|---|
| `0` | Success: updates started or completed as expected |
| `1` | An error, an interrupt (Ctrl+C), a monitoring timeout, or an EKS update that ended `Failed` or `Cancelled` |
| `2` | Health warnings (with `--health-only` or `--require-healthy`) |
| `3` | Health blocked: a pre-flight check failed, and nothing was rolled |
| `4` | One or more nodegroup updates failed to start |
| `5` | Post-roll verification found issues (nodes not Ready, or newly stuck pods) |

After an interrupt or a monitoring timeout, the EKS update keeps running in
AWS. Check it with `refresh nodegroup list <cluster>`.

In fleet mode (`--all-clusters`) the run exits with the worst code across
clusters: `5`, then `4`, then `3`, then `2`, then `1`. A cluster stopped by
health warnings (`--health-only` or `--require-healthy`) counts as `2`, and
an interrupted or timed-out cluster counts as `1`. A region whose clusters
could not be listed counts as `4`; see [Region sweeps](#region-sweeps).

```bash
refresh nodegroup update -c prod --yes --require-healthy -o json
case $? in
  0) echo "patched cleanly" ;;
  2) echo "health warnings: review" ;;
  3) echo "blocked by health: do not proceed" ;;
  4) echo "some updates failed to start" ;;
  5) echo "rolled, but verification flagged issues" ;;
esac
```

See [`nodegroup update`](../commands/nodegroup.md#update) for the flags that
drive these (`--health-only`, `--require-healthy`, `--skip-verify`).

## `addon update`

| Code | Meaning |
|---|---|
| `0` | Success: updates started, completed, were already `UP_TO_DATE`, or were already `IN_PROGRESS` |
| `1` | An error or an interrupt. For a single add-on, also a failed update: the API call failed, or with `--wait` the EKS update was `Failed`/`Cancelled`, the add-on ended at another version, or the wait timed out (`WAIT_FAILED`) |
| `4` | With `--all`: at least one add-on update failed, or was not attempted because the run hit its deadline |
| `5` | The update landed, but a post-update health check found issues (`COMPLETED_WITH_ISSUES`) |

With `--all`, a failure (`4`) wins over health issues (`5`). An `--all` run
stopped by Ctrl+C exits `1`. The command prints the result before it exits,
so `-o json` output always has the update ID and status.

```bash
refresh addon update -c prod vpc-cni --wait -o json
case $? in
  0) echo "updated" ;;
  5) echo "updated, but check the add-on's health" ;;
  *) echo "update failed" ;;
esac
```

## Changes in 0.11.0

The contract above is new in 0.11.0 (REF-165). If your scripts check exit
codes, review these changes:

| Command | Before | Now |
|---|---|---|
| `cluster upgrade-check` | Always `0` | `2` for warnings, `3` for blockers, `4` when a nodegroup or add-on could not be read. Add `--exit-zero` to keep the old behavior |
| `cluster list` (some regions failed) | `0` with a stderr warning | `4` |
| `status` (every region skipped as not accessible) | `4` | `1` |
| `status`, `cluster list`, `cluster describe`, `nodegroup list`, `addon list`, fleet mode (Ctrl+C after some data came back) | `4` | `1` |
| `status` (a region answered with no clusters, another failed) | `1` | `4` |
| `nodegroup update --all-clusters` (no region listed, or discovery hit `--wait-timeout`) | `4` | `1` |
| `cluster describe` (add-ons or nodegroups unreadable) | `0` with a stderr warning | `4` |
| `nodegroup list`, `addon list` (items not described) | `1` | `4` |
| `cluster upgrade` (blocked plan) | `1` | `3` |
| `nodegroup scale` (`--check-pdbs` refusal, pre-scaling health block) | `1` | `3` |
| `nodegroup scale` (post-scaling health check failed) | `1` | `5` |
| `addon update` (`COMPLETED_WITH_ISSUES`) | `2` | `5` |
| `addon update --all` (an add-on failed or was not attempted) | `1` | `4` |
