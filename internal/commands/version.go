// Package commands holds the top-level utility commands: version, shell
// completion, man page install, and the hidden docs generator.
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
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

func VersionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the version of this CLI",
		Description: `Print the running version (and, for release builds, the commit and build
date). On an interactive terminal it also checks GitHub Releases for a newer
version and prints a one-line hint to stderr when one is available. The check
is on by default, throttled, and fail-silent.

The check runs at most once per day (cached under the user config dir), never
adds measurable latency, and is skipped when stdout is piped/redirected. Disable
it with --no-update-check, or set REFRESH_NO_UPDATE_CHECK to any non-empty
value other than 0, false, or no.`,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-update-check",
				Usage: "Skip the check for a newer release (also REFRESH_NO_UPDATE_CHECK)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			PrintVersion(cmd.Root().Writer)
			maybePrintUpdateHint(ctx, cmd, ui.Stderr)
			return nil
		},
	}
}

// envTruthy reports whether an opt-out env var is set. Any non-empty value
// counts except "0", "false" and "no" (case-insensitive), so values like "yes"
// work instead of failing strconv.ParseBool.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

// maybePrintUpdateHint runs the update check (on by default) and writes a one-line hint
// to w when the local build is behind. It is fully suppressed when stdout is
// not a TTY, when --no-update-check/REFRESH_NO_UPDATE_CHECK is set, or when the
// version is "dev"/unparseable. All failures are silent.
func maybePrintUpdateHint(ctx context.Context, cmd *cli.Command, w io.Writer) {
	if cmd.Bool("no-update-check") || envTruthy(os.Getenv("REFRESH_NO_UPDATE_CHECK")) {
		return
	}
	if !ui.IsTerminal(os.Stdout) {
		return
	}
	latest, err := updatecheck.New().LatestTag(ctx)
	if err != nil || latest == "" {
		return // fail-silent
	}
	if hint := updatecheck.UpgradeHint(VersionInfo.Version, latest); hint != "" {
		_, _ = fmt.Fprintln(w, hint)
	}
}
