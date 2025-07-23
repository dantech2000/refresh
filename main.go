package main

import (
	"os"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/commands"
)

func main() {
	app := &cli.App{
		Name:  "refresh",
		Usage: "Manage and monitor AWS EKS node groups",
		Commands: []*cli.Command{
			commands.ListCommand(),
			commands.VersionCommand(),
			commands.UpdateAmiCommand(),
		},
	}
	if err := app.Run(os.Args); err != nil {
		color.Red("Error: %v", err)
		os.Exit(1)
	}
}
