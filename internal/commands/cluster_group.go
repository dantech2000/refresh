package commands

import (
	"github.com/urfave/cli/v2"
)

// ClusterCommand groups cluster-related subcommands using verb as subcommand
func ClusterCommand() *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "Cluster operations (list, describe, compare)",
		Subcommands: []*cli.Command{
			clusterListCommand(),
			clusterDescribeCommand(),
			clusterCompareCommand(),
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
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runDescribeCluster(c) },
	}
}

func clusterCompareCommand() *cli.Command {
	orig := CompareClustersCommand()
	return &cli.Command{
		Name:        "compare",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runCompareClusters(c) },
	}
}
