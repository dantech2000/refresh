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
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// ListClustersCommand creates the list-clusters command
func ListClustersCommand() *cli.Command {
	return &cli.Command{
		Name:    "list-clusters",
		Aliases: []string{"lc"},
		Usage:   "List EKS clusters with health status (multi-region support)",
		Description: `Fast cluster discovery across regions with integrated health validation.
Direct EKS API calls provide high performance along with comprehensive
health monitoring and multi-region capabilities.`,
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout (e.g. 60s, 2m)",
				Value:   60 * time.Second,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.IntFlag{
				Name:    "max-concurrency",
				Aliases: []string{"C"},
				Usage:   "Max concurrent region requests (ListAllRegions)",
				Value:   appconfig.DefaultMaxConcurrency,
				EnvVars: []string{"REFRESH_MAX_CONCURRENCY"},
			},
			&cli.BoolFlag{
				Name:    "all-regions",
				Aliases: []string{"A"},
				Usage:   "Query all EKS-supported regions",
				Value:   false,
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort by field: name,status,version,region",
				Value: "name",
			},
			&cli.BoolFlag{
				Name:  "desc",
				Usage: "Sort descending",
				Value: false,
			},
			&cli.StringSliceFlag{
				Name:    "region",
				Aliases: []string{"r"},
				Usage:   "Specific region(s) to query (can be used multiple times)",
			},
			&cli.BoolFlag{
				Name:    "show-health",
				Aliases: []string{"H"},
				Usage:   "Include health status for each cluster",
				Value:   false,
			},
			&cli.StringSliceFlag{
				Name:    "filter",
				Aliases: []string{"f"},
				Usage:   "Filter clusters (format: key=value, e.g., name=prod, status=ACTIVE)",
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table, json, yaml, tree)",
				Value:   "table",
			},
			&cli.BoolFlag{
				Name:    "tree",
				Aliases: []string{"T"},
				Usage:   "Display results as hierarchical tree (implies --all-regions for best effect)",
				Value:   false,
			},
		},
		Action: func(c *cli.Context) error {
			return runListClusters(c)
		},
	}
}

func runListClusters(c *cli.Context) error {
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

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create cluster service
	clusterService := newClusterService(awsCfg, c.Bool("show-health"), logger)

	// Parse filters
	filters := make(map[string]string)
	for _, filter := range c.StringSlice("filter") {
		parts := strings.SplitN(filter, "=", 2)
		if len(parts) == 2 {
			filters[parts[0]] = parts[1]
		}
	}

	// Set up options
	// Tree view implies all-regions for best visual effect
	allRegions := c.Bool("all-regions") || c.Bool("tree") || c.String("format") == "tree"

	options := cluster.ListOptions{
		Regions:        c.StringSlice("region"),
		ShowHealth:     c.Bool("show-health"),
		Filters:        filters,
		AllRegions:     allRegions,
		MaxConcurrency: c.Int("max-concurrency"),
	}

	// Create spinner
	// Get cluster list with appropriate progress indicator
	startTime := time.Now()
	var summaries []cluster.ClusterSummary

	if allRegions || len(c.StringSlice("region")) > 0 {
		// Multi-region operation - use progress tracker
		summaries, err = runMultiRegionListWithProgress(ctx, clusterService, options)
	} else {
		// Single region operation - use simple spinner
		spinner := ui.NewProgressSpinner("Querying EKS clusters...")
		cancelSpinner := spinner.Start(ctx)
		defer cancelSpinner()

		summaries, err = clusterService.List(ctx, options)
		spinner.Stop("Cluster information gathered!")
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	// Apply sorting
	summaries = sortClusterSummaries(summaries, c.String("sort"), c.Bool("desc"))

	// Output results based on format
	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputClustersJSON(summaries)
	case "yaml":
		return outputClustersYAML(summaries)
	case "tree":
		return outputClustersTree(summaries, elapsed, allRegions, c.Bool("show-health"))
	default:
		// Check for --tree flag even with table format
		if c.Bool("tree") {
			return outputClustersTree(summaries, elapsed, allRegions, c.Bool("show-health"))
		}
		return outputClustersTable(summaries, elapsed, allRegions, c.Bool("show-health"))
	}
}

