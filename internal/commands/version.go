package commands

import (
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/types"
)

var VersionInfo = types.VersionInfo{
	Version:   "v0.1.6",
	Commit:    "",
	BuildDate: "",
}

func VersionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the version of this CLI",
		Action: func(c *cli.Context) error {
			fmt.Printf("refresh version: %s", VersionInfo.Version)
			if VersionInfo.Commit != "" {
				fmt.Printf(" (commit: %s)", VersionInfo.Commit)
			}
			if VersionInfo.BuildDate != "" {
				fmt.Printf(" (built: %s)", VersionInfo.BuildDate)
			}
			fmt.Println()
			return nil
		},
	}
}
