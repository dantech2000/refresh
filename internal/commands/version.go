package commands

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/updatecheck"
)

// These variables are set at build time via -ldflags.
// Release builds always inject the real version from the git tag via GoReleaser.
// Example: go build -ldflags "-X github.com/dantech2000/refresh/internal/commands.version=v1.0.0"
var (
	version   = "dev" // overridden by GoReleaser: -X ...commands.version={{.Version}}
	commit    = ""    // overridden by GoReleaser: -X ...commands.commit={{.ShortCommit}}
	buildDate = ""    // overridden by GoReleaser: -X ...commands.buildDate={{.Date}}
)

// VersionInfo provides access to version information. ldflags -X values are
// applied at link time, so this package-level initializer (which runs after
// version/commit/buildDate in dependency order) already sees them — no init()
// re-assignment is needed.
var VersionInfo = types.VersionInfo{
	Version:   version,
	Commit:    commit,
	BuildDate: buildDate,
}

// PrintVersion writes the CLI version details to w. It is the single source of
// truth shared by the `version` subcommand and the global `--version` flag so
// both produce identical output.
func PrintVersion(w io.Writer) {
	_, _ = fmt.Fprintf(w, "refresh version: %s\n", VersionInfo.Version)
	if VersionInfo.Commit != "" {
		_, _ = fmt.Fprintf(w, "commit: %s\n", VersionInfo.Commit)
	}
	if VersionInfo.BuildDate != "" {
		_, _ = fmt.Fprintf(w, "built: %s\n", VersionInfo.BuildDate)
	}
}

// stdoutIsTerminal reports whether stdout is an interactive terminal. It
// mirrors runner.watchIsTerminal so the update-check hint is suppressed for
// piped/scripted consumers. Overridable in tests.
var stdoutIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

func VersionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the version of this CLI",
		Description: `Print the running version (and, for release builds, the commit and build
date). On an interactive terminal it also performs an opt-in, throttled,
fail-silent check against GitHub Releases and prints a one-line hint to stderr
when a newer version is available.

The check runs at most once per day (cached under the user config dir), never
adds measurable latency, and is skipped when stdout is piped/redirected. Disable
it with --no-update-check or REFRESH_NO_UPDATE_CHECK=1.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "no-update-check",
				Usage:   "Skip the check for a newer release",
				Sources: cli.EnvVars("REFRESH_NO_UPDATE_CHECK"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			PrintVersion(cmd.Root().Writer)
			maybePrintUpdateHint(ctx, cmd, os.Stderr)
			return nil
		},
	}
}

// updateChecker is overridable in tests so the hint logic can run against an
// httptest server and temp cache without real network.
var updateChecker = func() *updatecheck.Checker { return updatecheck.New() }

// maybePrintUpdateHint runs the opt-in update check and writes a one-line hint
// to w when the local build is behind. It is fully suppressed when stdout is
// not a TTY, when --no-update-check/REFRESH_NO_UPDATE_CHECK is set, or when the
// version is "dev"/unparseable. All failures are silent.
func maybePrintUpdateHint(ctx context.Context, cmd *cli.Command, w io.Writer) {
	if cmd.Bool("no-update-check") {
		return
	}
	if !stdoutIsTerminal() {
		return
	}
	latest, err := updateChecker().LatestTag(ctx)
	if err != nil || latest == "" {
		return // fail-silent
	}
	if hint := updatecheck.UpgradeHint(VersionInfo.Version, latest); hint != "" {
		_, _ = fmt.Fprintln(w, hint)
	}
}
