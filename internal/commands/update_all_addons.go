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
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview changes without applying"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "Skip specific addons (can be repeated)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runUpdateAllAddons(c) },
	}
}

func runUpdateAllAddons(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
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

	// Build update options
	options := addons.UpdateAllOptions{
		DryRun:      c.Bool("dry-run"),
		Parallel:    c.Bool("parallel"),
		Wait:        c.Bool("wait"),
		WaitTimeout: c.Duration("wait-timeout"),
		SkipAddons:  c.StringSlice("skip"),
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
	fmt.Printf("Addon Updates for cluster: %s%s\n\n", color.CyanString(cluster), color.YellowString(mode))

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

	for _, r := range results {
		var status string
		if strings.Contains(r.Status, "FAILED") {
			status = color.RedString(r.Status)
			failCount++
		} else if r.Status == "DRY_RUN" {
			status = color.YellowString(r.Status)
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

	fmt.Println()
	if !dryRun {
		fmt.Printf("Summary: %s successful, %s failed\n",
			color.GreenString("%d", successCount),
			color.RedString("%d", failCount))
	}

	return nil
}
