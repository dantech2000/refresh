package nodegroup

import (
	"context"
	"fmt"
	"log/slog"

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

// Service defines nodegroup operations.
type Service interface {
	List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error)
	Describe(ctx context.Context, clusterName, nodegroupName string, options DescribeOptions) (*NodegroupDetails, error)
	Scale(ctx context.Context, clusterName, nodegroupName string, desired, min, max *int32, options ScaleOptions) error
}

// ServiceImpl implements Service.
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

// List returns basic nodegroup summaries for a cluster.
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error) {
	s.logger.Info("listing nodegroups", "cluster", clusterName, "options", options)

	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	nodegroupNames, err := common.Paginate(ctx, func(rc context.Context, token *string) ([]string, *string, error) {
		out, err := common.WithRetry(rc, common.DefaultRetryConfig, func(rrc context.Context) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rrc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		})
		if err != nil {
			return nil, nil, awsinternal.FormatAWSError(err, fmt.Sprintf("listing nodegroups for cluster %s", clusterName))
		}
		return out.Nodegroups, out.NextToken, nil
	})
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
		ready := int32(0)
		if ng.ScalingConfig != nil && ng.Status == ekstypes.NodegroupStatusActive {
			ready = aws.ToInt32(ng.ScalingConfig.DesiredSize)
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
			DesiredSize:  aws.ToInt32(ng.ScalingConfig.DesiredSize),
			ReadyNodes:   ready,
			CurrentAMI:   currentAmiId,
			AMIStatus:    amiStatus,
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
				nodes := float64(aws.ToInt32(ng.ScalingConfig.DesiredSize))
				summary.Cost = SummaryCost{Monthly: perMonth * nodes}
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

	details := &NodegroupDetails{
		Name:         aws.ToString(ng.NodegroupName),
		Status:       string(ng.Status),
		InstanceType: firstInstanceType(ng.InstanceTypes),
		AmiType:      string(ng.AmiType),
		CapacityType: string(ng.CapacityType),
		CurrentAMI:   currentAmiId,
		LatestAMI:    latestAmiId,
		AMIStatus:    amiStatus,
		Scaling: ScalingConfig{
			DesiredSize: aws.ToInt32(ng.ScalingConfig.DesiredSize),
			MinSize:     aws.ToInt32(ng.ScalingConfig.MinSize),
			MaxSize:     aws.ToInt32(ng.ScalingConfig.MaxSize),
			AutoScaling: ng.ScalingConfig != nil,
		},
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
			nodes := float64(aws.ToInt32(ng.ScalingConfig.DesiredSize))
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

