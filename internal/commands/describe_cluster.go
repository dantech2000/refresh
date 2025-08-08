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
	"github.com/yarlson/pin"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// DescribeClusterCommand creates the describe-cluster command
func DescribeClusterCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe-cluster",
		Aliases:   []string{"dc"},
		Usage:     "Describe comprehensive cluster information",
		ArgsUsage: "[cluster]",
		Description: `Get detailed information about an EKS cluster including networking,
security configuration, add-ons, and health status. Direct EKS API calls
provide fast, comprehensive results without CloudFormation dependency.`,
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
				Name:    "detailed",
				Aliases: []string{"d"},
				Usage:   "Show comprehensive information including networking and security",
				Value:   false,
			},
			&cli.BoolFlag{
				Name:    "show-health",
				Aliases: []string{"H"},
				Usage:   "Include health status from existing health framework",
				Value:   true,
			},
			&cli.BoolFlag{
				Name:    "show-security",
				Aliases: []string{"s"},
				Usage:   "Include security configuration analysis",
				Value:   false,
			},
			&cli.BoolFlag{
				Name:    "include-addons",
				Aliases: []string{"a"},
				Usage:   "Include EKS add-on information",
				Value:   true,
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml)",
				Value:   "table",
			},
		},
		Action: func(c *cli.Context) error {
			return runDescribeCluster(c)
		},
	}
}

func runDescribeCluster(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

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

	// Create logger (early, we might need it for listing when no cluster given)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Prefer positional arg as cluster name; fallback to --cluster flag
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}

	// If no cluster specified, list available clusters in the current region and exit
	if strings.TrimSpace(requested) == "" {
		// List clusters (no health) in current region
		clusterService := newClusterService(awsCfg, false, logger)
		start := time.Now()
		summaries, err := clusterService.List(ctx, cluster.ListOptions{})
		if err != nil {
			return err
		}
		fmt.Println("No cluster specified. Available clusters:")
		fmt.Println()
		_ = outputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}

	// Resolve cluster name (supports patterns)
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
		return err
	}

	// Create cluster service
	clusterService := newClusterService(awsCfg, c.Bool("show-health"), logger)

	// Set up options
	options := cluster.DescribeOptions{
		ShowHealth:    c.Bool("show-health"),
		ShowSecurity:  c.Bool("show-security") || c.Bool("detailed"),
		IncludeAddons: c.Bool("include-addons"),
		Detailed:      c.Bool("detailed"),
	}

	// Create spinner
	spinner := pin.New("Gathering cluster information...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
	)

	startSpinner := spinner.Start
	stopSpinner := spinner.Stop
	cancelSpinner := startSpinner(ctx)
	defer cancelSpinner()

	// Get cluster details
	startTime := time.Now()
	details, err := clusterService.Describe(ctx, clusterName, options)

	stopSpinner("Cluster information gathered!")
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	// Output results based on format
	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputJSON(details)
	case "yaml":
		return outputYAML(details)
	default:
		return outputTable(details, elapsed)
	}
}

func outputJSON(details *cluster.ClusterDetails) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(details)
}

func outputYAML(details *cluster.ClusterDetails) error {
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	defer func() {
		_ = encoder.Close() // Ignore close errors for output streams
	}()
	return encoder.Encode(details)
}

