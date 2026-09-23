package cluster

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
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
	return runner.Watch(ctx, cmd, func() error { return listClustersOnce(ctx, cmd) })
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

	format := strings.ToLower(strings.TrimSpace(cmd.String("format")))
	tree := wantsTree(format, cmd.Bool("tree"), cmd.IsSet("format"))
	allRegions := cmd.Bool("all-regions") || tree
	// runner.Regions honors a global `refresh --region X cluster list` too
	// (not with -A/--tree, where it only picks the home region/partition).
	regions := runner.Regions(cmd, allRegions)
	options := clustersvc.ListOptions{
		Regions:        regions,
		ShowHealth:     cmd.Bool("show-health"),
		Filters:        filters,
		AllRegions:     allRegions,
		MaxConcurrency: appconfig.ClampMaxConcurrency(cmd.Int("max-concurrency")),
	}

	startTime := time.Now()
	var summaries []clustersvc.ClusterSummary
	if allRegions || len(regions) > 0 {
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
	warnClusterRows(ui.Stderr, summaries)

	if tree {
		return clusterview.OutputClustersTree(summaries, elapsed, allRegions, cmd.Bool("show-health"))
	}
	if summaries == nil {
		summaries = []clustersvc.ClusterSummary{} // -o json|yaml: [], not null
	}
	payload := map[string]any{"clusters": summaries, "count": len(summaries)}
	if handled, err := runner.EncodeStdout(format, payload); handled {
		return err
	}
	return clusterview.OutputClustersTable(summaries, elapsed, allRegions, cmd.Bool("show-health"))
}

// wantsTree reports whether cluster list renders the region tree: -o tree,
// or --tree when -o was not given (--format defaults to "table", so an
// explicit -o json|yaml|plain wins over --tree).
func wantsTree(format string, treeFlag, formatSet bool) bool {
	return format == "tree" || (treeFlag && !formatSet)
}

// warnClusterRows writes one stderr warning per partial failure in the rows
// (a cluster or nodegroup that could not be read), so a row with missing data
// is never mistaken for a complete one.
func warnClusterRows(w io.Writer, summaries []clustersvc.ClusterSummary) {
	for _, s := range summaries {
		for _, msg := range s.Warnings {
			_, _ = fmt.Fprintln(w, ui.StderrColor(color.FgYellow).Sprintf("warning: cluster %s (%s): %s", s.Name, s.Region, msg))
		}
	}
}

// reportRegionSweep writes the multi-region sweep's partial problems to w:
// one line naming the skipped regions and one single-line warning per failed
// region. Partial success keeps exit 0 (REF-165 tracks a distinct exit code).
func reportRegionSweep(w io.Writer, res clustersvc.RegionListResult) {
	yellow := ui.StderrColor(color.FgYellow)
	if len(res.Skipped) > 0 {
		_, _ = fmt.Fprintln(w, yellow.Sprintf("Skipped %d region(s) not accessible to these credentials: %s (%s)",
			len(res.Skipped), strings.Join(res.Skipped, ", "), clustersvc.RegionScopeHint))
	}
	for _, f := range res.Failed {
		_, _ = fmt.Fprintln(w, yellow.Sprintf("warning: region %s: %s", f.Region, awserr.Summary(f.Err)))
	}
	if len(res.Failed) > 0 {
		_, _ = fmt.Fprintln(w, yellow.Sprintf("warning: the list is incomplete: %d of %d region(s) failed", len(res.Failed), res.Regions))
	}
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
	showHealth, includeAddons := describeSections(cmd)
	var clusterService *clustersvc.ServiceImpl
	if cmd.Bool("check-readiness") {
		humanOutput := strings.EqualFold(cmd.String("format"), "table")
		k8sClient, kubeSel := resolveReadinessKubeClient(ctx, eks.NewFromConfig(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), humanOutput)
		// With cluster access, also wire metrics-server (best-effort) so the
		// health card's live-utilization check measures instead of skipping. (REF-146)
		var metricsClient health.NodeMetricsLister
		if k8sClient != nil {
			if m, err := health.BuildMetricsClient(kubeSel); err == nil {
				metricsClient = m
			}
		}
		clusterService = factory.NewClusterServiceWithHealth(awsCfg, k8sClient, metricsClient, nil)
	} else {
		clusterService = factory.NewClusterService(awsCfg, showHealth, nil)
	}
	options := clustersvc.DescribeOptions{
		ShowHealth:    showHealth,
		ShowSecurity:  cmd.Bool("show-security") || cmd.Bool("detailed"),
		IncludeAddons: includeAddons,
		Detailed:      cmd.Bool("detailed"),
	}

	var details *clustersvc.ClusterDetails
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
		posture = status.ApplySupportType(posture, ekstypes.SupportType(details.SupportType))
		details.Support = &posture
	}

	if details != nil {
		// Add-ons or nodegroups that could not be read would otherwise just be
		// missing from the output.
		for _, msg := range details.Warnings {
			_, _ = fmt.Fprintln(ui.Stderr, ui.StderrColor(color.FgYellow).Sprintf("warning: %s", msg))
		}
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
		return err
	}
	return clusterview.OutputClusterDetailsTable(details)
}

func runMultiRegionListWithProgress(ctx context.Context, clusterService *clustersvc.ServiceImpl, options clustersvc.ListOptions) ([]clustersvc.ClusterSummary, error) {
	spinner := ui.NewFunSpinnerForCategory("cluster")
	if err := spinner.Start(); err != nil {
		return nil, fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()

	res, err := clusterService.ListAllRegions(ctx, options)
	if err != nil {
		spinner.Stop()
		reportRegionSweep(ui.Stderr, clustersvc.RegionListResult{Skipped: res.Skipped})
		return nil, err
	}

	if len(res.Summaries) > 0 {
		spinner.Success(fmt.Sprintf("Found %d clusters across %d regions!", len(res.Summaries), res.Queried))
	} else {
		spinner.Success("Search complete - no clusters found")
	}
	reportRegionSweep(ui.Stderr, res)
	return res.Summaries, nil
}

// describeSections returns whether cluster describe shows the health and
// add-on sections. Both are on unless --no-health / --no-addons is given. The
// deprecated --show-health=false / --include-addons=false (hidden since
// 0.11.0) still turn them off; the flags themselves print the warning.
func describeSections(cmd *cli.Command) (showHealth, includeAddons bool) {
	showHealth = !cmd.Bool("no-health") && cmd.Bool("show-health")
	includeAddons = !cmd.Bool("no-addons") && cmd.Bool("include-addons")
	return showHealth, includeAddons
}
