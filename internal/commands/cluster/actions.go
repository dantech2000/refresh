package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/dantech2000/refresh/internal/commands/factory"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
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
	clusterService := factory.NewClusterService(awsCfg, c.Bool("show-health"), logger)

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
	options := clustersvc.ListOptions{
		Regions:        c.StringSlice("region"),
		ShowHealth:     c.Bool("show-health"),
		Filters:        filters,
		AllRegions:     allRegions,
		MaxConcurrency: c.Int("max-concurrency"),
	}

	startTime := time.Now()
	var summaries []clustersvc.ClusterSummary

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
		return OutputClustersTable(summaries, elapsed, allRegions, c.Bool("show-health"))
	}
}

func runDescribe(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	awsCfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		ui.Outln()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		svc := factory.NewClusterService(awsCfg, false, logger)
		start := time.Now()
		summaries, err := svc.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		return OutputClustersTable(summaries, time.Since(start), false, false)
	}

	clusterName, err := awsinternal.ClusterName(ctx, awsCfg, requested)
	if err != nil {
		return err
	}

	clusterService := factory.NewClusterService(awsCfg, c.Bool("show-health"), logger)
	options := clustersvc.DescribeOptions{
		ShowHealth:    c.Bool("show-health"),
		ShowSecurity:  c.Bool("show-security") || c.Bool("detailed"),
		IncludeAddons: c.Bool("include-addons"),
		Detailed:      c.Bool("detailed"),
	}

	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	startTime := time.Now()
	details, err := clusterService.Describe(ctx, clusterName, options)
	spinner.Success("Cluster information gathered!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputClusterDetailsJSON(details)
	case "yaml":
		return outputClusterDetailsYAML(details)
	default:
		return outputClusterDetailsTable(details, time.Since(startTime))
	}
}

func runDiff(c *cli.Context) error {
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
	clusterService := factory.NewClusterService(awsCfg, true, logger)

	clusterNames, err := resolveCompareClusterNames(ctx, awsCfg, clusterPatterns, c.Bool("interactive"))
	if err != nil {
		return err
	}

	options := clustersvc.CompareOptions{
		ShowDifferencesOnly: c.Bool("show-differences"),
		Include:             c.StringSlice("include"),
		Format:              c.String("format"),
	}

	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	startTime := time.Now()
	comparison, err := clusterService.Compare(ctx, clusterNames, options)
	spinner.Success("Analysis complete!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		return outputComparisonJSON(comparison)
	case "yaml":
		return outputComparisonYAML(comparison)
	default:
		return outputComparisonTable(comparison, time.Since(startTime))
	}
}

// resolveCompareClusterNames resolves each pattern to one or more cluster names.
// When a pattern is ambiguous it either launches an interactive multi-select or
// returns an error directing the user to use --interactive.
func resolveCompareClusterNames(ctx context.Context, awsCfg aws.Config, patterns []string, interactive bool) ([]string, error) {
	all, err := awsinternal.AvailableClusters(ctx, awsCfg)
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "listing EKS clusters")
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no EKS clusters found in current region")
	}

	seen := make(map[string]bool)
	var candidates []string
	ambiguous := false

	for _, pat := range patterns {
		matches := awsinternal.MatchingClusters(all, pat)
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("no clusters found matching pattern %q", pat)
		case 1:
			if !seen[matches[0]] {
				seen[matches[0]] = true
				candidates = append(candidates, matches[0])
			}
		default:
			ambiguous = true
			for _, m := range matches {
				if !seen[m] {
					seen[m] = true
					candidates = append(candidates, m)
				}
			}
		}
	}

	if ambiguous || interactive {
		return interactiveSelectClusters(candidates)
	}

	if len(candidates) < 2 {
		return nil, fmt.Errorf("need at least 2 unique clusters to compare (patterns resolved to %d)", len(candidates))
	}
	return candidates, nil
}

func interactiveSelectClusters(candidates []string) ([]string, error) {
	color.Cyan("Select clusters to compare (space to toggle, enter to confirm):")
	selected, err := pterm.DefaultInteractiveMultiselect.
		WithOptions(candidates).
		WithMaxHeight(15).
		Show()
	if err != nil {
		return nil, fmt.Errorf("cluster selection cancelled: %w", err)
	}
	if len(selected) < 2 {
		return nil, fmt.Errorf("select at least 2 clusters to compare (got %d)", len(selected))
	}
	return selected, nil
}

func runMultiRegionListWithProgress(ctx context.Context, clusterService clustersvc.Service, options clustersvc.ListOptions) ([]clustersvc.ClusterSummary, error) {
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
		summaries []clustersvc.ClusterSummary
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

	allSummaries := make([]clustersvc.ClusterSummary, 0)
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
