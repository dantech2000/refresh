package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EKSAPI abstracts the subset of EKS client methods used for nodegroups
type EKSAPI interface {
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	UpdateNodegroupConfig(ctx context.Context, params *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
}

// Service defines nodegroup operations
type Service interface {
	List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error)
	Describe(ctx context.Context, clusterName, nodegroupName string, options DescribeOptions) (*NodegroupDetails, error)
	Scale(ctx context.Context, clusterName, nodegroupName string, desired, min, max *int32, options ScaleOptions) error
	GetRecommendations(ctx context.Context, clusterName string, options RecommendationOptions) ([]Recommendation, error)
}

// ServiceImpl implements Service
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

// NewService creates a new nodegroup service
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	cache := NewCache()
	cw := cloudwatch.NewFromConfig(awsConfig)
	// Pricing is globally in us-east-1 for most accounts
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
	return svc
}

// List returns basic nodegroup summaries for a cluster
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error) {
	s.logger.Info("listing nodegroups", "cluster", clusterName, "options", options)

	// Get cluster information first for Kubernetes version (needed for AMI lookup)
	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{
			Name: aws.String(clusterName),
		})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	var nodegroupNames []string
	var nextToken *string
	for {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   nextToken,
			})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("listing nodegroups for cluster %s", clusterName))
		}
		nodegroupNames = append(nodegroupNames, out.Nodegroups...)
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
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

		// AMI Status Detection - Core functionality of refresh tool
		currentAmiId := awsinternal.CurrentAmiID(ctx, ng, s.ec2Client, s.asgClient)
		latestAmiId := awsinternal.LatestAmiIDForType(ctx, s.ssmClient, k8sVersion, ng.AmiType)

		var amiStatus types.AMIStatus
		if ng.Status == ekstypes.NodegroupStatusUpdating {
			amiStatus = types.AMIUpdating
		} else if currentAmiId == "" || latestAmiId == "" {
			amiStatus = types.AMIUnknown
		} else if currentAmiId == latestAmiId {
			amiStatus = types.AMILatest
		} else {
			amiStatus = types.AMIOutdated
		}

		summary := NodegroupSummary{
			Name:         aws.ToString(ng.NodegroupName),
			Status:       string(ng.Status),
			InstanceType: instanceType,
			DesiredSize:  aws.ToInt32(ng.ScalingConfig.DesiredSize),
			ReadyNodes:   ready,
			CurrentAMI:   currentAmiId,
			AMIStatus:    amiStatus,
		}
		// Optional enrichments (fail silently - missing data shown as "-" in output)
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

