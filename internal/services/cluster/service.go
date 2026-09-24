// Package cluster lists and describes EKS clusters, with optional health,
// networking, and upgrade-insight details.
package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/dantech2000/refresh/internal/apidoc"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/common"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/status"
)

// EKSAPI abstracts the subset of EKS client methods used by this service for easier testing
type EKSAPI interface {
	ListClusters(ctx context.Context, params *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersions(ctx context.Context, params *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
	ListInsights(ctx context.Context, params *eks.ListInsightsInput, optFns ...func(*eks.Options)) (*eks.ListInsightsOutput, error)
	DescribeInsight(ctx context.Context, params *eks.DescribeInsightInput, optFns ...func(*eks.Options)) (*eks.DescribeInsightOutput, error)
}

// ServiceImpl implements the cluster service
type ServiceImpl struct {
	eksClient     EKSAPI
	ec2Client     *ec2.Client
	healthChecker *health.HealthChecker
	cache         *Cache
	logger        *slog.Logger
	awsConfig     aws.Config
	// regionLister replaces the per-region List in ListAllRegions (tests).
	regionLister func(ctx context.Context, region string, options ListOptions) ([]ClusterSummary, error)
}

const (
	// Default cache TTLs (override via env if needed in future)
	defaultCacheTTLDescribe = 5 * time.Minute
	defaultCacheTTLList     = 2 * time.Minute

	// defaultRegionListConcurrency caps concurrent per-region List calls in
	// ListAllRegions when no --max-concurrency is given.
	defaultRegionListConcurrency = 8
)

// NewService creates a new cluster service instance
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	// Provide a default no-op logger to avoid panics when nil is passed
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return &ServiceImpl{
		eksClient:     eks.NewFromConfig(awsConfig),
		ec2Client:     ec2.NewFromConfig(awsConfig),
		healthChecker: healthChecker,
		cache:         NewCache(defaultCacheTTLDescribe),
		logger:        logger,
		awsConfig:     awsConfig,
	}
}

// buildListCacheKey returns a deterministic cache key for list options.
func buildListCacheKey(options ListOptions) string {
	regions := append([]string(nil), options.Regions...)
	sort.Strings(regions)

	filterKeys := make([]string, 0, len(options.Filters))
	for k := range options.Filters {
		filterKeys = append(filterKeys, k)
	}
	sort.Strings(filterKeys)
	filterParts := make([]string, len(filterKeys))
	for i, k := range filterKeys {
		filterParts[i] = k + "=" + options.Filters[k]
	}

	return fmt.Sprintf("list-regions=%s|filters=%s|showHealth=%t|allRegions=%t",
		strings.Join(regions, ","),
		strings.Join(filterParts, ";"),
		options.ShowHealth,
		options.AllRegions,
	)
}

// buildDescribeCacheKey returns a deterministic cache key for describe options
func buildDescribeCacheKey(name string, options DescribeOptions) string {
	flags := []string{
		fmt.Sprintf("health=%t", options.ShowHealth),
		fmt.Sprintf("security=%t", options.ShowSecurity),
		fmt.Sprintf("addons=%t", options.IncludeAddons),
		fmt.Sprintf("detailed=%t", options.Detailed),
	}
	return fmt.Sprintf("describe-%s-%s", name, strings.Join(flags, ","))
}

// Describe provides comprehensive cluster information
func (s *ServiceImpl) Describe(ctx context.Context, name string, options DescribeOptions) (*ClusterDetails, error) {
	s.logger.Info("describing cluster", "cluster", name, "options", options)

	// Check cache first
	cacheKey := buildDescribeCacheKey(name, options)
	if cached, found := s.cache.Get(cacheKey); found {
		if details, ok := cached.(*ClusterDetails); ok {
			s.logger.Debug("returning cached cluster details", "cluster", name)
			return details, nil
		}
	}

	// Get basic cluster information (with retry)
	clusterOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(name)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s", name))
	}
	if clusterOutput.Cluster == nil {
		return nil, fmt.Errorf("empty DescribeCluster response for %s", name)
	}

	cluster := clusterOutput.Cluster
	details := &ClusterDetails{
		Name:            aws.ToString(cluster.Name),
		Status:          string(cluster.Status),
		Version:         aws.ToString(cluster.Version),
		PlatformVersion: aws.ToString(cluster.PlatformVersion),
		Endpoint:        aws.ToString(cluster.Endpoint),
		CreatedAt:       aws.ToTime(cluster.CreatedAt),
		Region:          s.awsConfig.Region,
		Tags:            cluster.Tags,
		SupportType:     string(status.SupportTypeOf(cluster)),
	}

	// AWS-reported control-plane health issues (always surfaced — no extra API
	// call, no ShowHealth gate; these are real problems the user must see).
	if cluster.Health != nil {
		for _, issue := range cluster.Health.Issues {
			details.HealthIssues = append(details.HealthIssues, HealthIssue{
				Code:        string(issue.Code),
				Message:     aws.ToString(issue.Message),
				ResourceIDs: issue.ResourceIds,
			})
		}
	}

	// Add networking information
	if cluster.ResourcesVpcConfig != nil {
		endpointAccess := EndpointAccessInfo{
			PrivateAccess: cluster.ResourcesVpcConfig.EndpointPrivateAccess,
			PublicAccess:  cluster.ResourcesVpcConfig.EndpointPublicAccess,
			PublicCidrs:   cluster.ResourcesVpcConfig.PublicAccessCidrs,
		}

		details.Networking = NetworkingInfo{
			VpcID:            aws.ToString(cluster.ResourcesVpcConfig.VpcId),
			SubnetIDs:        cluster.ResourcesVpcConfig.SubnetIds,
			SecurityGroupIDs: cluster.ResourcesVpcConfig.SecurityGroupIds,
			EndpointAccess:   endpointAccess,
		}

		// Get VPC CIDR if detailed information requested
		if options.Detailed && details.Networking.VpcID != "" {
			if cidr, err := s.getVpcCidr(ctx, details.Networking.VpcID); err == nil {
				details.Networking.VpcCidr = cidr
			}
		}
	}

	// Add security information
	details.Security = SecurityInfo{
		ServiceRoleArn: aws.ToString(cluster.RoleArn),
	}

	if len(cluster.EncryptionConfig) > 0 {
		details.Security.EncryptionEnabled = true
		if cluster.EncryptionConfig[0].Provider != nil {
			details.Security.KmsKeyArn = aws.ToString(cluster.EncryptionConfig[0].Provider.KeyArn)
		}
	}

	if cluster.Logging != nil && len(cluster.Logging.ClusterLogging) > 0 {
		for _, logSetup := range cluster.Logging.ClusterLogging {
			if logSetup.Enabled != nil && *logSetup.Enabled {
				for _, logType := range logSetup.Types {
					details.Security.LoggingEnabled = append(details.Security.LoggingEnabled, string(logType))
				}
			}
		}
	}

	details.Security.DeletionProtection = aws.ToBool(cluster.DeletionProtection)

	// Lists and maps that were read print [] or {}, never null.
	details.Networking.SubnetIDs = apidoc.List(details.Networking.SubnetIDs)
	details.Networking.SecurityGroupIDs = apidoc.List(details.Networking.SecurityGroupIDs)
	details.Security.LoggingEnabled = apidoc.List(details.Security.LoggingEnabled)
	if details.Tags == nil {
		details.Tags = map[string]string{}
	}

	// Add add-ons information if requested
	if options.IncludeAddons {
		addons, failures, err := s.getClusterAddons(ctx, name)
		if err != nil {
			s.logger.Debug("failed to get cluster add-ons", "cluster", name, "error", err)
			details.Failures = append(details.Failures, s.failure(diag.KindCluster, name, name, diag.OpListAddons, err))
		} else {
			// Collected: [] when there are none, unlike nil (not collected).
			list := append([]AddonInfo{}, addons...)
			details.Addons = &list
			details.Failures = append(details.Failures, failures...)
		}
	}

	// Add nodegroups information if detailed
	if options.Detailed {
		nodegroups, failures, err := s.getClusterNodegroups(ctx, name)
		if err != nil {
			s.logger.Debug("failed to get cluster nodegroups", "cluster", name, "error", err)
			details.Failures = append(details.Failures, s.failure(diag.KindCluster, name, name, diag.OpListNodegroups, err))
		} else {
			list := append([]NodegroupSummary{}, nodegroups...)
			details.Nodegroups = &list
			details.Failures = append(details.Failures, failures...)
		}
	}

	// Add health information if requested
	if options.ShowHealth && s.healthChecker != nil {
		healthSummary := s.healthChecker.RunAllChecks(ctx, name)
		details.Health = &healthSummary
	}

	// Cache the result
	s.cache.Set(cacheKey, details, defaultCacheTTLDescribe)

	return details, nil
}

