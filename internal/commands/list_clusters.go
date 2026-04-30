package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
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
		ArgsUsage: "[name-pattern]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout (e.g. 60s, 2m)", Value: 60 * time.Second, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.IntFlag{Name: "max-concurrency", Aliases: []string{"C"}, Usage: "Max concurrent region requests", Value: appconfig.DefaultMaxConcurrency, EnvVars: []string{"REFRESH_MAX_CONCURRENCY"}},
			&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}, Usage: "Query all EKS-supported regions"},
			&cli.StringFlag{Name: "sort", Usage: "Sort by field: name,status,version,region", Value: "name"},
			&cli.BoolFlag{Name: "desc", Usage: "Sort descending"},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Specific region(s) to query (can be used multiple times)"},
			&cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health status for each cluster"},
			&cli.StringSliceFlag{Name: "filter", Aliases: []string{"f"}, Usage: "Filter clusters (format: key=value)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, tree)", Value: "table"},
			&cli.BoolFlag{Name: "tree", Aliases: []string{"T"}, Usage: "Display results as hierarchical tree (implies --all-regions)"},
		},
		Action: func(c *cli.Context) error { return runListClusters(c) },
	}
}

func runListClusters(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	clusterService := newClusterService(awsCfg, c.Bool("show-health"), logger)

	filters := make(map[string]string)
	for _, f := range c.StringSlice("filter") {
		if parts := strings.SplitN(f, "=", 2); len(parts) == 2 {
			filters[parts[0]] = parts[1]
		}
	}
	if pattern := strings.TrimSpace(c.Args().First()); pattern != "" {
		filters["name"] = pattern
	}

	allRegions := c.Bool("all-regions") || c.Bool("tree") || c.String("format") == "tree"
	options := cluster.ListOptions{
		Regions:        c.StringSlice("region"),
		ShowHealth:     c.Bool("show-health"),
		Filters:        filters,
		AllRegions:     allRegions,
		MaxConcurrency: c.Int("max-concurrency"),
	}

	startTime := time.Now()
	var summaries []cluster.ClusterSummary

	if allRegions || len(c.StringSlice("region")) > 0 {
		summaries, err = runMultiRegionListWithProgress(ctx, clusterService, options)
	} else {
		spinner := ui.NewFunSpinnerForCategory("cluster")
		if err := spinner.Start(); err != nil {
			return err
		}
		defer spinner.Stop()
		summaries, err = clusterService.List(ctx, options)
		spinner.Success("Cluster information gathered!")
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	summaries = sortClusterSummaries(summaries, c.String("sort"), c.Bool("desc"))

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputClustersJSON(summaries)
	case "yaml":
		return outputClustersYAML(summaries)
	case "tree":
		return outputClustersTree(summaries, elapsed, allRegions, c.Bool("show-health"))
	default:
		if c.Bool("tree") {
			return outputClustersTree(summaries, elapsed, allRegions, c.Bool("show-health"))
		}
		return outputClustersTable(summaries, elapsed, allRegions, c.Bool("show-health"))
	}
}

func runMultiRegionListWithProgress(ctx context.Context, clusterService cluster.Service, options cluster.ListOptions) ([]cluster.ClusterSummary, error) {
	var regions []string
	if options.AllRegions {
		regions = []string{
			"us-east-1", "us-east-2", "us-west-1", "us-west-2",
			"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
			"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
			"ca-central-1", "sa-east-1",
		}
	} else {
		regions = options.Regions
	}

	if len(regions) <= 1 {
		return clusterService.ListAllRegions(ctx, options)
	}

	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return nil, fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()

	type regionResult struct {
		region    string
		summaries []cluster.ClusterSummary
		err       error
	}
	resultChan := make(chan regionResult, len(regions))
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 8
	}
	sem := make(chan struct{}, maxConc)

	for _, region := range regions {
		go func(r string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			regionOptions := options
			regionOptions.Regions = []string{r}
			regionOptions.AllRegions = false
			sums, err := clusterService.ListAllRegions(ctx, regionOptions)
			resultChan <- regionResult{region: r, summaries: sums, err: err}
		}(region)
	}

	allSummaries := make([]cluster.ClusterSummary, 0)
	for i := 0; i < len(regions); i++ {
		result := <-resultChan
		if result.err != nil {
			slog.Warn("failed to list clusters in region", "region", result.region, "error", result.err)
			continue
		}
		allSummaries = append(allSummaries, result.summaries...)
	}

	if len(allSummaries) > 0 {
		spinner.Success(fmt.Sprintf("Found %d clusters across %d regions!", len(allSummaries), len(regions)))
	} else {
		spinner.Success("Search complete - no clusters found")
	}
	return allSummaries, nil
}
