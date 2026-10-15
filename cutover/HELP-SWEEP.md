# Help and exit-code sweep of the new binary

Findings, and what became of each. The sweep changed no Go code; the
`Status` column was added when the findings were worked through, and names
the commit of each fix. An open finding needs a decision of the owner. The
sweep compared the `--help` of every
command of the binary built at `b62e59d` with `CONTRACT.md` and with the other
commands. `docs-next/reference/` has the full help text.

## Exit codes in the help

| # | Command | Finding | Status |
|---|---|---|---|
| 1 | `cluster list`, `cluster describe`, `nodegroup list`, `nodegroup describe`, `addon list` | No `Exit codes:` block. The prose names exit 4, and `cluster list` names exit 1. The five changing commands and `status` have a block. | Fixed in `fb41208`: each has an `Exit codes:` block. |
| 2 | `addon describe` | The help names no exit code at all, and does not say what a partial read does. `nodegroup describe` and `cluster describe` say "prints what it has ... and exits 4". | Fixed in `fb41208`: the help says that the command has no partial result, and has the block. |
| 3 | `context *`, `ui`, `version`, `completion` | No exit-code help. | Fixed for `context list`, `add`, `remove`, `use`, `current`, `ui`, and `version` (`fb41208`, `fbe93c4`). `completion` has it since the fix of 26. |
| 4 | `cluster upgrade-check` | The codes are one sentence ("0 ready, 2 review (warnings), 3 blocked, 4 incomplete"), not a block, and exit 1 is not listed. | Fixed in `fb41208`: a block, with exit 1. |
| 5 | `status` | The block lists the codes in the order 0, 2, 3, 4, 1. The five changing commands list 0, 1, 2, 3, 4, 5. | Fixed in `fb41208`: the order is 0, 1, 2, 3, 4. |
| 6 | `cluster upgrade`, `cluster rollback` | Code 3 reads "Blocked, and nothing changed". `CONTRACT.md` says that when a gate stops a later phase "nothing more changed and the message names the phases that completed". The help contradicts the contract for a mid-run block. | Fixed in `fb41208`: code 3 has the mid-run wording of the contract, in all five commands. |
| 7 | `nodegroup scale` | The help has no code 5, and a nodegroup that is DEGRADED after the scale exits 1 (golden `scale-degraded-after.txt`). The contract's code 5 is "the change was applied and the check after it found issues". Either the help or the contract should say that a scale has no check after it. | Fixed in `fb41208` (the help says that a scale has no verification and never exits 5) and `8c79b23` (`CONTRACT.md`, "Exit codes"). |
| 8 | No test | `TestChangeCommandsAgreeOnExitCodes` covers the five changing commands. No test asks the other commands for exit-code help. The old tree had one for the whole tree. | Fixed in `fb41208`: `TestEveryCommandListsItsExitCodes` covers every command that is not a group and not from cobra. |
| 9 | All verdict exits | A verdict that is not an error prints as `Error:` on stderr: `Error: upgrade needs review: ...` (exit 2), `Error: dry run: a real run would ask about health warnings` (exit 2), `Error: incomplete data: 1 failure(s)` (exit 4). The word is the same for a crash and for a warning. | Open. The word before a verdict is a decision for the owner. |

## Flags against the contract

| # | Flag | Finding | Status |
|---|---|---|---|
| 10 | `--require-healthy` | `nodegroup scale` and `addon update` have it. The "Command flags" table of `CONTRACT.md` lists it for `cluster upgrade`, `cluster rollback`, and `nodegroup update` only, and the shared-flags table does not have it. The phase 4 note says the edit was accepted; the contract was not updated. | Fixed in `8c79b23`: both rows of the contract have `--require-healthy`. |
| 11 | `--kube-context` | Help: "Kubeconfig context (default: the current context)". The default is not the current context: without the flag, refresh picks the context whose server matches the cluster endpoint. The contract's point, "trusted even when its server does not match the cluster endpoint", is not in the help. | Fixed in `fb41208`. |
| 12 | `-t, --timeout` | Help: "Budget for the API calls of a read command". The flag is on every command. In a changing command it bounds the reads before the first change, and with `--watch` each refresh. The help says neither. The contract has the same short text. | Fixed in `fb41208`, in the help. The contract cell has the same text now. |
| 13 | `--profile` | The help names no env var. The contract's table gives `AWS_PROFILE`, and the other global flags name theirs. | Fixed in `fb41208`. |
| 14 | `-r, --region` | The help does not name `REFRESH_EKS_REGIONS`, which the contract gives as its env var for sweeps. `-A` names it. | Fixed in `fb41208`. |
| 15 | `-a` | The contract reserves `-a` for add-on. No command has a flag with that letter. | Decided: `-a` is not reserved (`CONTRACT.md` change 29). `TestFlagShorthands` fails on a reserved letter that no flag uses. |
| 16 | `ui --allow-changes`, `ui --wait-timeout` | Both are accepted and do nothing ("no effect yet"). The contract lists them as working flags. | No longer applies: both flags work since `c59d34b`. |
| 17 | `-o` on `context list` and `context current` | Neither has `-o`. The contract's "Output" rules do not say which commands print no document. | Decided: neither prints a document (`CONTRACT.md` change 30, "Commands that print no document"). `TestCommandsWithoutOutputAreInTheContract` holds the list to the tree. |
| 18 | `context add -r`, `--profile` | They shadow the global `--region` and `--profile`. On purpose, and no test stops a second command from doing the same. | Fixed in `43a4f27`: `TestNoCommandFlagShadowsGlobalFlag`. |
| 19 | `context *`, `version`, `completion` | They list every global flag (`--timeout`, `--max-concurrency`, `--kubeconfig`, `--kube-context`), and none applies. | Open. A decision: cobra shows the persistent flags on every command. |
| 20 | `cluster upgrade --to` | Required, and the usage line is `refresh cluster upgrade [cluster] [flags]`. `gen-docs` shows its required flag in the usage line (`gen-docs --out <directory>`). | Fixed in `fb41208`: `refresh cluster upgrade [cluster] --to <version> [flags]`. |
| 21 | `--version` | `refresh --version` prints three lines (version, commit, build date), the same as `refresh version`. The contract says "Print the version". | Fixed in `fb41208`, in the help. The contract cell has the same text now. |

