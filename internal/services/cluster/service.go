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

// Service defines the interface for cluster operations
type Service interface {
	// Single cluster operations
	Describe(ctx context.Context, name string, options DescribeOptions) (*ClusterDetails, error)
	GetHealth(ctx context.Context, name string) (*health.HealthSummary, error)

	// Multi-cluster operations
	List(ctx context.Context, options ListOptions) ([]ClusterSummary, error)
	ListAllRegions(ctx context.Context, options ListOptions) ([]ClusterSummary, error)

	// Comparison operations
	Compare(ctx context.Context, clusterNames []string, options CompareOptions) (*ClusterComparison, error)
}

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

// buildListCacheKey returns a deterministic cache key for list options
func buildListCacheKey(options ListOptions) string {
	// Build deterministic key with improved capacity estimation and without
	// intermediate string slices/joins.
	// Format: list- regions=..|filters=..|showHealth=x|allRegions=y

	// Precompute boolean fragments (always included for stability)
	showHealthFrag := fmt.Sprintf("showHealth=%t", options.ShowHealth)
	allRegionsFrag := fmt.Sprintf("allRegions=%t", options.AllRegions)

	// Estimate capacity
	estimated := 5 /* list- */ + len(showHealthFrag) + 1 /* | */ + len(allRegionsFrag)

	// Regions
	if len(options.Regions) > 0 {
		estimated += len("regions=")
		for _, r := range options.Regions {
			estimated += len(r)
		}
		estimated += len(options.Regions) - 1 // commas
		estimated += 1                        // '|'
	}

	// Filters (sorted for determinism)
	if len(options.Filters) > 0 {
		estimated += len("filters=")
		// sum of key=value plus separators
		keys := make([]string, 0, len(options.Filters))
		for k := range options.Filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			estimated += len(k) + 1 /* '=' */ + len(options.Filters[k])
		}
		estimated += len(keys) - 1 // semicolons
		estimated += 1             // '|'
	}

	var b strings.Builder
	if estimated < 32 {
		b.Grow(32)
	} else {
		b.Grow(estimated)
	}
	b.WriteString("list-")

	wroteSep := false
	if len(options.Regions) > 0 {
		b.WriteString("regions=")
		// Use a sorted copy for stable ordering
		regions := append([]string(nil), options.Regions...)
		sort.Strings(regions)
		for i, r := range regions {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(r)
		}
		wroteSep = true
	}

	if len(options.Filters) > 0 {
		if wroteSep {
			b.WriteByte('|')
		}
		b.WriteString("filters=")
		keys := make([]string, 0, len(options.Filters))
		for k := range options.Filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(options.Filters[k])
		}
		wroteSep = true
	}

	if wroteSep {
		b.WriteByte('|')
	}
	b.WriteString(showHealthFrag)
	b.WriteByte('|')
	b.WriteString(allRegionsFrag)

	return b.String()
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

	// Check deletion protection status (available in AWS SDK v1.73.1+)
	// EKS deletion protection was introduced in August 2025
	if cluster.DeletionProtection != nil {
		details.Security.DeletionProtection = aws.ToBool(cluster.DeletionProtection)
	} else {
		// Default to false if field is not present (older clusters)
		details.Security.DeletionProtection = false
	}

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

	// Get cluster names with pagination
	var clusterNames []string
	var nextToken *string
	for {
		listOutput, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListClustersOutput, error) {
			return s.eksClient.ListClusters(rc, &eks.ListClustersInput{NextToken: nextToken})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, "listing clusters")
		}
		clusterNames = append(clusterNames, listOutput.Clusters...)
		// Some paginated APIs may return an empty string token to indicate end
		if listOutput.NextToken == nil || (listOutput.NextToken != nil && aws.ToString(listOutput.NextToken) == "") {
			break
		}
		nextToken = listOutput.NextToken
	}

	// Debug: ensure we collected all pages during tests
	// s.logger.Debug("collected clusters", "count", len(clusterNames))

	var summaries []ClusterSummary

	// Process each cluster
	for _, clusterName := range clusterNames {
		// Apply filters
		if s.shouldSkipCluster(clusterName, options.Filters) {
			continue
		}

		summary, err := s.getClusterSummary(ctx, clusterName, options)
		if err != nil {
			s.logger.Warn("failed to get cluster summary", "cluster", clusterName, "error", err)
			continue
		}

		summaries = append(summaries, *summary)
	}

	// Cache the result
	s.cache.Set(cacheKey, summaries, defaultCacheTTLList)

	return summaries, nil
}

// ListAllRegions lists clusters across all EKS-supported regions
func (s *ServiceImpl) ListAllRegions(ctx context.Context, options ListOptions) ([]ClusterSummary, error) {
	s.logger.Info("listing clusters across all regions", "options", options)

	// Default regions by partition (fallback). Prefer user-specified regions or env override.
	regionsCommercial := []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
		"ca-central-1", "sa-east-1",
	}
	regionsGovCloud := []string{"us-gov-west-1", "us-gov-east-1"}
	regionsChina := []string{"cn-north-1", "cn-northwest-1"}

	eksRegions := regionsCommercial
	currentRegion := s.awsConfig.Region
	if strings.HasPrefix(currentRegion, "us-gov-") {
		eksRegions = regionsGovCloud
	} else if strings.HasPrefix(currentRegion, "cn-") {
		eksRegions = regionsChina
	}

	// Allow overriding region set via environment (comma-separated)
	if envRegions := appconfig.RegionsFromEnv(); len(envRegions) > 0 {
		eksRegions = envRegions
	}

	if len(options.Regions) > 0 {
		eksRegions = options.Regions
	}

	// Partition-aware defaults are applied above; users can still override via flags/env.

	allSummaries := make([]ClusterSummary, 0)

	// Use a channel to collect results from concurrent region queries
	type regionResult struct {
		region    string
		summaries []ClusterSummary
		err       error
	}

	resultChan := make(chan regionResult, len(eksRegions))

	// Limit concurrency across regions to reduce throttling
	maxConc := options.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 8
	}
	sem := make(chan struct{}, maxConc)

	// Query each region concurrently
	for _, region := range eksRegions {
		go func(r string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			// Create region-specific config
			regionConfig := s.awsConfig.Copy()
			regionConfig.Region = r

			regionService := &ServiceImpl{
				eksClient:     eks.NewFromConfig(regionConfig),
				ec2Client:     ec2.NewFromConfig(regionConfig),
				iamClient:     iam.NewFromConfig(regionConfig),
				stsClient:     sts.NewFromConfig(regionConfig),
				healthChecker: s.healthChecker,
				cache:         s.cache,
				logger:        s.logger,
				awsConfig:     regionConfig,
			}

			regionOptions := options
			regionOptions.AllRegions = false // Avoid infinite recursion

			summaries, err := regionService.List(ctx, regionOptions)

			// Add region to summaries
			for i := range summaries {
				summaries[i].Region = r
			}

			resultChan <- regionResult{
				region:    r,
				summaries: summaries,
				err:       err,
			}
		}(region)
	}

	// Collect results
	for i := 0; i < len(eksRegions); i++ {
		result := <-resultChan
		if result.err != nil {
			s.logger.Warn("failed to list clusters in region", "region", result.region, "error", result.err)
			continue
		}
		allSummaries = append(allSummaries, result.summaries...)
	}

	return allSummaries, nil
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