// runMultiRegionListWithProgress executes multi-region cluster listing with progress tracking
func runMultiRegionListWithProgress(ctx context.Context, clusterService cluster.Service, options cluster.ListOptions) ([]cluster.ClusterSummary, error) {
	// Determine which regions to query
	var regions []string
	if options.AllRegions {
		// Default regions by partition
		regionsCommercial := []string{
			"us-east-1", "us-east-2", "us-west-1", "us-west-2",
			"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
			"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
			"ca-central-1", "sa-east-1",
		}
		regions = regionsCommercial
	} else {
		regions = options.Regions
	}

	if len(regions) <= 1 {
		// Fall back to simple operation for single region
		return clusterService.ListAllRegions(ctx, options)
	}

	// Create fun spinner with cluster-specific entertaining messages
	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return nil, fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()

	// Use a channel to collect results from concurrent region queries
	type regionResult struct {
		region    string
		summaries []cluster.ClusterSummary
		err       error
	}

	resultChan := make(chan regionResult, len(regions))

	// Limit concurrency across regions to reduce throttling
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 8
	}
	sem := make(chan struct{}, maxConc)

	// Query each region concurrently
	for _, region := range regions {
		go func(r string) {
			sem <- struct{}{}
			defer func() { <-sem }()

			// Execute single-region query
			regionOptions := options
			regionOptions.Regions = []string{r}
			regionOptions.AllRegions = false

			summaries, err := clusterService.ListAllRegions(ctx, regionOptions)

			resultChan <- regionResult{
				region:    r,
				summaries: summaries,
				err:       err,
			}
		}(region)
	}

	// Collect results
	allSummaries := make([]cluster.ClusterSummary, 0)
	for i := 0; i < len(regions); i++ {
		result := <-resultChan
		if result.err != nil {
			slog.Warn("failed to list clusters in region", "region", result.region, "error", result.err)
			continue
		}
		allSummaries = append(allSummaries, result.summaries...)
	}

	// Complete the spinner with success message
	totalClusters := len(allSummaries)
	if totalClusters > 0 {
		spinner.Success(fmt.Sprintf("Found %d clusters across %d regions!", totalClusters, len(regions)))
	} else {
		spinner.Success("Search complete - no clusters found")
	}

	return allSummaries, nil
}

// outputClustersTree displays clusters in a hierarchical tree format
func outputClustersTree(summaries []cluster.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	// Group clusters by region for tree display
	regionGroups := make(map[string][]cluster.ClusterSummary)
	for _, summary := range summaries {
		region := summary.Region
		if region == "" {
			region = "unknown-region"
		}
		regionGroups[region] = append(regionGroups[region], summary)
	}

	// Build tree structure
	regionTree := ui.NewRegionTreeBuilder()

	// Sort regions for consistent display
	regions := make([]string, 0, len(regionGroups))
	for region := range regionGroups {
		regions = append(regions, region)
	}
	sort.Strings(regions)

	// Add each region and its clusters
	for _, region := range regions {
		clusters := regionGroups[region]
		regionTree.AddRegion(region, len(clusters))

		// Sort clusters within region
		sort.Slice(clusters, func(i, j int) bool {
			return clusters[i].Name < clusters[j].Name
		})

		// Add each cluster to the region
		for _, cluster := range clusters {
			status := cluster.Status
			if showHealth && cluster.Health != nil {
				// Enhance status with health info
				switch string(cluster.Health.Decision) {
				case "PROCEED":
					status = "HEALTHY"
				case "WARN":
					status = "WARNING"
				case "BLOCK":
					status = "CRITICAL"
				}
			}

			regionTree.AddClusterToRegion(cluster.Name, status, cluster.NodeCount.Ready)
		}

		regionTree.FinishRegion()
	}

	// Create header
	totalRegions := len(regions)
	totalClusters := len(summaries)

	var title string
	if multiRegion {
		title = fmt.Sprintf("EKS Clusters (%d regions, %d clusters)", totalRegions, totalClusters)
	} else {
		title = fmt.Sprintf("EKS Clusters (%d clusters)", totalClusters)
	}

	// Render tree with title
	err := regionTree.RenderWithTitle(title)
	if err != nil {
		return err
	}

	// Performance indicator
	fmt.Printf("\n%s\n", ui.FormatTreeSummary(totalClusters, "clusters", elapsed.Seconds()))

	// Summary statistics
	healthyCount := 0
	warningCount := 0
	criticalCount := 0

	if showHealth {
		for _, summary := range summaries {
			if summary.Health == nil {
				continue
			}
			switch string(summary.Health.Decision) {
			case "PROCEED":
				healthyCount++
			case "WARN":
				warningCount++
			case "BLOCK":
				criticalCount++
			}
		}

		fmt.Printf("\nHealth Summary: %s healthy, %s warnings, %s critical\n",
			color.GreenString("%d", healthyCount),
			color.YellowString("%d", warningCount),
			color.RedString("%d", criticalCount))
	}

	return nil
}

