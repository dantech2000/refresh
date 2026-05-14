package commands

import (
	"fmt"

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

// VersionInfo provides access to version information
var VersionInfo = types.VersionInfo{
	Version:   version,
	Commit:    commit,
	BuildDate: buildDate,
}

func init() {
	// Update VersionInfo with ldflags values (they're set before init runs)
	VersionInfo.Version = version
	VersionInfo.Commit = commit
	VersionInfo.BuildDate = buildDate
}

func VersionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the version of this CLI",
		Action: func(c *cli.Context) error {
			fmt.Printf("refresh version: %s\n", VersionInfo.Version)
			if VersionInfo.Commit != "" {
				fmt.Printf("commit: %s\n", VersionInfo.Commit)
			}
			if VersionInfo.BuildDate != "" {
				fmt.Printf("built: %s\n", VersionInfo.BuildDate)
			}
			return nil
		},
	}
}
