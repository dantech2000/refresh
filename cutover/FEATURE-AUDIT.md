# Feature audit: the old CLI against the rewrite

Taken on 2026-10-14 on the branch `rewrite-feature-audit`, from `rewrite` at
`559f010`. The old CLI is `legacy/` (0.16, what users run today). The new CLI
is `cmd/refresh`.

The question: does any capability that a user of 0.16 had disappear without a
record? A renamed command or flag is fine when `CONTRACT.md` or
`docs-next/migrating.md` records it.

## Method

1. Both binaries were built. A throwaway test in each tree walked the command
   tree with the framework's own types (urfave/cli v3 and cobra), so hidden
   commands and hidden flags are in the walk. The walks were diffed by script.
2. `os.Getenv`, `LookupEnv`, and flag environment sources were collected from
   the code of both trees by `grep`, and every `REFRESH_*` and `EKS_*` name of
   the old docs was looked up in the new code and the records.
3. Every read command and every `--dry-run` of a changing command ran with
   `-o json` on the fake fleet (`tools/fakefleet`), with both binaries. The
   key paths of each pair of documents were diffed by script.
4. Every `--flag` that the old docs (`docs/`, `README.md`) name was looked up
   in the new tree and in the records.
5. The headings of every old docs page were diffed with the new page, and the
   promised behaviours were run on the fake fleet where they can be.

"Record" cites a row of the "Summary of changes" table of `CONTRACT.md`
("row N"), another section of `CONTRACT.md`, or `docs-next/migrating.md`.

## Summary

| Kind | Checked | Same | Renamed | Removed on purpose | Missing: ported | Missing: now recorded as dropped |
|---|---|---|---|---|---|---|
| Commands | 29 | 25 | 2 | 1 | 0 | 1 |
| Aliases and positionals | 18 | 7 | 3 | 1 | 1 | 6 |
| Flags (hidden ones too) | 176 | 106 | 39 | 19 | 0 | 12 |
| Environment variables | 28 | 18 | 1 | 3 | 0 | 6 |
| Output formats | 5 | 4 | 0 | 1 | 0 | 0 |
| Document kinds | 21 | 9 | 12 | 0 | 0 | 0 |
| Groups of document keys that changed | 16 | 0 | 12 | 0 | 0 | 4 |
| Capabilities (docs, README) | 35 | 23 | 1 | 4 | 3 | 4 |

"Missing" counts items that had no record before this audit. Each was either
ported (with a test) or recorded as dropped in `CONTRACT.md` rows 35 to 40 and
in `docs-next/migrating.md`.

## What was missing, and what was done

| Missing item | Decision | Commit or record |
|---|---|---|
| `--log-level debug` and `--verbose` printed nothing of refresh itself: the level only routed klog, and the default slog logger kept its own level. 0.16 printed service detail and retries. | Ported: the default logger follows the level, and each attempt of each AWS call is a debug line | `364be71` |
| The hint of `nodegroup scale` when another nodegroup's roll makes the cluster busy: the `aws eks update-nodegroup-config` command that gives a waiting roll its capacity | Ported, as a notice of the refused plan | `e2bbf98` |
| The last lines of `status`: the fleet totals and the command for the most urgent cluster | Ported | `030992c` |
| The alias `ng` of `nodegroup` | Ported, with a test of every alias | `516cc2b` |
| The aliases `get` (of `describe`), `update-ami`, `install-manpage`, and the hidden `addon update-all` | Dropped | Row 35 |
| The `[name-pattern]` positional of `status` and `cluster list` | Dropped: patterns go through `-f` | Row 3; `migrating.md` "Command lines" |
| `nodegroup list --sort instance` | Renamed to `instanceType` | Row 36 |
| The hidden `nodegroup update --simulate`, and `REFRESH_DEV_SIMULATE`, `REFRESH_DEV_SIM_SPEED`, `REFRESH_DEV_SIM_SEED` | Dropped: `tools/fakefleet` | Row 37 |
| `refresh ui`: `ctrl+u`, `e` (session export, `XDG_STATE_HOME`), the AWS call pane and `tab`, the kubectl-cluster cursor and its region in the sweep, the Rolls and Upgrade screens | Dropped by owner decisions D2 to D5 of `docs/design/phase-5-tui.md`, which no user doc recorded | Row 38; `migrating.md` "refresh ui"; `docs-next/commands/ui.md` |
| The health check names, and the Resource Balance check | Renamed; Balance dropped by decision 7 of `docs/design/phase-3-health.md` | Row 39; `migrating.md` "Health check names" |
| Document keys: `count`, `support.extraCostUsdPerHour` and the `+$0.50/hr` text, `readyNodes`/`readyKnown`, `podDisruption`, `scaling.autoScaling`, `support.fallback` | Dropped or renamed | Row 40; `migrating.md` "Renamed and removed keys" |
| The nesting of the describe documents, and 25 renamed keys | Renamed under row 8, with no key list | `migrating.md` "Renamed and removed keys" |
| `migrating.md` said that `refresh ui` does not start changes | Wrong since the change screens were built | `migrating.md` "refresh ui" |

