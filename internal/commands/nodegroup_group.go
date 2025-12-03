package commands

import (
	"github.com/urfave/cli/v2"

	appconfig "github.com/dantech2000/refresh/internal/config"
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
	return &cli.Command{
		Name:      "list",
		Usage:     "List nodegroups in a cluster with optional health/cost/utilization",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout (e.g. 60s, 2m)",
				Value:   appconfig.DefaultTimeout,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.StringFlag{
				Name:    "cluster",
				Aliases: []string{"c"},
				Usage:   "EKS cluster name or pattern",
			},
			&cli.BoolFlag{
				Name:    "show-health",
				Aliases: []string{"H"},
				Usage:   "Include health status for each nodegroup",
			},
			&cli.BoolFlag{
				Name:    "show-costs",
				Aliases: []string{"C"},
				Usage:   "Include cost analysis",
			},
			&cli.BoolFlag{
				Name:    "show-utilization",
				Aliases: []string{"U"},
				Usage:   "Include CPU utilization metrics",
			},
			&cli.BoolFlag{
				Name:    "show-instances",
				Aliases: []string{"I"},
				Usage:   "Include instance details",
			},
			&cli.StringFlag{
				Name:    "timeframe",
				Aliases: []string{"T"},
				Usage:   "Utilization window (1h,3h,24h)",
				Value:   "24h",
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml)",
				Value:   "table",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by field: name,status,instance,nodes,cpu,cost",
				Value: "name",
			},
			&cli.BoolFlag{
				Name:  "desc",
				Usage: "Sort descending",
			},
			&cli.StringSliceFlag{
				Name:    "filter",
				Aliases: []string{"f"},
				Usage:   "Filter nodegroups (format: key=value, e.g., instanceType=m5.large)",
			},
		},
		Action: runListNodegroups,
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
