// Package nodegroup provides CLI commands for EKS nodegroup operations.
package nodegroup

import (
	"time"

	"github.com/urfave/cli/v2"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Command returns the nodegroup command group with list, describe, scale, and
// update subcommands.
func Command() *cli.Command {
	return &cli.Command{
		Name:    "nodegroup",
		Aliases: []string{"ng"},
		Usage:   "Nodegroup operations (list, get, scale, update)",
		Subcommands: []*cli.Command{
			listCommand(),
			describeCommand(),
			scaleCommand(),
			updateAMICommand(),
		},
	}
}

func listCommand() *cli.Command {
	return &cli.Command{
		Name:      "list",
		Usage:     "List nodegroups in a cluster with optional health/cost/utilization",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status for each nodegroup"},
			&cli.BoolFlag{Name: "show-costs", Aliases: []string{"C"}, Usage: "Include cost analysis"},
			&cli.BoolFlag{Name: "show-utilization", Aliases: []string{"U"}, Usage: "Include CPU utilization metrics"},
			&cli.BoolFlag{Name: "show-instances", Aliases: []string{"I"}, Usage: "Include instance details"},
			&cli.StringFlag{Name: "timeframe", Aliases: []string{"T"}, Usage: "Utilization window (1h,3h,24h)", Value: "24h"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,instance,nodes,cpu,cost", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter nodegroups (format: key=value, e.g., instanceType=m5.large)"},
		},
		Action: runList,
	}
}

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Aliases:   []string{"get"},
		Usage:     "Describe a nodegroup with optional instances/utilization/cost info",
		ArgsUsage: "[cluster] [nodegroup]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name (can be provided as second positional)"},
			&cli.BoolFlag{Name: "show-instances"},
			&cli.BoolFlag{Name: "show-utilization"},
			&cli.BoolFlag{Name: "show-workloads"},
			&cli.BoolFlag{Name: "show-costs"},
			&cli.BoolFlag{Name: "show-optimization"},
			&cli.StringFlag{Name: "timeframe", Aliases: []string{"T"}, Usage: "Utilization window (1h,3h,24h)", Value: "24h"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runDescribe(c) },
	}
}

func scaleCommand() *cli.Command {
	return &cli.Command{
		Name:  "scale",
		Usage: "Scale a nodegroup's desired/min/max size with optional health checks",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name"},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name", Required: true},
			&cli.IntFlag{Name: "desired", Usage: "Desired node count"},
			&cli.IntFlag{Name: "min", Usage: "Minimum node count"},
			&cli.IntFlag{Name: "max", Usage: "Maximum node count"},
			&cli.BoolFlag{Name: "health-check", Usage: "Validate cluster health before and after scaling"},
			&cli.BoolFlag{Name: "check-pdbs", Usage: "Validate Pod Disruption Budgets before scaling down"},
			&cli.BoolFlag{Name: "wait", Usage: "Wait for scaling operation to complete"},
			&cli.DurationFlag{Name: "op-timeout", Usage: "Scaling operation timeout", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "dry-run", Usage: "Preview scaling impact without executing"},
		},
		Action: func(c *cli.Context) error { return runScale(c) },
	}
}

func updateAMICommand() *cli.Command {
	return &cli.Command{
		Name:    "update",
		Aliases: []string{"update-ami"},
		Usage:   "Update the AMI for all or a specific node group (rolling by default)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or partial name pattern (overrides kubeconfig)", EnvVars: []string{"EKS_CLUSTER_NAME"}},
			&cli.StringFlag{Name: "nodegroup", Aliases: []string{"n"}, Usage: "Nodegroup name or partial name pattern (if not set, update all)"},
			&cli.BoolFlag{Name: "force", Aliases: []string{"f"}, Usage: "Force update if possible"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without executing them"},
			&cli.BoolFlag{Name: "no-wait", Aliases: []string{"w"}, Usage: "Don't wait for update completion (original behavior)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Minimal output mode"},
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Maximum time to wait for update completion", Value: 40 * time.Minute},
			&cli.DurationFlag{Name: "poll-interval", Aliases: []string{"p"}, Usage: "Polling interval for checking update status", Value: 15 * time.Second},
			&cli.BoolFlag{Name: "skip-health-check", Aliases: []string{"s"}, Usage: "Skip pre-flight health validation"},
			&cli.BoolFlag{Name: "health-only", Aliases: []string{"H"}, Usage: "Run health check only, don't update"},
		},
		Action: runUpdateAMI,
	}
}