// List provides fast cluster listing with optional health information
func (s *ServiceImpl) List(ctx context.Context, options ListOptions) ([]ClusterSummary, error) {
	s.logger.Info("listing clusters", "options", options)

	// Check cache first
	cacheKey := buildListCacheKey(options)
	if cached, found := s.cache.Get(cacheKey); found {
		if summaries, ok := cached.([]ClusterSummary); ok {
			s.logger.Debug("returning cached cluster list")
			return summaries, nil
		}
	}

	clusterNames, err := awsinternal.ListAllPages(ctx, "listing clusters",
		func(rc context.Context, token *string) (*eks.ListClustersOutput, error) {
			return s.eksClient.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken },
	)
	if err != nil {
		return nil, err
	}

	selected := make([]string, 0, len(clusterNames))
	for _, clusterName := range clusterNames {
		if !s.shouldSkipCluster(clusterName, options.Filters) {
			selected = append(selected, clusterName)
		}
	}

	// Each summary costs a DescribeCluster + nodegroup describes; fan out with
	// bounded concurrency instead of paying the per-cluster latency serially.
	results := common.ForEachParallel(ctx, selected, common.DefaultItemConcurrency,
		func(fctx context.Context, clusterName string) *ClusterSummary {
			return s.getClusterSummary(fctx, clusterName, options)
		})

	summaries := make([]ClusterSummary, 0, len(results))
	for _, r := range results {
		if r != nil {
			summaries = append(summaries, *r)
		}
	}

	// Apply status/version filters now that each summary carries those fields
	// (the name filter was already applied at the list stage).
	summaries = filterSummaries(summaries, options.Filters)

	// Cache the result
	s.cache.Set(cacheKey, summaries, defaultCacheTTLList)

	return summaries, nil
}

