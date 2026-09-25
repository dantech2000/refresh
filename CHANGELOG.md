# Changelog

## [0.12.1](https://github.com/dantech2000/refresh/compare/v0.12.0...v0.12.1) (2026-09-25)


### Bug Fixes

* **addon:** order prereleases correctly and tighten the update gates ([#396](https://github.com/dantech2000/refresh/issues/396)) ([f9b1fd6](https://github.com/dantech2000/refresh/commit/f9b1fd6b54437649bcd9abc1fce6da168520c72d))
* **cluster:** close the list/describe/upgrade-check correctness leftovers ([#400](https://github.com/dantech2000/refresh/issues/400)) ([7d9f109](https://github.com/dantech2000/refresh/commit/7d9f109a986b8b8faf76b04bd25a3a3c5a32f3a6))
* **config:** apply a context as a unit and fail on bad context config ([#399](https://github.com/dantech2000/refresh/issues/399)) ([ddc7865](https://github.com/dantech2000/refresh/commit/ddc7865d4a30a0c94a464816089ca88e265a5b1d))
* **health:** stop false BLOCKs from throttled reads and clusters without managed nodegroups ([#398](https://github.com/dantech2000/refresh/issues/398)) ([0ee4877](https://github.com/dantech2000/refresh/commit/0ee4877a65072f9b4ac0d305f3d7d0f492b57f98))
* **nodegroup:** close the scale, list sort, and fleet health-gate leftovers ([#397](https://github.com/dantech2000/refresh/issues/397)) ([6d70a1a](https://github.com/dantech2000/refresh/commit/6d70a1a72f8e5f930f2bd637743b2809cee846c1))
* retry the remaining unretried AWS calls and guard new ones ([#401](https://github.com/dantech2000/refresh/issues/401)) ([7cb12ef](https://github.com/dantech2000/refresh/commit/7cb12efaa4e91d7276127f2127bd70a121284256))
* small leftovers from the 2026-09-23 review ([#394](https://github.com/dantech2000/refresh/issues/394)) ([7f87139](https://github.com/dantech2000/refresh/commit/7f871399bc57523dec1f439ffc2f93e8bb0c7738))
* **status:** show Auto Mode nodegroup AMIs, name the region in the hint, reject unknown --sort ([#395](https://github.com/dantech2000/refresh/issues/395)) ([ef8acaa](https://github.com/dantech2000/refresh/commit/ef8acaa96f0d96f93f29c4d7ebdd9946bd0e8157))


### Performance Improvements

* **runner:** drop the STS pre-check from command setup ([#385](https://github.com/dantech2000/refresh/issues/385)) ([3deb68a](https://github.com/dantech2000/refresh/commit/3deb68a30cab7985ad326446f3cefbdbff1e9299))
* **status:** share lookups across the fleet sweep and run cluster checks concurrently ([#384](https://github.com/dantech2000/refresh/issues/384)) ([3f98e0a](https://github.com/dantech2000/refresh/commit/3f98e0a245f2cd80c8964728d2e54c8bf55c28d8))


### Code Refactoring

* **commands:** build AWS clients only through the factory ([#391](https://github.com/dantech2000/refresh/issues/391)) ([050f177](https://github.com/dantech2000/refresh/commit/050f177f7c112c5ab646e0a79a3b1672b5d55e25))
* move internal/services/common to internal/common ([#387](https://github.com/dantech2000/refresh/issues/387)) ([3f2ca00](https://github.com/dantech2000/refresh/commit/3f2ca00bd756d62c4875b84da5e368fcbb72f291))
* **nodegroup:** one decision table for nodegroup update and its dry run ([#388](https://github.com/dantech2000/refresh/issues/388)) ([57baa6c](https://github.com/dantech2000/refresh/commit/57baa6c3b072289ad1ad33e7a0180ab265b117a0))
* one region sweep for every multi-region command ([#389](https://github.com/dantech2000/refresh/issues/389)) ([15bb4df](https://github.com/dantech2000/refresh/commit/15bb4dfcf4293a8c2379facb45974ec9fd95ae22))
* **render:** move health, verify, and monitoring output to status tokens ([#402](https://github.com/dantech2000/refresh/issues/402)) ([05ba080](https://github.com/dantech2000/refresh/commit/05ba080b8b0d40217acfa0042a3097de246fa5f3))
* **render:** move lists, describe, contexts, and notices to status tokens ([#404](https://github.com/dantech2000/refresh/issues/404)) ([e303887](https://github.com/dantech2000/refresh/commit/e3038873272dc2bc9a914b00d8515511ead613aa))
* **render:** move upgrade, dry-run, add-on, and scale output to status tokens ([#403](https://github.com/dantech2000/refresh/issues/403)) ([b68a867](https://github.com/dantech2000/refresh/commit/b68a867a29143fa2a03708f15604520bdb3d4deb))
* share the nodegroup roll start and the permanent-error check ([#390](https://github.com/dantech2000/refresh/issues/390)) ([ea5b058](https://github.com/dantech2000/refresh/commit/ea5b05870f789989a7bec7db3805b63dd4476ca6))

## [0.12.0](https://github.com/dantech2000/refresh/compare/v0.11.1...v0.12.0) (2026-09-24)


### ⚠ BREAKING CHANGES

* AddonUpdate results leave out newVersion, updateId, and startedAt when the update was not resolved or sent.
* **output:** every -o json/yaml document has two new leading keys, apiVersion and kind. Consumers that compare whole documents or reject unknown keys must accept them.
* **diag:** nodegroup update -o json/yaml replaces started, skipped, customUnmanaged, failed, and rollFailures with nodegroups[] ({name, status, updateId, reason, failure}) and failures. The dry-run plan gains failures and the action unknown. nodegroup update --all-clusters -o json/yaml: each cluster has status and nodegroups instead of outcomes, healthBlocked, healthWarned, verifyFailed, interrupted, timedOut, and error; the document drops discoveryErrors for failures (Kind Region). A fleet dry-run entry has status and failure instead of error. Clusters the run never reached are listed as NotAttempted. addon update statuses are PascalCase (UpToDate, InProgress, Started, Completed, CompletedWithIssues, WaitFailed, ...) instead of UP_TO_DATE/IN_PROGRESS/COMPLETED/... and the raw EKS InProgress; FAILED: ... becomes Failed or NotAttempted with a failure; the error field is removed. The single-add-on document adds failures; --all is {cluster, dryRun, results, failures}. cluster upgrade plan warnings is renamed notices and the plan gains failures; the report replaces failedAt with stoppedAt and adds status and failure; completed and remaining are always lists; the run document is {plan, report, failures}. exit codes: nodegroup update --dry-run and fleet dry run with a read failure 0 -> 4; post-roll describe failure 5 -> 4; addon post-update read failure 5 -> 4; cluster upgrade --dry-run with a plan read failure 0 -> 4, and a finished run with one 0 -> 4; nodegroup scale --check-pdbs --force with unreadable PDBs 0 -> 4; nodegroup update --health-only that passes but could not read everything 0 -> 4.
* **diag:** JSON/YAML output of the read commands changed. status: the row "errors" list is gone (rows get "incomplete": true); "failures" is always present and holds diag.Failure objects for regions and cluster rows instead of {"region","error"}. cluster list: the row "warnings" list is gone (rows get "incomplete": true); "failures" is always present and holds diag.Failure objects instead of {"region","error"}. cluster describe: "warnings" is replaced by "failures". cluster upgrade-check: "incomplete" is replaced by "failures"; a skew addon whose latest version could not be read has "incomplete": true. nodegroup list and addon list: "failures" is always present and holds diag.Failure objects instead of "name: reason" strings. nodegroup list and describe: "amiLookupError" (string) is replaced by "amiLookupFailure" (object). nodegroup describe, addon describe, and upgrade-check --id carry an empty "failures". status -o plain has no ERRORS column. The types.RegionFailure type is removed.

### Features

* **diag:** add the failure type, classifier, and stderr/exit helpers ([#377](https://github.com/dantech2000/refresh/issues/377)) ([48e40a1](https://github.com/dantech2000/refresh/commit/48e40a1f06a744b1cce4aa2fa0e4e2dad53f7b8b))
* **diag:** report mutating-command failures as diag.Failure (REF-179) ([#380](https://github.com/dantech2000/refresh/issues/380)) ([c5790e1](https://github.com/dantech2000/refresh/commit/c5790e194f80d44aa9456b9b8bf424ca05f211f1))
* **diag:** report read-command failures as diag.Failure (REF-178) ([#379](https://github.com/dantech2000/refresh/issues/379)) ([ced2f8c](https://github.com/dantech2000/refresh/commit/ced2f8c3f6fcb95598e12238a12c98af7e18bcf9))
* **output:** version every document and publish JSON schemas (REF-180) ([#381](https://github.com/dantech2000/refresh/issues/381)) ([d7a8379](https://github.com/dantech2000/refresh/commit/d7a8379be1028716e608cc76ed1927e959255367))


### Bug Fixes

* close three v1 contract gaps found by the E2E harness ([#382](https://github.com/dantech2000/refresh/issues/382)) ([c651829](https://github.com/dantech2000/refresh/commit/c65182921d10a515bfab39f5a344ac0d9b8d625e))
* **health:** build every checker with its region ([#383](https://github.com/dantech2000/refresh/issues/383)) ([7a7d433](https://github.com/dantech2000/refresh/commit/7a7d4331f503c4ff61dc17abc15985ee5f0e43c0))

## [0.11.1](https://github.com/dantech2000/refresh/compare/v0.11.0...v0.11.1) (2026-09-24)


### Bug Fixes

* **addon:** --wait-timeout 0 means no limit on addon update ([#363](https://github.com/dantech2000/refresh/issues/363)) ([118d9ca](https://github.com/dantech2000/refresh/commit/118d9cad9c0d164db9b80f71267e3125653a0540))
* **addon:** fail closed when the update --all preview can't read an add-on ([#362](https://github.com/dantech2000/refresh/issues/362)) ([e1fbade](https://github.com/dantech2000/refresh/commit/e1fbade61958eedb1032f75969a8f7921d2b147d))
* **cli:** treat h and help as positionals on leaf commands ([#375](https://github.com/dantech2000/refresh/issues/375)) ([b7f191b](https://github.com/dantech2000/refresh/commit/b7f191bfaaecfc041937ca9e67242c496a2a374c))
* **flagcanon:** reject a deprecated wait alias together with --wait-timeout ([#364](https://github.com/dantech2000/refresh/issues/364)) ([efa1232](https://github.com/dantech2000/refresh/commit/efa12328a95829e4bbeb934eba32c16d65808072))
* **nodegroup:** report Ctrl+C during scale --wait as interrupted, not timed out ([#372](https://github.com/dantech2000/refresh/issues/372)) ([babd7ab](https://github.com/dantech2000/refresh/commit/babd7ab26f7952a090d830cccc28139abe85d085))
* **nodegroup:** scale --dry-run --check-pdbs exits with the gate's code ([#365](https://github.com/dantech2000/refresh/issues/365)) ([688739a](https://github.com/dantech2000/refresh/commit/688739a1b7c741b08604d2e947baef483d7374ae))
* **nodegroup:** update -o json encodes empty lists as [] and reports roll failures ([#370](https://github.com/dantech2000/refresh/issues/370)) ([5ac95a1](https://github.com/dantech2000/refresh/commit/5ac95a1f39fa0401342f9d099d33610a06228ccb))
* **output:** report partial failures in the document and on stderr ([#373](https://github.com/dantech2000/refresh/issues/373)) ([d861527](https://github.com/dantech2000/refresh/commit/d8615274aca1c7d9ab53ab2a953195df0542993e))
* **upgrade:** report a --wait-timeout expiry as a timeout, not an interrupt ([#371](https://github.com/dantech2000/refresh/issues/371)) ([af0bbf3](https://github.com/dantech2000/refresh/commit/af0bbf34ff5e11d279a2a491587910f15efb0a5d))

## [0.11.0](https://github.com/dantech2000/refresh/compare/v0.10.4...v0.11.0) (2026-09-23)


### ⚠ BREAKING CHANGES

* Removed shorthands: cluster describe -d (--detailed), -s (--show-security), -a (add-ons show by default; --no-addons hides them); cluster upgrade -s (--skip), -p (--poll-interval); nodegroup update -f (--force), -s (--skip-health-check), -p (--poll-interval); addon update -s (--skip), -p (--parallel); context add -p (--profile). Renamed flags (old names deprecated, removed in 0.12.0): --timeout/-t after nodegroup update and cluster upgrade is now --wait-timeout; nodegroup scale --op-timeout is now --wait-timeout; cluster describe --show-health and --include-addons are replaced by --no-health and --no-addons. addon update and nodegroup scale now ask for confirmation before they change anything, and cluster upgrade needs --yes without a terminal: use --yes in scripts.
* cluster upgrade-check now exits 2 (warnings) or 3 (blockers) instead of always 0; pass --exit-zero for the old behavior. cluster list exits 4 on a partial region failure instead of 0. Also: cluster describe exits 4 on unreadable parts, nodegroup list and addon list exit 4 instead of 1 on partial results, cluster upgrade and nodegroup scale --check-pdbs exit 3 instead of 1 when blocked, and addon update exits 5 instead of 2 for COMPLETED_WITH_ISSUES (4 instead of 1 for failed add-ons with --all).

### Features

* consistent exit codes and upgrade-check as a CI gate (REF-165) ([#358](https://github.com/dantech2000/refresh/issues/358)) ([52b303d](https://github.com/dantech2000/refresh/commit/52b303d767f2d2b09b673e9e2e1741109b9bbf4d))
* consistent flag shorthands and shared mutating-command flags (REF-164) ([#357](https://github.com/dantech2000/refresh/issues/357)) ([91df4b4](https://github.com/dantech2000/refresh/commit/91df4b418f21a7ce2f0dced151d5477ce4df6096))


### Bug Fixes

* **common:** never start ForEachParallel work after cancellation ([#360](https://github.com/dantech2000/refresh/issues/360)) ([6191097](https://github.com/dantech2000/refresh/commit/61910971286cec0acdd95a689be551fcddebf063))
* **nodegroup:** gate --max scale-downs on PDBs and wait on the EKS update ([#359](https://github.com/dantech2000/refresh/issues/359)) ([b997edb](https://github.com/dantech2000/refresh/commit/b997edb374cc6a432d910380b8c5d8a46de2aeb3))

## [0.10.4](https://github.com/dantech2000/refresh/compare/v0.10.3...v0.10.4) (2026-09-23)


### Bug Fixes

* **addon:** make addon update and addon list report what really happened ([#349](https://github.com/dantech2000/refresh/issues/349)) ([d8e9ddc](https://github.com/dantech2000/refresh/commit/d8e9ddcfc1d0ab70f7325d50364288c412cf741d))
* address docs audit findings ([#356](https://github.com/dantech2000/refresh/issues/356)) ([4463d72](https://github.com/dantech2000/refresh/commit/4463d72fe9f7dd0a0111580ac2e478d0c8886cfa))
* **cli:** accept any NO_COLOR value and make prompts cancellable ([#322](https://github.com/dantech2000/refresh/issues/322)) ([90711f7](https://github.com/dantech2000/refresh/commit/90711f7d3fca4bbd02a6c151151ca5d54e2097c7))
* correct AMI SSM paths, EKS support calendar, and AMI patch version ([#348](https://github.com/dantech2000/refresh/issues/348)) ([8075d7c](https://github.com/dantech2000/refresh/commit/8075d7c6df4493bedfa4cab9af2666926c8cfd57))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#306](https://github.com/dantech2000/refresh/issues/306)) ([a172c60](https://github.com/dantech2000/refresh/commit/a172c60ac0ddd8f9133262cda3f0d6ec53927914))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#308](https://github.com/dantech2000/refresh/issues/308)) ([17767ff](https://github.com/dantech2000/refresh/commit/17767ff599a0e83dde5db7ae214bd8d19b6c95d5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#316](https://github.com/dantech2000/refresh/issues/316)) ([1122795](https://github.com/dantech2000/refresh/commit/112279563fb3457d0accf072452364468054a6c8))
* **deps:** bump github.com/fxamacker/cbor/v2 from 2.9.3 to 2.9.4 ([#309](https://github.com/dantech2000/refresh/issues/309)) ([f9d9e46](https://github.com/dantech2000/refresh/commit/f9d9e4612bad1d987c4e64e88469f1fe3f0789f1))
* **deps:** bump github.com/go-openapi/jsonreference from 1.0.1 to 1.0.2 ([#317](https://github.com/dantech2000/refresh/issues/317)) ([1f2708c](https://github.com/dantech2000/refresh/commit/1f2708ca3a259b3b80b14b1a717003570235a8c9))
* **deps:** bump github.com/go-openapi/swag from 0.29.1 to 0.29.2 ([#312](https://github.com/dantech2000/refresh/issues/312)) ([23d902e](https://github.com/dantech2000/refresh/commit/23d902ecae5c6b48100e10b56cf044440945e0fb))
* **deps:** bump github.com/go-openapi/swag/cmdutils ([#314](https://github.com/dantech2000/refresh/issues/314)) ([cda4d59](https://github.com/dantech2000/refresh/commit/cda4d59970c97907b938a6be526ac2713388442e))
* **deps:** bump github.com/go-openapi/swag/loading from 0.29.1 to 0.29.2 ([#307](https://github.com/dantech2000/refresh/issues/307)) ([7014de5](https://github.com/dantech2000/refresh/commit/7014de5dcda79e5557d90627d9a3201549d0cdd2))
* **deps:** bump github.com/go-openapi/swag/mangling ([#304](https://github.com/dantech2000/refresh/issues/304)) ([bb1c4c4](https://github.com/dantech2000/refresh/commit/bb1c4c42b32fe206182c020725f39e5c07aeb613))
* **deps:** bump github.com/urfave/cli/v3 from 3.11.0 to 3.12.0 ([#311](https://github.com/dantech2000/refresh/issues/311)) ([a975fb6](https://github.com/dantech2000/refresh/commit/a975fb6fc7a6a480ea05c58b8c86b6e882f2f9af))
* **deps:** bump github.com/xo/terminfo from 1.0.0 to 1.2.0 ([#310](https://github.com/dantech2000/refresh/issues/310)) ([1c668ec](https://github.com/dantech2000/refresh/commit/1c668ec8a03b63a3a3b226f8821b3232fc7d2476))
* harden read paths for region sweeps, global flags, and contexts ([#350](https://github.com/dantech2000/refresh/issues/350)) ([90a4a14](https://github.com/dantech2000/refresh/commit/90a4a1472f0e5171927299c42ee9d92daaba3c37))
* **health:** catch PDBs that block node drains in pre-flight ([#318](https://github.com/dantech2000/refresh/issues/318)) ([3a38558](https://github.com/dantech2000/refresh/commit/3a385588ead7c26f1026ce1c80021278f2d020a7))
* **health:** match PDB drain blockers to eviction behavior ([#330](https://github.com/dantech2000/refresh/issues/330)) ([29c4682](https://github.com/dantech2000/refresh/commit/29c4682403a144f2846861f5bc8662913c1dfb38))
* **health:** match the Kubernetes client to the target EKS cluster ([#325](https://github.com/dantech2000/refresh/issues/325)) ([3388fa3](https://github.com/dantech2000/refresh/commit/3388fa34eb601d403947a4aeb8adc47f075af503))
* **health:** tighten drain, scale-down, workload and node readiness checks ([#346](https://github.com/dantech2000/refresh/issues/346)) ([b6a0946](https://github.com/dantech2000/refresh/commit/b6a0946cabce6a08f43f71881549e3f35db2f586))
* keep stdout to one document with -o json/yaml (REF-162) ([#340](https://github.com/dantech2000/refresh/issues/340)) ([acff312](https://github.com/dantech2000/refresh/commit/acff312d03251d034d2c03ae67a91a00e1177ca7))
* **monitoring:** handle cancelled and interrupted nodegroup updates ([#321](https://github.com/dantech2000/refresh/issues/321)) ([88d54e9](https://github.com/dantech2000/refresh/commit/88d54e91a2261be834c1f9345b1fff91f511a8dd))
* **nodegroup:** compare AMI freshness against the nodegroup's k8s version ([#319](https://github.com/dantech2000/refresh/issues/319)) ([78c04c6](https://github.com/dantech2000/refresh/commit/78c04c6b9349919fbc3be90beffeca40050e1a26))
* **nodegroup:** fleet discovery errors, unsafe fleet flags, and live panel gating ([#331](https://github.com/dantech2000/refresh/issues/331)) ([1d57892](https://github.com/dantech2000/refresh/commit/1d5789267ff124b0e41088685aa9f57fd2625b01))
* **nodegroup:** harden the update start path and monitor ([#335](https://github.com/dantech2000/refresh/issues/335)) ([0bd03d4](https://github.com/dantech2000/refresh/commit/0bd03d41caf2d5d6922a6c3b63bf93bbe2ef7c15))
* **nodegroup:** tighten update confirmation and health gates ([#345](https://github.com/dantech2000/refresh/issues/345)) ([dc9d648](https://github.com/dantech2000/refresh/commit/dc9d64818ef3c8256efb67894441302f44c6c90e))
* **noderoll:** scale drain reads and keep the live roll panel up through API errors ([#336](https://github.com/dantech2000/refresh/issues/336)) ([c762c9d](https://github.com/dantech2000/refresh/commit/c762c9d118ee136bc6a6119d158883ec5998d848))
* **noderoll:** stop the live roll panel from blocking the EKS update wait ([#320](https://github.com/dantech2000/refresh/issues/320)) ([98e2905](https://github.com/dantech2000/refresh/commit/98e29059301696d1a163a228d6bebb17eb74f80a))
* **output:** decide stderr color from stderr, not stdout ([#342](https://github.com/dantech2000/refresh/issues/342)) ([71b78a1](https://github.com/dantech2000/refresh/commit/71b78a103466c5b413b6a21d63052709d35e2ad6))
* **output:** make -o plain pure TSV across list and describe commands ([#338](https://github.com/dantech2000/refresh/issues/338)) ([c28945f](https://github.com/dantech2000/refresh/commit/c28945f0fea821e0fd45aefa44c87f2666e29030))
* refuse PDB-blocked scale-downs, plus status fan-out and output-tag fixes ([#339](https://github.com/dantech2000/refresh/issues/339)) ([5a87e13](https://github.com/dantech2000/refresh/commit/5a87e1363fe1d446d2646078a35162b466fac50f))
* route fleet regions through runner.Regions and drop duplicate addon --timeout ([#351](https://github.com/dantech2000/refresh/issues/351)) ([75dfdaf](https://github.com/dantech2000/refresh/commit/75dfdaf9344d77bf222f81443df5d3323afb61c9))
* **runner:** unify cluster resolution and prefer exact name matches ([#326](https://github.com/dantech2000/refresh/issues/326)) ([a188452](https://github.com/dantech2000/refresh/commit/a188452bc6b12b2da07ff17fa155fa3096669ace))
* **status:** stop reporting failed or missing data as healthy ([#323](https://github.com/dantech2000/refresh/issues/323)) ([587f491](https://github.com/dantech2000/refresh/commit/587f491e86831d2620380ecc234dd198ba658c70))
* stop EKS_CLUSTER_NAME overriding a positional cluster and --timeout cancelling prompts ([#332](https://github.com/dantech2000/refresh/issues/332)) ([6d2a793](https://github.com/dantech2000/refresh/commit/6d2a793cff99e7974f5f829cba0c474242a87f46))
* surface failed AMI lookups, partial nodegroup lists, and undispatched add-on updates ([#333](https://github.com/dantech2000/refresh/issues/333)) ([112d37d](https://github.com/dantech2000/refresh/commit/112d37df1fb52cfbdf08b77dcd6754c24ec7c151))
* **timeouts:** scope deadlines to the work they bound ([#324](https://github.com/dantech2000/refresh/issues/324)) ([9352d4c](https://github.com/dantech2000/refresh/commit/9352d4c5ccfc6d41bb4d9cacfff0c4aaa2fad46b))
* typed AWS error handling, retry jitter, and AWS-call hygiene ([#337](https://github.com/dantech2000/refresh/issues/337)) ([178ee52](https://github.com/dantech2000/refresh/commit/178ee52835ef07ee156237c5686f8fe0017b32f9))
* **upgrade:** full resume command, TTY-gated roll panel, no prompt with -o json ([#344](https://github.com/dantech2000/refresh/issues/344)) ([6a9c071](https://github.com/dantech2000/refresh/commit/6a9c07132ea74cda3ede5a0fcb22ab51f613ca3a))
* **upgrade:** gate every hop on live readiness and resume interrupted hops ([#327](https://github.com/dantech2000/refresh/issues/327)) ([ab91f58](https://github.com/dantech2000/refresh/commit/ab91f580c1aa82950ad7724cff09ce872684fdaf))
* **upgrade:** refresh cluster insights before readiness and gate nodegroup rolls ([#347](https://github.com/dantech2000/refresh/issues/347)) ([423904b](https://github.com/dantech2000/refresh/commit/423904bc7480ad4894a173278ef8605eecc4b564))
* **upgrade:** remove duplicate isSkippedAddon declaration ([#328](https://github.com/dantech2000/refresh/issues/328)) ([bcf2322](https://github.com/dantech2000/refresh/commit/bcf2322201280bb47aa90df88b629f3ccdac9725))
* **upgrade:** stop steady-state catch-up rolls and poll through wait errors ([#329](https://github.com/dantech2000/refresh/issues/329)) ([98e46fb](https://github.com/dantech2000/refresh/commit/98e46fb5e7b7dfd21c9660b50b0bf2fd3dbe97aa))

## [0.10.3](https://github.com/dantech2000/refresh/compare/v0.10.2...v0.10.3) (2026-09-14)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#266](https://github.com/dantech2000/refresh/issues/266)) ([15520f9](https://github.com/dantech2000/refresh/commit/15520f9c0b5c2a7293903c01f569a729e00f1008))
* **deps:** bump github.com/aws/aws-sdk-go-v2/credentials ([#282](https://github.com/dantech2000/refresh/issues/282)) ([7b74f03](https://github.com/dantech2000/refresh/commit/7b74f038e1fd6fa02e5335b6acbb6eeb537eb048))
* **deps:** bump github.com/aws/aws-sdk-go-v2/internal/v4a ([#284](https://github.com/dantech2000/refresh/issues/284)) ([e1f58ac](https://github.com/dantech2000/refresh/commit/e1f58acff007f7f4afd377ec0a86083714c0a793))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#268](https://github.com/dantech2000/refresh/issues/268)) ([44ba03d](https://github.com/dantech2000/refresh/commit/44ba03de8f5316558bf9682f566146208b3bc47f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#302](https://github.com/dantech2000/refresh/issues/302)) ([21cebe9](https://github.com/dantech2000/refresh/commit/21cebe9ed07540aa30be73cd24ee0f2826352f3f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#260](https://github.com/dantech2000/refresh/issues/260)) ([d75f50c](https://github.com/dantech2000/refresh/commit/d75f50cf9fd51bc123e7489fe1ae1833ff7a69b8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#301](https://github.com/dantech2000/refresh/issues/301)) ([db0cade](https://github.com/dantech2000/refresh/commit/db0cadef1052219ad51967c8f784347390117c06))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#265](https://github.com/dantech2000/refresh/issues/265)) ([84299bd](https://github.com/dantech2000/refresh/commit/84299bd3f93b9a59ebc1ad5320b2fa28bfba02be))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#290](https://github.com/dantech2000/refresh/issues/290)) ([95181ee](https://github.com/dantech2000/refresh/commit/95181eeaaf99e076e4ca1164432cfcaef2acb03d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#261](https://github.com/dantech2000/refresh/issues/261)) ([2f89440](https://github.com/dantech2000/refresh/commit/2f89440a276f950d553e91d038c88a63d5b8ed5e))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#294](https://github.com/dantech2000/refresh/issues/294)) ([7af30b9](https://github.com/dantech2000/refresh/commit/7af30b9e9b1b87a15c22bcfc7f015a163ad85b12))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#267](https://github.com/dantech2000/refresh/issues/267)) ([e038407](https://github.com/dantech2000/refresh/commit/e038407904dec6267651b1c0a25ab1689a743866))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#276](https://github.com/dantech2000/refresh/issues/276)) ([a156e1b](https://github.com/dantech2000/refresh/commit/a156e1b47485a369649ba028f53b2cc18ceb980b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/presigned-url ([#293](https://github.com/dantech2000/refresh/issues/293)) ([86d77fa](https://github.com/dantech2000/refresh/commit/86d77fa93a98b116ef12e9295108182d6b789791))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#262](https://github.com/dantech2000/refresh/issues/262)) ([79b22e7](https://github.com/dantech2000/refresh/commit/79b22e7e6a60272a70761e8befb39e94ddf93de8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#287](https://github.com/dantech2000/refresh/issues/287)) ([0b2c4ae](https://github.com/dantech2000/refresh/commit/0b2c4ae94a21fedc0554365638d29713e2743c7c))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#273](https://github.com/dantech2000/refresh/issues/273)) ([14caa62](https://github.com/dantech2000/refresh/commit/14caa628d49ea0114dbb9e4e6d3c7516a12aaaa4))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#292](https://github.com/dantech2000/refresh/issues/292)) ([8bf5338](https://github.com/dantech2000/refresh/commit/8bf5338ea22b7fe60888959f81345dc3d09ecf74))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#289](https://github.com/dantech2000/refresh/issues/289)) ([e108acd](https://github.com/dantech2000/refresh/commit/e108acde997f7b368fa9ae676c2234f9cc8090f1))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#264](https://github.com/dantech2000/refresh/issues/264)) ([ded1acf](https://github.com/dantech2000/refresh/commit/ded1acfe0ee5b2ae4863557bf05695e56b94d5d0))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#298](https://github.com/dantech2000/refresh/issues/298)) ([897556e](https://github.com/dantech2000/refresh/commit/897556ed9b1f84f02807ac3072d769ea210c765f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#300](https://github.com/dantech2000/refresh/issues/300)) ([66f24a3](https://github.com/dantech2000/refresh/commit/66f24a3a9c8551807e24e12713d7b2dd04dce521))
* **deps:** bump github.com/go-openapi/jsonpointer from 1.0.0 to 1.0.1 ([#271](https://github.com/dantech2000/refresh/issues/271)) ([d884228](https://github.com/dantech2000/refresh/commit/d8842286988b2c0170b9f89a7065f7911f59eec7))
* **deps:** bump github.com/go-openapi/swag/fileutils ([#295](https://github.com/dantech2000/refresh/issues/295)) ([54f21db](https://github.com/dantech2000/refresh/commit/54f21dbe275c30671607dbc8bf67e18edda4ec09))
* **deps:** bump github.com/go-openapi/swag/netutils ([#277](https://github.com/dantech2000/refresh/issues/277)) ([e0a23ce](https://github.com/dantech2000/refresh/commit/e0a23ceca83fa06255eac50b857c499d3c93a53e))
* **deps:** bump github.com/go-openapi/swag/pools from 0.29.1 to 0.29.2 ([#278](https://github.com/dantech2000/refresh/issues/278)) ([551e4ad](https://github.com/dantech2000/refresh/commit/551e4ad5ecddbbd3f94db3d3b1746ca5301db785))
* **deps:** bump github.com/go-openapi/swag/stringutils ([#296](https://github.com/dantech2000/refresh/issues/296)) ([6f3714c](https://github.com/dantech2000/refresh/commit/6f3714c88f441bc186763a9d075920784ba26d10))
* **deps:** bump github.com/go-openapi/swag/yamlutils ([#283](https://github.com/dantech2000/refresh/issues/283)) ([7194bc9](https://github.com/dantech2000/refresh/commit/7194bc9413f3150a19c90357fbfd87ab5991da8b))
* **deps:** bump github.com/mattn/go-runewidth from 0.0.28 to 0.0.29 ([#274](https://github.com/dantech2000/refresh/issues/274)) ([b200000](https://github.com/dantech2000/refresh/commit/b2000005ad30b7f9ebdecf8b035787e3d4c93b1f))
* **deps:** bump golang.org/x/net from 0.58.0 to 0.59.0 ([#279](https://github.com/dantech2000/refresh/issues/279)) ([8fefcb6](https://github.com/dantech2000/refresh/commit/8fefcb6469c7deed35b79233269a60bc9dd876d7))
* **deps:** bump golang.org/x/oauth2 from 0.36.0 to 0.37.0 ([#285](https://github.com/dantech2000/refresh/issues/285)) ([7b7ce4b](https://github.com/dantech2000/refresh/commit/7b7ce4be696d0e0b904db092435bfdaf5c16c2eb))
* **deps:** bump golang.org/x/time from 0.15.0 to 0.16.0 ([#280](https://github.com/dantech2000/refresh/issues/280)) ([ecfcb86](https://github.com/dantech2000/refresh/commit/ecfcb86ce3e4d947caffcd7a0616ffe1a9d6dfb6))
* **release:** emit postflight_steps in the Homebrew cask ([#257](https://github.com/dantech2000/refresh/issues/257)) ([281ddc2](https://github.com/dantech2000/refresh/commit/281ddc205f1f7be9be7c9cdf380aaed15d9171cb))
* **security:** harden pagination, HTTP reads, and concurrency bounds ([#254](https://github.com/dantech2000/refresh/issues/254)) ([5bb22f0](https://github.com/dantech2000/refresh/commit/5bb22f07b8628956bc9488b8a1e74bc797882cbc))
* **update:** decouple --force from health-gate skip; refuse 'latest' addon when k8s unknown ([#256](https://github.com/dantech2000/refresh/issues/256)) ([b828784](https://github.com/dantech2000/refresh/commit/b828784c92f5dee59f464ddaa15645a03069767c))

## [0.10.2](https://github.com/dantech2000/refresh/compare/v0.10.1...v0.10.2) (2026-08-31)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/credentials ([#239](https://github.com/dantech2000/refresh/issues/239)) ([9eb6b97](https://github.com/dantech2000/refresh/commit/9eb6b97db137e9067a12bac13f6e5a16418a1d71))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#250](https://github.com/dantech2000/refresh/issues/250)) ([4cf35e8](https://github.com/dantech2000/refresh/commit/4cf35e8341f84b3e55fda7dadc60b6affb37f4e8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#252](https://github.com/dantech2000/refresh/issues/252)) ([883286e](https://github.com/dantech2000/refresh/commit/883286e902ddfe11ad42579da22715f224a827b8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#241](https://github.com/dantech2000/refresh/issues/241)) ([87e6174](https://github.com/dantech2000/refresh/commit/87e61748ec9e6a67d78ee4e626c3a152eb8cdb45))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#237](https://github.com/dantech2000/refresh/issues/237)) ([bdfb021](https://github.com/dantech2000/refresh/commit/bdfb021b429fc340b47b4dcc2ce7781d6c80e81d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#223](https://github.com/dantech2000/refresh/issues/223)) ([84dd5f6](https://github.com/dantech2000/refresh/commit/84dd5f604787c7943f4c89574a6615f9479a9c17))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#233](https://github.com/dantech2000/refresh/issues/233)) ([ffef94b](https://github.com/dantech2000/refresh/commit/ffef94b55343a6e1ec0d5c592ac7620ce3be8645))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#236](https://github.com/dantech2000/refresh/issues/236)) ([e0b9a69](https://github.com/dantech2000/refresh/commit/e0b9a69b44336a87430ef07e09f7b4bebfb047b8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#231](https://github.com/dantech2000/refresh/issues/231)) ([322c7f6](https://github.com/dantech2000/refresh/commit/322c7f664945f237aa56beae55f905ce96bf69a5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#247](https://github.com/dantech2000/refresh/issues/247)) ([6e4987b](https://github.com/dantech2000/refresh/commit/6e4987b1268feb3e49a49f2fe6e7060675b74366))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#221](https://github.com/dantech2000/refresh/issues/221)) ([9a6a67b](https://github.com/dantech2000/refresh/commit/9a6a67b551cd66e54de9975418772e8a00917bc6))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#232](https://github.com/dantech2000/refresh/issues/232)) ([c993621](https://github.com/dantech2000/refresh/commit/c993621036d4e16c7ba3a40e1cc855a63ff19db0))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#238](https://github.com/dantech2000/refresh/issues/238)) ([3ac722b](https://github.com/dantech2000/refresh/commit/3ac722bdecd7457ad0b9dd1948d5e750e4431547))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#243](https://github.com/dantech2000/refresh/issues/243)) ([42c257f](https://github.com/dantech2000/refresh/commit/42c257f3ad9b6827fcb037208fb67be1ecc2131e))
* **deps:** bump github.com/go-openapi/jsonreference from 1.0.0 to 1.0.1 ([#225](https://github.com/dantech2000/refresh/issues/225)) ([8d5c72c](https://github.com/dantech2000/refresh/commit/8d5c72c71fb9ebeb05cff343cd4c68df025ac25c))
* **deps:** bump github.com/go-openapi/swag from 0.28.0 to 0.29.1 ([#224](https://github.com/dantech2000/refresh/issues/224)) ([1ca5f93](https://github.com/dantech2000/refresh/commit/1ca5f93e99abe8688c378e7a94d536010ab37ad1))
* **deps:** bump github.com/go-openapi/swag/pools from 0.29.0 to 0.29.1 ([#222](https://github.com/dantech2000/refresh/issues/222)) ([3b80435](https://github.com/dantech2000/refresh/commit/3b80435cd39efb59c8b1814b590c0a0a7fe58b90))
* **deps:** bump github.com/mattn/go-runewidth from 0.0.27 to 0.0.28 ([#248](https://github.com/dantech2000/refresh/issues/248)) ([dc3a459](https://github.com/dantech2000/refresh/commit/dc3a45930b4739bda53d0a8498d42edad4ffa541))
* **deps:** bump github.com/urfave/cli/v3 from 3.10.1 to 3.11.0 ([#245](https://github.com/dantech2000/refresh/issues/245)) ([19a51e2](https://github.com/dantech2000/refresh/commit/19a51e22fb1bcabe94dd62de6fef7dce5be50e4f))
* **deps:** bump k8s.io/metrics from 0.36.4 to 0.37.0 ([#234](https://github.com/dantech2000/refresh/issues/234)) ([88d1f2e](https://github.com/dantech2000/refresh/commit/88d1f2e37f780696e8c147b3a666257245cdd68c))

## [0.10.1](https://github.com/dantech2000/refresh/compare/v0.10.0...v0.10.1) (2026-08-26)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2 from 1.43.4 to 1.43.5 ([#167](https://github.com/dantech2000/refresh/issues/167)) ([bae87fc](https://github.com/dantech2000/refresh/commit/bae87fcbe8f72b0f1c35c48b81fc3060f10afedc))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#191](https://github.com/dantech2000/refresh/issues/191)) ([ab7eacb](https://github.com/dantech2000/refresh/commit/ab7eacbafbf476dd50580bd093fb4f8fd61874d9))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#207](https://github.com/dantech2000/refresh/issues/207)) ([10fb562](https://github.com/dantech2000/refresh/commit/10fb5624681c2555cbc5f90f3c88c2e8d6b7f9f8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#189](https://github.com/dantech2000/refresh/issues/189)) ([d4cb7b6](https://github.com/dantech2000/refresh/commit/d4cb7b6fb35e14d42e2cb9e468b3cc73096fdc58))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#200](https://github.com/dantech2000/refresh/issues/200)) ([0aa1eb8](https://github.com/dantech2000/refresh/commit/0aa1eb821cab9ca107951724ffbacd7cf5d52b93))
* **deps:** bump github.com/aws/aws-sdk-go-v2/internal/v4a ([#170](https://github.com/dantech2000/refresh/issues/170)) ([be51b45](https://github.com/dantech2000/refresh/commit/be51b455d0bfa4e6991ddf4a5e0fa094d804fa4a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#184](https://github.com/dantech2000/refresh/issues/184)) ([bf59bce](https://github.com/dantech2000/refresh/commit/bf59bce2554578e65b45fff8fc8c7f48d044e972))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#193](https://github.com/dantech2000/refresh/issues/193)) ([c7063b5](https://github.com/dantech2000/refresh/commit/c7063b5a27970114e08769e0d640174d3b9b1b35))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#182](https://github.com/dantech2000/refresh/issues/182)) ([5af8128](https://github.com/dantech2000/refresh/commit/5af8128ce310f2de0647d0a06bdc7ce7e619bc7c))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#219](https://github.com/dantech2000/refresh/issues/219)) ([d6ab49b](https://github.com/dantech2000/refresh/commit/d6ab49b6b48a142bf8193e52f259e6b6999daff4))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#175](https://github.com/dantech2000/refresh/issues/175)) ([ccb7029](https://github.com/dantech2000/refresh/commit/ccb7029061d53a8d1af1d16892fa1148c72e498d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#205](https://github.com/dantech2000/refresh/issues/205)) ([efc4677](https://github.com/dantech2000/refresh/commit/efc467705a1022f7c31b5f0000535d73483a17a4))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#188](https://github.com/dantech2000/refresh/issues/188)) ([285c55b](https://github.com/dantech2000/refresh/commit/285c55b41a397dff785b021fcd43cbd11756f368))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#195](https://github.com/dantech2000/refresh/issues/195)) ([5b6c42f](https://github.com/dantech2000/refresh/commit/5b6c42f6361a0004f2b48de9ede5fc4abf26ff32))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#180](https://github.com/dantech2000/refresh/issues/180)) ([0257a1b](https://github.com/dantech2000/refresh/commit/0257a1b51f59852dde7a1675452146733affc45d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#218](https://github.com/dantech2000/refresh/issues/218)) ([fb9d6ec](https://github.com/dantech2000/refresh/commit/fb9d6ece3180d4e95c7fbc2519aca563d4986943))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/presigned-url ([#166](https://github.com/dantech2000/refresh/issues/166)) ([63f0bd4](https://github.com/dantech2000/refresh/commit/63f0bd4ec320ef12306337631423a963fa2c19f5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#172](https://github.com/dantech2000/refresh/issues/172)) ([8422252](https://github.com/dantech2000/refresh/commit/842225250e5b189745b903b35e6aaef4123f48d0))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#211](https://github.com/dantech2000/refresh/issues/211)) ([c304b05](https://github.com/dantech2000/refresh/commit/c304b050bd2de5544e763afb33b5ebfbf57d0b53))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#174](https://github.com/dantech2000/refresh/issues/174)) ([01f978f](https://github.com/dantech2000/refresh/commit/01f978f2ceee17d8964381ab9b9948ef6538568a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#208](https://github.com/dantech2000/refresh/issues/208)) ([2922167](https://github.com/dantech2000/refresh/commit/2922167f9024cfa8a58cf1165bde402680e50cd1))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#183](https://github.com/dantech2000/refresh/issues/183)) ([9a9b306](https://github.com/dantech2000/refresh/commit/9a9b3064181e728ed83f5cee7087d3dba5dcfed8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#213](https://github.com/dantech2000/refresh/issues/213)) ([1412be2](https://github.com/dantech2000/refresh/commit/1412be2cadbf15b7981e14cb4ccaf8774edfc259))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#173](https://github.com/dantech2000/refresh/issues/173)) ([0a99a46](https://github.com/dantech2000/refresh/commit/0a99a46cf8e5a013da65f1f13cbf4f34ca4e54b6))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#203](https://github.com/dantech2000/refresh/issues/203)) ([6c1b9c7](https://github.com/dantech2000/refresh/commit/6c1b9c7afe256e874d0d77fb8bed459529c1fd20))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#186](https://github.com/dantech2000/refresh/issues/186)) ([7cfd35b](https://github.com/dantech2000/refresh/commit/7cfd35bf321f65a1b4f55fd913cd998b95daad5b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#202](https://github.com/dantech2000/refresh/issues/202)) ([c58c343](https://github.com/dantech2000/refresh/commit/c58c343317055b7d6d6e96d10e1514ee72b0ddfb))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#168](https://github.com/dantech2000/refresh/issues/168)) ([dc2d30d](https://github.com/dantech2000/refresh/commit/dc2d30d3d20ac7e24546055f0eef65ae017e722f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#192](https://github.com/dantech2000/refresh/issues/192)) ([0c6c1a4](https://github.com/dantech2000/refresh/commit/0c6c1a4a1881cd321fd70cba43aac92323be5e24))
* **deps:** bump github.com/aws/smithy-go from 1.27.6 to 1.27.7 ([#176](https://github.com/dantech2000/refresh/issues/176)) ([56dee20](https://github.com/dantech2000/refresh/commit/56dee202548d6ad9e38867082847718837dc1387))
* **deps:** bump github.com/fxamacker/cbor/v2 from 2.9.2 to 2.9.3 ([#199](https://github.com/dantech2000/refresh/issues/199)) ([f17471f](https://github.com/dantech2000/refresh/commit/f17471f8a6f60d81e0374563c6d2b2edd27bda4d))
* **deps:** bump github.com/go-openapi/swag/cmdutils ([#210](https://github.com/dantech2000/refresh/issues/210)) ([5cabe0d](https://github.com/dantech2000/refresh/commit/5cabe0db961b975e732ab3b5f98b65fbf27d2c4c))
* **deps:** bump github.com/go-openapi/swag/fileutils ([#216](https://github.com/dantech2000/refresh/issues/216)) ([63d445c](https://github.com/dantech2000/refresh/commit/63d445c6cbd8b27f6dcfe600f73d19228c63f72c))
* **deps:** bump github.com/go-openapi/swag/loading from 0.28.0 to 0.29.0 ([#197](https://github.com/dantech2000/refresh/issues/197)) ([98ff045](https://github.com/dantech2000/refresh/commit/98ff045af1155878b699f81421da1f2aa8f7fd76))
* **deps:** bump github.com/go-openapi/swag/mangling ([#214](https://github.com/dantech2000/refresh/issues/214)) ([f4b0bc1](https://github.com/dantech2000/refresh/commit/f4b0bc190f8a386613cc7d6ea3e1380374489b76))
* **deps:** bump github.com/go-openapi/swag/netutils ([#196](https://github.com/dantech2000/refresh/issues/196)) ([eccbab9](https://github.com/dantech2000/refresh/commit/eccbab9e39b6eb4d69c7d1b838995899dc06b458))
* **deps:** bump github.com/go-openapi/swag/typeutils ([#201](https://github.com/dantech2000/refresh/issues/201)) ([c063b1e](https://github.com/dantech2000/refresh/commit/c063b1ed439a3c1708eaab9287b0bf0d64b74a7b))
* **deps:** bump github.com/xo/terminfo ([#181](https://github.com/dantech2000/refresh/issues/181)) ([e88e9ea](https://github.com/dantech2000/refresh/commit/e88e9ea84bcf90ad14909f993f17e86d4cd29228))
* **deps:** bump golang.org/x/net from 0.57.0 to 0.58.0 ([#185](https://github.com/dantech2000/refresh/issues/185)) ([7089347](https://github.com/dantech2000/refresh/commit/7089347bf2397ec7c4ef7d288f7c40bfb00a8c43))
* **deps:** bump golang.org/x/text from 0.40.0 to 0.41.0 ([#171](https://github.com/dantech2000/refresh/issues/171)) ([7e8b955](https://github.com/dantech2000/refresh/commit/7e8b955990b53b05036712ebb4446316d20bcbef))
* **deps:** bump google.golang.org/protobuf ([#179](https://github.com/dantech2000/refresh/issues/179)) ([e94c80f](https://github.com/dantech2000/refresh/commit/e94c80f158f52a347b2dfb6dad4ba8ba0287af79))
* **deps:** bump k8s.io/client-go from 0.36.3 to 0.36.4 ([#198](https://github.com/dantech2000/refresh/issues/198)) ([7846e2c](https://github.com/dantech2000/refresh/commit/7846e2c41e16d85b32e811080bc6824fa2fe4104))
* **deps:** bump k8s.io/metrics from 0.36.3 to 0.36.4 ([#209](https://github.com/dantech2000/refresh/issues/209)) ([6b56f51](https://github.com/dantech2000/refresh/commit/6b56f513098db74a94d34249fa90afb9a952566e))

## [0.10.0](https://github.com/dantech2000/refresh/compare/v0.9.4...v0.10.0) (2026-08-12)


### Features

* **noderoll:** watch-backed live roll view via informers ([#163](https://github.com/dantech2000/refresh/issues/163)) ([ed7657a](https://github.com/dantech2000/refresh/commit/ed7657aea8780e64c1db3371fbdb997b96c0c702))


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2 from 1.43.2 to 1.43.4 ([#154](https://github.com/dantech2000/refresh/issues/154)) ([1b3b563](https://github.com/dantech2000/refresh/commit/1b3b563c2092aa4762b0ec6ceae3e758fa7c2e64))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#145](https://github.com/dantech2000/refresh/issues/145)) ([258217e](https://github.com/dantech2000/refresh/commit/258217e9315186d6bb632edab33a6f537d03af1b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#150](https://github.com/dantech2000/refresh/issues/150)) ([561b18f](https://github.com/dantech2000/refresh/commit/561b18f40e13465bb88553f1058da96c5e895d93))
* **deps:** bump github.com/aws/aws-sdk-go-v2/internal/v4a ([#156](https://github.com/dantech2000/refresh/issues/156)) ([6c0908f](https://github.com/dantech2000/refresh/commit/6c0908fbf9c44ff37f30e36fd9d79a872e6ba2f1))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#158](https://github.com/dantech2000/refresh/issues/158)) ([4b9add6](https://github.com/dantech2000/refresh/commit/4b9add6307a1020462ba627825d420f566c73b13))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#148](https://github.com/dantech2000/refresh/issues/148)) ([16cbbab](https://github.com/dantech2000/refresh/commit/16cbbab97335686f02d824ab375b443beda34131))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#144](https://github.com/dantech2000/refresh/issues/144)) ([0545892](https://github.com/dantech2000/refresh/commit/0545892eb456ebae923d51ba54b79a2ef09d6599))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#151](https://github.com/dantech2000/refresh/issues/151)) ([8d6a74e](https://github.com/dantech2000/refresh/commit/8d6a74e1e31f6bc85cd6cc829de2dcff5f5336b8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#155](https://github.com/dantech2000/refresh/issues/155)) ([8f8049a](https://github.com/dantech2000/refresh/commit/8f8049a169f505e657d2b70da0c3c51c9c1e69e2))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/presigned-url ([#160](https://github.com/dantech2000/refresh/issues/160)) ([791a206](https://github.com/dantech2000/refresh/commit/791a206c632ad348a45472c6dfd64f4b71ae14a8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#162](https://github.com/dantech2000/refresh/issues/162)) ([cb33da2](https://github.com/dantech2000/refresh/commit/cb33da2a95db21600da7c25e7ac5a018e0c229ed))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#147](https://github.com/dantech2000/refresh/issues/147)) ([f52c6d7](https://github.com/dantech2000/refresh/commit/f52c6d70d116376fb156a3156a7b8c26e5ba8ac1))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#161](https://github.com/dantech2000/refresh/issues/161)) ([451ede2](https://github.com/dantech2000/refresh/commit/451ede28cd5330569107853fa447e976abc9f7d5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#142](https://github.com/dantech2000/refresh/issues/142)) ([4835ed9](https://github.com/dantech2000/refresh/commit/4835ed9e43d776c58a492c00e5818ad184a2a58c))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#159](https://github.com/dantech2000/refresh/issues/159)) ([c1c095f](https://github.com/dantech2000/refresh/commit/c1c095f2ad4abc915adeb98967034433408d7bfc))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#157](https://github.com/dantech2000/refresh/issues/157)) ([93ab088](https://github.com/dantech2000/refresh/commit/93ab08868cf7ab70432296b80562357a883f9bbd))
* **deps:** bump github.com/aws/smithy-go from 1.27.5 to 1.27.6 ([#153](https://github.com/dantech2000/refresh/issues/153)) ([55b008e](https://github.com/dantech2000/refresh/commit/55b008e127616957e0683cfd2dc1532dcd2ed7a4))

## [0.9.4](https://github.com/dantech2000/refresh/compare/v0.9.3...v0.9.4) (2026-08-04)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#110](https://github.com/dantech2000/refresh/issues/110)) ([1744112](https://github.com/dantech2000/refresh/commit/17441122b8a38d23f117715b7ff9601c67148c44))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#109](https://github.com/dantech2000/refresh/issues/109)) ([dc72b4c](https://github.com/dantech2000/refresh/commit/dc72b4c3eca04379f796fca0be69a25bb1d136be))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#136](https://github.com/dantech2000/refresh/issues/136)) ([45895bb](https://github.com/dantech2000/refresh/commit/45895bb0492bb0ee36f8baf2bf9bbec84fa17bac))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#128](https://github.com/dantech2000/refresh/issues/128)) ([0363cee](https://github.com/dantech2000/refresh/commit/0363ceecaa422278beb3fd6faa103bd41c0a5aee))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#127](https://github.com/dantech2000/refresh/issues/127)) ([2b6dc5a](https://github.com/dantech2000/refresh/commit/2b6dc5aed1438091962e7d855dbb58dc374f8a35))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#130](https://github.com/dantech2000/refresh/issues/130)) ([3819698](https://github.com/dantech2000/refresh/commit/38196987b7a58904f6957e77646dc373ba39da23))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#138](https://github.com/dantech2000/refresh/issues/138)) ([7eaa902](https://github.com/dantech2000/refresh/commit/7eaa902a8e1476e11f0372b3fb402b65441573fd))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#135](https://github.com/dantech2000/refresh/issues/135)) ([57ef00a](https://github.com/dantech2000/refresh/commit/57ef00a06d0cbbc76dc5b3d32eea6bb3710c0eb4))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#113](https://github.com/dantech2000/refresh/issues/113)) ([92b3178](https://github.com/dantech2000/refresh/commit/92b31784468d563cdd7385e2b3ff49695ffef8b0))
* **deps:** bump github.com/cpuguy83/go-md2man/v2 from 2.0.5 to 2.0.7 ([#115](https://github.com/dantech2000/refresh/issues/115)) ([0006ab9](https://github.com/dantech2000/refresh/commit/0006ab98e4c6c73de38da6da632fc6dfb52c1191))
* **deps:** bump github.com/fxamacker/cbor/v2 from 2.9.0 to 2.9.2 ([#107](https://github.com/dantech2000/refresh/issues/107)) ([332e829](https://github.com/dantech2000/refresh/commit/332e829ac552821927ea5497acb9c3258c93dc22))
* **deps:** bump github.com/go-logr/logr from 1.4.3 to 1.4.4 ([#108](https://github.com/dantech2000/refresh/issues/108)) ([57678bb](https://github.com/dantech2000/refresh/commit/57678bb4fe08eb6b0d1052112370142cec60f791))
* **deps:** bump github.com/go-openapi/jsonreference from 0.20.2 to 1.0.0 ([#118](https://github.com/dantech2000/refresh/issues/118)) ([1339a8c](https://github.com/dantech2000/refresh/commit/1339a8c5d86809e5347e88030954f17478ba07b0))
* **deps:** bump github.com/google/gnostic-models from 0.7.0 to 0.7.1 ([#129](https://github.com/dantech2000/refresh/issues/129)) ([26dac65](https://github.com/dantech2000/refresh/commit/26dac6563d6524b69f54625c1c588dd01217d75c))
* **deps:** bump github.com/mattn/go-runewidth from 0.0.24 to 0.0.27 ([#132](https://github.com/dantech2000/refresh/issues/132)) ([3c90d89](https://github.com/dantech2000/refresh/commit/3c90d89227b55a590f06fccda693df904db6f602))
* **deps:** bump github.com/spf13/pflag from 1.0.9 to 1.0.10 ([#121](https://github.com/dantech2000/refresh/issues/121)) ([3d3bb0a](https://github.com/dantech2000/refresh/commit/3d3bb0a74c31d99bd924b928c28a858aef6d0571))
* **deps:** bump go.yaml.in/yaml/v2 from 2.4.3 to 2.4.4 ([#134](https://github.com/dantech2000/refresh/issues/134)) ([cdcb62e](https://github.com/dantech2000/refresh/commit/cdcb62e8d17c74d21d9e086e7565e1f275dc168e))
* **deps:** bump go.yaml.in/yaml/v3 from 3.0.4 to 3.0.5 ([#131](https://github.com/dantech2000/refresh/issues/131)) ([39f8d6f](https://github.com/dantech2000/refresh/commit/39f8d6f793656e1d9d590280af955a2cdec7f2de))
* **deps:** bump golang.org/x/net from 0.56.0 to 0.57.0 ([#116](https://github.com/dantech2000/refresh/issues/116)) ([41039a8](https://github.com/dantech2000/refresh/commit/41039a8be0241a12936992a9875ba6b3f6f56f8f))
* **deps:** bump golang.org/x/oauth2 from 0.34.0 to 0.36.0 ([#137](https://github.com/dantech2000/refresh/issues/137)) ([6488c97](https://github.com/dantech2000/refresh/commit/6488c97e62b1410460768b90f6134d6dcc86c0bd))
* **deps:** bump golang.org/x/term from 0.44.0 to 0.45.0 ([#117](https://github.com/dantech2000/refresh/issues/117)) ([bdc1c16](https://github.com/dantech2000/refresh/commit/bdc1c16a0e270f217eaf4fade16bdbd113a16c7c))
* **deps:** bump golang.org/x/time from 0.14.0 to 0.15.0 ([#111](https://github.com/dantech2000/refresh/issues/111)) ([0a2b0ca](https://github.com/dantech2000/refresh/commit/0a2b0ca0ec3093f9c29c3a626bb8a4e867d9e6e8))
* **deps:** bump sigs.k8s.io/structured-merge-diff/v6 from 6.3.3 to 6.4.2 ([#126](https://github.com/dantech2000/refresh/issues/126)) ([66b954c](https://github.com/dantech2000/refresh/commit/66b954c195a236bb8323c02f49a947c3f6a96283))

## [0.9.3](https://github.com/dantech2000/refresh/compare/v0.9.2...v0.9.3) (2026-07-29)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/credentials ([#103](https://github.com/dantech2000/refresh/issues/103)) ([1e2ea35](https://github.com/dantech2000/refresh/commit/1e2ea35ed466fe53ed51aa9658c7a63c9f27a475))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#96](https://github.com/dantech2000/refresh/issues/96)) ([829ecb3](https://github.com/dantech2000/refresh/commit/829ecb33e739aa08cd98c0d8392ef3f1d5c5d567))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#102](https://github.com/dantech2000/refresh/issues/102)) ([112d6cc](https://github.com/dantech2000/refresh/commit/112d6ccf696553e9959bfe8e3f0bf688ba939940))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#98](https://github.com/dantech2000/refresh/issues/98)) ([a58f8bb](https://github.com/dantech2000/refresh/commit/a58f8bb34c47c9c0fb87d38c8c2a0b2d70279782))
* **deps:** bump github.com/go-openapi/swag from 0.23.0 to 0.28.0 ([#83](https://github.com/dantech2000/refresh/issues/83)) ([cb6f304](https://github.com/dantech2000/refresh/commit/cb6f30492458b3dc2c30806294ed0100e4eb5d57))

## [0.9.2](https://github.com/dantech2000/refresh/compare/v0.9.1...v0.9.2) (2026-07-29)


### Bug Fixes

* **ci:** merge Dependabot PRs as a user so CI and releases trigger ([#99](https://github.com/dantech2000/refresh/issues/99)) ([7ca269a](https://github.com/dantech2000/refresh/commit/7ca269ac2ea9f7b3c335d0abcd4f2cc723028eef))
* **ci:** merge Dependabot PRs directly instead of via --auto ([#104](https://github.com/dantech2000/refresh/issues/104)) ([c70031e](https://github.com/dantech2000/refresh/commit/c70031ee76d50724ffb1bf6aac04f735afcf7247))
* **deps:** bump atomicgo.dev/keyboard from 0.2.9 to 0.2.10 ([#82](https://github.com/dantech2000/refresh/issues/82)) ([a291390](https://github.com/dantech2000/refresh/commit/a29139017a85884bdf5ec17178ab343f1ae89342))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#94](https://github.com/dantech2000/refresh/issues/94)) ([b1f1a04](https://github.com/dantech2000/refresh/commit/b1f1a04d651d45a12bb539ba6e7ed5b357b13067))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#95](https://github.com/dantech2000/refresh/issues/95)) ([8c1d70f](https://github.com/dantech2000/refresh/commit/8c1d70f689c51b6aa0099f38bdc4ffc84dec8eaf))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#100](https://github.com/dantech2000/refresh/issues/100)) ([c3f4c22](https://github.com/dantech2000/refresh/commit/c3f4c2201c6cfcdb46a458570ae00be4cbb7cb19))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#90](https://github.com/dantech2000/refresh/issues/90)) ([08bcccb](https://github.com/dantech2000/refresh/commit/08bcccbac34b88bbb7566a1a8ecb871e8fe3e35d))
* **deps:** bump github.com/gookit/color from 1.6.0 to 1.6.1 ([#91](https://github.com/dantech2000/refresh/issues/91)) ([992d777](https://github.com/dantech2000/refresh/commit/992d7773946d2e7f69263e90c1dcb3ff40e24e38))
* **deps:** bump github.com/mattn/go-colorable from 0.1.14 to 0.1.15 ([#86](https://github.com/dantech2000/refresh/issues/86)) ([7e98157](https://github.com/dantech2000/refresh/commit/7e981579bc7e9afe62bb473f395ebb5a061819a8))
* **deps:** bump github.com/mattn/go-isatty from 0.0.22 to 0.0.24 ([#81](https://github.com/dantech2000/refresh/issues/81)) ([ca43e47](https://github.com/dantech2000/refresh/commit/ca43e477fd3b7ad73e2c95b16891677762d08d22))
* **deps:** bump github.com/urfave/cli/v3 from 3.9.1 to 3.10.1 ([#80](https://github.com/dantech2000/refresh/issues/80)) ([35a8d30](https://github.com/dantech2000/refresh/commit/35a8d302fea334c5aa3da5610f231c3901b14b53))
* **deps:** bump golang.org/x/sys from 0.46.0 to 0.47.0 ([#88](https://github.com/dantech2000/refresh/issues/88)) ([c3b6242](https://github.com/dantech2000/refresh/commit/c3b62427dd44d40684548cf7f914b0bd67fc9396))
* **deps:** bump k8s.io/metrics from 0.36.2 to 0.36.3 ([#89](https://github.com/dantech2000/refresh/issues/89)) ([3a143a8](https://github.com/dantech2000/refresh/commit/3a143a8862caa4bb25b75e19bd9c66274e2acdf6))

## [0.9.1](https://github.com/dantech2000/refresh/compare/v0.9.0...v0.9.1) (2026-07-29)


### Bug Fixes

* rebuild release binaries against patched x/text and Go 1.26.5 ([30f983f](https://github.com/dantech2000/refresh/commit/30f983fe4178fb6eb06b2c4b36181a4261dcf908))

## [0.9.0](https://github.com/dantech2000/refresh/compare/v0.8.0...v0.9.0) (2026-06-15)


### Features

* **upgrade-check:** themed insight detail view + discoverable insight IDs (REF-149) ([#69](https://github.com/dantech2000/refresh/issues/69)) ([d687760](https://github.com/dantech2000/refresh/commit/d687760b66f37a982cc541ec049a5b8f126c9aff))


### Bug Fixes

* **health:** wire all checks consistently + don't let skipped checks force WARN (REF-148) ([#67](https://github.com/dantech2000/refresh/issues/67)) ([5a3e8d2](https://github.com/dantech2000/refresh/commit/5a3e8d23f3c8825f928b0e7b2f80a6a9a27036e7))

## [0.8.0](https://github.com/dantech2000/refresh/compare/v0.7.0...v0.8.0) (2026-06-15)


### Features

* **cluster:** itemize per-check health results in cluster describe (REF-146) ([#65](https://github.com/dantech2000/refresh/issues/65)) ([685d4b9](https://github.com/dantech2000/refresh/commit/685d4b97e6bb5ae340c675ddf96cf30e35915ce1))
* **health:** control-plane readiness gate from AWS/EKS CloudWatch metrics (REF-140) ([#59](https://github.com/dantech2000/refresh/issues/59)) ([7bc4ddf](https://github.com/dantech2000/refresh/commit/7bc4ddf6635d6142ab4ea7379700b837e840b8ed))
* **health:** EC2 vCPU service-quota headroom pre-flight (REF-144) ([#63](https://github.com/dantech2000/refresh/issues/63)) ([a919564](https://github.com/dantech2000/refresh/commit/a919564736e47fe10c731ab6f709e336defc4f2f))
* **health:** live node CPU+memory drain headroom via metrics-server (REF-142) ([#60](https://github.com/dantech2000/refresh/issues/60)) ([b9ff419](https://github.com/dantech2000/refresh/commit/b9ff419d64f7d4dafd805bbcf8fd3569ce87c0dd))
* **nodegroup:** pre-flight instance-type availability per AZ on scale/update (REF-143) ([#62](https://github.com/dantech2000/refresh/issues/62)) ([9783171](https://github.com/dantech2000/refresh/commit/97831713b943ff56d5dd03cb1a710c7cc448a907))
* **noderoll:** surface live Kubernetes Warning events during a roll (REF-138) ([#61](https://github.com/dantech2000/refresh/issues/61)) ([68eacdf](https://github.com/dantech2000/refresh/commit/68eacdfa6bca9b0004f9bff8749d6b536c3f85d5))


### Bug Fixes

* CLI/help/status UX bugs (REF-129, REF-131, REF-132, REF-133, REF-134) ([#51](https://github.com/dantech2000/refresh/issues/51)) ([b2509a7](https://github.com/dantech2000/refresh/commit/b2509a7acdea7e34293ee8d2d34bd78d2d4fac1e))
* **nodegroup,cluster:** measure real node readiness instead of synthesizing it (REF-130) ([#52](https://github.com/dantech2000/refresh/issues/52)) ([cf9874c](https://github.com/dantech2000/refresh/commit/cf9874cfc831e1717dc613a1564282e709d249e7))

## [0.7.0](https://github.com/dantech2000/refresh/compare/v0.6.0...v0.7.0) (2026-06-13)


### Features

* **cluster:** upgrade-check — EKS Cluster Insights + version-skew readiness (REF-12) ([#31](https://github.com/dantech2000/refresh/issues/31)) ([8783d57](https://github.com/dantech2000/refresh/commit/8783d57cfd44db47ee87bd137053d1c848854c1d))
* **docs:** generate the command/flag reference from the CLI tree (REF-108) ([#43](https://github.com/dantech2000/refresh/issues/43)) ([aa24ca6](https://github.com/dantech2000/refresh/commit/aa24ca6550c61214d7d971609748da59dc7b838d))
* **health:** --kubeconfig flag + connection diagnostics for pre-flight checks (REF-3) ([#32](https://github.com/dantech2000/refresh/issues/32)) ([f7c038a](https://github.com/dantech2000/refresh/commit/f7c038ae0462b3e3517d581b86a484a894a1caa7))
* **nodegroup:** AMI refresh flagship — fleet mode, verification, changelog, unattended, custom-AMI safety (REF-80) ([#33](https://github.com/dantech2000/refresh/issues/33)) ([afaf0b0](https://github.com/dantech2000/refresh/commit/afaf0b07d76e56fe0215d98d5adb14074a3aaf30))
* output redesign + live cluster-roll observability (REF-119) ([#50](https://github.com/dantech2000/refresh/issues/50)) ([c600acc](https://github.com/dantech2000/refresh/commit/c600accd0d7e01e47db786093b7389975f466893))
* signal cancellation + mechanical hygiene (salvage of [#19](https://github.com/dantech2000/refresh/issues/19)/[#21](https://github.com/dantech2000/refresh/issues/21)) ([#25](https://github.com/dantech2000/refresh/issues/25)) ([c140eb1](https://github.com/dantech2000/refresh/commit/c140eb19de2d32cbcffc453efc805a8fbe73fb4e))
* **status:** refresh status — fleet patch posture across clusters/regions (REF-79) ([#30](https://github.com/dantech2000/refresh/issues/30)) ([5260646](https://github.com/dantech2000/refresh/commit/52606460df9eccc3b271dde919f921a943fd5bf0))


### Bug Fixes

* **cli:** consistency & robustness hardening — flags, positional, ctx-cancel, nil-derefs (REF-52) ([#38](https://github.com/dantech2000/refresh/issues/38)) ([15b81f6](https://github.com/dantech2000/refresh/commit/15b81f6f57804d7d4541f0e8c828e6e9817c8415))
* **cli:** output & flag correctness — filters, format validation, global region/profile, yaml keys ([#34](https://github.com/dantech2000/refresh/issues/34)) ([dcbb16d](https://github.com/dantech2000/refresh/commit/dcbb16d6660f1cbcf90c00343fd4e115774a5080))
* harden defensive nil-checks and input validation (REF-115, REF-116, REF-117) ([#47](https://github.com/dantech2000/refresh/issues/47)) ([5a8f8ad](https://github.com/dantech2000/refresh/commit/5a8f8adddd349cfa5525ebe03093f928a79882ba))
* **health:** scoring accuracy — skip exclusion, peak CPU, proxy honesty, std-dev relabel (REF-63) ([#29](https://github.com/dantech2000/refresh/issues/29)) ([4f74a42](https://github.com/dantech2000/refresh/commit/4f74a42fafc00729530eebe092a234d766d45f74))
* **ui:** output/formatting data-integrity — TSV escaping, zero-time, display-cell widths (REF-62) ([#37](https://github.com/dantech2000/refresh/issues/37)) ([115a1f3](https://github.com/dantech2000/refresh/commit/115a1f37842159d4a7a796f2fdec0561550a7742))
* **upgrade:** attach to in-flight addon updates on resume (REF-114) ([#48](https://github.com/dantech2000/refresh/issues/48)) ([b3afb7d](https://github.com/dantech2000/refresh/commit/b3afb7d276ba9e0641d6e6fded3fddafe7546c9a))


### Code Refactoring

* consolidate duplicated table, timing, filter, pagination, and badge code ([#22](https://github.com/dantech2000/refresh/issues/22)) ([c9342bb](https://github.com/dantech2000/refresh/commit/c9342bb7db081763c2af46facabd67c487283e36))
* logging, addon factory, batched ASG, scale-dry-run PDBs, split actions.go (REF-37, 39, 50, 4, 38) ([#39](https://github.com/dantech2000/refresh/issues/39)) ([f6892d2](https://github.com/dantech2000/refresh/commit/f6892d2ac654bfb9af5ed4404c8efb21b06f707e))
* migrate CLI from urfave/cli v2 to v3 (REF-11) ([#27](https://github.com/dantech2000/refresh/issues/27)) ([46bf877](https://github.com/dantech2000/refresh/commit/46bf8776ca08a4f6e1e558fb5a415bd8cffefe4e))
* **trim:** refocus as the EKS upgrade companion — remove diff, cost, utilization, workload pdbs (REF-78) ([#36](https://github.com/dantech2000/refresh/issues/36)) ([6eb6feb](https://github.com/dantech2000/refresh/commit/6eb6feb7e56270eba41867c63a64725ee3bb2b0f))

## [0.6.0](https://github.com/dantech2000/refresh/compare/v0.5.12...v0.6.0) (2026-06-06)


### Features

* **version:** support --version/-v flag in addition to version subcommand ([4521b0c](https://github.com/dantech2000/refresh/commit/4521b0c3eccc278eeaa05cf7cd7201ec2f58bf12))


### Bug Fixes

* addon update version positional + addons semaphore ordering ([e2df7f9](https://github.com/dantech2000/refresh/commit/e2df7f9edd7515e954b36a5ad10d3f6367caccd0))
* **clusterview:** tree view preserves cluster status under unknown health ([8e82dec](https://github.com/dantech2000/refresh/commit/8e82dec9487f96b345a384b0884603f3404a26d8))
* multi-region cache collision + addon resolver nil deref ([2407eb8](https://github.com/dantech2000/refresh/commit/2407eb86b2b24ea4d41c67e7636a3e4e6cd68cbe))
* **nodegroup:** --health-only always prints verdict, even under --quiet ([447ffb0](https://github.com/dantech2000/refresh/commit/447ffb08d7828d35a364e1b0b0c2c0fb79b17e76))


### Code Refactoring

* **addon:** adopt runner.SetupAWSStrict and PositionalAt ([a9eb703](https://github.com/dantech2000/refresh/commit/a9eb703ef03ef33126e323f36e014761638d9e06))
* **addons:** dedupe UpdateAll parallel/serial branches ([3da29e8](https://github.com/dantech2000/refresh/commit/3da29e8303379573032483652b6fa55e67300173))
* **cluster:** clean ListAllRegions structure ([edad1a0](https://github.com/dantech2000/refresh/commit/edad1a0f605a3aa1c20afd4b5be2b359250452d8))
* **cluster:** collapse outputClustersTable's multiRegion×showHealth branches ([3a8a35c](https://github.com/dantech2000/refresh/commit/3a8a35c5c3b7f597aa8d93426a498b7d82d77cac))
* **cluster:** consolidate color/status formatters ([52b941e](https://github.com/dantech2000/refresh/commit/52b941e6a4c649125c88ec8c17a5d904db63376c))
* **cluster:** extract diff helpers in analyzeDifferences ([ccad79f](https://github.com/dantech2000/refresh/commit/ccad79f44fab86644e4851fbdd826190def6ba07))
* **cluster:** getClusterSummary drops always-nil error return ([0d67046](https://github.com/dantech2000/refresh/commit/0d6704632d14a9e78512906862ae3ea009bd6b97))
* **cluster:** simplify buildListCacheKey ([552d713](https://github.com/dantech2000/refresh/commit/552d713de8ac6ae7fe2cd26ef32d74ec682f0196))
* **clusterview:** split into color/list/detail/compare files ([241bdaa](https://github.com/dantech2000/refresh/commit/241bdaac8d73ab5a2200d74814bcb445b0c3c3bf))
* **commands:** extract clusterview pkg, finish runner adoption ([cb9e4f3](https://github.com/dantech2000/refresh/commit/cb9e4f3b1f951c31650e3e78886013c2574b4099))
* **commands:** extract runner package for shared CLI primitives ([ea5a2a4](https://github.com/dantech2000/refresh/commit/ea5a2a4e7830a249dc3d78d8e1b99760049c58d7))
* **common:** add Paginate generic and migrate 5 ListX loops ([7866e1d](https://github.com/dantech2000/refresh/commit/7866e1de8543aea1836c1c0d48c2930c556620eb))
* **nodegroup:** adopt runner in runScale and runUpdateAMI ([cb7b97c](https://github.com/dantech2000/refresh/commit/cb7b97cc3187ed718c903966d1d80b1de72b3251))
* **nodegroup:** dedupe CloudWatch utilization collectors ([b77f0c7](https://github.com/dantech2000/refresh/commit/b77f0c7bfd7a4bb9bf51c240410691b55b6d0972))
* **nodegroup:** extract classifyAMI helper ([e8cca82](https://github.com/dantech2000/refresh/commit/e8cca82c6bec797d864bdc747bcda511b328188a))
* **nodegroup:** split runUpdateAMI into pipeline stages ([d2719b5](https://github.com/dantech2000/refresh/commit/d2719b542ae8fbdc93b2c7f5e6c8ea3dae9b0a19))
* P3 dead code + helper consolidations ([ecce9b9](https://github.com/dantech2000/refresh/commit/ecce9b9114b84f020b316a2b55c67d37971f7395))

## [0.5.12](https://github.com/dantech2000/refresh/compare/v0.5.11...v0.5.12) (2026-05-14)


### Bug Fixes

* seed release-please manifest at v0.5.11 (actual latest release) ([43c7bef](https://github.com/dantech2000/refresh/commit/43c7beff30e8a3635aeb3a00a9413ea22834e80d))
* use GH_PAT for release-please PR creation ([4e625a3](https://github.com/dantech2000/refresh/commit/4e625a30c67477897f13e0fa767c2a97e16220e8))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([3264540](https://github.com/dantech2000/refresh/commit/3264540336e61f5ad0f41d1bbcf4f84a5db96d26))
* split internal/commands into focused sub-packages ([bea3204](https://github.com/dantech2000/refresh/commit/bea3204dfd1a4b03f242fa46d78ac37f4230b748))
* split internal/commands into focused sub-packages ([ccf10c2](https://github.com/dantech2000/refresh/commit/ccf10c2e81f8655b990d00e5ce4e647ef25f1324))

## [0.5.1](https://github.com/dantech2000/refresh/compare/v0.5.0...v0.5.1) (2026-05-14)


### Bug Fixes

* add post-install xattr hook to remove macOS quarantine bit ([1ce57a8](https://github.com/dantech2000/refresh/commit/1ce57a83c5e141029ca05639d22387ef44a592d7))
* polish cluster and nodegroup workflows ([2e8b232](https://github.com/dantech2000/refresh/commit/2e8b232af3d2c2bcb672fd1a65349b80ada5b9c9))
* resolve golangci-lint issues ([963c88c](https://github.com/dantech2000/refresh/commit/963c88c3ede9faf9ddbf40895b39f7fcda71eada))
* use GH_PAT for release-please PR creation ([4e625a3](https://github.com/dantech2000/refresh/commit/4e625a30c67477897f13e0fa767c2a97e16220e8))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([3264540](https://github.com/dantech2000/refresh/commit/3264540336e61f5ad0f41d1bbcf4f84a5db96d26))
* split internal/commands into focused sub-packages ([bea3204](https://github.com/dantech2000/refresh/commit/bea3204dfd1a4b03f242fa46d78ac37f4230b748))
* split internal/commands into focused sub-packages ([ccf10c2](https://github.com/dantech2000/refresh/commit/ccf10c2e81f8655b990d00e5ce4e647ef25f1324))
