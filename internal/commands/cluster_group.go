package commands

import (
	"github.com/urfave/cli/v2"
)

// ClusterCommand groups cluster-related subcommands using verb as subcommand
func ClusterCommand() *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "Cluster operations (list, get, diff)",
		Subcommands: []*cli.Command{
			clusterListCommand(),
			clusterDescribeCommand(),
			clusterDiffCommand(),
		},
	}
}

func clusterListCommand() *cli.Command {
	orig := ListClustersCommand()
	return &cli.Command{
		Name:        "list",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runListClusters(c) },
	}
}

func clusterDescribeCommand() *cli.Command {
	orig := DescribeClusterCommand()
	return &cli.Command{
		Name:        "describe",
		Aliases:     []string{"get"},
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runDescribeCluster(c) },
	}
}

func clusterDiffCommand() *cli.Command {
	orig := CompareClustersCommand()
	return &cli.Command{
		Name:        "diff",
		Aliases:     []string{"compare"},
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runCompareClusters(c) },
	}
}
