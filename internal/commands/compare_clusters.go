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
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"github.com/yarlson/pin"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// CompareClustersCommand creates the compare-clusters command
func CompareClustersCommand() *cli.Command {
	return &cli.Command{
		Name:    "compare-clusters",
		Aliases: []string{"cc"},
		Usage:   "Compare EKS clusters side-by-side for consistency validation",
		Description: `Analyze configuration differences between EKS clusters to ensure
consistency across environments. Supports comparison of networking, 
security, add-ons, and version configurations.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:     "cluster",
				Aliases:  []string{"c"},
				Usage:    "Cluster name or pattern (specify multiple times)",
				Required: true,
			},
			&cli.BoolFlag{
				Name:  "interactive",
				Usage: "Interactively select clusters when multiple patterns match",
				Value: false,
			},
			&cli.BoolFlag{
				Name:    "show-differences",
				Aliases: []string{"d"},
				Usage:   "Show only differences (hide identical configurations)",
				Value:   false,
			},
			&cli.StringSliceFlag{
				Name:    "include",
				Aliases: []string{"i"},
				Usage:   "Compare specific aspects (networking, security, addons, versions)",
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml)",
				Value:   "table",
			},
		},
		Action: func(c *cli.Context) error {
			return runCompareClusters(c)
		},
	}
}

func runCompareClusters(c *cli.Context) error {
	// Honor global timeout if provided at app level; fallback to 60s
	timeout := c.Duration("timeout")
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	clusterPatterns := c.StringSlice("cluster")
	if len(clusterPatterns) < 2 {
		return fmt.Errorf("need at least 2 clusters to compare, use --cluster flag multiple times")
	}

	// Load AWS config
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}

	// Validate AWS credentials early to provide better error messages
	if err := awsinternal.ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn, // Only show warnings and errors
	}))

	// Create health checker
	eksClient := eks.NewFromConfig(awsCfg)
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	asgClient := autoscaling.NewFromConfig(awsCfg)
	healthChecker := health.NewChecker(eksClient, nil, cwClient, asgClient) // k8sClient is optional

	// Create cluster service
	clusterService := cluster.NewService(awsCfg, healthChecker, logger)

	// Resolve cluster names
	var clusterNames []string
	for _, pattern := range clusterPatterns {
		// In interactive mode we allow ambiguous patterns and prompt via ClusterName helper
		clusterName, err := awsinternal.ClusterName(ctx, awsCfg, pattern)
		if err != nil {
			return fmt.Errorf("failed to resolve cluster '%s': %w", pattern, err)
		}
		clusterNames = append(clusterNames, clusterName)
	}

	// Remove duplicates
	clusterNames = removeDuplicates(clusterNames)
	if len(clusterNames) < 2 {
		return fmt.Errorf("resolved clusters contain duplicates, need at least 2 unique clusters")
	}

	// Set up options
	options := cluster.CompareOptions{
		ShowDifferencesOnly: c.Bool("show-differences"),
		Include:             c.StringSlice("include"),
		Format:              c.String("format"),
	}

	// Create spinner
	spinner := pin.New("Analyzing cluster configurations...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)

	startSpinner := spinner.Start
	stopSpinner := spinner.Stop
	cancelSpinner := startSpinner(ctx)
	defer cancelSpinner()

	// Perform comparison
	startTime := time.Now()
	comparison, err := clusterService.Compare(ctx, clusterNames, options)

	stopSpinner("Analysis complete!")
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	// Output results based on format
	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputComparisonJSON(comparison)
	case "yaml":
		return outputComparisonYAML(comparison)
	default:
		return outputComparisonTable(comparison, elapsed)
	}
}

func outputComparisonJSON(comparison *cluster.ClusterComparison) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(comparison)
}

func outputComparisonYAML(comparison *cluster.ClusterComparison) error {
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	defer func() {
		_ = encoder.Close() // Ignore close errors for output streams
	}()
	return encoder.Encode(comparison)
}

func outputComparisonTable(comparison *cluster.ClusterComparison, elapsed time.Duration) error {
	// Header
	clusterNames := make([]string, len(comparison.Clusters))
	for i, c := range comparison.Clusters {
		clusterNames[i] = c.Name
	}

	fmt.Printf("Cluster Comparison: %s\n", color.CyanString(strings.Join(clusterNames, " vs ")))
	fmt.Printf("Analyzed in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	// Summary
	summary := comparison.Summary
	fmt.Printf("Comparison Summary:\n")
	printTableRow("Total Differences", fmt.Sprintf("%d", summary.TotalDifferences))
	printTableRow("Critical Issues", formatDifferenceCount(summary.CriticalDifferences, "critical"))
	printTableRow("Warnings", formatDifferenceCount(summary.WarningDifferences, "warning"))
	printTableRow("Informational", formatDifferenceCount(summary.InfoDifferences, "info"))

	equivalent := color.RedString("NO")
	if summary.ClustersAreEquivalent {
		equivalent = color.GreenString("YES")
	}
	printTableRow("Equivalent", equivalent)
	fmt.Println()

	// If no differences, we're done
	if len(comparison.Differences) == 0 {
		color.Green("PASS: Clusters are identical in all analyzed aspects")
		return nil
	}

	// Basic cluster information comparison
	fmt.Printf("Basic Information:\n")
	columns := []ui.Column{
		{Title: "CLUSTER", Min: 14, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 7, Max: 0, Align: ui.AlignLeft},
		{Title: "VERSION", Min: 7, Max: 0, Align: ui.AlignLeft},
		{Title: "HEALTH", Min: 15, Max: 0, Align: ui.AlignLeft},
	}
	table := ui.NewTable(columns, ui.WithHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, cl := range comparison.Clusters {
		healthStatus := color.WhiteString("UNKNOWN")
		if cl.Health != nil {
			switch cl.Health.Decision {
			case health.DecisionProceed:
				healthStatus = color.GreenString("PASS")
			case health.DecisionWarn:
				healthStatus = color.YellowString("WARN")
			case health.DecisionBlock:
				healthStatus = color.RedString("FAIL")
			}
		}
		table.AddRow(
			truncateString(cl.Name, 14),
			formatStatus(cl.Status),
			cl.Version,
			healthStatus,
		)
	}
	table.Render()
	fmt.Println()

	// Detailed differences
	if len(comparison.Differences) > 0 {
		fmt.Printf("Configuration Differences:\n\n")

		criticalDiffs := filterDifferencesBySeverity(comparison.Differences, "critical")
		warningDiffs := filterDifferencesBySeverity(comparison.Differences, "warning")
		infoDiffs := filterDifferencesBySeverity(comparison.Differences, "info")

		if len(criticalDiffs) > 0 {
			fmt.Printf("%s Critical Issues:\n", color.RedString("[CRITICAL]"))
			printDifferences(criticalDiffs)
			fmt.Println()
		}

		if len(warningDiffs) > 0 {
			fmt.Printf("%s Warnings:\n", color.YellowString("[WARNING]"))
			printDifferences(warningDiffs)
			fmt.Println()
		}

		if len(infoDiffs) > 0 {
			fmt.Printf("%s Information:\n", color.BlueString("[INFO]"))
			printDifferences(infoDiffs)
			fmt.Println()
		}
	}

	// Recommendations
	if summary.CriticalDifferences > 0 {
		color.Red("\n[CRITICAL] Action Required:")
		color.Red("Critical differences detected that may affect cluster security or functionality.")
		color.Red("Review and address these issues before proceeding with production workloads.")
	} else if summary.WarningDifferences > 0 {
		color.Yellow("\n[WARNING] Consider Review:")
		color.Yellow("Configuration differences detected that may affect consistency.")
		color.Yellow("Review these differences to ensure they are intentional.")
	} else {
		color.Green("\n[PASS] Analysis Complete:")
		color.Green("Only informational differences found. Clusters are functionally equivalent.")
	}

	return nil
}

// migrated basic info table to ui.Table

func printDifferences(differences []cluster.Difference) {
	for _, diff := range differences {
		// Print difference header
		severity := ""
		switch diff.Severity {
		case "critical":
			severity = color.RedString("[CRITICAL]")
		case "warning":
			severity = color.YellowString("[WARNING]")
		case "info":
			severity = color.BlueString("[INFO]")
		}

		fmt.Printf("  %s %s: %s\n", severity, color.YellowString(diff.Field), diff.Description)

		// Print values for each cluster
		for _, valuePair := range diff.Values {
			fmt.Printf("    • %s: %v\n", color.CyanString(valuePair.ClusterName), valuePair.Value)
		}
		fmt.Println()
	}
}

func formatDifferenceCount(count int, severity string) string {
	if count == 0 {
		return "0"
	}

	switch severity {
	case "critical":
		return color.RedString("%d", count)
	case "warning":
		return color.YellowString("%d", count)
	case "info":
		return color.BlueString("%d", count)
	default:
		return fmt.Sprintf("%d", count)
	}
}

func filterDifferencesBySeverity(differences []cluster.Difference, severity string) []cluster.Difference {
	var filtered []cluster.Difference
	for _, diff := range differences {
		if diff.Severity == severity {
			filtered = append(filtered, diff)
		}
	}
	return filtered
}

func removeDuplicates(slice []string) []string {
	keys := make(map[string]bool)
	var result []string

	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}

	return result
}