## The same thing in different words

| # | Where | Finding | Status |
|---|---|---|---|
| 22 | `--skip-addon` | `cluster upgrade`: "Leave these add-ons alone, by exact name". `addon update`: "Add-on that --all leaves out; repeat it or separate the names with commas". | Fixed in `fb41208`: `addon update` says "Leave these add-ons out of --all, by exact name". |
| 23 | `--skip-verify` | `cluster upgrade`, `cluster rollback`: "after each nodegroup roll". `nodegroup update`: "after the rolls". | Fixed in `fb41208`: all three say "after each nodegroup roll". |
| 24 | `-q, --quiet` | Help: "Print no progress; questions, warnings, and the result still show". Contract: "Minimal progress output". | Fixed: the contract cell has the help's text now. |
| 25 | Where the cluster comes from | `nodegroup update`, `nodegroup scale`, `addon update`, `cluster upgrade`, `cluster rollback`, and `cluster describe` say it in their own help. `nodegroup list`, `nodegroup describe`, `addon list`, `addon describe`, and `cluster upgrade-check` do not; only the flag text or the group help says it. | Fixed in `fb41208`: the five commands say it in their own help. |
| 26 | `completion` | The help is cobra's own text ("Generate the autocompletion script for the specified shell"), in another voice than the rest. `help` is a command of the tree and is not in the contract's command list. | Decided: `completion` and its shells have refresh's text and an `Exit codes:` block; `help` keeps cobra's text and is in the contract's command list (`CONTRACT.md` change 31). |
| 27 | `ui` | The first line of the long help is 87 characters wide; every other help wraps at 76. | Fixed in `fb41208`. |
| 28 | Health of one cluster | `cluster list --show-health` shows `Pass` for a cluster whose `cluster describe` shows `summary Warn`, because describe counts the `Kubernetes` access warning and list does not run it. Seen on `prod-payments-00` of the fake fleet. | Fixed in `74ee870`. Cause: without `--check-readiness`, describe counted the warning of the Kubernetes access check, which list does not run. The check is now `Skipped` there. Test: `TestHealthSummary_ListAndDescribeAgree`. |
| 29 | `cluster upgrade-check`, INSIGHTS table | The column `NAME` holds the short ID that `--insight` takes, and the column `INSIGHT` holds the name. The help calls it "the short ID of the table". The fake fleet seeds insights with no ID, so the column shows `-` there. | The fake fleet seeds an ID since `79f39e5`. Open for the column name: the first column of every item table is `NAME`. |
| 30 | `version -o table` | The "table" format prints three `key: value` lines. | Open. A decision. |
| 31 | `context add --use` | It prints `Saved context "prod"` and not the `Switched to context` line that `context use` prints. | Fixed in `fbe93c4`: `--use` prints both lines. |

## Checked and consistent

- Every flag letter has one meaning. `-A` is the only capital letter.
- The five changing commands share `-d`, `-y`, `--poll-interval`, `--wait-timeout`, `--skip-health-check`, `--require-healthy`, and `-q` with the same text. `--no-wait` is on `nodegroup update`, `nodegroup scale`, and `addon update` only. `--force` is not on `addon update`.
- The `--wait-timeout` defaults are those of the contract: 40m, 5m, 5m for each add-on, 4h, 4h.
- `-c` on a changing command says "never the kubeconfig"; on a read command it names the kubeconfig as the last source.
- No removed flag is left: `--format`, `--tree`, `--health-only`, `--wait`, `--health-check`, `--check-pdbs`, `--parallel`, `--dependency-order`, `--op-timeout`, `--id`, `--detailed`, `-v` are all unknown to the binary.
- `-A` with `-r` is a usage error, and `--watch` needs `-o table` and a terminal.
