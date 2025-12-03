package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// DescribeNodegroupCommand creates the describe-nodegroup command
func DescribeNodegroupCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe-nodegroup",
		Usage:     "Describe a nodegroup with optional instances/utilization/cost info",
		ArgsUsage: "[cluster] [nodegroup]",
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
				Usage:    "EKS cluster name",
				Required: false,
			},
			&cli.StringFlag{
				Name:    "nodegroup",
				Aliases: []string{"n"},
				Usage:   "Nodegroup name (can be provided as second positional)",
			},
			&cli.BoolFlag{Name: "show-instances"},
			&cli.BoolFlag{Name: "show-utilization"},
			&cli.BoolFlag{Name: "show-workloads"},
			&cli.BoolFlag{Name: "show-costs"},
			&cli.BoolFlag{Name: "show-optimization"},
			&cli.StringFlag{Name: "timeframe", Aliases: []string{"T"}, Usage: "Utilization window (1h,3h,24h)", Value: "24h"},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml)",
				Value:   "table",
			},
		},
		Action: func(c *cli.Context) error {
			return runDescribeNodegroup(c)
		},
	}
}

func runDescribeNodegroup(c *cli.Context) error {
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

	// Positional cluster support; list clusters if omitted
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		svc := newClusterService(awsCfg, false, nil)
		start := time.Now()
		summaries, err := svc.List(ctx, cluster.ListOptions{})
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

	ngName := c.String("nodegroup")
	if strings.TrimSpace(ngName) == "" {
		// Derive from second positional arg skipping flags
		var nonFlags []string
		for _, tok := range c.Args().Slice() {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			nonFlags = append(nonFlags, tok)
		}
		if len(nonFlags) >= 2 {
			ngName = nonFlags[1]
		}
	}
	if strings.TrimSpace(ngName) == "" {
		return fmt.Errorf("missing nodegroup name; pass as second argument or --nodegroup <name>")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Health checker not required for describe (yet)
	svc := newNodegroupService(awsCfg, false, logger)

	opts := nodegroup.DescribeOptions{
		ShowInstances:    c.Bool("show-instances"),
		ShowUtilization:  c.Bool("show-utilization"),
		ShowWorkloads:    c.Bool("show-workloads"),
		ShowCosts:        c.Bool("show-costs"),
		ShowOptimization: c.Bool("show-optimization"),
		Timeframe:        c.String("timeframe"),
	}

	spinner := ui.NewFunSpinnerForCategory("nodegroup")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	start := time.Now()
	details, err := svc.Describe(ctx, clusterName, ngName, opts)
	spinner.Success("Nodegroup details gathered!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(details)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(details)
	default:
		return outputNodegroupDetailsTable(details, time.Since(start))
	}
}

func outputNodegroupDetailsTable(details *nodegroup.NodegroupDetails, elapsed time.Duration) error {
	ui.Outf("Nodegroup: %s\n", color.CyanString(details.Name))
	if details.Utilization.TimeRange != "" {
		ui.Outf("Retrieved in %s (utilization window %s)\n\n", color.GreenString("%.1fs", elapsed.Seconds()), details.Utilization.TimeRange)
	} else {
		ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))
	}

	// Main information table
	table := ui.NewDynamicTable()
	table.AddStatus("Status", details.Status).
		Add("Instance", details.InstanceType).
		Add("AMI Type", details.AmiType).
		Add("Capacity", details.CapacityType).
		Add("Current AMI", details.CurrentAMI).
		Add("Latest AMI", details.LatestAMI).
		AddColored("AMI Status", details.AMIStatus.String(), func(s string) string { return details.AMIStatus.String() }).
		Add("Scaling", fmt.Sprintf("%d desired (%d-%d)", details.Scaling.DesiredSize, details.Scaling.MinSize, details.Scaling.MaxSize))

	// Add optional utilization information
	if details.Utilization.TimeRange != "" || (details.Utilization.CPU.Average > 0 || details.Utilization.CPU.Current > 0) {
		avg := details.Utilization.CPU.Average
		cur := details.Utilization.CPU.Current
		peak := details.Utilization.CPU.Peak
		table.Add("CPU (avg)", fmt.Sprintf("%.1f%%", avg))
		if cur > 0 {
			table.Add("CPU (current)", fmt.Sprintf("%.1f%%", cur))
		}
		if peak > 0 {
			table.Add("CPU (peak)", fmt.Sprintf("%.1f%%", peak))
		}
	}

	// Add optional cost information
	if details.Cost.CostPerNode > 0 {
		table.Add("Cost per node", fmt.Sprintf("$%.0f/mo", details.Cost.CostPerNode))
	}
	if details.Cost.CurrentMonthlyCost > 0 {
		table.Add("Cost/month", fmt.Sprintf("$%.0f", details.Cost.CurrentMonthlyCost))
	}

	table.Render()

	// Optional workload information in separate section
	if details.Workloads.TotalPods > 0 || details.Workloads.PodDisruption != "" {
		workloadTable := ui.NewDynamicTable()
		workloadTable.Add("Total Pods", fmt.Sprintf("%d", details.Workloads.TotalPods)).
			Add("Critical Pods", fmt.Sprintf("%d", details.Workloads.CriticalPods)).
			Add("PDBs", details.Workloads.PodDisruption)
		workloadTable.RenderSection("Workloads")
	}

	// Optional instances
	if len(details.Instances) > 0 {
		ui.Outln()
		ui.Outln("Instances:")
		columns := []ui.Column{
			{Title: "INSTANCE ID", Min: 10, Max: 22, Align: ui.AlignLeft},
			{Title: "TYPE", Min: 10, Max: 0, Align: ui.AlignLeft},
			{Title: "LAUNCH", Min: 10, Max: 0, Align: ui.AlignLeft},
			{Title: "LIFECYCLE", Min: 9, Max: 0, Align: ui.AlignLeft},
			{Title: "STATE", Min: 8, Max: 0, Align: ui.AlignLeft},
		}
		table := ui.NewPTable(columns, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
		for _, inst := range details.Instances {
			table.AddRow(
				truncateString(inst.InstanceID, 22),
				inst.InstanceType,
				inst.LaunchTime.Format("2006-01-02"),
				inst.Lifecycle,
				inst.State,
			)
		}
		table.Render()
	}
	return nil
}
