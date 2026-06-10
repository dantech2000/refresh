package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/dantech2000/refresh/internal/types"
)

// classifyAMI compares the nodegroup's current AMI against the latest available
// for its type and returns the appropriate status. Returns AMIUpdating while an
// update is in flight, regardless of AMI identities.
func classifyAMI(status ekstypes.NodegroupStatus, currentAmiId, latestAmiId string) types.AMIStatus {
	switch {
	case status == ekstypes.NodegroupStatusUpdating:
		return types.AMIUpdating
	case currentAmiId == "" || latestAmiId == "":
		return types.AMIUnknown
	case currentAmiId == latestAmiId:
		return types.AMILatest
	default:
		return types.AMIOutdated
	}
}

// EKSAPI abstracts the subset of EKS client methods used for nodegroups.
type EKSAPI interface {
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	UpdateNodegroupConfig(ctx context.Context, params *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
}

// ServiceImpl is the nodegroup service.
type ServiceImpl struct {
	eksClient     EKSAPI
	logger        *slog.Logger
	awsConfig     aws.Config
	healthChecker *health.HealthChecker
	cache         *Cache
	util          *UtilizationCollector
	cost          *CostAnalyzer
	asgClient     *autoscaling.Client
	ec2Client     *ec2.Client
	ssmClient     *ssm.Client
}

// NewService creates a new nodegroup service.
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	cache := NewCache()
	cw := cloudwatch.NewFromConfig(awsConfig)
	pCfg := awsConfig.Copy()
	pCfg.Region = "us-east-1"
	p := pricing.NewFromConfig(pCfg)
	svc := &ServiceImpl{
		eksClient:     eks.NewFromConfig(awsConfig),
		logger:        logger,
		awsConfig:     awsConfig,
		healthChecker: healthChecker,
		cache:         cache,
		asgClient:     autoscaling.NewFromConfig(awsConfig),
		ec2Client:     ec2.NewFromConfig(awsConfig),
		ssmClient:     ssm.NewFromConfig(awsConfig),
	}
	svc.util = NewUtilizationCollector(cw, logger, cache)
	svc.cost = NewCostAnalyzer(p, logger, cache, awsConfig.Region)
	svc.cost.SetEC2Client(svc.ec2Client)
	return svc
}

// supportedFilterKeys maps normalized --filter keys to a matcher against a
// built summary. Keys are matched case-insensitively.
var supportedFilterKeys = map[string]func(s NodegroupSummary, want string) bool{
	"name":         func(s NodegroupSummary, want string) bool { return strings.Contains(strings.ToLower(s.Name), strings.ToLower(want)) },
	"status":       func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.Status, want) },
	"instancetype": func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.InstanceType, want) },
	"amistatus":    func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.AMIStatus.PlainString(), want) },
}

// validateFilters rejects unknown filter keys up front so a typo'd
// --filter doesn't silently match everything.
func validateFilters(filters map[string]string) error {
	for k := range filters {
		if _, ok := supportedFilterKeys[normalizeFilterKey(k)]; !ok {
			return fmt.Errorf("unsupported filter key %q (supported: name, status, instanceType, amiStatus)", k)
		}
	}
	return nil
}

func normalizeFilterKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "-", ""))
}

func matchesFilters(s NodegroupSummary, filters map[string]string) bool {
	for k, want := range filters {
		if match := supportedFilterKeys[normalizeFilterKey(k)]; match != nil && !match(s, want) {
			return false
		}
	}
	return true
}