// Describe returns expanded details for a nodegroup (minimal for now)
func (s *ServiceImpl) Describe(ctx context.Context, clusterName, nodegroupName string, options DescribeOptions) (*NodegroupDetails, error) {
	s.logger.Info("describing nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "options", options)

	// Get cluster information first for Kubernetes version (needed for AMI lookup)
	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{
			Name: aws.String(clusterName),
		})
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

	// AMI Status Detection - Core functionality of refresh tool
	currentAmiId := awsinternal.CurrentAmiID(ctx, ng, s.ec2Client, s.asgClient)
	latestAmiId := awsinternal.LatestAmiIDForType(ctx, s.ssmClient, k8sVersion, ng.AmiType)

	var amiStatus types.AMIStatus
	if ng.Status == ekstypes.NodegroupStatusUpdating {
		amiStatus = types.AMIUpdating
	} else if currentAmiId == "" || latestAmiId == "" {
		amiStatus = types.AMIUnknown
	} else if currentAmiId == latestAmiId {
		amiStatus = types.AMILatest
	} else {
		amiStatus = types.AMIOutdated
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
		Scaling: ScalingConfig{
			DesiredSize: aws.ToInt32(ng.ScalingConfig.DesiredSize),
			MinSize:     aws.ToInt32(ng.ScalingConfig.MinSize),
			MaxSize:     aws.ToInt32(ng.ScalingConfig.MaxSize),
			AutoScaling: ng.ScalingConfig != nil,
		},
	}
	if options.ShowUtilization && s.util != nil {
		// Derive instance IDs from ASG and use EC2 CPU metrics
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
			details.Cost = CostAnalysis{CurrentMonthlyCost: perMonth * nodes, ProjectedMonthlyCost: perMonth * nodes, CostPerNode: perMonth}
		}
	}
	if options.ShowWorkloads {
		// Ensure section renders even if analysis fails
		details.Workloads.PodDisruption = "no data"
		if wi, ok := s.analyzeWorkloads(ctx, clusterName, aws.ToString(ng.NodegroupName)); ok {
			details.Workloads = wi
		} else {
			// Surface a helpful note so users see why it's empty
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

func firstInstanceType(types []string) string {
	if len(types) == 0 {
		return "Unknown"
	}
	return types[0]
}

func normalizeWindow(w string) string {
	switch w {
	case "1h", "3h", "24h":
		return w
	default:
		return "24h"
	}
}

// getNodegroupInstanceIDs resolves backing ASG instances for a managed nodegroup
func (s *ServiceImpl) getNodegroupInstanceIDs(ctx context.Context, clusterName, nodegroupName string) ([]string, bool) {
	// Describe nodegroup to get resources -> autoScalingGroups
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(nodegroupName)})
	})
	if err != nil || out.Nodegroup == nil || out.Nodegroup.Resources == nil {
		return nil, false
	}
	var asgNames []string
	for _, asg := range out.Nodegroup.Resources.AutoScalingGroups {
		if asg.Name != nil && *asg.Name != "" {
			asgNames = append(asgNames, *asg.Name)
		}
	}
	if len(asgNames) == 0 || s.asgClient == nil {
		return nil, false
	}
	// Describe each ASG to collect instance IDs
	var ids []string
	for _, name := range asgNames {
		asgOut, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
			return s.asgClient.DescribeAutoScalingGroups(rc, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{name}})
		})
		if err != nil || len(asgOut.AutoScalingGroups) == 0 {
			continue
		}
		for _, inst := range asgOut.AutoScalingGroups[0].Instances {
			if inst.InstanceId != nil && *inst.InstanceId != "" {
				ids = append(ids, *inst.InstanceId)
			}
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

// getInstanceDetails describes EC2 instances and converts to InstanceDetails
func (s *ServiceImpl) getInstanceDetails(ctx context.Context, instanceIDs []string) ([]InstanceDetails, bool) {
	if s.ec2Client == nil || len(instanceIDs) == 0 {
		return nil, false
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeInstancesOutput, error) {
		return s.ec2Client.DescribeInstances(rc, &ec2.DescribeInstancesInput{InstanceIds: instanceIDs})
	})
	if err != nil {
		s.logger.Warn("failed to describe instances", "error", err)
		return nil, false
	}
	var results []InstanceDetails
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			lifecycle := "on-demand"
			if inst.InstanceLifecycle != "" {
				lifecycle = string(inst.InstanceLifecycle)
			}
			state := ""
			if inst.State != nil {
				state = string(inst.State.Name)
			}
			az := ""
			if inst.Placement != nil && inst.Placement.AvailabilityZone != nil {
				az = *inst.Placement.AvailabilityZone
			}
			id := aws.ToString(inst.InstanceId)
			it := string(inst.InstanceType)
			lt := aws.ToTime(inst.LaunchTime)
			results = append(results, InstanceDetails{
				InstanceID:   id,
				InstanceType: it,
				LaunchTime:   lt,
				Lifecycle:    lifecycle,
				State:        state,
				AZ:           az,
			})
		}
	}
	return results, len(results) > 0
}

// analyzeWorkloads summarizes pods running on nodegroup nodes (all namespaces) and PDB posture
func (s *ServiceImpl) analyzeWorkloads(ctx context.Context, clusterName, nodegroupName string) (WorkloadInfo, bool) {
	k8s, err := health.GetKubernetesClient()
	if err != nil || k8s == nil {
		return WorkloadInfo{}, false
	}
	// Resolve instance IDs for this nodegroup
	instanceIDs, ok := s.getNodegroupInstanceIDs(ctx, clusterName, nodegroupName)
	if !ok || len(instanceIDs) == 0 {
		return WorkloadInfo{}, false
	}
	idSet := make(map[string]struct{}, len(instanceIDs))
	for _, id := range instanceIDs {
		idSet[id] = struct{}{}
	}

	// Map nodes by providerID -> instanceID
	nodes, err := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return WorkloadInfo{}, false
	}
	nodeOnNg := make(map[string]bool)
	for _, n := range nodes.Items {
		if n.Spec.ProviderID == "" {
			continue
		}
		iid := extractInstanceIDFromProviderID(n.Spec.ProviderID)
		if iid == "" {
			continue
		}
		if _, exists := idSet[iid]; exists {
			nodeOnNg[n.Name] = true
		}
	}
	if len(nodeOnNg) == 0 {
		// Fallback to label selector if providerID mapping failed
		selector := fmt.Sprintf("eks.amazonaws.com/nodegroup=%s", nodegroupName)
		if labeled, lerr := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector}); lerr == nil {
			for _, n := range labeled.Items {
				nodeOnNg[n.Name] = true
			}
		}
		if len(nodeOnNg) == 0 {
			return WorkloadInfo{}, false
		}
	}

	// List all pods and filter by nodeName
	pods, err := k8s.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return WorkloadInfo{}, false
	}
	total := 0
	critical := 0
	for _, p := range pods.Items {
		if nodeOnNg[p.Spec.NodeName] {
			if p.Status.Phase == corev1.PodSucceeded {
				continue
			}
			total++
			if p.Namespace == "kube-system" {
				critical++
			}
		}
	}

	// Basic PDB presence count
	pdbs, _ := k8s.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	pdbStatus := "Unknown"
	if pdbs != nil {
		pdbStatus = fmt.Sprintf("%d PDBs observed", len(pdbs.Items))
	}
	return WorkloadInfo{TotalPods: total, CriticalPods: critical, PodDisruption: pdbStatus}, true
}

