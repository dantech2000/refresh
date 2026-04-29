package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

// UpdateAllAddonsCommand updates all EKS add-ons in a cluster
func UpdateAllAddonsCommand() *cli.Command {
	return &cli.Command{
		Name:      "update-all-addons",
		Usage:     "Update all EKS add-ons to their latest versions",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: 10 * time.Minute, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "parallel", Aliases: []string{"p"}, Usage: "Update addons in parallel (faster but riskier)"},
			&cli.BoolFlag{Name: "wait", Aliases: []string{"w"}, Usage: "Wait for each update to complete before proceeding"},
			&cli.DurationFlag{Name: "wait-timeout", Usage: "Timeout for waiting on each addon update", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "health-check", Aliases: []string{"H"}, Usage: "Verify each addon is ACTIVE before updating and validate version compatibility"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without applying"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "Skip specific addons (can be repeated)"},
			&cli.BoolFlag{Name: "dependency-order", Usage: "Update addons in dependency-safe order (vpc-cni → coredns/kube-proxy → others)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runUpdateAllAddons(c) },
	}
}

func runUpdateAllAddons(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		ui.Outln()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	// Resolve cluster name
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		return fmt.Errorf("cluster name is required")
	}
	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

	// Create addon service
	eksClient := eks.NewFromConfig(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	addonSvc := addons.NewService(eksClient, logger)

	// Start spinner
	spinner := ui.NewFunSpinnerForCategory("addon")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	if c.Bool("parallel") && c.Bool("dependency-order") {
		return fmt.Errorf("--parallel and --dependency-order cannot be used together: parallel execution defeats dependency ordering")
	}

	// Build update options
	options := addons.UpdateAllOptions{
		DryRun:          c.Bool("dry-run"),
		Parallel:        c.Bool("parallel"),
		Wait:            c.Bool("wait"),
		WaitTimeout:     c.Duration("wait-timeout"),
		SkipAddons:      c.StringSlice("skip"),
		DependencyOrder: c.Bool("dependency-order"),
		HealthCheck:     c.Bool("health-check"),
	}

	// Perform updates
	results, err := addonSvc.UpdateAll(ctx, clusterName, options)
	spinner.Success("Addon updates processed!")

	if err != nil {
		return err
	}

	// Output results
	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"cluster": clusterName,
			"dryRun":  options.DryRun,
			"results": results,
		})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{
			"cluster": clusterName,
			"dryRun":  options.DryRun,
			"results": results,
		})
	default:
		return outputUpdateAllResults(clusterName, results, options.DryRun)
	}
}

func outputUpdateAllResults(cluster string, results []addons.AddonUpdateResult, dryRun bool) error {
	mode := ""
	if dryRun {
		mode = " (DRY RUN)"
	}
	ui.Outf("Addon Updates for cluster: %s%s\n\n", color.CyanString(cluster), color.YellowString(mode))

	if len(results) == 0 {
		color.Yellow("No addons to update")
		return nil
	}

	columns := []ui.Column{
		{Title: "ADDON", Min: 20, Max: 30, Align: ui.AlignLeft},
		{Title: "PREVIOUS", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "NEW", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
	}
	table := ui.NewPTable(columns, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))

	successCount := 0
	failCount := 0

	warnCount := 0
	for _, r := range results {
		var status string
		if strings.Contains(r.Status, "FAILED") {
			status = color.RedString(r.Status)
			failCount++
		} else if r.Status == "DRY_RUN" {
			status = color.YellowString(r.Status)
		} else if r.Status == "COMPLETED_WITH_ISSUES" {
			status = color.YellowString(r.Status)
			warnCount++
		} else {
			status = color.GreenString(r.Status)
			successCount++
		}

		newVersion := r.NewVersion
		if r.NewVersion != r.PreviousVersion {
			newVersion = color.GreenString(r.NewVersion)
		}

		table.AddRow(r.AddonName, r.PreviousVersion, newVersion, status)
	}
	table.Render()

	ui.Outln()
	if !dryRun {
		summary := fmt.Sprintf("Summary: %s successful", color.GreenString("%d", successCount))
		if warnCount > 0 {
			summary += fmt.Sprintf(", %s with issues", color.YellowString("%d", warnCount))
		}
		summary += fmt.Sprintf(", %s failed", color.RedString("%d", failCount))
		ui.Outf("%s\n", summary)
	}

	return nil
}
