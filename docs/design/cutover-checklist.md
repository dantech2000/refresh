# Cutover checklist

This directory holds the changes that switch the repository from the old tree
to the new one. Nothing here is live: the root `Taskfile.yml`, the workflows,
`.goreleaser.yml`, and `docs/` still build and describe the old binary.

The cutover is one commit on `rewrite`, made after the work in "Before the
cutover" is done. Do the steps in order. Each step has a command that checks
it. Run every command from the repository root.

The steps were rehearsed on a copy of the tree at `b62e59d`. "What the
rehearsal found" at the end lists what passed and what failed.

## Files in this directory

| File | Replaces | What changes |
|---|---|---|
| `Taskfile.yml` | `Taskfile.yml` | `build` and `build:release` target `./cmd/refresh`, and the three `-X` symbols name `internal/build`. `docs:gen` writes `docs/reference` and `api/schema/v2`, and `docs:check` covers `docs/reference` and `api/schema`. `test:live` runs every `TestLive` of `./internal/...` and `./tools/...`. New: `docs:man`, `test:parity`, `release:verify`. Gone: `build:next`, `test:integration`, `install:man`. `run:sim` and `run:tui:sim` use `dist/refresh`. |
| `.goreleaser.yml` | `.goreleaser.yml` | `main: ./cmd/refresh`. The ldflags name `internal/build`. A before-hook generates the man pages with `gen-docs --format man` under `SOURCE_DATE_EPOCH={{ .CommitTimestamp }}`. The archives carry `man/man1/*.1`, and the Homebrew cask lists the 30 pages. A new cask description. The old config ships no shell completions, so this one ships none. |
| `workflows/test.yml` | `.github/workflows/test.yml` | The docs check runs `./cmd/refresh gen-docs` and `./tools/genschema`, and covers `docs/reference` and `api/schema`. The `rewrite` branch leaves the triggers. The Codecov flag stays `unittests`: the workflow never had the `legacy` and `next` flags that `REWRITE.md` planned. |
| `workflows/lint.yml` | `.github/workflows/lint.yml` | The `rewrite` branch leaves the triggers. |
| `workflows/docs.yml` | `.github/workflows/docs.yml` | The path filters gain `api/schema/**` and `tools/mkdocs/**`, so a schema change publishes the site. |
| `.golangci.yml` | `.golangci.yml` | The two `^legacy/` exclusions and their comments are gone. |
| `codecov.yml` | `codecov.yml` | An `ignore` list for the fakes (`internal/**/*test/**`), `tools/`, and `test/`. This changes the coverage ratio. Drop the list if you want the number to stay comparable. |
| `mkdocs.yml` | `mkdocs.yml` | The nav of the new docs, the schema hook, and a site name with no dash. It is `mkdocs-next.yml` with `docs_dir: docs`. |
| `README.md` | `README.md` | The new command lines, the `go install` path, `v2` documents, the man pages. |
| `CLAUDE.md` | `CLAUDE.md` | Written for the new tree only. |
| `HELP-SWEEP.md` | nothing | Findings about the help of the new binary. Delete it, or move the open items to Linear. |

`nightly.yml`, `release.yml`, `release-please.yml`, `auto-merge.yml`,
`dependabot.yml`, `release-please-config.json`, `pyproject.toml`, and
`uv.lock` need no change: they name no path of the old tree.

## Decisions to take first

