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

	clusterNames, err := common.Paginate(ctx, func(rc context.Context, token *string) ([]string, *string, error) {
		out, err := common.WithRetry(rc, common.DefaultRetryConfig, func(rrc context.Context) (*eks.ListClustersOutput, error) {
			return s.eksClient.ListClusters(rrc, &eks.ListClustersInput{NextToken: token})
		})
		if err != nil {
			return nil, nil, awsinternal.FormatAWSError(err, "listing clusters")
		}
		return out.Clusters, out.NextToken, nil
	})
	if err != nil {
		return nil, err
	}

	var summaries []ClusterSummary
	for _, clusterName := range clusterNames {
		if s.shouldSkipCluster(clusterName, options.Filters) {
			continue
		}
		summaries = append(summaries, *s.getClusterSummary(ctx, clusterName, options))
	}

	// Cache the result
	s.cache.Set(cacheKey, summaries, defaultCacheTTLList)

	return summaries, nil
}

// forRegion returns a ServiceImpl bound to the given AWS region. It reuses the
// shared cache, logger, and health checker.
func (s *ServiceImpl) forRegion(region string) *ServiceImpl {
	regionConfig := s.awsConfig.Copy()
	regionConfig.Region = region
	out := NewService(regionConfig, s.healthChecker, s.logger)
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

// ListAllRegions lists clusters across all EKS-supported regions.
func (s *ServiceImpl) ListAllRegions(ctx context.Context, options ListOptions) ([]ClusterSummary, error) {
	summaries, _, err := s.ListAllRegionsWithMeta(ctx, options)
	return summaries, err
}

// ListAllRegionsWithMeta is like ListAllRegions but also returns the number of
// regions that were actually queried, so the caller can display an accurate
// progress message.
func (s *ServiceImpl) ListAllRegionsWithMeta(ctx context.Context, options ListOptions) ([]ClusterSummary, int, error) {
	s.logger.Info("listing clusters across all regions", "options", options)

	eksRegions := s.resolveRegions(options)
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 8
	}

	type regionResult struct {
		region    string
		summaries []ClusterSummary
		err       error
	}
	resultChan := make(chan regionResult, len(eksRegions))
	sem := make(chan struct{}, maxConc)

	for _, region := range eksRegions {
		sem <- struct{}{}
		go func(r string) {
			defer func() { <-sem }()
			summaries, err := s.forRegion(r).List(ctx, regionOptionsFor(options, r))
			for i := range summaries {
				summaries[i].Region = r
			}
			resultChan <- regionResult{region: r, summaries: summaries, err: err}
		}(region)
	}

	allSummaries := make([]ClusterSummary, 0)
	for i := 0; i < len(eksRegions); i++ {
		result := <-resultChan
		if result.err != nil {
			s.logger.Warn("failed to list clusters in region", "region", result.region, "error", result.err)
			continue
		}
		allSummaries = append(allSummaries, result.summaries...)
	}

	return allSummaries, len(eksRegions), nil
}

// Compare provides side-by-side cluster comparison
func (s *ServiceImpl) Compare(ctx context.Context, clusterNames []string, options CompareOptions) (*ClusterComparison, error) {
	s.logger.Info("comparing clusters", "clusters", clusterNames, "options", options)

	if len(clusterNames) < 2 {
		return nil, fmt.Errorf("need at least 2 clusters to compare, got %d", len(clusterNames))
	}

	var clusters []ClusterDetails

	// Get detailed information for each cluster
	for _, name := range clusterNames {
		details, err := s.Describe(ctx, name, DescribeOptions{
			ShowHealth:    true,
			ShowSecurity:  true,
			IncludeAddons: true,
			Detailed:      true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get details for cluster %s: %w", name, err)
		}
		clusters = append(clusters, *details)
	}

	// Analyze differences
	differences := s.analyzeDifferences(clusters, options)

	// Create summary
	summary := ComparisonSummary{
		TotalDifferences: len(differences),
	}

	for _, diff := range differences {
		switch diff.Severity {
		case "critical":
			summary.CriticalDifferences++
		case "warning":
			summary.WarningDifferences++
		case "info":
			summary.InfoDifferences++
		}
	}

	summary.ClustersAreEquivalent = summary.CriticalDifferences == 0 && summary.WarningDifferences == 0

	return &ClusterComparison{
		Clusters:    clusters,
		Differences: differences,
		Summary:     summary,
	}, nil
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