// forRegion returns a ServiceImpl bound to the given AWS region. It reuses the
// shared cache and logger, but rebuilds the health checker the same way the
// factory does: its AWS clients are region-bound, so reusing the parent's
// checker would evaluate clusters against the wrong region's APIs. The
// Kubernetes client and node-metrics lister are not carried over: they talk to
// one cluster's API server, not to every cluster in the region.
func (s *ServiceImpl) forRegion(region string) *ServiceImpl {
	regionConfig := s.awsConfig.Copy()
	regionConfig.Region = region
	hc := s.healthChecker
	if hc != nil {
		hc = health.NewCheckerForConfig(regionConfig, nil, nil)
	}
	out := NewService(regionConfig, hc, s.logger)
	out.cache = s.cache
	return out
}

// resolveRegions picks the region set for a multi-region operation in
// preference order: explicit options, REFRESH_EKS_REGIONS env, partition default.
// defaultSweep reports the partition default, which nobody scoped. Only that
// sweep skips regions closed to these credentials, as `status -A` does.
func (s *ServiceImpl) resolveRegions(options ListOptions) (regions []string, defaultSweep bool) {
	if len(options.Regions) > 0 {
		return options.Regions, false
	}
	if env := appconfig.RegionsFromEnv(); len(env) > 0 {
		return env, false
	}
	return appconfig.GetRegionsForPartition(s.awsConfig.Region), true
}

// regionOptionsFor returns options narrowed to a single AWS region. The
// returned value is what ListAllRegions hands to each per-region
// goroutine so the per-region List's cache key (which hashes options.Regions)
// distinguishes between regions instead of colliding on the parent's full
// region slice.
func regionOptionsFor(options ListOptions, region string) ListOptions {
	out := options
	out.Regions = []string{region}
	out.AllRegions = false
	return out
}

