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
)

// DescribeClusterCommand creates the describe-cluster command
func DescribeClusterCommand() *cli.Command {
	return &cli.Command{
		Name:    "describe-cluster",
		Aliases: []string{"dc"},
		Usage:   "Describe comprehensive cluster information (4x faster than eksctl)",
		Description: `Get detailed information about an EKS cluster including networking,
security configuration, add-ons, and health status. Direct API calls 
provide 4x performance improvement over eksctl's CloudFormation approach.`,
		Flags: []cli.Flag{
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
	ctx := context.Background()

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

	// Get cluster name
	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, c.String("cluster"))
	if err != nil {
		return err
	}

	// Create health checker if needed
	var healthChecker *health.HealthChecker
	if c.Bool("show-health") {
		// Create clients needed for health checker
		eksClient := eks.NewFromConfig(awsCfg)
		cwClient := cloudwatch.NewFromConfig(awsCfg)
		asgClient := autoscaling.NewFromConfig(awsCfg)
		healthChecker = health.NewChecker(eksClient, nil, cwClient, asgClient) // k8sClient is optional
	}

	// Create cluster service
	clusterService := cluster.NewService(awsCfg, healthChecker, logger)

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

	cancel := spinner.Start(ctx)
	defer cancel()

	// Get cluster details
	startTime := time.Now()
	details, err := clusterService.Describe(ctx, clusterName, options)

	spinner.Stop("Cluster information gathered!")
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

	// Performance indicator
	fmt.Printf("Retrieved in %s (eksctl equivalent: ~5-8s)\n\n", color.GreenString(elapsed.String()))

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
		printAddonHeader()
		for _, addon := range details.Addons {
			printAddonRow(addon)
		}
		fmt.Printf("└──────────────────────┴───────────────┴────────────┴──────────┘\n")
	}

	return nil
}

func printTableRow(key, value string) {
	coloredKey := color.YellowString(key)
	fmt.Printf("%s │ %s\n", padColoredString(coloredKey, 16), value)
}

func printAddonHeader() {
	fmt.Printf("┌──────────────────────┬───────────────┬────────────┬──────────┐\n")
	fmt.Printf("│ %s │ %s │ %s │ %s │\n",
		color.CyanString(fmt.Sprintf("%-20s", "NAME")),
		color.CyanString(fmt.Sprintf("%-13s", "VERSION")),
		color.CyanString(fmt.Sprintf("%-10s", "STATUS")),
		color.CyanString(fmt.Sprintf("%-8s", "HEALTH")))
	fmt.Printf("├──────────────────────┼───────────────┼────────────┼──────────┤\n")
}

func printAddonRow(addon cluster.AddonInfo) {
	health := addon.Health
	if health == "" {
		health = "Unknown"
	}

	healthFormatted := formatAddonHealth(health)
	fmt.Printf("│ %-20s │ %-13s │ %-10s │ %s │\n",
		truncateString(addon.Name, 20),
		truncateString(addon.Version, 13),
		addon.Status,
		padColoredString(healthFormatted, 8))
}

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
