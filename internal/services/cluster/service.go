package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
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
	iamClient     *iam.Client
	stsClient     *sts.Client
	healthChecker *health.HealthChecker
	cache         *Cache
	logger        *slog.Logger
	awsConfig     aws.Config
}

const (
	// Default cache TTLs (override via env if needed in future)
	defaultCacheTTLDescribe = 5 * time.Minute
	defaultCacheTTLList     = 2 * time.Minute

	// defaultRegionListConcurrency caps concurrent per-region List calls in
	// ListAllRegionsWithMeta when no --max-concurrency is given.
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
		iamClient:     iam.NewFromConfig(awsConfig),
		stsClient:     sts.NewFromConfig(awsConfig),
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
	}

	// Add networking information
	if cluster.ResourcesVpcConfig != nil {
		endpointAccess := EndpointAccessInfo{
			PrivateAccess: cluster.ResourcesVpcConfig.EndpointPrivateAccess,
			PublicAccess:  cluster.ResourcesVpcConfig.EndpointPublicAccess,
			PublicCidrs:   cluster.ResourcesVpcConfig.PublicAccessCidrs,
		}

		details.Networking = NetworkingInfo{
			VpcId:            aws.ToString(cluster.ResourcesVpcConfig.VpcId),
			SubnetIds:        cluster.ResourcesVpcConfig.SubnetIds,
			SecurityGroupIds: cluster.ResourcesVpcConfig.SecurityGroupIds,
			EndpointAccess:   endpointAccess,
		}

		// Get VPC CIDR if detailed information requested
		if options.Detailed && details.Networking.VpcId != "" {
			if cidr, err := s.getVpcCidr(ctx, details.Networking.VpcId); err == nil {
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

	// Add add-ons information if requested
	if options.IncludeAddons {
		addons, err := s.getClusterAddons(ctx, name)
		if err != nil {
			s.logger.Warn("failed to get cluster add-ons", "cluster", name, "error", err)
		} else {
			details.Addons = addons
		}
	}

	// Add nodegroups information if detailed
	if options.Detailed {
		nodegroups, err := s.getClusterNodegroups(ctx, name)
		if err != nil {
			s.logger.Warn("failed to get cluster nodegroups", "cluster", name, "error", err)
		} else {
			details.Nodegroups = nodegroups
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
// shared cache and logger, but rebuilds the health checker: its EKS/CloudWatch/
// ASG clients are region-bound, so reusing the parent's checker would evaluate
// clusters against the wrong region's APIs.
func (s *ServiceImpl) forRegion(region string) *ServiceImpl {
	regionConfig := s.awsConfig.Copy()
	regionConfig.Region = region
	hc := s.healthChecker
	if hc != nil {
		hc = health.NewChecker(
			eks.NewFromConfig(regionConfig),
			nil,
			cloudwatch.NewFromConfig(regionConfig),
			autoscaling.NewFromConfig(regionConfig),
		)
	}
	out := NewService(regionConfig, hc, s.logger)
	out.cache = s.cache
	return out
}

// resolveRegions picks the region set for a multi-region operation in
// preference order: explicit options, REFRESH_EKS_REGIONS env, partition default.
func (s *ServiceImpl) resolveRegions(options ListOptions) []string {
	if len(options.Regions) > 0 {
		return options.Regions
	}
	if env := appconfig.RegionsFromEnv(); len(env) > 0 {
		return env
	}
	return appconfig.GetRegionsForPartition(s.awsConfig.Region)
}

// regionOptionsFor returns options narrowed to a single AWS region. The
// returned value is what ListAllRegionsWithMeta hands to each per-region
// goroutine so the per-region List's cache key (which hashes options.Regions)
// distinguishes between regions instead of colliding on the parent's full
// region slice.
func regionOptionsFor(options ListOptions, region string) ListOptions {
	out := options
	out.Regions = []string{region}
	out.AllRegions = false
	return out
}

// ListAllRegionsWithMeta is like ListAllRegions but also returns the number of
// regions that were actually queried, so the caller can display an accurate
// progress message.
func (s *ServiceImpl) ListAllRegionsWithMeta(ctx context.Context, options ListOptions) ([]ClusterSummary, int, error) {
	s.logger.Info("listing clusters across all regions", "options", options)

	eksRegions := s.resolveRegions(options)
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = defaultRegionListConcurrency
	}

	type regionResult struct {
		region    string
		summaries []ClusterSummary
		err       error
	}
	resultChan := make(chan regionResult, len(eksRegions))
	sem := make(chan struct{}, maxConc)

	// Observe cancellation at the dispatch point; track how many goroutines we
	// actually started so the collection loop below reads exactly that many
	// and never blocks on results that were never queued. (REF-56)
	dispatched := 0
dispatch:
	for _, region := range eksRegions {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		dispatched++
		go func(r string) {
			defer func() { <-sem }()
			summaries, err := s.forRegion(r).List(ctx, regionOptionsFor(options, r))
			// Copy before stamping the region: List may have returned the
			// cached slice, which must not be mutated in place.
			stamped := make([]ClusterSummary, len(summaries))
			copy(stamped, summaries)
			for i := range stamped {
				stamped[i].Region = r
			}
			resultChan <- regionResult{region: r, summaries: stamped, err: err}
		}(region)
	}

	allSummaries := make([]ClusterSummary, 0)
	var failedRegions []string
	var firstErr error
	for i := 0; i < dispatched; i++ {
		result := <-resultChan
		if result.err != nil {
			s.logger.Warn("failed to list clusters in region", "region", result.region, "error", result.err)
			failedRegions = append(failedRegions, result.region)
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		allSummaries = append(allSummaries, result.summaries...)
	}

	// Total failure must not masquerade as "no clusters found": expired
	// credentials or a network outage fail every region at once.
	if len(failedRegions) == len(eksRegions) && len(eksRegions) > 0 {
		sort.Strings(failedRegions)
		return nil, 0, fmt.Errorf("listing clusters failed in all %d regions (e.g. %s): %w",
			len(eksRegions), failedRegions[0], firstErr)
	}

	return allSummaries, len(eksRegions) - len(failedRegions), nil
}

// GetHealth gets health status for a specific cluster
func (s *ServiceImpl) GetHealth(ctx context.Context, name string) (*health.HealthSummary, error) {
	if s.healthChecker == nil {
		return nil, fmt.Errorf("health checker not available")
	}

	healthSummary := s.healthChecker.RunAllChecks(ctx, name)
	return &healthSummary, nil
}

// Helper methods are implemented in helpers.go