1. **The parity suite with no old binary.** Proposal: keep the suite, and make
   the recorded traces the reference. `test/parity/testdata/legacy/*.json` (160
   files) already holds what the old binary did in every scenario, and the old
   tree cannot change any more. The suite then runs the new binary alone and
   compares it with the recorded trace and the allowlist. This needs three
   edits in `test/parity`: `TestMain` stops building `../../legacy`;
   `TestLegacyTraces`, which checks the recorded traces against the old
   binary, is deleted; `TestPairedSelf`, which pairs the old binary with
   itself, pairs two recorded traces or is deleted. The `Legacy` command lines
   of the scenarios stay as the record of what each trace ran.
   `internal/change/planparity_test.go` reads the same traces and needs no
   change. The alternative is to delete the suite and keep only the goldens;
   that loses the evidence rule ("which facts did the binary read before each
   change"), which no other test has.
2. **The `v1` schemas.** Proposal: move `docs/schema/v1` to `api/schema/v1`.
   The MkDocs hook publishes all of `api/schema`, so
   `https://drod.dev/refresh/schema/v1/<Kind>.json` keeps working for scripts
   of 0.16. `docs-next/migrating.md` and `docs-next/reference/schemas.md` say
   that the `v1` schemas stay. If you drop them, remove those two sentences.
3. **The version.** `release-please-config.json` has
   `bump-minor-pre-major: true`, so a `feat!:` commit gives 0.17.0. The docs
   say "the release after 0.16" and "Migrating from 0.16", so they hold for
   0.17.0 and for 1.0.0.
4. **`codecov.yml`.** Keep or drop the `ignore` list (see the table).

## Before the cutover

These are not part of the commit. The cutover should wait for them.

- [ ] The change screens of `refresh ui` are built, or the release ships the
      read-only UI. If they are built: update `docs-next/commands/ui.md` (it
      says read-only, and it lists two screens), `docs-next/migrating.md`
      ("Other changes"), and the README paragraph. Check:
      `./dist/refresh-next ui --help | grep -c 'no effect yet'` prints `0`.
- [ ] Every row of "The new code behaves differently" in `legacy/PORTING.md`
      has a decision. `legacy/` is deleted in the cutover, so copy
      `legacy/PORTING.md` to Linear or to `docs/design/porting.md` first if the
      open rows are not closed.
- [ ] `main` is merged into `rewrite` one last time.
      Check: `git merge-base --is-ancestor origin/main HEAD && echo merged`.
- [ ] `task test:live` passes against a real account with a `ReadOnlyAccess`
      identity, and a manual upgrade ran on a test cluster (`REWRITE.md`,
      phase 6). Today the new tree has one livecheck, `TestLiveRegions`.
      `TestLiveSupportCalendar` does not read prices: check the
      extended-support premium (`fleetdata.ExtendedSupportPremiumUSDPerHour`)
      by hand against https://aws.amazon.com/eks/pricing/ and move
      `fleetdata.PremiumAsOf`.
- [ ] The reference in `docs-next` is fresh.
      Check: `task build:next && ./dist/refresh-next gen-docs --out docs-next/reference && git status --short docs-next/reference` prints nothing.

## The cutover commit

### 1. Delete the old tree

```bash
git rm -r -q legacy
```

Check: `test ! -e legacy && echo gone`.

### 2. Fix the three tests that read the old tree

| Test | Change |
|---|---|
| `internal/awsx/permissions_test.go`: `TestRequiredPermissionsMatchLegacy` | Delete it, as its comment says. `TestRequiredPermissionsMatchDocs` holds the table to the docs. |
| `internal/awsx/awsxtest/matrix_test.go`: `TestMatrixCoversEveryOperation` | It reads the IAM table from `legacy/internal/aws/awserr/permissions.go` as text. Read `awsx.RequiredPermissions()` instead. |
| `test/parity/parity_test.go`: `TestMain`, `TestLegacyTraces`; `compare_test.go`: `TestPairedSelf` | Apply decision 1. |

Also remove the `legacy/` case from `internal/tui/imports_test.go` and
`internal/tui/backend/imports_test.go`. It does no harm, and it names a
directory that is gone.

Check: `grep -rn 'legacy/' --include='*.go' internal cmd tools test | grep -v 'testdata/legacy'` prints nothing.

### 3. Tidy the module

```bash
go mod tidy
```

The rehearsal removed `urfave/cli/v3`, `urfave/cli-docs/v3`, `pterm`,
`fatih/color`, `mattn/go-isatty`, and `charm.land/lipgloss/v2` from the direct
requirements.

Check: `go mod tidy -diff` prints nothing, and
`grep -c 'urfave\|pterm\|fatih' go.mod` prints `0`.

### 4. Swap the docs

```bash
git mv docs/design docs-next/design
mkdir -p api/schema && git mv docs/schema/v1 api/schema/v1    # decision 2
git rm -r -q docs
rm -rf docs            # git leaves the empty docs/schema behind; see the rehearsal
git mv docs-next docs
git rm -q mkdocs-next.yml
cp cutover/mkdocs.yml mkdocs.yml
```

Then remove the `site-next/` line from `.gitignore`.

`internal/awsx/permissions_docs_test.go` reads `docs-next/` when it exists and
`docs/` otherwise, so it needs no edit. After the swap, remove `docs-next`
from its list and from its comment.

Check: `test -f docs/migrating.md && test -d docs/design && test ! -e docs-next && uv run --frozen mkdocs build --strict && ls site/schema` prints `v1` and `v2`.

### 5. Apply the tooling files

```bash
cp cutover/Taskfile.yml cutover/.goreleaser.yml cutover/.golangci.yml cutover/codecov.yml .
cp cutover/README.md cutover/CLAUDE.md .
cp cutover/workflows/*.yml .github/workflows/
git rm -r -q cutover
```

Keep `REWRITE.md` and `CONTRACT.md`: the new `CLAUDE.md` points at both.
Update the first line of each to say that the cutover is done.

Check: `grep -rn 'legacy\|build:next\|refresh-next\|docs-next' Taskfile.yml .goreleaser.yml .golangci.yml .github/workflows mkdocs.yml README.md CLAUDE.md` prints nothing.

### 6. Check every build path

```bash
grep -n 'cmd/refresh\|tools/' Taskfile.yml .goreleaser.yml .github/workflows/test.yml
```

Check: the build, the release build, `docs:gen`, the GoReleaser `main:` and
its before-hook, and the docs step of `test.yml` all name `./cmd/refresh` or
`./tools/...`.

### 7. Check the release build

```bash
task release:verify
goreleaser check
goreleaser release --snapshot --clean --skip=publish,sign,sbom
tar -tzf dist/refresh_*_linux_amd64.tar.gz | grep -c 'man/man1/.*\.1$'
grep -c manpage dist/homebrew/Casks/refresh.rb
```

Check: `task release:verify` prints a version, a commit, and a build date, and
exits 0. The two counts are both `30`. If a command was added or removed, the
`manpages` list of `.goreleaser.yml` must change with it:
`ls build/man/man1 | wc -l` must equal the count in the cask.

Then check the update hint and the completion from the release binary:

```bash
B=$(ls dist/refresh_darwin_arm64*/refresh dist/refresh_linux_amd64*/refresh 2>/dev/null | head -1)
$B completion zsh | head -1
$B version            # on a terminal: prints the hint when a newer release exists
```

### 8. Check the docs freshness check

```bash
task docs:check
```

Check: it prints "docs/reference and api/schema are up to date". Then prove
that it can fail: `echo x >> api/schema/v2/Plan.json && task docs:check; git checkout api/schema`
must fail before the checkout.

### 9. Run the full check list

```bash
task dev:full
task test:parity
task fuzz FUZZTIME=10s
uv run --frozen mkdocs build --strict
```

Check: all four exit 0, and `task lint` reports `0 issues`.

### 10. Commit, merge, release

```bash
git add -A
git commit            # feat!: replace the CLI with the rewrite
```

The commit message needs a `BREAKING CHANGE:` footer, so release-please bumps
the minor version and puts the note in the changelog. Point the note at
<https://drod.dev/refresh/migrating/>. `docs/migrating.md` lists every contract
change.

After the merge to `main`:

- [ ] The Docs workflow published the site. Check:
      `curl -sf https://drod.dev/refresh/schema/v2/Plan.json | head -3` and
      `curl -sf https://drod.dev/refresh/migrating/ > /dev/null && echo ok`.
      Since the cutover, `tools/mkdocs/build_site.py` builds the site in two
      versions: `/refresh/v2/` and `/refresh/v1/` (the docs of 0.16). The old
      flat paths, such as `/refresh/migrating/`, are redirect stubs to v2, so
      also check
      `curl -sf https://drod.dev/refresh/v2/migrating/ > /dev/null && echo ok`.
- [ ] After the release: `brew upgrade --cask refresh && man refresh-status`
      opens the page, and `refresh version` prints the new version.
- [ ] `go install github.com/dantech2000/refresh/cmd/refresh@latest` builds.

## What the rehearsal found

The steps ran on a copy of the tree at `b62e59d`, with `legacy/` deleted, the
docs swapped, and the files of this directory applied.

Passed, from the staged `Taskfile.yml`: `build`, `build:release`,
`release:verify`, `tidy:check` (after `go mod tidy`), `vet`, `lint` (0 issues
with the staged `.golangci.yml`), `deadcode` (none, with the old tree gone),
`vuln`, `docs:check`, `docs:build` (strict, with `schema/v1` and `schema/v2`
in the site), `docs:man` (30 pages), `run:version`, `run:sim`.

Passed, GoReleaser 2 with the staged config: `goreleaser check`,
`goreleaser build --snapshot --single-target --clean`, and
`goreleaser release --snapshot --clean --skip=publish,sign,sbom`. Each archive
has the binary, `README.md`, `LICENSE`, and 30 man pages. The cask has 30
`manpage` lines. The man pages carry the release version.

Failed, as step 2 expects: `TestRequiredPermissionsMatchLegacy`,
`TestMatrixCoversEveryOperation`, and the `TestMain` of `test/parity`. Every
other package of `go test ./...` passed with the old tree gone.

Found: `git rm -r docs` leaves `docs/schema` behind as an empty directory
after `docs/schema/v1` moved, and `git mv docs-next docs` then moves the new
tree to `docs/docs-next`. Step 4 has the `rm -rf docs` for that.

Not run: `task test:live` (it needs an account), `task fuzz`, `task bench`,
the signing and SBOM steps of a release, and the publish of the cask. The
workflows were not run: they were checked by reading them against the staged
Taskfile.
