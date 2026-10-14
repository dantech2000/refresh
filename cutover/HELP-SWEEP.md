# Help and exit-code sweep of the new binary

Findings only. No Go code was changed. The sweep compared the `--help` of every
command of the binary built at `b62e59d` with `CONTRACT.md` and with the other
commands. `docs-next/reference/` has the full help text.

## Exit codes in the help

| # | Command | Finding |
|---|---|---|
| 1 | `cluster list`, `cluster describe`, `nodegroup list`, `nodegroup describe`, `addon list` | No `Exit codes:` block. The prose names exit 4, and `cluster list` names exit 1. The five changing commands and `status` have a block. |
| 2 | `addon describe` | The help names no exit code at all, and does not say what a partial read does. `nodegroup describe` and `cluster describe` say "prints what it has ... and exits 4". |
| 3 | `context *`, `ui`, `version`, `completion` | No exit-code help. |
| 4 | `cluster upgrade-check` | The codes are one sentence ("0 ready, 2 review (warnings), 3 blocked, 4 incomplete"), not a block, and exit 1 is not listed. |
| 5 | `status` | The block lists the codes in the order 0, 2, 3, 4, 1. The five changing commands list 0, 1, 2, 3, 4, 5. |
| 6 | `cluster upgrade`, `cluster rollback` | Code 3 reads "Blocked, and nothing changed". `CONTRACT.md` says that when a gate stops a later phase "nothing more changed and the message names the phases that completed". The help contradicts the contract for a mid-run block. |
| 7 | `nodegroup scale` | The help has no code 5, and a nodegroup that is DEGRADED after the scale exits 1 (golden `scale-degraded-after.txt`). The contract's code 5 is "the change was applied and the check after it found issues". Either the help or the contract should say that a scale has no check after it. |
| 8 | No test | `TestChangeCommandsAgreeOnExitCodes` covers the five changing commands. No test asks the other commands for exit-code help. The old tree had one for the whole tree. |
| 9 | All verdict exits | A verdict that is not an error prints as `Error:` on stderr: `Error: upgrade needs review: ...` (exit 2), `Error: dry run: a real run would ask about health warnings` (exit 2), `Error: incomplete data: 1 failure(s)` (exit 4). The word is the same for a crash and for a warning. |

## Flags against the contract

| # | Flag | Finding |
|---|---|---|
| 10 | `--require-healthy` | `nodegroup scale` and `addon update` have it. The "Command flags" table of `CONTRACT.md` lists it for `cluster upgrade`, `cluster rollback`, and `nodegroup update` only, and the shared-flags table does not have it. The phase 4 note says the edit was accepted; the contract was not updated. |
| 11 | `--kube-context` | Help: "Kubeconfig context (default: the current context)". The default is not the current context: without the flag, refresh picks the context whose server matches the cluster endpoint. The contract's point, "trusted even when its server does not match the cluster endpoint", is not in the help. |
| 12 | `-t, --timeout` | Help: "Budget for the API calls of a read command". The flag is on every command. In a changing command it bounds the reads before the first change, and with `--watch` each refresh. The help says neither. The contract has the same short text. |
| 13 | `--profile` | The help names no env var. The contract's table gives `AWS_PROFILE`, and the other global flags name theirs. |
| 14 | `-r, --region` | The help does not name `REFRESH_EKS_REGIONS`, which the contract gives as its env var for sweeps. `-A` names it. |
| 15 | `-a` | The contract reserves `-a` for add-on. No command has a flag with that letter. |
| 16 | `ui --allow-changes`, `ui --wait-timeout` | Both are accepted and do nothing ("no effect yet"). The contract lists them as working flags. |
| 17 | `-o` on `context list` and `context current` | Neither has `-o`. The contract's "Output" rules do not say which commands print no document. |
| 18 | `context add -r`, `--profile` | They shadow the global `--region` and `--profile`. On purpose, and no test stops a second command from doing the same. |
| 19 | `context *`, `version`, `completion` | They list every global flag (`--timeout`, `--max-concurrency`, `--kubeconfig`, `--kube-context`), and none applies. |
| 20 | `cluster upgrade --to` | Required, and the usage line is `refresh cluster upgrade [cluster] [flags]`. `gen-docs` shows its required flag in the usage line (`gen-docs --out <directory>`). |
| 21 | `--version` | `refresh --version` prints three lines (version, commit, build date), the same as `refresh version`. The contract says "Print the version". |

## The same thing in different words

| # | Where | Finding |
|---|---|---|
| 22 | `--skip-addon` | `cluster upgrade`: "Leave these add-ons alone, by exact name". `addon update`: "Add-on that --all leaves out; repeat it or separate the names with commas". |
| 23 | `--skip-verify` | `cluster upgrade`, `cluster rollback`: "after each nodegroup roll". `nodegroup update`: "after the rolls". |
| 24 | `-q, --quiet` | Help: "Print no progress; questions, warnings, and the result still show". Contract: "Minimal progress output". |
| 25 | Where the cluster comes from | `nodegroup update`, `nodegroup scale`, `addon update`, `cluster upgrade`, `cluster rollback`, and `cluster describe` say it in their own help. `nodegroup list`, `nodegroup describe`, `addon list`, `addon describe`, and `cluster upgrade-check` do not; only the flag text or the group help says it. |
| 26 | `completion` | The help is cobra's own text ("Generate the autocompletion script for the specified shell"), in another voice than the rest. `help` is a command of the tree and is not in the contract's command list. |
| 27 | `ui` | The first line of the long help is 87 characters wide; every other help wraps at 76. |
| 28 | Health of one cluster | `cluster list --show-health` shows `Pass` for a cluster whose `cluster describe` shows `summary Warn`, because describe counts the `Kubernetes` access warning and list does not run it. Seen on `prod-payments-00` of the fake fleet. |
| 29 | `cluster upgrade-check`, INSIGHTS table | The column `NAME` holds the short ID that `--insight` takes, and the column `INSIGHT` holds the name. The help calls it "the short ID of the table". The fake fleet seeds insights with no ID, so the column shows `-` there. |
| 30 | `version -o table` | The "table" format prints three `key: value` lines. |
| 31 | `context add --use` | It prints `Saved context "prod"` and not the `Switched to context` line that `context use` prints. |

## Checked and consistent

- Every flag letter has one meaning. `-A` is the only capital letter.
- The five changing commands share `-d`, `-y`, `--poll-interval`, `--wait-timeout`, `--skip-health-check`, `--require-healthy`, and `-q` with the same text. `--no-wait` is on `nodegroup update`, `nodegroup scale`, and `addon update` only. `--force` is not on `addon update`.
- The `--wait-timeout` defaults are those of the contract: 40m, 5m, 5m for each add-on, 4h, 4h.
- `-c` on a changing command says "never the kubeconfig"; on a read command it names the kubeconfig as the last source.
- No removed flag is left: `--format`, `--tree`, `--health-only`, `--wait`, `--health-check`, `--check-pdbs`, `--parallel`, `--dependency-order`, `--op-timeout`, `--id`, `--detailed`, `-v` are all unknown to the binary.
- `-A` with `-r` is a usage error, and `--watch` needs `-o table` and a terminal.
