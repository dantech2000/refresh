// Package cluster provides CLI commands for EKS cluster operations.
package cluster

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Command returns the cluster command group with list, describe, and diff subcommands.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "Cluster operations (list, get, diff)",
		Commands: []*cli.Command{
			listCommand(),
			describeCommand(),
			diffCommand(),
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List EKS clusters with health status (multi-region support)",
		ArgsUsage: "[name-pattern]",
		Description: `Fast cluster discovery across regions with integrated health validation.
Direct EKS API calls provide high performance along with comprehensive
health monitoring and multi-region capabilities.`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: 60 * time.Second, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.IntFlag{Name: "max-concurrency", Aliases: []string{"C"}, Usage: "Max concurrent region requests", Value: appconfig.DefaultMaxConcurrency, Sources: cli.EnvVars("REFRESH_MAX_CONCURRENCY")},
			&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}, Usage: "Query all EKS-supported regions"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,version,region", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Specific region(s) to query (can be used multiple times)"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status for each cluster"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter clusters (format: key=value)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain, tree)", Value: "table"},
			&cli.BoolFlag{Name: "tree", Aliases: []string{"T"}, Usage: "Display results as hierarchical tree (implies --all-regions)"},
			&cli.BoolFlag{Name: "watch", Aliases: []string{"w"}, Usage: "Re-run and redraw every --watch-interval until interrupted"},
			&cli.DurationFlag{Name: "watch-interval", Usage: "Refresh interval for --watch", Value: 10 * time.Second},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runList(ctx, cmd) },
	}
}

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Aliases:   []string{"get"},
		Usage:     "Describe comprehensive cluster information",
		ArgsUsage: "[cluster]",
		Description: `Get detailed information about an EKS cluster including networking,
security configuration, add-ons, and health status. Direct EKS API calls
provide fast, comprehensive results without CloudFormation dependency.`,
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "detailed", Aliases: []string{"d"}, Usage: "Show comprehensive information including networking and security"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status from existing health framework", Value: true},
			&cli.BoolFlag{Name: "show-security", Aliases: []string{"s"}, Usage: "Include security configuration analysis"},
			&cli.BoolFlag{Name: "include-addons", Aliases: []string{"a"}, Usage: "Include EKS add-on information", Value: true},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runDescribe(ctx, cmd) },
	}
}

func diffCommand() *cli.Command {
	return &cli.Command{
		Name:    "diff",
		Aliases: []string{"compare"},
		Usage:   "Compare EKS clusters side-by-side for consistency validation",
		Description: `Analyze configuration differences between EKS clusters to ensure
consistency across environments. Supports comparison of networking,
security, add-ons, and version configurations.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "Cluster name or pattern (specify multiple times)", Required: true},
			&cli.BoolFlag{Name: "interactive", Usage: "Interactively select clusters when multiple patterns match"},
			&cli.BoolFlag{Name: "show-differences", Aliases: []string{"d"}, Usage: "Show only differences (hide identical configurations)"},
			&cli.StringSliceFlag{Name: "include", Aliases: []string{"i"}, Usage: "Compare specific aspects (networking, security, addons, versions)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runDiff(ctx, cmd) },
	}
}
