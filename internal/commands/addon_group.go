package commands

import (
	"github.com/urfave/cli/v2"
)

// AddonCommand groups add-on commands
func AddonCommand() *cli.Command {
	return &cli.Command{
		Name:  "addon",
		Usage: "EKS add-on operations (list, describe, update)",
		Subcommands: []*cli.Command{
			addonListCommand(),
			addonDescribeCommand(),
			addonUpdateCommand(),
		},
	}
}

func addonListCommand() *cli.Command {
	orig := ListAddonsCommand()
	return &cli.Command{
		Name:        "list",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runListAddons(c) },
	}
}

func addonDescribeCommand() *cli.Command {
	orig := DescribeAddonCommand()
	return &cli.Command{
		Name:        "describe",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runDescribeAddon(c) },
	}
}

func addonUpdateCommand() *cli.Command {
	orig := UpdateAddonCommand()
	return &cli.Command{
		Name:        "update",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      orig.Action,
	}
}