## Commands

| Old | Result | New | Record |
|---|---|---|---|
| `refresh` | Same | `refresh` | |
| `refresh status` | Same | `refresh status` | |
| `refresh cluster` | Same | `refresh cluster` | |
| `refresh cluster list` | Same | `refresh cluster list` | |
| `refresh cluster describe` | Same | `refresh cluster describe` | |
| `refresh cluster upgrade-check` | Same | `refresh cluster upgrade-check` | |
| `refresh cluster upgrade` | Same | `refresh cluster upgrade` | |
| `refresh cluster rollback` | Same | `refresh cluster rollback` | |
| `refresh nodegroup` | Same | `refresh nodegroup` | |
| `refresh nodegroup list` | Same | `refresh nodegroup list` | |
| `refresh nodegroup describe` | Same | `refresh nodegroup describe` | |
| `refresh nodegroup scale` | Same | `refresh nodegroup scale` | |
| `refresh nodegroup update` | Same | `refresh nodegroup update` | |
| `refresh addon` | Same | `refresh addon` | |
| `refresh addon list` | Same | `refresh addon list` | |
| `refresh addon describe` | Same | `refresh addon describe` | |
| `refresh addon update` | Same | `refresh addon update` | |
| `refresh addon update-all` (hidden) | Missing: recorded | `refresh addon update --all` | Row 35 |
| `refresh use` | Renamed | `refresh context use` | Row 12 |
| `refresh current` | Renamed | `refresh context current` | Row 12 |
| `refresh context` | Same | `refresh context` | |
| `refresh context list` | Same | `refresh context list` | |
| `refresh context add` | Same | `refresh context add` | |
| `refresh context remove` | Same | `refresh context remove` | |
| `refresh version` | Same | `refresh version` (now also `-o json|yaml`) | |
| `refresh install-man` | Removed on purpose | Man pages in the archives and the cask (`gen-docs --format man` in `cutover/.goreleaser.yml`; checked: 30 pages) | Row 16 |
| `refresh completion` | Same | `refresh completion <bash|zsh|fish|powershell>` | Row 31 |
| `refresh gen-docs` (hidden) | Same | `refresh gen-docs` (hidden), now with `--format man` | |
| `refresh ui` | Same | `refresh ui` | Row 38 for its screens |

New commands with no old counterpart: `context use` and `context current` (moved), `help`.

## Aliases and positionals

| Old | Result | New | Record |
|---|---|---|---|
| `nodegroup` alias `ng` | Missing: ported | `ng` | `516cc2b` |
| `context` alias `ctx` | Same | `ctx` | |
| `context list` alias `ls` | Same | `ls` | |
| `context remove` aliases `rm`, `delete` | Same | `rm`, `delete` | |
| `cluster describe` alias `get` | Missing: recorded | `cluster describe` | Row 35 |
| `nodegroup describe` alias `get` | Missing: recorded | `nodegroup describe` | Row 35 |
| `addon describe` alias `get` | Missing: recorded | `addon describe` | Row 35 |
| `nodegroup update` alias `update-ami` | Missing: recorded | `nodegroup update` | Row 35 |
| `install-man` alias `install-manpage` | Removed on purpose | | Rows 16, 35 |
| `status [name-pattern]` | Missing: recorded | `-f name=*pattern*` | Row 3; `migrating.md` |
| `cluster list [name-pattern]` | Missing: recorded | `-f name=*pattern*` | Row 3; `migrating.md` |
| `cluster describe|upgrade-check|upgrade|rollback [cluster]` | Same | `[cluster]` | Row 2 |
| `nodegroup list [cluster]` | Renamed | `-c` | Row 2 |
| `nodegroup describe|scale [cluster] [nodegroup]` | Renamed | `<nodegroup> -c <cluster>` | Row 2 |
| `nodegroup update [cluster] [nodegroup]` | Renamed | `[nodegroup...] -c <cluster>` | Row 2 |
| `addon list [cluster]`, `addon describe [cluster] [addon]` | Same meaning, new shape | `-c`, `<addon>` | Row 2 |
| `addon update [cluster] [addon] [version]` | Same meaning, new shape | `[addon...] -c ... --version` | Row 2, "Command flags" |
| `use [context-name|-]` | Same | `context use [context|-]`; `-` checked on the fake fleet | Row 12 |

