package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

// allowedClusterFilterKeys are the --filter keys cluster list understands.
// "name" is applied at the list stage; "status"/"version" need the per-cluster
// summary and are applied afterwards.
var allowedClusterFilterKeys = map[string]bool{"name": true, "status": true, "version": true}

// validateClusterFilters rejects unsupported --filter keys so a typo like
// `--filter staus=ACTIVE` errors instead of silently returning everything.
func validateClusterFilters(filters map[string]string) error {
	var unknown []string
	for k := range filters {
		if !allowedClusterFilterKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unsupported filter key(s): %s (supported: name, status, version)", strings.Join(unknown, ", "))
	}
	return nil
}

func runList(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsWithTree); err != nil {
		return err
	}
	if err := validateClusterFilters(runner.ParseFilters(cmd.StringSlice("filter"))); err != nil {
		return err
	}
	// Each --watch iteration performs the full setup+fetch+render cycle so a
	// fresh service (and cache) is used every time.
	return runner.Watch(cmd, func() error { return listClustersOnce(ctx, cmd) })
}

func listClustersOnce(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterService := factory.NewClusterService(awsCfg, cmd.Bool("show-health"), nil)

	filters := runner.ParseFilters(cmd.StringSlice("filter"))
	if pattern := strings.TrimSpace(cmd.Args().First()); pattern != "" {
		filters["name"] = pattern
	}

	allRegions := cmd.Bool("all-regions") || cmd.Bool("tree") || cmd.String("format") == "tree"
	options := clustersvc.ListOptions{
		Regions:        cmd.StringSlice("region"),
		ShowHealth:     cmd.Bool("show-health"),
		Filters:        filters,
		AllRegions:     allRegions,
		MaxConcurrency: cmd.Int("max-concurrency"),
	}

	startTime := time.Now()
	var summaries []clustersvc.ClusterSummary
	if allRegions || len(cmd.StringSlice("region")) > 0 {
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

	summaries = clusterview.SortClusterSummaries(summaries, cmd.String("sort"), cmd.Bool("desc"))

	format := strings.ToLower(cmd.String("format"))
	if format == "tree" || (format == "" && cmd.Bool("tree")) {
		return clusterview.OutputClustersTree(summaries, elapsed, allRegions, cmd.Bool("show-health"))
	}
	payload := map[string]any{"clusters": summaries, "count": len(summaries)}
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		return err
	}
	return clusterview.OutputClustersTable(summaries, elapsed, allRegions, cmd.Bool("show-health"))
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	// --check-readiness measures real Kubernetes Ready node counts (per
	// nodegroup) via the cluster API instead of leaving NODES at desired-only.
	// When unreachable, the kube client is nil and readiness stays honestly
	// unknown. It also gives the health checks a real k8s client. (REF-130)
	var clusterService *clustersvc.ServiceImpl
	if cmd.Bool("check-readiness") {
		humanOutput := strings.EqualFold(cmd.String("format"), "table")
		k8sClient := resolveReadinessKubeClient(ctx, cmd.String("kubeconfig"), humanOutput)
		// With cluster access, also wire metrics-server (best-effort) so the
		// health card's live-utilization check measures instead of skipping. (REF-146)
		var metricsClient health.NodeMetricsLister
		if k8sClient != nil {
			if m, err := health.BuildMetricsClient(cmd.String("kubeconfig")); err == nil {
				metricsClient = m
			}
		}
		clusterService = factory.NewClusterServiceWithHealth(awsCfg, k8sClient, metricsClient, nil)
	} else {
		clusterService = factory.NewClusterService(awsCfg, cmd.Bool("show-health"), nil)
	}
	options := clustersvc.DescribeOptions{
		ShowHealth:    cmd.Bool("show-health"),
		ShowSecurity:  cmd.Bool("show-security") || cmd.Bool("detailed"),
		IncludeAddons: cmd.Bool("include-addons"),
		Detailed:      cmd.Bool("detailed"),
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

	// Support posture for the cluster's version, via the same resolver behind
	// `refresh status` (REF-145).
	if details != nil && details.Version != "" {
		posture := status.NewSupportResolver(eks.NewFromConfig(awsCfg)).Resolve(ctx, details.Version)
		details.Support = &posture
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
		return err
	}
	return clusterview.OutputClusterDetailsTable(details, time.Since(startTime))
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