func outputClustersJSON(summaries []cluster.ClusterSummary) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(map[string]interface{}{
		"clusters": summaries,
		"count":    len(summaries),
	})
}

func outputClustersYAML(summaries []cluster.ClusterSummary) error {
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	defer func() {
		_ = encoder.Close() // Ignore close errors for output streams
	}()
	return encoder.Encode(map[string]interface{}{
		"clusters": summaries,
		"count":    len(summaries),
	})
}

func outputClustersTable(summaries []cluster.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	// Count regions
	regionCount := 1
	if multiRegion {
		regions := make(map[string]bool)
		for _, summary := range summaries {
			regions[summary.Region] = true
		}
		regionCount = len(regions)
	}

	// Header
	if multiRegion {
		fmt.Printf("EKS Clusters (%d regions, %d clusters)\n", regionCount, len(summaries))
	} else {
		fmt.Printf("EKS Clusters (%d clusters)\n", len(summaries))
	}

	// Performance indicator (formatted to one decimal place)
	fmt.Printf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	// Build table using shared renderer
	headerColor := func(s string) string { return color.CyanString(s) }
	if multiRegion {
		columns := []ui.Column{{Title: "CLUSTER", Min: 14, Max: 0, Align: ui.AlignLeft}, {Title: "REGION", Min: 10, Max: 0, Align: ui.AlignLeft}, {Title: "STATUS", Min: 7, Max: 0, Align: ui.AlignLeft}, {Title: "VERSION", Min: 7, Max: 0, Align: ui.AlignLeft}}
		if showHealth {
			columns = append(columns, ui.Column{Title: "HEALTH", Min: 8, Max: 0, Align: ui.AlignLeft})
		}
		columns = append(columns, ui.Column{Title: "READY/DESIRED", Min: 15, Max: 0, Align: ui.AlignRight})
		table := ui.NewPTable(columns, ui.WithPTableHeaderColor(headerColor))
		for _, summary := range summaries {
			nodes := formatNodeCount(summary.NodeCount)
			if showHealth {
				healthText := formatClusterHealth(summary.Health)
				table.AddRow(summary.Name, summary.Region, formatStatus(summary.Status), summary.Version, healthText, nodes)
			} else {
				table.AddRow(summary.Name, summary.Region, formatStatus(summary.Status), summary.Version, nodes)
			}
		}
		table.Render()
	} else {
		columns := []ui.Column{{Title: "CLUSTER", Min: 14, Max: 0, Align: ui.AlignLeft}, {Title: "STATUS", Min: 7, Max: 0, Align: ui.AlignLeft}, {Title: "VERSION", Min: 7, Max: 0, Align: ui.AlignLeft}}
		if showHealth {
			columns = append(columns, ui.Column{Title: "HEALTH", Min: 8, Max: 0, Align: ui.AlignLeft})
		}
		columns = append(columns, ui.Column{Title: "READY/DESIRED", Min: 15, Max: 0, Align: ui.AlignRight})
		table := ui.NewPTable(columns, ui.WithPTableHeaderColor(headerColor))
		for _, summary := range summaries {
			nodes := formatNodeCount(summary.NodeCount)
			if showHealth {
				healthText := formatClusterHealth(summary.Health)
				table.AddRow(summary.Name, formatStatus(summary.Status), summary.Version, healthText, nodes)
			} else {
				table.AddRow(summary.Name, formatStatus(summary.Status), summary.Version, nodes)
			}
		}
		table.Render()
	}

	// Count statuses for summary line
	healthyCount := 0
	warningCount := 0
	criticalCount := 0
	updatingCount := 0
	for _, summary := range summaries {
		if showHealth && summary.Health != nil {
			switch summary.Health.Decision {
			case health.DecisionProceed:
				healthyCount++
			case health.DecisionWarn:
				warningCount++
			case health.DecisionBlock:
				criticalCount++
			}
		}
		if strings.Contains(strings.ToUpper(summary.Status), "UPDAT") {
			updatingCount++
		}
	}

	// Table printed by renderer

	// Summary (only when health is requested)
	if showHealth {
		fmt.Printf("\nSummary: ")
		if healthyCount > 0 {
			fmt.Printf("%s healthy", color.GreenString("%d", healthyCount))
		}
		if warningCount > 0 {
			if healthyCount > 0 {
				fmt.Printf(", ")
			}
			fmt.Printf("%s warning", color.YellowString("%d", warningCount))
		}
		if criticalCount > 0 {
			if healthyCount > 0 || warningCount > 0 {
				fmt.Printf(", ")
			}
			fmt.Printf("%s critical", color.RedString("%d", criticalCount))
		}
		if updatingCount > 0 {
			if healthyCount > 0 || warningCount > 0 || criticalCount > 0 {
				fmt.Printf(", ")
			}
			fmt.Printf("%s updating", color.CyanString("%d", updatingCount))
		}
		fmt.Printf("\n")
	}

	return nil
}

