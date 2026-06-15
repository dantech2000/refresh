# Changelog

## [0.9.0](https://github.com/dantech2000/refresh/compare/v0.8.0...v0.9.0) (2026-06-15)


### Features

* **upgrade-check:** themed insight detail view + discoverable insight IDs (REF-149) ([#69](https://github.com/dantech2000/refresh/issues/69)) ([e546903](https://github.com/dantech2000/refresh/commit/e546903a9b7764af16c26b278ab1790807c6e47e))


### Bug Fixes

* **health:** wire all checks consistently + don't let skipped checks force WARN (REF-148) ([#67](https://github.com/dantech2000/refresh/issues/67)) ([abc3aa0](https://github.com/dantech2000/refresh/commit/abc3aa0cbc48c5ae2f2ffd8af60510c0e919c426))

## [0.8.0](https://github.com/dantech2000/refresh/compare/v0.7.0...v0.8.0) (2026-06-15)


### Features

* **cluster:** itemize per-check health results in cluster describe (REF-146) ([#65](https://github.com/dantech2000/refresh/issues/65)) ([2c1443c](https://github.com/dantech2000/refresh/commit/2c1443cd1722e329c7c5904c81d091fc0739b347))
* **health:** control-plane readiness gate from AWS/EKS CloudWatch metrics (REF-140) ([#59](https://github.com/dantech2000/refresh/issues/59)) ([96ae965](https://github.com/dantech2000/refresh/commit/96ae965b23abc7f2459f4e64fc1bf4eff4381acf))
* **health:** EC2 vCPU service-quota headroom pre-flight (REF-144) ([#63](https://github.com/dantech2000/refresh/issues/63)) ([cf44489](https://github.com/dantech2000/refresh/commit/cf44489e5e3902f8e68c843f3684e89cfd331df9))
* **health:** live node CPU+memory drain headroom via metrics-server (REF-142) ([#60](https://github.com/dantech2000/refresh/issues/60)) ([bdd6af4](https://github.com/dantech2000/refresh/commit/bdd6af4f48a45cf34d43e1deb4073e1992beed8e))
* **nodegroup:** pre-flight instance-type availability per AZ on scale/update (REF-143) ([#62](https://github.com/dantech2000/refresh/issues/62)) ([164b293](https://github.com/dantech2000/refresh/commit/164b293eb54682e3dbbbe3fd0ef0ba05a830384c))
* **noderoll:** surface live Kubernetes Warning events during a roll (REF-138) ([#61](https://github.com/dantech2000/refresh/issues/61)) ([7f6428d](https://github.com/dantech2000/refresh/commit/7f6428df478ae33f466e98372d2421caf06b6995))


### Bug Fixes

* CLI/help/status UX bugs (REF-129, REF-131, REF-132, REF-133, REF-134) ([#51](https://github.com/dantech2000/refresh/issues/51)) ([e0d9010](https://github.com/dantech2000/refresh/commit/e0d90104e8f5509bafd8f64e61319859efb0398b))
* **nodegroup,cluster:** measure real node readiness instead of synthesizing it (REF-130) ([#52](https://github.com/dantech2000/refresh/issues/52)) ([17fc71f](https://github.com/dantech2000/refresh/commit/17fc71f68373b42a7448149f9658de4a67ea9423))

## [0.7.0](https://github.com/dantech2000/refresh/compare/v0.6.0...v0.7.0) (2026-06-13)


### Features

* **cluster:** upgrade-check — EKS Cluster Insights + version-skew readiness (REF-12) ([#31](https://github.com/dantech2000/refresh/issues/31)) ([16d6fdf](https://github.com/dantech2000/refresh/commit/16d6fdfd3e282a21623ec06cd2d8293b89fd6721))
* **docs:** generate the command/flag reference from the CLI tree (REF-108) ([#43](https://github.com/dantech2000/refresh/issues/43)) ([dac59b4](https://github.com/dantech2000/refresh/commit/dac59b459552b3a3f17f36de9ab7c590a8b5d8a8))
* **health:** --kubeconfig flag + connection diagnostics for pre-flight checks (REF-3) ([#32](https://github.com/dantech2000/refresh/issues/32)) ([0b8f1ac](https://github.com/dantech2000/refresh/commit/0b8f1ac8047509fc46035a868bf29da52d01b6bd))
* **nodegroup:** AMI refresh flagship — fleet mode, verification, changelog, unattended, custom-AMI safety (REF-80) ([#33](https://github.com/dantech2000/refresh/issues/33)) ([601274c](https://github.com/dantech2000/refresh/commit/601274ca98470b44cb9d9afbfb3f800272e8d1d3))
* output redesign + live cluster-roll observability (REF-119) ([#50](https://github.com/dantech2000/refresh/issues/50)) ([3dbc24e](https://github.com/dantech2000/refresh/commit/3dbc24e866153a150a2c6ebe7e657d439aae1bff))
* signal cancellation + mechanical hygiene (salvage of [#19](https://github.com/dantech2000/refresh/issues/19)/[#21](https://github.com/dantech2000/refresh/issues/21)) ([#25](https://github.com/dantech2000/refresh/issues/25)) ([5b27147](https://github.com/dantech2000/refresh/commit/5b2714720d8424c2d037524014be9910ab67c728))
* **status:** refresh status — fleet patch posture across clusters/regions (REF-79) ([#30](https://github.com/dantech2000/refresh/issues/30)) ([1e9cc61](https://github.com/dantech2000/refresh/commit/1e9cc612bb2d0782bdf8de95ac5cff300b128107))


### Bug Fixes

* **cli:** consistency & robustness hardening — flags, positional, ctx-cancel, nil-derefs (REF-52) ([#38](https://github.com/dantech2000/refresh/issues/38)) ([af93607](https://github.com/dantech2000/refresh/commit/af9360768496f4b290d2bec00e7ad085b32efdae))
* **cli:** output & flag correctness — filters, format validation, global region/profile, yaml keys ([#34](https://github.com/dantech2000/refresh/issues/34)) ([2a06a7f](https://github.com/dantech2000/refresh/commit/2a06a7f1b01b1b58b1a6fbcf468c9734e640c8b8))
* harden defensive nil-checks and input validation (REF-115, REF-116, REF-117) ([#47](https://github.com/dantech2000/refresh/issues/47)) ([771e487](https://github.com/dantech2000/refresh/commit/771e4877b51c1ac476a8b4ff9215f582db529d53))
* **health:** scoring accuracy — skip exclusion, peak CPU, proxy honesty, std-dev relabel (REF-63) ([#29](https://github.com/dantech2000/refresh/issues/29)) ([5445a23](https://github.com/dantech2000/refresh/commit/5445a23d6e0f167112b73881d1ce9b1b6f8f64aa))
* **ui:** output/formatting data-integrity — TSV escaping, zero-time, display-cell widths (REF-62) ([#37](https://github.com/dantech2000/refresh/issues/37)) ([1c06dd9](https://github.com/dantech2000/refresh/commit/1c06dd97f813f1e23cf20a8d96295b108c639a2c))
* **upgrade:** attach to in-flight addon updates on resume (REF-114) ([#48](https://github.com/dantech2000/refresh/issues/48)) ([780b097](https://github.com/dantech2000/refresh/commit/780b097560238e3c41e21c5d01c113698fa7fbc2))


### Code Refactoring

* consolidate duplicated table, timing, filter, pagination, and badge code ([#22](https://github.com/dantech2000/refresh/issues/22)) ([8788a28](https://github.com/dantech2000/refresh/commit/8788a28d43417dc7454acc7a317af4173576a1f4))
* logging, addon factory, batched ASG, scale-dry-run PDBs, split actions.go (REF-37, 39, 50, 4, 38) ([#39](https://github.com/dantech2000/refresh/issues/39)) ([03e857f](https://github.com/dantech2000/refresh/commit/03e857f5c0aa2215c202c9cc2df37f0a21d9a8ed))
* migrate CLI from urfave/cli v2 to v3 (REF-11) ([#27](https://github.com/dantech2000/refresh/issues/27)) ([19ea6e4](https://github.com/dantech2000/refresh/commit/19ea6e456a8e724a04a92c0c1209ac3d1dc8d5b6))
* **trim:** refocus as the EKS upgrade companion — remove diff, cost, utilization, workload pdbs (REF-78) ([#36](https://github.com/dantech2000/refresh/issues/36)) ([9c6ce30](https://github.com/dantech2000/refresh/commit/9c6ce30a408e7ffcd09a541f53ae6ff75c5b9bc7))

## [0.6.0](https://github.com/dantech2000/refresh/compare/v0.5.12...v0.6.0) (2026-06-06)


### Features

* **version:** support --version/-v flag in addition to version subcommand ([b3713de](https://github.com/dantech2000/refresh/commit/b3713de0db6129de00085416efefa75c2c53866e))


### Bug Fixes

* addon update version positional + addons semaphore ordering ([a7c09d2](https://github.com/dantech2000/refresh/commit/a7c09d260ced9ef3c24be772910b771d7c0b3b5f))
* **clusterview:** tree view preserves cluster status under unknown health ([f48da16](https://github.com/dantech2000/refresh/commit/f48da16fd754c9a4bbe4f48819386f4839135587))
* multi-region cache collision + addon resolver nil deref ([b08ca75](https://github.com/dantech2000/refresh/commit/b08ca75829b73d03e0e6530fb6c2f3ca66855acf))
* **nodegroup:** --health-only always prints verdict, even under --quiet ([9b242f1](https://github.com/dantech2000/refresh/commit/9b242f135c677caf4c531c6be357d641926bf3cb))


### Code Refactoring

* **addon:** adopt runner.SetupAWSStrict and PositionalAt ([6568846](https://github.com/dantech2000/refresh/commit/65688464eda6a0d25ffa1e782937cd957de6455f))
* **addons:** dedupe UpdateAll parallel/serial branches ([b666f8d](https://github.com/dantech2000/refresh/commit/b666f8d0bf987a5ce9a31fccc84bdf556fd677fe))
* **cluster:** clean ListAllRegions structure ([dc23fc3](https://github.com/dantech2000/refresh/commit/dc23fc374f7bbedc513177ba0fa7d373a87d7a31))
* **cluster:** collapse outputClustersTable's multiRegion×showHealth branches ([1e97291](https://github.com/dantech2000/refresh/commit/1e97291a16f954b6b39f92d17e237d5bc3bbde11))
* **cluster:** consolidate color/status formatters ([ea2e311](https://github.com/dantech2000/refresh/commit/ea2e3113293f754bcc1147d0d4a99ebd9cb09da4))
* **cluster:** extract diff helpers in analyzeDifferences ([4e202ab](https://github.com/dantech2000/refresh/commit/4e202ab61f3e103e1c21ec6b16b4cf00c4e4db8e))
* **cluster:** getClusterSummary drops always-nil error return ([d569c23](https://github.com/dantech2000/refresh/commit/d569c235ce5c0b06b7a0bc28ca055b91db5eb243))
* **cluster:** simplify buildListCacheKey ([6c575e3](https://github.com/dantech2000/refresh/commit/6c575e3555fa0a169480bc9ed9db8482860ede99))
* **clusterview:** split into color/list/detail/compare files ([a13f7c0](https://github.com/dantech2000/refresh/commit/a13f7c0e362857329768dd305ecf117cb9671e09))
* **commands:** extract clusterview pkg, finish runner adoption ([c9101ea](https://github.com/dantech2000/refresh/commit/c9101eab8ef789819f2912d3ece0d061192ed3ec))
* **commands:** extract runner package for shared CLI primitives ([f324738](https://github.com/dantech2000/refresh/commit/f324738cb4df132eceef0a3ab27f9e404f9fa8fc))
* **common:** add Paginate generic and migrate 5 ListX loops ([38d76a7](https://github.com/dantech2000/refresh/commit/38d76a7f4a2abd157d6a6b554edc5e9c42085835))
* **nodegroup:** adopt runner in runScale and runUpdateAMI ([7728468](https://github.com/dantech2000/refresh/commit/7728468833a92c1c0f7a6e253d8af8f78436d1d7))
* **nodegroup:** dedupe CloudWatch utilization collectors ([d8dda36](https://github.com/dantech2000/refresh/commit/d8dda364c6acf8416640fe58aa7be5ae3ea4fd66))
* **nodegroup:** extract classifyAMI helper ([617d693](https://github.com/dantech2000/refresh/commit/617d693f1e3e80e78ccd9ef0694734d7ecdb3ead))
* **nodegroup:** split runUpdateAMI into pipeline stages ([5848223](https://github.com/dantech2000/refresh/commit/58482230e39e7cc0532b47b0e683c203506a6aee))
* P3 dead code + helper consolidations ([ad9a24d](https://github.com/dantech2000/refresh/commit/ad9a24d3a65e1bb2dd9abf7912ae3249f63b3460))

## [0.5.12](https://github.com/dantech2000/refresh/compare/v0.5.11...v0.5.12) (2026-05-14)


### Bug Fixes

* seed release-please manifest at v0.5.11 (actual latest release) ([a8cac41](https://github.com/dantech2000/refresh/commit/a8cac41beb8278d36e5cb9a2a3d873b2af672624))
* use GH_PAT for release-please PR creation ([218215b](https://github.com/dantech2000/refresh/commit/218215b028a961b9da2d3cf459168caaa76168f7))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([7ddd9d0](https://github.com/dantech2000/refresh/commit/7ddd9d0e0baad164133d576f08d9eee7c21ca53b))
* split internal/commands into focused sub-packages ([5d1a004](https://github.com/dantech2000/refresh/commit/5d1a0047772f37e30158fccd1365db7be4b48e33))
* split internal/commands into focused sub-packages ([fc4f785](https://github.com/dantech2000/refresh/commit/fc4f785dfc2fd29193dc62ef2a41d9a0ddf7ad0e))

## [0.5.1](https://github.com/dantech2000/refresh/compare/v0.5.0...v0.5.1) (2026-05-14)


### Bug Fixes

* add post-install xattr hook to remove macOS quarantine bit ([9933ffc](https://github.com/dantech2000/refresh/commit/9933ffcd8e400392a0ed7d57831678396384af2b))
* polish cluster and nodegroup workflows ([b7b3685](https://github.com/dantech2000/refresh/commit/b7b3685b67714d0fac1db1ddb318f73a9c021440))
* resolve golangci-lint issues ([899002e](https://github.com/dantech2000/refresh/commit/899002ee6fdf1b5dacd4e24127d8cfed6e6a3bb9))
* use GH_PAT for release-please PR creation ([218215b](https://github.com/dantech2000/refresh/commit/218215b028a961b9da2d3cf459168caaa76168f7))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([7ddd9d0](https://github.com/dantech2000/refresh/commit/7ddd9d0e0baad164133d576f08d9eee7c21ca53b))
* split internal/commands into focused sub-packages ([5d1a004](https://github.com/dantech2000/refresh/commit/5d1a0047772f37e30158fccd1365db7be4b48e33))
* split internal/commands into focused sub-packages ([fc4f785](https://github.com/dantech2000/refresh/commit/fc4f785dfc2fd29193dc62ef2a41d9a0ddf7ad0e))