New aliases: `ls` on `cluster list`, `nodegroup list`, `addon list`.

## Flags

Every flag of the old tree, from the walk. "Same" means the same long name on
the same command, or a global flag of the new root with that name.

| Old | Result | New | Record |
|---|---|---|---|
| `refresh --log-level` | Same | `refresh --log-level` |  |
| `refresh --max-concurrency` (`-C`) | Renamed | `refresh --max-concurrency` (no shorthand) | row 10 |
| `refresh --no-color` | Same | `refresh --no-color` |  |
| `refresh --profile` | Same | `refresh --profile` |  |
| `refresh --region` (`-r`) | Same | `refresh --region` (`-r`) |  |
| `refresh --timeout` (`-t`) | Same | `refresh --timeout` (`-t`) |  |
| `refresh --verbose` | Same | `refresh --verbose` |  |
| `refresh status --all-regions` (`-A`) | Same | `refresh status --all-regions` (`-A`) |  |
| `refresh status --desc` | Same | `refresh status --desc` |  |
| `refresh status --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh status --region` (`-r`) | Same | global `--region` (`-r`) |  |
| `refresh status --sort` | Same | `refresh status --sort` |  |
| `refresh cluster list --all-regions` (`-A`) | Same | `refresh cluster list --all-regions` (`-A`) |  |
| `refresh cluster list --desc` | Same | `refresh cluster list --desc` |  |
| `refresh cluster list --filter` (`-f`) | Same | `refresh cluster list --filter` (`-f`) |  |
| `refresh cluster list --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh cluster list --region` (`-r`) | Same | global `--region` (`-r`) |  |
| `refresh cluster list --show-health` (`-H`) | Renamed | `refresh cluster list --show-health` (no shorthand) | row 10 |
| `refresh cluster list --sort` | Same | `refresh cluster list --sort` |  |
| `refresh cluster list --tree` (`-T`) | Removed on purpose | `refresh status` | rows 10, 15 |
| `refresh cluster list --watch` (`-w`) | Same | `refresh cluster list --watch` (`-w`) |  |
| `refresh cluster list --watch-interval` | Same | `refresh cluster list --watch-interval` |  |
| `refresh cluster describe --check-readiness` (`-R`) | Renamed | `refresh cluster describe --check-readiness` (no shorthand) | row 10 |
| `refresh cluster describe --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh cluster describe --detailed` | Removed on purpose | every section always shows | row 14 |
| `refresh cluster describe --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh cluster describe --include-addons` (hidden) | Removed on purpose | (hidden, deprecated) | rows 9, 14 |
| `refresh cluster describe --kube-context` | Same | global `--kube-context` |  |
| `refresh cluster describe --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh cluster describe --no-addons` | Removed on purpose | every section always shows | row 14 |
| `refresh cluster describe --no-health` | Removed on purpose | every section always shows | row 14 |
| `refresh cluster describe --show-health` (`-H`) (hidden) | Removed on purpose | (hidden, deprecated) | rows 9, 14 |
| `refresh cluster describe --show-security` | Removed on purpose | every section always shows | row 14 |
| `refresh cluster upgrade-check --category` | Same | `refresh cluster upgrade-check --category` |  |
| `refresh cluster upgrade-check --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh cluster upgrade-check --exit-zero` | Same | `refresh cluster upgrade-check --exit-zero` |  |
| `refresh cluster upgrade-check --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh cluster upgrade-check --id` | Renamed | `--insight` | rows 17, 25 |
| `refresh cluster upgrade-check --show-passing` | Same | `refresh cluster upgrade-check --show-passing` |  |
| `refresh cluster upgrade-check --status` | Same | `refresh cluster upgrade-check --status` |  |
| `refresh cluster upgrade --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh cluster upgrade --dry-run` (`-d`) | Same | `refresh cluster upgrade --dry-run` (`-d`) |  |
| `refresh cluster upgrade --force` | Same | `refresh cluster upgrade --force` |  |
| `refresh cluster upgrade --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh cluster upgrade --kube-context` | Same | global `--kube-context` |  |
| `refresh cluster upgrade --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh cluster upgrade --nodegroup` (`-n`) | Same | `refresh cluster upgrade --nodegroup` (`-n`) |  |
| `refresh cluster upgrade --only` | Same | `refresh cluster upgrade --only` |  |
| `refresh cluster upgrade --poll-interval` | Same | `refresh cluster upgrade --poll-interval` |  |
| `refresh cluster upgrade --quiet` (`-q`) | Same | `refresh cluster upgrade --quiet` (`-q`) |  |
| `refresh cluster upgrade --skip` | Renamed | `--skip-addon` | "Command flags" renames; migrating.md |
| `refresh cluster upgrade --skip-health-check` | Same | `refresh cluster upgrade --skip-health-check` |  |
| `refresh cluster upgrade --skip-insights-check` | Same | `refresh cluster upgrade --skip-insights-check` |  |
| `refresh cluster upgrade --skip-nodegroup` | Same | `refresh cluster upgrade --skip-nodegroup` |  |
| `refresh cluster upgrade --skip-verify` | Same | `refresh cluster upgrade --skip-verify` |  |
| `refresh cluster upgrade --timeout` (`-t`) (hidden) | Removed on purpose | `--wait-timeout` | row 9 |
| `refresh cluster upgrade --to` | Same | `refresh cluster upgrade --to` |  |
| `refresh cluster upgrade --wait-timeout` | Same | `refresh cluster upgrade --wait-timeout` |  |
| `refresh cluster upgrade --yes` (`-y`) | Same | `refresh cluster upgrade --yes` (`-y`) |  |
| `refresh cluster rollback --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh cluster rollback --dry-run` (`-d`) | Same | `refresh cluster rollback --dry-run` (`-d`) |  |
| `refresh cluster rollback --force` | Same | `refresh cluster rollback --force` |  |
| `refresh cluster rollback --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh cluster rollback --kube-context` | Same | global `--kube-context` |  |
| `refresh cluster rollback --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh cluster rollback --poll-interval` | Same | `refresh cluster rollback --poll-interval` |  |
| `refresh cluster rollback --quiet` (`-q`) | Same | `refresh cluster rollback --quiet` (`-q`) |  |
| `refresh cluster rollback --rollback-timeout` | Same | `refresh cluster rollback --rollback-timeout` |  |
| `refresh cluster rollback --skip-health-check` | Same | `refresh cluster rollback --skip-health-check` |  |
| `refresh cluster rollback --skip-insights-check` | Same | `refresh cluster rollback --skip-insights-check` |  |
| `refresh cluster rollback --skip-nodegroup` | Same | `refresh cluster rollback --skip-nodegroup` |  |
| `refresh cluster rollback --skip-verify` | Same | `refresh cluster rollback --skip-verify` |  |
| `refresh cluster rollback --wait-timeout` | Same | `refresh cluster rollback --wait-timeout` |  |
| `refresh cluster rollback --yes` (`-y`) | Same | `refresh cluster rollback --yes` (`-y`) |  |
| `refresh nodegroup list --check-readiness` (`-R`) | Renamed | `refresh nodegroup list --check-readiness` (no shorthand) | row 10 |
| `refresh nodegroup list --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh nodegroup list --desc` | Same | `refresh nodegroup list --desc` |  |
| `refresh nodegroup list --filter` (`-f`) | Same | `refresh nodegroup list --filter` (`-f`) |  |
| `refresh nodegroup list --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh nodegroup list --kube-context` | Same | global `--kube-context` |  |
| `refresh nodegroup list --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh nodegroup list --sort` | Same | `refresh nodegroup list --sort` |  |
| `refresh nodegroup list --watch` (`-w`) | Same | `refresh nodegroup list --watch` (`-w`) |  |
| `refresh nodegroup list --watch-interval` | Same | `refresh nodegroup list --watch-interval` |  |
| `refresh nodegroup describe --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh nodegroup describe --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh nodegroup describe --kube-context` | Same | global `--kube-context` |  |
| `refresh nodegroup describe --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh nodegroup describe --nodegroup` (`-n`) | Renamed | the positional `<nodegroup>` | row 2; migrating.md "Removed flags" |
| `refresh nodegroup describe --show-instances` (`-I`) | Renamed | `refresh nodegroup describe --show-instances` (no shorthand) | row 10 |
| `refresh nodegroup describe --show-workloads` (`-W`) | Renamed | `refresh nodegroup describe --show-workloads` (no shorthand) | row 10 |
| `refresh nodegroup scale --check-pdbs` | Removed on purpose | the health gate, on by default (DrainBlockers check) | rows 5, "Command flags" |
| `refresh nodegroup scale --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh nodegroup scale --desired` | Same | `refresh nodegroup scale --desired` |  |
| `refresh nodegroup scale --dry-run` (`-d`) | Same | `refresh nodegroup scale --dry-run` (`-d`) |  |
| `refresh nodegroup scale --force` | Same | `refresh nodegroup scale --force` |  |
| `refresh nodegroup scale --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh nodegroup scale --health-check` | Removed on purpose | the health gate, on by default | row 5 |
| `refresh nodegroup scale --kube-context` | Same | global `--kube-context` |  |
| `refresh nodegroup scale --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh nodegroup scale --max` | Same | `refresh nodegroup scale --max` |  |
| `refresh nodegroup scale --min` | Same | `refresh nodegroup scale --min` |  |
| `refresh nodegroup scale --nodegroup` (`-n`) | Renamed | the positional `<nodegroup>` | row 2; migrating.md "Removed flags" |
| `refresh nodegroup scale --op-timeout` (hidden) | Removed on purpose | `--wait-timeout` | row 9 |
| `refresh nodegroup scale --wait` | Removed on purpose | waits by default; `--no-wait` | row 4 |
| `refresh nodegroup scale --wait-timeout` | Same | `refresh nodegroup scale --wait-timeout` | row 4 |
| `refresh nodegroup scale --yes` (`-y`) | Same | `refresh nodegroup scale --yes` (`-y`) |  |
| `refresh nodegroup update --all-clusters` | Same | `refresh nodegroup update --all-clusters` |  |
| `refresh nodegroup update --changelog` | Same | `refresh nodegroup update --changelog` |  |
| `refresh nodegroup update --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh nodegroup update --dry-run` (`-d`) | Same | `refresh nodegroup update --dry-run` (`-d`) |  |
| `refresh nodegroup update --force` | Same | `refresh nodegroup update --force` | migrating.md "--force and --reroll" |
| `refresh nodegroup update --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh nodegroup update --health-only` | Removed on purpose | `--dry-run` | row 13 |
| `refresh nodegroup update --kube-context` | Same | global `--kube-context` |  |
| `refresh nodegroup update --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh nodegroup update --live` | Same | `refresh nodegroup update --live` |  |
| `refresh nodegroup update --no-wait` | Same | `refresh nodegroup update --no-wait` |  |
| `refresh nodegroup update --nodegroup` (`-n`) | Renamed | the positional `[nodegroup...]`, or `-f name=...` for a pattern | rows 2, 3; migrating.md "Command lines" |
| `refresh nodegroup update --poll-interval` | Same | `refresh nodegroup update --poll-interval` |  |
| `refresh nodegroup update --quiet` (`-q`) | Same | `refresh nodegroup update --quiet` (`-q`) |  |
| `refresh nodegroup update --region` (`-r`) | Same | global `--region` (`-r`) |  |
| `refresh nodegroup update --require-healthy` | Same | `refresh nodegroup update --require-healthy` |  |
| `refresh nodegroup update --reroll` | Same | `refresh nodegroup update --reroll` |  |
| `refresh nodegroup update --simulate` (hidden) | Missing: recorded | `tools/fakefleet` (`task run:sim`) | row 37 |
| `refresh nodegroup update --skip-health-check` | Same | `refresh nodegroup update --skip-health-check` |  |
| `refresh nodegroup update --skip-verify` | Same | `refresh nodegroup update --skip-verify` |  |
| `refresh nodegroup update --timeout` (`-t`) (hidden) | Removed on purpose | `--wait-timeout` | row 9 |
| `refresh nodegroup update --wait-timeout` | Same | `refresh nodegroup update --wait-timeout` |  |
| `refresh nodegroup update --yes` (`-y`) | Same | `refresh nodegroup update --yes` (`-y`) |  |
| `refresh addon list --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh addon list --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh addon list --show-health` (`-H`) | Removed on purpose | the column always shows | row 20 |
| `refresh addon list --watch` (`-w`) | Same | `refresh addon list --watch` (`-w`) |  |
| `refresh addon list --watch-interval` | Same | `refresh addon list --watch-interval` |  |
| `refresh addon describe --addon` (`-a`) | Renamed | the positional `<addon>` | row 2; migrating.md "Removed flags" |
| `refresh addon describe --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh addon describe --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh addon update --addon` (`-a`) | Renamed | the positional `[addon...]` | row 2; migrating.md "Removed flags" |
| `refresh addon update --all` | Same | `refresh addon update --all` |  |
| `refresh addon update --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh addon update --dependency-order` | Removed on purpose | always dependency order | row 7 |
| `refresh addon update --dry-run` (`-d`) | Same | `refresh addon update --dry-run` (`-d`) |  |
| `refresh addon update --format` (`-o`) | Renamed | `-o/--output` | row 1 |
| `refresh addon update --health-check` | Removed on purpose | the health gate, on by default | row 5 |
| `refresh addon update --parallel` | Removed on purpose | always dependency order | row 7 |
| `refresh addon update --skip` | Renamed | `--skip-addon` | "Command flags" renames; migrating.md |
| `refresh addon update --timeout` (`-t`) | Same | global `--timeout` (`-t`), the budget of the reads and requests outside the wait, also from `REFRESH_TIMEOUT` | "Global flags" |
| `refresh addon update --version` | Same | `refresh addon update --version` |  |
| `refresh addon update --wait` | Removed on purpose | waits by default; `--no-wait` | row 4 |
| `refresh addon update --wait-timeout` | Same | `refresh addon update --wait-timeout` | row 4 |
| `refresh addon update --yes` (`-y`) | Same | `refresh addon update --yes` (`-y`) |  |
| `refresh addon update-all --cluster` (`-c`) | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --dependency-order` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --dry-run` (`-d`) | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --format` (`-o`) | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --health-check` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --parallel` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --skip` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --timeout` (`-t`) | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --wait` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --wait-timeout` | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh addon update-all --yes` (`-y`) | Missing: recorded | `refresh addon update --all` | row 35 |
| `refresh context add --cluster` (`-c`) | Renamed | the positional `[cluster]` | row 2; migrating.md "Command lines" |
| `refresh context add --profile` | Same | `refresh context add --profile` |  |
| `refresh context add --region` (`-r`) | Same | `refresh context add --region` (`-r`) |  |
| `refresh context add --use` | Same | `refresh context add --use` |  |
| `refresh version --no-update-check` | Same | `refresh version --no-update-check` |  |
| `refresh gen-docs --out` | Same | `refresh gen-docs --out` |  |
| `refresh ui --all-regions` (`-A`) | Same | `refresh ui --all-regions` (`-A`) |  |
| `refresh ui --allow-changes` | Same | `refresh ui --allow-changes` |  |
| `refresh ui --interval` | Same | `refresh ui --interval` |  |
| `refresh ui --kube-context` | Same | global `--kube-context` |  |
| `refresh ui --kubeconfig` | Same | global `--kubeconfig` |  |
| `refresh ui --region` (`-r`) | Same | global `--region` (`-r`) |  |
| `refresh ui --wait-timeout` | Same | `refresh ui --wait-timeout` |  |

