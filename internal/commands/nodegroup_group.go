package commands

import (
	"github.com/urfave/cli/v2"
)

// NodegroupCommand groups nodegroup-related subcommands
func NodegroupCommand() *cli.Command {
	return &cli.Command{
		Name:    "nodegroup",
		Aliases: []string{"ng"},
		Usage:   "Nodegroup operations (list, describe, scale, update-ami, recommendations)",
		Subcommands: []*cli.Command{
			ngListCommand(),
			ngDescribeCommand(),
			ngScaleCommand(),
			ngUpdateAmiCommand(),
			ngRecommendationsCommand(),
		},
	}
}

func ngListCommand() *cli.Command {
	orig := ListNodegroupsCommand()
	return &cli.Command{
		Name:        "list",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runListNodegroups(c) },
	}
}

func ngDescribeCommand() *cli.Command {
	orig := DescribeNodegroupCommand()
	return &cli.Command{
		Name:        "describe",
		Usage:       orig.Usage,
		Description: orig.Description,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runDescribeNodegroup(c) },
	}
}

func ngScaleCommand() *cli.Command {
	orig := ScaleNodegroupCommand()
	return &cli.Command{
		Name:        "scale",
		Usage:       orig.Usage,
		Description: orig.Description,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runScaleNodegroup(c) },
	}
}

func ngUpdateAmiCommand() *cli.Command {
	orig := UpdateAmiCommand()
	return &cli.Command{
		Name:        "update-ami",
		Usage:       orig.Usage,
		Description: orig.Description,
		Flags:       orig.Flags,
		Action:      orig.Action,
	}
}

func ngRecommendationsCommand() *cli.Command {
	orig := NodegroupRecommendationsCommand()
	return &cli.Command{
		Name:        "recommendations",
		Usage:       orig.Usage,
		Description: orig.Description,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runNodegroupRecommendations(c) },
	}
}