// List returns basic nodegroup summaries for a cluster.
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error) {
	s.logger.Info("listing nodegroups", "cluster", clusterName, "options", options)

	if err := validateFilters(options.Filters); err != nil {
		return nil, err
	}

	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	nodegroupNames, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing nodegroups for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		return nil, err
	}

	summaries := make([]NodegroupSummary, 0, len(nodegroupNames))
	for _, name := range nodegroupNames {
		desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(name),
			})
		})
		if err != nil {
			s.logger.Warn("failed to describe nodegroup", "cluster", clusterName, "nodegroup", name, "error", err)
			continue
		}
		ng := desc.Nodegroup
		var desiredSize int32
		if ng.ScalingConfig != nil {
			desiredSize = aws.ToInt32(ng.ScalingConfig.DesiredSize)
		}
		ready := int32(0)
		if ng.Status == ekstypes.NodegroupStatusActive {
			ready = desiredSize
		}
		instanceType := "Unknown"
		if len(ng.InstanceTypes) > 0 {
			instanceType = string(ng.InstanceTypes[0])
		}

		currentAmiId := awsinternal.CurrentAmiID(ctx, ng, s.ec2Client, s.asgClient)
		latestAmiId := awsinternal.LatestAmiIDForType(ctx, s.ssmClient, k8sVersion, ng.AmiType)
		amiStatus := classifyAMI(ng.Status, currentAmiId, latestAmiId)

		summary := NodegroupSummary{
			Name:         aws.ToString(ng.NodegroupName),
			Status:       string(ng.Status),
			InstanceType: instanceType,
			DesiredSize:  desiredSize,
			ReadyNodes:   ready,
			CurrentAMI:   currentAmiId,
			AMIStatus:    amiStatus,
		}
		if !matchesFilters(summary, options.Filters) {
			continue
		}
		if options.ShowUtilization && s.util != nil {
			window := normalizeWindow(options.Timeframe)
			if ids, ok := s.getNodegroupInstanceIDs(ctx, clusterName, aws.ToString(ng.NodegroupName)); ok {
				if cpu, ok2 := s.util.CollectEC2CPUForInstances(ctx, ids, window); ok2 {
					summary.Metrics = SummaryMetrics{CPU: cpu.Average}
				}
			}
		}
		if options.ShowCosts && s.cost != nil {
			if _, perMonth, ok := s.cost.EstimateOnDemandUSD(ctx, instanceType); ok {
				summary.Cost = SummaryCost{Monthly: perMonth * float64(desiredSize)}
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// Describe returns expanded details for a single nodegroup.
func (s *ServiceImpl) Describe(ctx context.Context, clusterName, nodegroupName string, options DescribeOptions) (*NodegroupDetails, error) {
	s.logger.Info("describing nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "options", options)

	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	ng := out.Nodegroup

	currentAmiId := awsinternal.CurrentAmiID(ctx, ng, s.ec2Client, s.asgClient)
	latestAmiId := awsinternal.LatestAmiIDForType(ctx, s.ssmClient, k8sVersion, ng.AmiType)
	amiStatus := classifyAMI(ng.Status, currentAmiId, latestAmiId)

	var scaling ScalingConfig
	if sc := ng.ScalingConfig; sc != nil {
		scaling = ScalingConfig{
			DesiredSize: aws.ToInt32(sc.DesiredSize),
			MinSize:     aws.ToInt32(sc.MinSize),
			MaxSize:     aws.ToInt32(sc.MaxSize),
			AutoScaling: true,
		}
	}

	details := &NodegroupDetails{
		Name:         aws.ToString(ng.NodegroupName),
		Status:       string(ng.Status),
		InstanceType: firstInstanceType(ng.InstanceTypes),
		AmiType:      string(ng.AmiType),
		CapacityType: string(ng.CapacityType),
		CurrentAMI:   currentAmiId,
		LatestAMI:    latestAmiId,
		AMIStatus:    amiStatus,
		Scaling:      scaling,
	}
	if options.ShowUtilization && s.util != nil {
		window := normalizeWindow(options.Timeframe)
		if ids, ok := s.getNodegroupInstanceIDs(ctx, clusterName, aws.ToString(ng.NodegroupName)); ok {
			if cpu, ok2 := s.util.CollectEC2CPUForInstances(ctx, ids, window); ok2 {
				details.Utilization = UtilizationMetrics{CPU: cpu, TimeRange: window}
			}
		}
	}
	if options.ShowCosts && s.cost != nil {
		if _, perMonth, ok := s.cost.EstimateOnDemandUSD(ctx, details.InstanceType); ok {
			nodes := float64(scaling.DesiredSize)
			details.Cost = CostAnalysis{
				CurrentMonthlyCost:   perMonth * nodes,
				ProjectedMonthlyCost: perMonth * nodes,
				CostPerNode:          perMonth,
			}
		}
	}
	if options.ShowWorkloads {
		details.Workloads.PodDisruption = "no data"
		if wi, ok := s.analyzeWorkloads(ctx, clusterName, aws.ToString(ng.NodegroupName)); ok {
			details.Workloads = wi
		} else {
			details.Workloads.PodDisruption = "unavailable: Kubernetes API not accessible or no matching nodes"
		}
	}
	if options.ShowInstances {
		if ids, ok := s.getNodegroupInstanceIDs(ctx, clusterName, aws.ToString(ng.NodegroupName)); ok {
			if insts, ok2 := s.getInstanceDetails(ctx, ids); ok2 {
				details.Instances = insts
			}
		}
	}
	return details, nil
}