// RegionListResult is the outcome of a multi-region cluster list.
type RegionListResult struct {
	Summaries []ClusterSummary
	// Regions is the number of regions in the sweep.
	Regions int
	// Queried is the number of regions that answered.
	Queried int
	// Failed has one failure (kind Region, eks:ListClusters) per failed
	// region, in sweep order. A region that never started because the
	// context ended is ReasonNotAttempted.
	Failed []diag.Failure
	// Errors holds the error behind each failure, in the same order. A
	// failure is a one-line summary; callers that must classify or report
	// the cause (runner.NoRegionAnswered) need the error itself.
	Errors []error
	// Skipped lists (sorted) the regions of a default sweep that are closed
	// to these credentials. They are not failures.
	Skipped []string
}

// RegionScopeHint tells the user how to narrow a region sweep.
// runner.RegionScopeHint is the same text for the command layer.
const RegionScopeHint = "scope with -r or REFRESH_EKS_REGIONS"

// listRegion lists the clusters of one region.
func (s *ServiceImpl) listRegion(ctx context.Context, region string, options ListOptions) ([]ClusterSummary, error) {
	if s.regionLister != nil {
		return s.regionLister(ctx, region, options)
	}
	return s.forRegion(region).List(ctx, options)
}

// ListAllRegions lists clusters in every region of the sweep, at most
// options.MaxConcurrency regions at a time. It fails only when no region
// answered. Partial failures are in the result, for the caller to report.
func (s *ServiceImpl) ListAllRegions(ctx context.Context, options ListOptions) (RegionListResult, error) {
	s.logger.Info("listing clusters across all regions", "options", options)

	regions, defaultSweep := s.resolveRegions(options)
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = defaultRegionListConcurrency
	}

	type regionResult struct {
		ran       bool
		summaries []ClusterSummary
		err       error
	}
	results := common.ForEachParallel(ctx, regions, maxConc, func(rctx context.Context, r string) regionResult {
		summaries, err := s.listRegion(rctx, r, regionOptionsFor(options, r))
		// Copy before stamping the region: List may have returned the
		// cached slice, which must not be mutated in place.
		stamped := make([]ClusterSummary, len(summaries))
		copy(stamped, summaries)
		for i := range stamped {
			stamped[i].Region = r
			stamped[i].Failures = slices.Clone(stamped[i].Failures)
			for j := range stamped[i].Failures {
				stamped[i].Failures[j].Region = r
			}
		}
		return regionResult{ran: true, summaries: stamped, err: err}
	})

	out := RegionListResult{Summaries: make([]ClusterSummary, 0), Regions: len(regions)}
	var firstRegion string
	var firstErr error
	for i, r := range regions {
		res := results[i]
		if !res.ran {
			// The context ended before this region got a slot. Count it as
			// failed, so an interrupted sweep never looks complete.
			res.err = fmt.Errorf("not queried: %w", context.Cause(ctx))
		}
		switch {
		case res.err == nil:
			out.Queried++
			out.Summaries = append(out.Summaries, res.summaries...)
		case defaultSweep && awserr.IsRegionInaccessible(res.err):
			s.logger.Debug("skipping region not accessible to these credentials", "region", r, "error", res.err)
			out.Skipped = append(out.Skipped, r)
		default:
			s.logger.Debug("failed to list clusters in region", "region", r, "error", res.err)
			f := diag.FromError(diag.KindRegion, r, diag.OpListClusters, res.err)
			if !res.ran {
				f = diag.New(diag.KindRegion, r, diag.ReasonNotAttempted, res.err.Error())
				f.Region = r
			}
			out.Failed = append(out.Failed, f)
			out.Errors = append(out.Errors, res.err)
			if firstErr == nil {
				firstRegion, firstErr = r, res.err
			}
		}
	}
	sort.Strings(out.Skipped)

	// No region answered. An empty list must not look like "no clusters
	// found": expired credentials or an outage fail every region at once.
	if out.Queried == 0 && len(regions) > 0 {
		if len(out.Failed) == 0 {
			return out, fmt.Errorf("could not list clusters in any of %d region(s): none is accessible to these credentials; %s",
				len(regions), RegionScopeHint)
		}
		if len(out.Failed) == len(regions) {
			return out, fmt.Errorf("listing clusters failed in all %d regions (e.g. %s): %w", len(regions), firstRegion, firstErr)
		}
		return out, fmt.Errorf("listing clusters failed in %d region(s) (e.g. %s) and %d region(s) are not accessible: %w",
			len(out.Failed), firstRegion, len(out.Skipped), firstErr)
	}
	return out, nil
}

// Helper methods are implemented in helpers.go
