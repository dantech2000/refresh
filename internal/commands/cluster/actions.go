package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterService := factory.NewClusterService(awsCfg, c.Bool("show-health"), nil)

	filters := runner.ParseFilters(c.StringSlice("filter"))
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
		err = runner.WithSpinner("cluster", "Cluster information gathered!", func() error {
			var lerr error
			summaries, lerr = clusterService.List(ctx, options)
			return lerr
		})
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(startTime)

	summaries = clusterview.SortClusterSummaries(summaries, c.String("sort"), c.Bool("desc"))

	format := strings.ToLower(c.String("format"))
	if format == "tree" || (format == "" && c.Bool("tree")) {
		return clusterview.OutputClustersTree(summaries, elapsed, allRegions, c.Bool("show-health"))
	}
	payload := map[string]any{"clusters": summaries, "count": len(summaries)}
	if handled, err := runner.EncodeStdout(c.String("format"), payload); handled {
		return err
	}
	return clusterview.OutputClustersTable(summaries, elapsed, allRegions, c.Bool("show-health"))
}

func runDescribe(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, c)
	if err != nil || listed {
		return err
	}

	clusterService := factory.NewClusterService(awsCfg, c.Bool("show-health"), nil)
	options := clustersvc.DescribeOptions{
		ShowHealth:    c.Bool("show-health"),
		ShowSecurity:  c.Bool("show-security") || c.Bool("detailed"),
		IncludeAddons: c.Bool("include-addons"),
		Detailed:      c.Bool("detailed"),
	}

	var details *clustersvc.ClusterDetails
	startTime := time.Now()
	if err := runner.WithSpinner("cluster", "Cluster information gathered!", func() error {
		var derr error
		details, derr = clusterService.Describe(ctx, clusterName, options)
		return derr
	}); err != nil {
		return err
	}

	if handled, err := runner.EncodeStdout(c.String("format"), details); handled {
		return err
	}
	return clusterview.OutputClusterDetailsTable(details, time.Since(startTime))
}

func runDiff(c *cli.Context) error {
	ctx, cancel, awsCfg, err := runner.SetupAWSWithTimeout(c, 60*time.Second)
	if err != nil {
		return err
	}
	defer cancel()

	clusterPatterns := c.StringSlice("cluster")
	if len(clusterPatterns) < 2 {
		return fmt.Errorf("need at least 2 clusters to compare, use --cluster flag multiple times")
	}

	clusterService := factory.NewClusterService(awsCfg, true, nil)

	clusterNames, err := resolveCompareClusterNames(ctx, awsCfg, clusterPatterns, c.Bool("interactive"))
	if err != nil {
		return err
	}

	options := clustersvc.CompareOptions{
		ShowDifferencesOnly: c.Bool("show-differences"),
		Include:             c.StringSlice("include"),
		Format:              c.String("format"),
	}

	var comparison *clustersvc.ClusterComparison
	startTime := time.Now()
	if err := runner.WithSpinner("cluster", "Analysis complete!", func() error {
		var cerr error
		comparison, cerr = clusterService.Compare(ctx, clusterNames, options)
		return cerr
	}); err != nil {
		return err
	}

	if handled, err := runner.EncodeStdout(c.String("format"), comparison); handled {
		return err
	}
	return clusterview.OutputComparisonTable(comparison, time.Since(startTime))
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

func runMultiRegionListWithProgress(ctx context.Context, clusterService *clustersvc.ServiceImpl, options clustersvc.ListOptions) ([]clustersvc.ClusterSummary, error) {
	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return nil, fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()

	summaries, regionsQueried, err := clusterService.ListAllRegionsWithMeta(ctx, options)
	if err != nil {
		return nil, err
	}

	if len(summaries) > 0 {
		spinner.Success(fmt.Sprintf("Found %d clusters across %d regions!", len(summaries), regionsQueried))
	} else {
		spinner.Success("Search complete - no clusters found")
	}
	return summaries, nil
}
