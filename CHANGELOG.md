# Changelog

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