// sortClusterSummaries sorts by the requested key
func sortClusterSummaries(items []cluster.ClusterSummary, key string, desc bool) []cluster.ClusterSummary {
	less := func(i, j int) bool { return false }
	switch strings.ToLower(key) {
	case "status":
		less = func(i, j int) bool { return items[i].Status < items[j].Status }
	case "version":
		less = func(i, j int) bool { return items[i].Version < items[j].Version }
	case "region":
		less = func(i, j int) bool { return items[i].Region < items[j].Region }
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

// deprecated helpers removed after migration to ui.Table

func formatClusterHealth(healthSummary *health.HealthSummary) string {
	if healthSummary == nil {
		return color.WhiteString("UNKNOWN")
	}

	switch healthSummary.Decision {
	case health.DecisionProceed:
		return color.GreenString("PASS")
	case health.DecisionWarn:
		return color.YellowString("WARN")
	case health.DecisionBlock:
		return color.RedString("FAIL")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatNodeCount(nodeCount cluster.NodeCountInfo) string {
	switch {
	case nodeCount.Total == 0:
		return "0/0 ready"
	case nodeCount.Ready == nodeCount.Total:
		return color.GreenString("%d/%d ready", nodeCount.Ready, nodeCount.Total)
	case nodeCount.Ready == 0:
		return color.RedString("%d/%d ready", nodeCount.Ready, nodeCount.Total)
	default:
		return color.YellowString("%d/%d ready", nodeCount.Ready, nodeCount.Total)
	}
}

// Utility functions are in utils.go

// (removed eksctl comparison helper)