func extractInstanceIDFromProviderID(providerID string) string {
	// providerID formats: aws:///us-west-2a/i-123, aws://us-west-2a/i-123 or aws:///i-123
	if providerID == "" {
		return ""
	}
	// Find last '/'
	for i := len(providerID) - 1; i >= 0; i-- {
		if providerID[i] == '/' {
			return providerID[i+1:]
		}
	}
	return ""
}

// Scale updates the desired/min/max size for a nodegroup
func (s *ServiceImpl) Scale(ctx context.Context, clusterName, nodegroupName string, desired, min, max *int32, options ScaleOptions) error {
	s.logger.Info("scaling nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "desired", desired, "min", min, "max", max, "options", options)
	if options.DryRun {
		// No-op for now; later we can simulate checks and output impact
		return nil
	}

	// Pre-scaling health validation
	if options.HealthCheck && s.healthChecker != nil {
		summary := s.healthChecker.RunAllChecks(ctx, clusterName)
		if summary.Decision == health.DecisionBlock {
			return fmt.Errorf("pre-scaling health check blocked operation: %v", summary.Errors)
		}
		if summary.Decision == health.DecisionWarn {
			s.logger.Warn("pre-scaling health warnings", "warnings", summary.Warnings)
		}
	}

	// If scaling down, optionally validate PDBs
	if options.CheckPDBs && s.healthChecker != nil {
		// Determine if desired is a scale down relative to current desired
		if desired != nil {
			desc, err := s.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(nodegroupName)})
			if err == nil && desc.Nodegroup.ScalingConfig != nil && desc.Nodegroup.ScalingConfig.DesiredSize != nil {
				current := *desc.Nodegroup.ScalingConfig.DesiredSize
				if *desired < current {
					pdb := s.healthChecker.CheckPodDisruptionBudgets(ctx)
					if pdb.Status == health.StatusFail && pdb.IsBlocking {
						return fmt.Errorf("PDB validation failed: %s", pdb.Message)
					}
				}
			}
		}
	}

	input := &eks.UpdateNodegroupConfigInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(nodegroupName),
	}
	if desired != nil || min != nil || max != nil {
		input.ScalingConfig = &ekstypes.NodegroupScalingConfig{
			DesiredSize: desired,
			MinSize:     min,
			MaxSize:     max,
		}
	}

	_, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateNodegroupConfigOutput, error) {
		return s.eksClient.UpdateNodegroupConfig(rc, input)
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("updating nodegroup scaling for %s/%s", clusterName, nodegroupName))
	}

	// Optionally wait for completion
	if options.Wait {
		waitCtx := ctx
		var cancel context.CancelFunc
		if options.Timeout > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, options.Timeout)
			defer cancel()
		}
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-waitCtx.Done():
				return fmt.Errorf("timed out waiting for nodegroup scaling to complete: %w", waitCtx.Err())
			case <-ticker.C:
				out, derr := s.eksClient.DescribeNodegroup(waitCtx, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(nodegroupName),
				})
				if derr != nil {
					s.logger.Warn("failed to describe nodegroup while waiting", "error", derr)
					continue
				}
				ng := out.Nodegroup
				// Consider complete when status is ACTIVE again and desired (if set) is reached
				if ng.Status == ekstypes.NodegroupStatusActive {
					if desired == nil || (ng.ScalingConfig != nil && ng.ScalingConfig.DesiredSize != nil && *ng.ScalingConfig.DesiredSize == *desired) {
						// Completed
						goto POST_HEALTH
					}
				}
			}
		}
	}

POST_HEALTH:
	// Post-scaling health validation
	if options.HealthCheck && s.healthChecker != nil {
		summary := s.healthChecker.RunAllChecks(ctx, clusterName)
		if summary.Decision == health.DecisionBlock {
			return fmt.Errorf("post-scaling health check blocked operation: %v", summary.Errors)
		}
		if summary.Decision == health.DecisionWarn {
			s.logger.Warn("post-scaling health warnings", "warnings", summary.Warnings)
		}
	}
	return nil
}

// GetRecommendations returns placeholder recommendations until analyzers are implemented
func (s *ServiceImpl) GetRecommendations(ctx context.Context, clusterName string, options RecommendationOptions) ([]Recommendation, error) {
	// Placeholder examples; future work will compute based on utilization/cost
	recs := []Recommendation{}
	if options.RightSizing {
		recs = append(recs, Recommendation{
			Type:            "right-size",
			Priority:        "medium",
			Impact:          "cost",
			Description:     "Consider using a smaller instance type based on average CPU < 40%",
			Implementation:  "Migrate from m5.large to m5a.large",
			ExpectedSavings: 15.0,
			RiskLevel:       "low",
		})
	}
	if options.SpotAnalysis {
		recs = append(recs, Recommendation{
			Type:            "spot-integration",
			Priority:        "low",
			Impact:          "cost",
			Description:     "Shift 30% of capacity to Spot for tolerant workloads",
			Implementation:  "Enable mixed instances policy with Spot",
			ExpectedSavings: 25.0,
			RiskLevel:       "medium",
		})
	}
	return recs, nil
}
