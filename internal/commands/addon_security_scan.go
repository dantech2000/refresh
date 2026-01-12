package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

// AddonSecurityScanCommand performs security analysis on EKS add-ons
func AddonSecurityScanCommand() *cli.Command {
	return &cli.Command{
		Name:      "addon-security-scan",
		Usage:     "Scan EKS add-ons for security issues",
		ArgsUsage: "[cluster]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.BoolFlag{Name: "check-outdated", Usage: "Check for outdated addons", Value: true},
			&cli.BoolFlag{Name: "check-vulnerabilities", Usage: "Check for known vulnerabilities", Value: true},
			&cli.BoolFlag{Name: "check-misconfigurations", Usage: "Check for misconfigurations", Value: true},
			&cli.StringFlag{Name: "min-severity", Usage: "Minimum severity to report (critical, high, medium, low, info)", Value: "low"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error { return runAddonSecurityScan(c) },
	}
}

func runAddonSecurityScan(c *cli.Context) error {
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

	// Build scan options
	options := addons.SecurityScanOptions{
		CheckOutdated:          c.Bool("check-outdated"),
		CheckVulnerabilities:   c.Bool("check-vulnerabilities"),
		CheckMisconfigurations: c.Bool("check-misconfigurations"),
		MinSeverity:            c.String("min-severity"),
	}

	// Perform scan
	result, err := addonSvc.SecurityScan(ctx, clusterName, options)
	spinner.Success("Security scan complete!")

	if err != nil {
		return err
	}

	// Output results
	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(result)
	default:
		return outputSecurityScanResults(result)
	}
}

func outputSecurityScanResults(result *addons.SecurityScanResult) error {
	fmt.Printf("Security Scan Results for cluster: %s\n", color.CyanString(result.ClusterName))
	fmt.Printf("Scanned at: %s\n\n", result.ScannedAt.Format("2006-01-02 15:04:05"))

	// Summary section
	fmt.Printf("%s\n", color.CyanString("SUMMARY"))
	fmt.Printf("  Total Addons:     %d\n", result.Summary.TotalAddons)
	fmt.Printf("  Scanned:          %d\n", result.Summary.ScannedAddons)
	fmt.Printf("  Outdated:         %s\n", colorCount(result.Summary.OutdatedCount, "yellow"))
	fmt.Println()

	// Severity breakdown
	fmt.Printf("%s\n", color.CyanString("FINDINGS BY SEVERITY"))
	fmt.Printf("  Critical:  %s\n", colorCount(result.Summary.CriticalCount, "red"))
	fmt.Printf("  High:      %s\n", colorCount(result.Summary.HighCount, "red"))
	fmt.Printf("  Medium:    %s\n", colorCount(result.Summary.MediumCount, "yellow"))
	fmt.Printf("  Low:       %s\n", colorCount(result.Summary.LowCount, "white"))
	fmt.Printf("  Info:      %s\n", colorCount(result.Summary.InfoCount, "cyan"))
	fmt.Println()

	// Findings table
	if len(result.Findings) == 0 {
		color.Green("No security issues found!")
		return nil
	}

	fmt.Printf("%s\n\n", color.CyanString("FINDINGS"))

	columns := []ui.Column{
		{Title: "SEVERITY", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "ADDON", Min: 15, Max: 25, Align: ui.AlignLeft},
		{Title: "CATEGORY", Min: 12, Max: 0, Align: ui.AlignLeft},
		{Title: "TITLE", Min: 20, Max: 50, Align: ui.AlignLeft},
	}
	table := ui.NewPTable(columns, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))

	for _, f := range result.Findings {
		severity := colorSeverity(f.Severity)
		table.AddRow(severity, f.AddonName, f.Category, f.Title)
	}
	table.Render()

	// Detailed findings
	fmt.Printf("\n%s\n\n", color.CyanString("DETAILED FINDINGS"))

	for i, f := range result.Findings {
		fmt.Printf("%d. [%s] %s - %s\n",
			i+1,
			colorSeverity(f.Severity),
			color.WhiteString(f.AddonName),
			f.Title)
		fmt.Printf("   %s\n", f.Description)
		if f.Remediation != "" {
			fmt.Printf("   %s %s\n", color.GreenString("Remediation:"), f.Remediation)
		}
		fmt.Println()
	}

	return nil
}

func colorSeverity(severity string) string {
	switch severity {
	case "critical":
		return color.New(color.FgRed, color.Bold).Sprintf("CRITICAL")
	case "high":
		return color.RedString("HIGH")
	case "medium":
		return color.YellowString("MEDIUM")
	case "low":
		return color.WhiteString("LOW")
	case "info":
		return color.CyanString("INFO")
	default:
		return severity
	}
}

func colorCount(count int, colorName string) string {
	if count == 0 {
		return color.GreenString("0")
	}
	switch colorName {
	case "red":
		return color.RedString("%d", count)
	case "yellow":
		return color.YellowString("%d", count)
	case "cyan":
		return color.CyanString("%d", count)
	default:
		return fmt.Sprintf("%d", count)
	}
}
