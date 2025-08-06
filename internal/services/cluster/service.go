package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
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

// ServiceImpl implements the cluster service
type ServiceImpl struct {
	eksClient     *eks.Client
	ec2Client     *ec2.Client
	iamClient     *iam.Client
	stsClient     *sts.Client
	healthChecker *health.HealthChecker
	cache         *Cache
	logger        *slog.Logger
	awsConfig     aws.Config
}

// NewService creates a new cluster service instance
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	return &ServiceImpl{
		eksClient:     eks.NewFromConfig(awsConfig),
		ec2Client:     ec2.NewFromConfig(awsConfig),
		iamClient:     iam.NewFromConfig(awsConfig),
		stsClient:     sts.NewFromConfig(awsConfig),
		healthChecker: healthChecker,
		cache:         NewCache(5 * time.Minute), // 5 minute cache
		logger:        logger,
		awsConfig:     awsConfig,
	}
}

// Describe provides comprehensive cluster information
func (s *ServiceImpl) Describe(ctx context.Context, name string, options DescribeOptions) (*ClusterDetails, error) {
	s.logger.Info("describing cluster", "cluster", name, "options", options)

	// Check cache first
	cacheKey := fmt.Sprintf("describe-%s-%v", name, options)
	if cached, found := s.cache.Get(cacheKey); found {
		if details, ok := cached.(*ClusterDetails); ok {
			s.logger.Debug("returning cached cluster details", "cluster", name)
			return details, nil
		}
	}

	// Get basic cluster information
	clusterOutput, err := s.eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(name),
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
	s.cache.Set(cacheKey, details, 5*time.Minute)

	return details, nil
}

// List provides fast cluster listing with optional health information
func (s *ServiceImpl) List(ctx context.Context, options ListOptions) ([]ClusterSummary, error) {
	s.logger.Info("listing clusters", "options", options)

	// Check cache first
	cacheKey := fmt.Sprintf("list-%v", options)
	if cached, found := s.cache.Get(cacheKey); found {
		if summaries, ok := cached.([]ClusterSummary); ok {
			s.logger.Debug("returning cached cluster list")
			return summaries, nil
		}
	}

	// Get cluster names
	listOutput, err := s.eksClient.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "listing clusters")
	}

	var summaries []ClusterSummary

	// Process each cluster
	for _, clusterName := range listOutput.Clusters {
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
	s.cache.Set(cacheKey, summaries, 2*time.Minute)

	return summaries, nil
}

// ListAllRegions lists clusters across all EKS-supported regions
func (s *ServiceImpl) ListAllRegions(ctx context.Context, options ListOptions) ([]ClusterSummary, error) {
	s.logger.Info("listing clusters across all regions", "options", options)

	eksRegions := []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
		"ca-central-1", "sa-east-1",
	}

	if len(options.Regions) > 0 {
		eksRegions = options.Regions
	}

	allSummaries := make([]ClusterSummary, 0)

	// Use a channel to collect results from concurrent region queries
	type regionResult struct {
		region    string
		summaries []ClusterSummary
		err       error
	}

	resultChan := make(chan regionResult, len(eksRegions))

	// Query each region concurrently
	for _, region := range eksRegions {
		go func(r string) {
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
