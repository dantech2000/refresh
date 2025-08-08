package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// ListNodegroupsCommand creates the list-nodegroups command
func ListNodegroupsCommand() *cli.Command {
	return &cli.Command{
		Name:      "list-nodegroups",
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
				Name:     "cluster",
				Aliases:  []string{"c"},
				Usage:    "EKS cluster name or pattern",
				Required: false,
			},
			&cli.BoolFlag{
				Name:    "show-health",
				Aliases: []string{"H"},
				Usage:   "Include health status for each nodegroup",
			},
			&cli.BoolFlag{
				Name:  "show-costs",
				Usage: "Include basic cost analysis (placeholder)",
			},
			&cli.BoolFlag{
				Name:  "show-utilization",
				Usage: "Include utilization metrics (placeholder)",
			},
			&cli.BoolFlag{
				Name:  "show-instances",
				Usage: "Include instance details (placeholder)",
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
				Value: false,
			},
			&cli.StringSliceFlag{
				Name:    "filter",
				Aliases: []string{"f"},
				Usage:   "Filter nodegroups (format: key=value, e.g., instanceType=m5.large)",
			},
		},
		Action: func(c *cli.Context) error {
			return runListNodegroups(c)
		},
	}
}

func runListNodegroups(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}

	if err := awsinternal.ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	// Positional cluster name support; list clusters if omitted
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		fmt.Println("No cluster specified. Available clusters:")
		fmt.Println()
		start := time.Now()
		svcList := newClusterService(awsCfg, false, nil)
		summaries, err := svcList.List(ctx, cluster.ListOptions{})
		if err != nil {
			return err
		}
		_ = outputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Health checker optional for list (disabled by default)
	svc := newNodegroupService(awsCfg, false, logger)

	filters := make(map[string]string)
	for _, f := range c.StringSlice("filter") {
		parts := strings.SplitN(f, "=", 2)
		if len(parts) == 2 {
			filters[parts[0]] = parts[1]
		}
	}
	opts := nodegroup.ListOptions{
		ShowHealth:      c.Bool("show-health"),
		ShowCosts:       c.Bool("show-costs"),
		ShowUtilization: c.Bool("show-utilization"),
		ShowInstances:   c.Bool("show-instances"),
		Filters:         filters,
		Timeframe:       c.String("timeframe"),
	}

	spinner := pin.New("Gathering nodegroup information...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)
	cancelSpinner := spinner.Start(ctx)
	defer cancelSpinner()

	start := time.Now()
	items, err := svc.List(ctx, clusterName, opts)
	spinner.Stop("Nodegroup information gathered!")
	if err != nil {
		return err
	}

	// Apply sorting for table and when unspecified
	if strings.ToLower(c.String("format")) == "table" || c.String("format") == "" {
		items = sortNodegroupSummaries(items, c.String("sort"), c.Bool("desc"))
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)})
	default:
		return outputNodegroupsTableWithWindow(clusterName, c.String("timeframe"), items, time.Since(start))
	}
}

func outputNodegroupsTableWithWindow(clusterName, timeframe string, items []nodegroup.NodegroupSummary, elapsed time.Duration) error {
	if len(items) == 0 {
		color.Yellow("No nodegroups found for cluster: %s", clusterName)
		return nil
	}
	fmt.Printf("Nodegroups for cluster: %s\n", clusterName)
	fmt.Printf("Retrieved in %s (utilization window %s)\n\n", color.GreenString("%.1fs", elapsed.Seconds()), timeframe)

	// Define columns; widths computed dynamically with caps
	columns := []ui.Column{
		{Title: "NAME", Min: 4, Max: 60, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "INSTANCE", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "NODES", Min: 7, Max: 0, Align: ui.AlignRight},
		{Title: "CPU%", Min: 0, Max: 0, Align: ui.AlignRight},
		{Title: "COST/MO", Min: 0, Max: 0, Align: ui.AlignRight},
	}
	table := ui.NewTable(columns, ui.WithHeaderColor(func(s string) string { return color.CyanString(s) }))

	for _, ng := range items {
		cpu := ""
		if ng.Metrics.CPU > 0 {
			cpu = fmt.Sprintf("%.0f%%", ng.Metrics.CPU)
		}
		cost := ""
		if ng.Cost.Monthly > 0 {
			cost = fmt.Sprintf("$%.0f", ng.Cost.Monthly)
		}
		table.AddRow(
			ng.Name,
			ng.Status,
			ng.InstanceType,
			fmt.Sprintf("%d/%d", ng.ReadyNodes, ng.DesiredSize),
			cpu,
			cost,
		)
	}
	table.Render()
	return nil
}

func outputNodegroupsTable(clusterName string, items []nodegroup.NodegroupSummary, elapsed time.Duration) error {
	// Default rendering with 24h if caller didn't provide
	return outputNodegroupsTableWithWindow(clusterName, "24h", items, elapsed)
}

// sortNodegroupSummaries sorts nodegroup list output
func sortNodegroupSummaries(items []nodegroup.NodegroupSummary, key string, desc bool) []nodegroup.NodegroupSummary {
	less := func(i, j int) bool { return false }
	switch strings.ToLower(key) {
	case "status":
		less = func(i, j int) bool { return items[i].Status < items[j].Status }
	case "instance":
		less = func(i, j int) bool { return items[i].InstanceType < items[j].InstanceType }
	case "nodes":
		less = func(i, j int) bool { return items[i].ReadyNodes < items[j].ReadyNodes }
	case "cpu":
		less = func(i, j int) bool { return items[i].Metrics.CPU < items[j].Metrics.CPU }
	case "cost":
		less = func(i, j int) bool { return items[i].Cost.Monthly < items[j].Cost.Monthly }
	default: // name
		less = func(i, j int) bool { return items[i].Name < items[j].Name }
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return !less(i, j)
		}
		return less(i, j)
	})
	return items
}
