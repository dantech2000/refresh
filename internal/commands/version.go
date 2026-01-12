package commands

import (
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/types"
)

// These variables are set at build time via -ldflags
// Example: go build -ldflags "-X github.com/dantech2000/refresh/internal/commands.version=v1.0.0"
var (
	version   = "v0.3.0" // Set via: -X ...commands.version=v0.3.0
	commit    = ""       // Set via: -X ...commands.commit=abc1234
	buildDate = ""       // Set via: -X ...commands.buildDate=2024-01-01
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