## Environment variables

| Old | Result | New | Record |
|---|---|---|---|
| `REFRESH_TIMEOUT` | Same | `REFRESH_TIMEOUT` | |
| `REFRESH_MAX_CONCURRENCY` | Same | `REFRESH_MAX_CONCURRENCY` | Row 19 (new meaning) |
| `REFRESH_LOG_LEVEL` | Same | `REFRESH_LOG_LEVEL` | Fixed in `364be71` |
| `REFRESH_EKS_REGIONS` | Same | `REFRESH_EKS_REGIONS` | |
| `REFRESH_CONTEXT` | Same | `REFRESH_CONTEXT` | |
| `REFRESH_CONFIG_HOME` | Same | `REFRESH_CONFIG_HOME` | |
| `REFRESH_NO_UPDATE_CHECK` | Same | `REFRESH_NO_UPDATE_CHECK` | |
| `REFRESH_IN_CLUSTER_NAME` | Same | `REFRESH_IN_CLUSTER_NAME` | |
| `EKS_CLUSTER_NAME` | Renamed | `REFRESH_CLUSTER`, read by every command | Row 6 |
| `NO_COLOR`, `TERM`, `COLORTERM` | Same | Same; `TERM=dumb` also turns color off | "Global flags" |
| `KUBECONFIG` | Same | `KUBECONFIG`, and the global `--kubeconfig` | |
| `AWS_PROFILE`, `AWS_DEFAULT_PROFILE`, `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_ACCESS_KEY_ID` | Same | Same | |
| `XDG_CONFIG_HOME` | Same | Same | |
| `REFRESH_DEV_SIMULATE`, `REFRESH_DEV_SIM_SPEED`, `REFRESH_DEV_SIM_SEED` | Missing: recorded | `tools/fakefleet` | Row 37 |
| `XDG_STATE_HOME` (the UI session export) | Missing: recorded | | Row 38 |
| `DISPLAY`, `WAYLAND_DISPLAY` (the UI's clipboard tools) | Missing: recorded | OSC 52 | Row 38; `docs-next/commands/ui.md` |
| `HOME` (`install-man`) | Removed on purpose | | Row 16 |
| `REFRESH_INTEGRATION`, `REFRESH_TEST_CLUSTER` | Removed on purpose: the test harness of the old tree, not read by the binary | | `cutover/CHECKLIST.md` (`test:integration` goes) |

New: `REFRESH_CLUSTER`, `LANG`, `LC_ALL`, `LC_CTYPE` (UTF-8 detection).

## Output formats

| Old | Result | New | Record |
|---|---|---|---|
| `-o table` | Same | `-o table` | Row 1 (`--output`) |
| `-o json` | Same | `-o json` | Row 1 |
| `-o yaml` | Same | `-o yaml` | Row 1 |
| `-o plain` | Same | `-o plain`, now on every command with a document | Row 1, 27 |
| `-o tree` | Removed on purpose | `refresh status` | Row 15 |

## Document kinds

| Old (`v1`) | Result | New (`v2`) | Record |
|---|---|---|---|
| `FleetStatus`, `ClusterList`, `ClusterDescription`, `NodegroupList`, `NodegroupDescription`, `AddonList`, `AddonDescription`, `UpgradeCheck`, `InsightDescription` | Same kind, new shape | Same kind | Row 8; `migrating.md` "Renamed and removed keys" |
| `NodegroupUpdatePlan`, `FleetUpdatePlan`, `UpgradePlan`, `RollbackPlan`, `HealthSummary` | Renamed | `Plan` | Row 8, "Documents" |
| `NodegroupUpdate`, `FleetUpdate`, `UpgradeRun`, `RollbackRun` | Renamed | `Run` | Row 8 |
| `NodegroupScale`, `AddonUpdate`, `AddonUpdateAll` | Renamed | `Plan` with `--dry-run`, else `Run` | Row 8 |

New: `Version`. The `v1` schemas stay published (`cutover/CHECKLIST.md`, decision 2).

## Document keys

The key paths of 15 pairs of documents from the fake fleet were diffed
(`status -A`, `cluster list -A --show-health`, `cluster describe`,
`upgrade-check` twice and with `--insight`, `nodegroup list`, `nodegroup
describe --show-instances --show-workloads`, `addon list`, `addon describe`,
and the dry runs of `nodegroup update`, `nodegroup update --all-clusters`,
`nodegroup scale`, `addon update --all`, `cluster upgrade`, `cluster
rollback`). Every old key either has a new path or is in the table
"Renamed and removed keys" of `docs-next/migrating.md`, which this audit
wrote. The keys of the plan documents (`currentAmi`, `latestAmi`, `dryRun`,
`force`, `hops`, `previousVersion`, `newVersion`, `updateId`, ...) map to the
`Plan` and `Run` fields of `CONTRACT.md` "Documents" (`fromImage`, `toImage`,
`hop`, `from`, `to`, `results[].updateId`).

## Capabilities

| Promise of the old docs or README | Result | Checked how |
|---|---|---|
| `status`: fleet sweep, `-A`, `-r`, `--sort` (5 keys), `--desc` | Same; `--sort cluster` is `name` (row 23) | Run on the fake fleet |
| `status`: filter by name, region, version, support | Same through `-f` (row 3) | Run on the fake fleet |
| `status`: exit codes 0, 2, 3, 4 | Same, highest wins (row 28 note) | Run: 4 with an unread region |
| `status`: totals line and next-step hint | Missing: ported (`030992c`) | Golden and unit test |
| `cluster list --watch` | Same, table and terminal only (row 22) | Help |
| `cluster list --show-health` | Same | Run |
| `cluster describe`: overview, network, security (KMS key, deletion protection, logs), nodegroups, add-ons, health | Same, always every section (row 14) | Run, both binaries |
| `cluster describe --check-readiness` | Same | Help |
| `upgrade-check`: insights, version skew, support window, `--exit-zero`, `--category`, `--status`, `--show-passing`, one insight | Same; `--insight` (rows 17, 25, 26) | Run, both binaries |
| The rollback window in `upgrade-check` and `cluster rollback` | Same | Run on `staging-ml-07` (rollback-window), both binaries |
| `cluster upgrade`: plan, gates, `--only`, `-n`, skips, `--dry-run`, exit 3 when blocked | Same | Dry run on the fake fleet |
| `cluster upgrade`: resume by running the same command after a stop or failure | Same: the message names the command, which carries `--only`, `-n`, the skips, `--force`, `--skip-health-check`, `--require-healthy`, `--skip-verify`, `--wait-timeout`, `--yes`, the profile, the region, and the kube flags | `internal/change/engine.go` `resumeHint`, `enginewire.go` `engineCommand` |
| `cluster rollback`: nodegroups, add-ons, then control plane, in the window | Same | Dry run |
| `nodegroup update`: AMI roll, health gate, PDB drain gate, post-roll verification, `--changelog`, `--live`, `--reroll` | Same; `--force` no longer re-rolls (`migrating.md`) | Dry run, help |
| `nodegroup update --all-clusters` | Same | Dry run with both binaries on 8 clusters |
| `nodegroup update --health-only` | Replaced by `--dry-run` (row 13) | |
| `nodegroup update --simulate` (hidden demo) | Missing: recorded (row 37) | |
| `nodegroup scale`: PDB check on a scale-down | Same, on by default (row 5) | Dry run on `prod-payments-00` (pdb-blocker): blocked by `DrainBlockers`, as the old `--check-pdbs` refused |
| `nodegroup scale`: AWS CLI hint when another roll makes the cluster busy | Missing: ported (`e2bbf98`) | Unit test, run on the fake fleet |
| `nodegroup list --filter amiStatus=outdated` | Same, keys case-insensitive | Run |
| `addon update`: version guard, downgrade warning, busy check, dependency order of `--all` | Same (rows 7, 33; "does not copy") | Dry run |
| `context add --use`, `context use -`, `REFRESH_CONTEXT` | Same | Run with a scratch `REFRESH_CONFIG_HOME` |
| Shell completion of commands, flags, clusters, nodegroups, add-ons, contexts | Same, plus PowerShell | `__complete` on the fake fleet |
| Man pages | Same, through the release (row 16) | `gen-docs --format man`: 30 pages; `cutover/.goreleaser.yml` |
| The update check of `refresh version` on a terminal, `--no-update-check`, `REFRESH_NO_UPDATE_CHECK` | Same | `internal/cmd/version`; cache next to the context file |
| `--log-level`, `--verbose`, logs on stderr | Was a no-op for refresh's lines: ported (`364be71`) | `TestLogs`, `TestLogAttempts`, run on the fake fleet |
| `--no-color`, `NO_COLOR`, `TERM=dumb` | Same | Run |
| `-o json|yaml` prints one document; failures and exit 4 | Same | Every run above |
| `refresh ui`: fleet, cluster readiness, dry runs of `p`, `a`, `U`, `B`, `--allow-changes`, `y`/`n` questions, `P`, `S`, changes started elsewhere, `f`, `w`, `space`, `c`, `ctrl+r`, `ctrl+l` | Same | `docs-next/commands/ui.md`, key table in `internal/tui/keys.go` |
| `refresh ui`: `ctrl+u`, `e` export, AWS call pane, `tab` log sources, kubectl-cluster cursor and region | Missing: recorded (row 38) | Owner decisions D2 to D5 |
| `refresh ui`: Rolls and Upgrade screens | Merged into the Change screen (row 38) | |
| Health checks Node Health, Cluster Capacity, Control Plane, Service Quotas, Critical Workloads, PDBs, Node Utilization | Renamed (row 39) | `cluster describe` with both binaries |
| Health check Resource Balance | Removed on purpose (row 39) | |
| The extended-support premium (`+$0.50/hr`) | Missing: recorded (row 40) | |
| `cluster list` node count | Removed on purpose (row 18) | |
| Health score | Removed on purpose (row 8) | |

## For the owner to decide

- Row 40 drops the extended-support premium. It is a hard-coded price, and
  cost is out of scope, but it is also the strongest reason to leave extended
  support. Porting it means one optional key of `Support` and its text in the
  `SUPPORT` cell.
- Row 38 records the phase 5 decisions. The session export is the one real
  loss: a change run from the UI leaves no record on disk.
- `nodegroup describe` no longer counts the PodDisruptionBudgets of a
  nodegroup, and `cluster describe` no longer counts the ready nodes of each
  nodegroup (row 40). The health checks read both; say if either should come
  back as a key.
