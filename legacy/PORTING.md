# Porting record

This file says what happened to each test file of the old tree in the rewrite. A file is ported, covered by a new test, or dropped with a reason. A test that names a past regression is never dropped. `REWRITE.md` has the rule.

Paths in the first column are below `legacy/internal/`. Paths in the other columns are from the repository root.

## Read side (phase 2)

| Old test file | Result | New test |
|---|---|---|
| `services/status/support_test.go` | Ported | `internal/fleet/status_test.go`: `TestFetch_Everything` (the day boundary: a part of a day counts), `TestSupportOfTheBuiltInCalendar`, `TestFetch_AdvisoryReads` |
| `services/status/support_fuzz_test.go` | Ported | `internal/fleet/version_test.go`: `FuzzParseVersion`, and `TestParseVersion` rejects `1.-5` |
| `services/status/incomplete_test.go` | Covered | `internal/fleet/sweep_test.go`: `TestSweep_CancelKeepsFinishedRowsAndMarksTheRest`, `TestSweep_ClusterThatFailsIsARow`. `internal/fleet/nodegroups_test.go`: `TestFetch_AMILookupFails`, `TestFetch_OneNodegroupFails`. `internal/fleet/status_test.go`: the add-on with no compatible version in `TestFetch_Everything` |
| `services/status/sweep_test.go` | Covered | `internal/fleet/status_test.go`: `TestSweep_SharesTheLookups`. `internal/fleet/nodegroups_test.go`: `TestFetch_LatestAMIIsShared` (a failed lookup is not kept). A `Fetcher` lives for one run, so a lookup cannot outlive a sweep |
| `services/status/paging_test.go` | Covered | `internal/awsx` paging tests for `ListAll`; every list call of `fleet` goes through it |
| `services/status/retry_test.go` | Covered | `internal/awsx/clients_test.go` for the retries. `internal/fleet/status_test.go`: `TestFetch_Compute` (the Karpenter probe names its cluster) |
| `services/status/service_test.go` | Covered | `internal/cmd/status/status_test.go`: `TestStatus_Formats`, `TestStatus_ExitCodes` (a nodegroup behind the control plane, a control-plane issue), `TestStatus_SortAndFilter` |
| `services/status/livecheck_test.go` | Not ported yet | The calendar livecheck needs a real account. `internal/fleet/fleetdata/regions_live_test.go` is the first livecheck of the new tree |
| `services/cluster/filter_test.go`, `commands/cluster/filter_test.go` | Ported | `internal/cmd/cluster/list_test.go`: `TestList_SortAndFilter` (the status and version filters apply). `internal/cmdutil/filter_test.go` |
| `services/cluster/service_test.go` | Covered | `internal/cmd/cluster/describe_test.go`: `TestDescribe_Partial` (a part that fails is a failure, not a silent gap). `internal/fleet/sweep_test.go`. The ready count of a nodegroup is phase 3: no document has a `readyNodes` key until it is measured |
| `services/cluster/insights_test.go`, `busy_test.go`, `readiness_test.go` | Phase 3 | Health and upgrade checks |
| `services/nodegroup/service_test.go` | Ported | `internal/fleet/nodegroups_test.go`: `TestFetch_NodegroupsAndAMIs` (the status of updating and custom nodegroups), `TestFetch_Instances`. The scale tests are phase 4 |
| `services/nodegroup/list_failures_test.go` | Ported | `internal/fleet/nodegroups_test.go`: `TestFetch_OneNodegroupFails`. `internal/cmd/nodegroup/nodegroup_test.go`: `TestList_SortAndFilter` (a row that a filter drops is not a failure), `TestList_Deadline` |
| `services/nodegroup/list_options_test.go` | Covered | `TestFetch_LatestAMIIsShared`. The cluster version no longer needs an option: `Fetch` reads the cluster next to the nodegroups |
| `services/nodegroup/readiness_test.go`, `commands/nodegroup/list_render_test.go` | Phase 3 | The ready count. The rule that it is never invented holds now: the key does not exist |
| `aws/ami_test.go` | Ported | `internal/fleet/fleetdata/ami_test.go` (every AMI type, types that are not guessed). `internal/fleet/nodegroups_test.go`: `TestFetch_AMILookupFails`, `TestFetch_OneAMISourceIsEnough` |
| `aws/ami_cache_test.go` | Covered | `internal/fanout` memo tests, and `TestFetch_LatestAMIIsShared`. `TestFetch_NodegroupsAndAMIs` checks that the key is the version of the nodegroup |
| `aws/ami_release_test.go` | Phase 4 | The release version is for the changelog of `nodegroup update` |
| `aws/cluster_test.go`, `aws/nodegroup_test.go` | Dropped | They test the substring match of names and its prompts. A name is exact in v2. `internal/cmdutil/target_test.go` and the `TestNotFound` tests of the command packages cover the suggestions that replace them |
| `services/addons/version_order_test.go`, `version_fuzz_test.go` | Ported | `internal/fleet/addonversion_test.go`. `TestFetch_Everything` checks that the newest version is found by comparison |
| `services/addons/update_*_test.go`, `health_check_test.go` | Phase 4 | The add-on update |
| `regionsweep/regionsweep_test.go` | Ported | `internal/fleet/sweep_test.go`: `TestSweep_DefaultSweepSkipsAClosedRegion`, `TestSweep_NamedClosedRegionFails`, `TestSweep_CancelledBeforeItStarts` |
| `commands/statuscmd/actions_test.go`, `run_test.go`, `partial_failures_test.go` | Ported | `internal/cmd/status/status_test.go`: `TestStatus_ExitCodes`, `TestStatus_Partial`. `internal/cmd/cluster/list_test.go`: `TestList_NoRegionAnswers`, `TestList_DefaultSweepSkipsAClosedRegion`. `internal/cmd/fleetcmd/fleetcmd_test.go`: `TestSweepRegions`. The exit order changed on purpose: the highest code wins |
| `commands/statuscmd/interrupt_test.go` | Ported | `TestStatus_Deadline` (exit 4), `TestList_Interrupt` (exit 1) |
| `commands/statuscmd/fanout_test.go` | Covered | `TestStatus_LargeFleetCallsAndLimit`, `TestSweep_RequestLimitCountsRequests`. The cap of four regions is gone on purpose |
| `commands/statuscmd/sort_test.go` | Ported | `TestStatus_SortAndFilter`, `TestStatus_InvalidInput` |
| `commands/cluster/list_regions_test.go` | Ported | `internal/cmd/cluster/list_test.go`: `TestList_Partial`, `TestList_RegionsFromTheEnvironmentAreNamed`, `TestList_NoRegionAnswers`. The tree view is removed |
| `commands/cluster/exitcodes_test.go`, `describe_test.go`, `machine_output_test.go` | Ported | `TestList_Partial`, `TestDescribe_Partial`, `TestDescribe_Formats`: the failures are in the document, and the exit code is 4 |
| `commands/cluster/flag_canon_test.go` | Covered | `internal/cmd/root/tree_test.go`: `TestFlagShorthands` |
| `commands/cluster/upgradecheck_*_test.go`, `upgrade*_test.go`, `rollback*_test.go` | Phases 3 and 4 | |
| `commands/nodegroup/failure_fields_test.go` | Covered | The schemas in `api/schema/v2`, and `cmdutiltest.ValidateJSON` in every golden test: a failure has its fields |
| `commands/addon/actions_test.go` (the read cases) | Ported | `internal/cmd/addon/addon_test.go` |
| `commands/clusterview/clusterview_test.go`, `detail_render_test.go` | Covered | `internal/view/list_test.go`, `description_test.go`, and the golden files of the command packages |
| `commands/clusterview/insights_render_test.go` | Phase 3 | |
| `ui/ansi_test.go` | Ported | `internal/view/list_test.go`: `TestList_WideRunesLineUp`, `TestList_Widths`. The table pads and cuts plain text and paints it after, so there is no escape code to parse, and the tests for malformed escape codes have nothing left to test |
| `ui/plain_test.go`, `ui/plaintest` | Ported | `TestList_Plain`, `TestDescription_Plain`, `TestList_TableAndPlainHaveTheSameText` |
| `ui/tree_test.go` | Dropped | `-o tree` is removed |
| `commands/runner/runner_test.go` | Covered | `internal/cmd/addon/addon_test.go`: `TestClusterResolution`. `internal/kube/kubeconfig_test.go` |
| `commands/runner/exitcodes_test.go`, `deadline_test.go` | Covered | `internal/cmdutil` tests of `ExitError` and of the pausable deadline, and the deadline test of each command |
| `diag/reason_test.go` | Covered | `internal/awsx/class_test.go`, and `TestErrorClassesAreReasons` in `internal/fleet`. The check of the reason table against the docs waits for the docs of phase 6 |
