package commands

import (
	"fmt"
	"io"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/types"
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
		Action: func(c *cli.Context) error {
			PrintVersion(c.App.Writer)
			return nil
		},
	}
}