func outputTable(details *cluster.ClusterDetails, elapsed time.Duration) error {
	// Print main cluster information
	fmt.Printf("Cluster Information: %s\n", color.CyanString(details.Name))

	// Performance indicator (formatted to one decimal place)
	fmt.Printf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	// Basic information table
	printTableRow("Status", formatStatus(details.Status))
	printTableRow("Version", details.Version)
	printTableRow("Platform", details.PlatformVersion)
	printTableRow("Endpoint", truncateEndpoint(details.Endpoint))

	// Health status
	if details.Health != nil {
		printTableRow("Health", formatHealth(details.Health))
	}

	// Node information
	if len(details.Nodegroups) > 0 {
		totalNodes := int32(0)
		activeNodegroups := 0
		for _, ng := range details.Nodegroups {
			totalNodes += ng.ReadyNodes
			if ng.Status == "ACTIVE" {
				activeNodegroups++
			}
		}
		printTableRow("Nodegroups", fmt.Sprintf("%d active (%d nodes total)", activeNodegroups, totalNodes))
	}

	// Networking information
	if details.Networking.VpcId != "" {
		vpcInfo := details.Networking.VpcId
		if details.Networking.VpcCidr != "" {
			vpcInfo += fmt.Sprintf(" (%s)", details.Networking.VpcCidr)
		}
		printTableRow("VPC", vpcInfo)

		if len(details.Networking.SubnetIds) > 0 {
			printTableRow("Subnets", fmt.Sprintf("%d subnets", len(details.Networking.SubnetIds)))
		}

		if len(details.Networking.SecurityGroupIds) > 0 {
			printTableRow("Security Groups", fmt.Sprintf("%d groups", len(details.Networking.SecurityGroupIds)))
		}
	}

	// Security information
	loggingStatus := "Disabled"
	if len(details.Security.LoggingEnabled) > 0 {
		loggingStatus = strings.Join(details.Security.LoggingEnabled, ", ") + " enabled"
	}
	printTableRow("Logging", loggingStatus)

	encryptionStatus := color.RedString("DISABLED")
	if details.Security.EncryptionEnabled {
		encryptionStatus = color.GreenString("ENABLED") + " (at rest via KMS)"
	}
	printTableRow("Encryption", encryptionStatus)

	// Timestamps
	printTableRow("Created", details.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
	age := time.Since(details.CreatedAt)
	printTableRow("Age", formatAge(age))

	// Add-ons table
	if len(details.Addons) > 0 {
		fmt.Println("\nAdd-ons:")
		columns := []ui.Column{
			{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 8, Max: 0, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
			{Title: "HEALTH", Min: 8, Max: 0, Align: ui.AlignLeft},
		}
		table := ui.NewTable(columns, ui.WithHeaderColor(func(s string) string { return color.CyanString(s) }))
		for _, addon := range details.Addons {
			health := addon.Health
			if health == "" {
				health = "Unknown"
			}
			table.AddRow(
				truncateString(addon.Name, 24),
				addon.Version,
				addon.Status,
				formatAddonHealth(health),
			)
		}
		table.Render()
	}

	return nil
}

func printTableRow(key, value string) {
	coloredKey := color.YellowString(key)
	fmt.Printf("%s │ %s\n", padColoredString(coloredKey, 16), value)
}

// removed printAddonHeader/printAddonRow in favor of ui.Table

func formatStatus(status string) string {
	switch strings.ToUpper(status) {
	case "ACTIVE":
		return color.GreenString("Active")
	case "CREATING":
		return color.YellowString("Creating")
	case "UPDATING":
		return color.YellowString("Updating")
	case "DELETING":
		return color.RedString("Deleting")
	case "FAILED":
		return color.RedString("Failed")
	default:
		return status
	}
}

func formatHealth(healthSummary *health.HealthSummary) string {
	if healthSummary == nil {
		return color.WhiteString("UNKNOWN")
	}

	totalChecks := len(healthSummary.Results)
	passedChecks := 0
	for _, result := range healthSummary.Results {
		if result.Status == health.StatusPass {
			passedChecks++
		}
	}

	switch healthSummary.Decision {
	case health.DecisionProceed:
		return color.GreenString("PASS (%d/%d checks passed)",
			passedChecks, totalChecks)
	case health.DecisionWarn:
		return color.YellowString("WARN (%d issues)",
			len(healthSummary.Warnings)+len(healthSummary.Errors))
	case health.DecisionBlock:
		return color.RedString("FAIL (%d issues)",
			len(healthSummary.Errors))
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatAddonHealth(health string) string {
	switch health {
	case "Healthy":
		return color.GreenString("PASS")
	case "Issues":
		return color.RedString("FAIL")
	case "Failed":
		return color.RedString("FAIL")
	case "Updating":
		return color.CyanString("[IN PROGRESS]")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func truncateEndpoint(endpoint string) string {
	// Don't truncate endpoints in cluster info - they're important to see fully
	// EKS endpoints are typically around 70-80 characters
	if len(endpoint) > 120 {
		return endpoint[:117] + "..."
	}
	return endpoint
}

// Utility functions are in utils.go

func formatAge(duration time.Duration) string {
	days := int(duration.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	hours := int(duration.Hours())
	if hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d minutes", int(duration.Minutes()))
}
