package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/dantech2000/refresh/internal/types"
)

// classifyAMI compares the nodegroup's current AMI against the latest available
// for its type and returns the appropriate status. Returns AMIUpdating while an
// update is in flight, AMICustom for custom-AMI nodegroups (whose AMI is managed
// via the user's launch template, not by EKS, so there's no recommended AMI to
// compare against), regardless of AMI identities.
func classifyAMI(amiType ekstypes.AMITypes, status ekstypes.NodegroupStatus, currentAmiId, latestAmiId string) types.AMIStatus {
	switch {
	case status == ekstypes.NodegroupStatusUpdating:
		return types.AMIUpdating
	case amiType == ekstypes.AMITypesCustom:
		return types.AMICustom
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
	asgClient     *autoscaling.Client
	ec2Client     *ec2.Client
	ssmClient     *ssm.Client
}

// NewService creates a new nodegroup service.
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	cache := NewCache()
	return &ServiceImpl{
		eksClient:     eks.NewFromConfig(awsConfig),
		logger:        logger,
		awsConfig:     awsConfig,
		healthChecker: healthChecker,
		cache:         cache,
		asgClient:     autoscaling.NewFromConfig(awsConfig),
		ec2Client:     ec2.NewFromConfig(awsConfig),
		ssmClient:     ssm.NewFromConfig(awsConfig),
	}
}

// supportedFilterKeys maps normalized --filter keys to a matcher against a
// built summary. Keys are matched case-insensitively.
var supportedFilterKeys = map[string]func(s NodegroupSummary, want string) bool{
	"name": func(s NodegroupSummary, want string) bool {
		return strings.Contains(strings.ToLower(s.Name), strings.ToLower(want))
	},
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
	if clusterDesc.Cluster == nil {
		return nil, fmt.Errorf("empty DescribeCluster response for %s", clusterName)
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

	// The latest AMI is constant per (cluster version, AMI type); memoize the
	// SSM lookup across the (concurrent) per-nodegroup work.
	latestAMI := s.newLatestAMIResolver(k8sVersion)

	results := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, name string) *NodegroupSummary {
			desc, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
				return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(name),
				})
			})
			if err != nil {
				s.logger.Warn("failed to describe nodegroup", "cluster", clusterName, "nodegroup", name, "error", err)
				return nil
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
				instanceType = ng.InstanceTypes[0]
			}

			currentAmiId := awsinternal.CurrentAmiID(fctx, ng, s.ec2Client, s.asgClient)
			latestAmiId := latestAMI(fctx, ng.AmiType)
			amiStatus := classifyAMI(ng.AmiType, ng.Status, currentAmiId, latestAmiId)

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
				return nil
			}
			return &summary
		})

	summaries := make([]NodegroupSummary, 0, len(results))
	for _, r := range results {
		if r != nil {
			summaries = append(summaries, *r)
		}
	}
	return summaries, nil
}

// newLatestAMIResolver returns a concurrency-safe, memoized resolver for the
// latest recommended AMI per AMI type at the given cluster version.
func (s *ServiceImpl) newLatestAMIResolver(k8sVersion string) func(context.Context, ekstypes.AMITypes) string {
	var mu sync.Mutex
	byType := make(map[ekstypes.AMITypes]string)
	return func(ctx context.Context, amiType ekstypes.AMITypes) string {
		mu.Lock()
		if v, ok := byType[amiType]; ok {
			mu.Unlock()
			return v
		}
		mu.Unlock()
		v := awsinternal.LatestAmiIDForType(ctx, s.ssmClient, k8sVersion, amiType)
		mu.Lock()
		byType[amiType] = v
		mu.Unlock()
		return v
	}
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
	if clusterDesc.Cluster == nil {
		return nil, fmt.Errorf("empty DescribeCluster response for %s", clusterName)
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
	amiStatus := classifyAMI(ng.AmiType, ng.Status, currentAmiId, latestAmiId)

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
	// Resolve backing instances once from the nodegroup we already described;
	// workloads and instance details reuse the result.
	var instanceIDs []string
	if options.ShowWorkloads || options.ShowInstances {
		instanceIDs, _ = s.instanceIDsForNodegroup(ctx, ng)
	}

	if options.ShowWorkloads {
		details.Workloads.PodDisruption = "no data"
		if wi, ok := s.analyzeWorkloads(ctx, aws.ToString(ng.NodegroupName), instanceIDs); ok {
			details.Workloads = wi
		} else {
			details.Workloads.PodDisruption = "unavailable: Kubernetes API not accessible or no matching nodes"
		}
	}
	if options.ShowInstances && len(instanceIDs) > 0 {
		if insts, ok := s.getInstanceDetails(ctx, instanceIDs); ok {
			details.Instances = insts
		}
	}
	return details, nil
}
