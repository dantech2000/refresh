package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/dantech2000/refresh/internal/services/common"
)

// RecommendationsAnalyzer generates optimization recommendations for nodegroups
type RecommendationsAnalyzer struct {
	eksClient EKSAPI
	ec2Client *ec2.Client
	asgClient ASGClient
	util      *UtilizationCollector
	cost      *CostAnalyzer
	logger    *slog.Logger
	cache     *Cache
}

// ASGClient abstracts autoscaling client methods needed for recommendations
type ASGClient interface {
	DescribeAutoScalingGroups(ctx context.Context, params *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

// NewRecommendationsAnalyzer creates a new analyzer
func NewRecommendationsAnalyzer(eksClient EKSAPI, ec2Client *ec2.Client, asgClient ASGClient, util *UtilizationCollector, cost *CostAnalyzer, logger *slog.Logger, cache *Cache) *RecommendationsAnalyzer {
	return &RecommendationsAnalyzer{
		eksClient: eksClient,
		ec2Client: ec2Client,
		asgClient: asgClient,
		util:      util,
		cost:      cost,
		logger:    logger,
		cache:     cache,
	}
}

// NodegroupAnalysisData contains data collected for analysis
type NodegroupAnalysisData struct {
	Name         string
	InstanceType string
	DesiredSize  int32
	MinSize      int32
	MaxSize      int32
	CapacityType string
	Instances    []InstanceDetails
	CPUUtil      UtilizationData
	HasCPUData   bool
	HourlyCost   float64
	MonthlyCost  float64
	HasCostData  bool
}

// AnalyzeNodegroups generates recommendations based on actual cluster data
func (a *RecommendationsAnalyzer) AnalyzeNodegroups(ctx context.Context, clusterName string, options RecommendationOptions) ([]Recommendation, error) {
	// Gather nodegroup data
	nodegroups, err := a.collectNodegroupData(ctx, clusterName, options)
	if err != nil {
		return nil, err
	}

	var recommendations []Recommendation

	for _, ng := range nodegroups {
		// Skip if specific nodegroup requested and this isn't it
		if options.Nodegroup != "" && ng.Name != options.Nodegroup {
			continue
		}

		// Right-sizing analysis
		if options.RightSizing {
			recs := a.analyzeRightSizing(ctx, ng)
			recommendations = append(recommendations, recs...)
		}

		// Spot analysis
		if options.SpotAnalysis {
			recs := a.analyzeSpotOpportunities(ctx, ng, clusterName)
			recommendations = append(recommendations, recs...)
		}

		// Cost optimization (general)
		if options.CostOptimization {
			recs := a.analyzeCostOptimization(ctx, ng)
			recommendations = append(recommendations, recs...)
		}

		// Performance optimization
		if options.PerformanceOptimization {
			recs := a.analyzePerformance(ctx, ng)
			recommendations = append(recommendations, recs...)
		}
	}

	// Sort recommendations by priority
	sort.Slice(recommendations, func(i, j int) bool {
		return priorityOrder(recommendations[i].Priority) < priorityOrder(recommendations[j].Priority)
	})

	return recommendations, nil
}

func priorityOrder(p string) int {
	switch p {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

// collectNodegroupData gathers all data needed for analysis
func (a *RecommendationsAnalyzer) collectNodegroupData(ctx context.Context, clusterName string, options RecommendationOptions) ([]NodegroupAnalysisData, error) {
	// List nodegroups
	var nodegroupNames []string
	var nextToken *string
	for {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListNodegroupsOutput, error) {
			return a.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   nextToken,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("listing nodegroups: %w", err)
		}
		nodegroupNames = append(nodegroupNames, out.Nodegroups...)
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	var results []NodegroupAnalysisData
	for _, name := range nodegroupNames {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return a.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(name),
			})
		})
		if err != nil {
			a.logger.Warn("failed to describe nodegroup", "name", name, "error", err)
			continue
		}

		ng := out.Nodegroup
		instanceType := "Unknown"
		if len(ng.InstanceTypes) > 0 {
			instanceType = ng.InstanceTypes[0]
		}

		data := NodegroupAnalysisData{
			Name:         aws.ToString(ng.NodegroupName),
			InstanceType: instanceType,
			CapacityType: string(ng.CapacityType),
			DesiredSize:  aws.ToInt32(ng.ScalingConfig.DesiredSize),
			MinSize:      aws.ToInt32(ng.ScalingConfig.MinSize),
			MaxSize:      aws.ToInt32(ng.ScalingConfig.MaxSize),
		}

		// Collect utilization data if available
		if a.util != nil {
			window := normalizeWindow(options.Timeframe)
			if ids, ok := a.getNodegroupInstanceIDs(ctx, clusterName, name); ok {
				data.Instances = a.getInstanceDetails(ctx, ids)
				if cpu, ok := a.util.CollectEC2CPUForInstances(ctx, ids, window); ok {
					data.CPUUtil = cpu
					data.HasCPUData = true
				}
			}
		}

		// Collect cost data if available
		if a.cost != nil {
			if hourly, monthly, ok := a.cost.EstimateOnDemandUSD(ctx, instanceType); ok {
				data.HourlyCost = hourly * float64(data.DesiredSize)
				data.MonthlyCost = monthly * float64(data.DesiredSize)
				data.HasCostData = true
			}
		}

		results = append(results, data)
	}

	return results, nil
}

// analyzeRightSizing looks for over/under-provisioned nodegroups
func (a *RecommendationsAnalyzer) analyzeRightSizing(ctx context.Context, ng NodegroupAnalysisData) []Recommendation {
	var recs []Recommendation

	if !ng.HasCPUData {
		return recs
	}

	avgCPU := ng.CPUUtil.Average
	peakCPU := ng.CPUUtil.Peak

	// Under-utilized: average < 30% and peak < 50%
	if avgCPU < 30 && peakCPU < 50 && ng.HasCostData {
		smallerType := suggestSmallerInstance(ng.InstanceType)
		if smallerType != "" {
			savings := estimateRightSizingSavings(ng.MonthlyCost)
			recs = append(recs, Recommendation{
				Type:            "right-size",
				Priority:        "high",
				Impact:          "cost",
				Description:     fmt.Sprintf("Nodegroup '%s' is under-utilized (avg CPU: %.1f%%, peak: %.1f%%). Consider downsizing instances.", ng.Name, avgCPU, peakCPU),
				Implementation:  fmt.Sprintf("Migrate from %s to %s to reduce costs", ng.InstanceType, smallerType),
				ExpectedSavings: savings,
				RiskLevel:       "low",
			})
		}
	}

	// Over-utilized: average > 75% or peak > 90%
	if avgCPU > 75 || peakCPU > 90 {
		largerType := suggestLargerInstance(ng.InstanceType)
		if largerType != "" {
			recs = append(recs, Recommendation{
				Type:            "right-size",
				Priority:        "high",
				Impact:          "performance",
				Description:     fmt.Sprintf("Nodegroup '%s' may be resource-constrained (avg CPU: %.1f%%, peak: %.1f%%). Consider upsizing or adding nodes.", ng.Name, avgCPU, peakCPU),
				Implementation:  fmt.Sprintf("Consider migrating from %s to %s or increasing node count", ng.InstanceType, largerType),
				ExpectedSavings: 0,
				RiskLevel:       "low",
			})
		}
	}

	// Scaling optimization: if consistently at min/max
	if ng.DesiredSize == ng.MaxSize && avgCPU > 60 {
		recs = append(recs, Recommendation{
			Type:            "scaling",
			Priority:        "medium",
			Impact:          "reliability",
			Description:     fmt.Sprintf("Nodegroup '%s' is at max capacity (%d nodes) with elevated utilization. Consider increasing max size.", ng.Name, ng.MaxSize),
			Implementation:  fmt.Sprintf("Increase max size from %d to %d to allow auto-scaling headroom", ng.MaxSize, ng.MaxSize+2),
			ExpectedSavings: 0,
			RiskLevel:       "low",
		})
	}

	return recs
}

// analyzeSpotOpportunities identifies candidates for Spot instances
func (a *RecommendationsAnalyzer) analyzeSpotOpportunities(ctx context.Context, ng NodegroupAnalysisData, clusterName string) []Recommendation {
	var recs []Recommendation

	// Skip if already using Spot
	if strings.EqualFold(ng.CapacityType, "SPOT") {
		return recs
	}

	// Only recommend Spot for non-critical workloads and larger nodegroups
	if ng.DesiredSize < 2 {
		return recs
	}

	// Get spot price history for comparison
	spotSavings := a.estimateSpotSavings(ctx, ng.InstanceType)
	if spotSavings <= 0 {
		// Default estimate if we can't get actual prices
		spotSavings = 60.0
	}

	potentialMonthlySavings := ng.MonthlyCost * (spotSavings / 100)

	// Only recommend if savings are meaningful (> $50/month)
	if potentialMonthlySavings > 50 || (ng.MonthlyCost > 0 && spotSavings > 50) {
		recs = append(recs, Recommendation{
			Type:            "spot-integration",
			Priority:        "medium",
			Impact:          "cost",
			Description:     fmt.Sprintf("Nodegroup '%s' uses On-Demand instances. Spot instances can save ~%.0f%% for fault-tolerant workloads.", ng.Name, spotSavings),
			Implementation:  fmt.Sprintf("Create mixed instances policy with %d%% Spot capacity or create a parallel Spot nodegroup", 70),
			ExpectedSavings: math.Round(potentialMonthlySavings),
			RiskLevel:       "medium",
		})
	}

	return recs
}

// analyzeCostOptimization looks for general cost savings
func (a *RecommendationsAnalyzer) analyzeCostOptimization(ctx context.Context, ng NodegroupAnalysisData) []Recommendation {
	var recs []Recommendation

	// Check for cost-effective instance family alternatives
	alternative := suggestCostEffectiveAlternative(ng.InstanceType)
	if alternative != "" && ng.HasCostData {
		// Estimate savings from ARM/Graviton migration
		savings := estimateArchitectureSavings(ng.MonthlyCost)
		if savings > 0 {
			recs = append(recs, Recommendation{
				Type:            "instance-optimization",
				Priority:        "low",
				Impact:          "cost",
				Description:     fmt.Sprintf("Nodegroup '%s' uses %s. Consider AWS Graviton-based instances for better price-performance.", ng.Name, ng.InstanceType),
				Implementation:  fmt.Sprintf("Migrate to %s for improved cost efficiency (requires workload compatibility testing)", alternative),
				ExpectedSavings: savings,
				RiskLevel:       "medium",
			})
		}
	}

	// Reserved Instances recommendation for stable workloads
	if ng.HasCPUData && ng.CPUUtil.Trend == "stable" && ng.MinSize >= 2 && ng.HasCostData {
		riSavings := ng.MonthlyCost * 0.30 * float64(ng.MinSize) / float64(ng.DesiredSize)
		if riSavings > 100 {
			recs = append(recs, Recommendation{
				Type:            "reserved-instances",
				Priority:        "low",
				Impact:          "cost",
				Description:     fmt.Sprintf("Nodegroup '%s' has stable baseline capacity. Reserved Instances could save ~30%% on committed usage.", ng.Name),
				Implementation:  fmt.Sprintf("Purchase Reserved Instances for %d baseline nodes", ng.MinSize),
				ExpectedSavings: math.Round(riSavings),
				RiskLevel:       "low",
			})
		}
	}

	return recs
}

// analyzePerformance looks for performance improvements
func (a *RecommendationsAnalyzer) analyzePerformance(ctx context.Context, ng NodegroupAnalysisData) []Recommendation {
	var recs []Recommendation

	if !ng.HasCPUData {
		return recs
	}

	// High CPU variability suggests burst workloads
	if ng.CPUUtil.Peak > ng.CPUUtil.Average*2.5 && ng.CPUUtil.Average < 40 {
		recs = append(recs, Recommendation{
			Type:            "burstable-instances",
			Priority:        "low",
			Impact:          "cost",
			Description:     fmt.Sprintf("Nodegroup '%s' shows bursty workload patterns (avg: %.1f%%, peak: %.1f%%). T-family instances might be cost-effective.", ng.Name, ng.CPUUtil.Average, ng.CPUUtil.Peak),
			Implementation:  "Consider T3/T3a instances with unlimited credits for better burst handling",
			ExpectedSavings: 0,
			RiskLevel:       "low",
		})
	}

	// Multi-AZ distribution check
	if len(ng.Instances) >= 3 {
		azCounts := make(map[string]int)
		for _, inst := range ng.Instances {
			azCounts[inst.AZ]++
		}
		if len(azCounts) == 1 {
			recs = append(recs, Recommendation{
				Type:            "availability",
				Priority:        "high",
				Impact:          "reliability",
				Description:     fmt.Sprintf("Nodegroup '%s' has all %d nodes in a single AZ. This poses availability risk.", ng.Name, len(ng.Instances)),
				Implementation:  "Configure nodegroup subnets to span multiple availability zones",
				ExpectedSavings: 0,
				RiskLevel:       "low",
			})
		}
	}

	return recs
}

// estimateSpotSavings queries EC2 spot price history to estimate savings
func (a *RecommendationsAnalyzer) estimateSpotSavings(ctx context.Context, instanceType string) float64 {
	if a.ec2Client == nil {
		return 0
	}

	// Get spot price history
	out, err := a.ec2Client.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		MaxResults:          aws.Int32(10),
	})
	if err != nil || len(out.SpotPriceHistory) == 0 {
		return 0
	}

	// Calculate average spot price
	var sum float64
	for _, price := range out.SpotPriceHistory {
		var p float64
		if _, err := fmt.Sscanf(aws.ToString(price.SpotPrice), "%f", &p); err == nil {
			sum += p
		}
	}
	avgSpotPrice := sum / float64(len(out.SpotPriceHistory))

	// Get on-demand price for comparison
	if a.cost != nil {
		if onDemandHourly, _, ok := a.cost.EstimateOnDemandUSD(ctx, instanceType); ok && onDemandHourly > 0 {
			savings := (1 - avgSpotPrice/onDemandHourly) * 100
			if savings > 0 && savings < 95 { // Sanity check
				return savings
			}
		}
	}

	return 0
}

// Helper functions for instance type recommendations

func suggestSmallerInstance(current string) string {
	// Map common instance types to smaller alternatives
	alternatives := map[string]string{
		"m5.2xlarge":  "m5.xlarge",
		"m5.xlarge":   "m5.large",
		"m5.large":    "m5.medium",
		"m5a.2xlarge": "m5a.xlarge",
		"m5a.xlarge":  "m5a.large",
		"m5a.large":   "m5a.medium",
		"m6i.2xlarge": "m6i.xlarge",
		"m6i.xlarge":  "m6i.large",
		"m6i.large":   "m6i.medium",
		"c5.2xlarge":  "c5.xlarge",
		"c5.xlarge":   "c5.large",
		"r5.2xlarge":  "r5.xlarge",
		"r5.xlarge":   "r5.large",
		"t3.xlarge":   "t3.large",
		"t3.large":    "t3.medium",
		"t3.medium":   "t3.small",
	}
	return alternatives[current]
}

func suggestLargerInstance(current string) string {
	alternatives := map[string]string{
		"m5.medium":  "m5.large",
		"m5.large":   "m5.xlarge",
		"m5.xlarge":  "m5.2xlarge",
		"m5a.medium": "m5a.large",
		"m5a.large":  "m5a.xlarge",
		"m5a.xlarge": "m5a.2xlarge",
		"m6i.medium": "m6i.large",
		"m6i.large":  "m6i.xlarge",
		"m6i.xlarge": "m6i.2xlarge",
		"c5.large":   "c5.xlarge",
		"c5.xlarge":  "c5.2xlarge",
		"r5.large":   "r5.xlarge",
		"r5.xlarge":  "r5.2xlarge",
		"t3.small":   "t3.medium",
		"t3.medium":  "t3.large",
		"t3.large":   "t3.xlarge",
	}
	return alternatives[current]
}

func suggestCostEffectiveAlternative(current string) string {
	// Map x86 instances to Graviton alternatives
	alternatives := map[string]string{
		"m5.large":   "m6g.large",
		"m5.xlarge":  "m6g.xlarge",
		"m5.2xlarge": "m6g.2xlarge",
		"m5a.large":  "m6g.large",
		"m5a.xlarge": "m6g.xlarge",
		"m6i.large":  "m6g.large",
		"m6i.xlarge": "m6g.xlarge",
		"c5.large":   "c6g.large",
		"c5.xlarge":  "c6g.xlarge",
		"c5.2xlarge": "c6g.2xlarge",
		"r5.large":   "r6g.large",
		"r5.xlarge":  "r6g.xlarge",
		"t3.medium":  "t4g.medium",
		"t3.large":   "t4g.large",
		"t3.xlarge":  "t4g.xlarge",
	}
	return alternatives[current]
}

func estimateRightSizingSavings(monthlyCost float64) float64 {
	// Downsizing typically saves ~40-50%
	return math.Round(monthlyCost * 0.40)
}

func estimateArchitectureSavings(monthlyCost float64) float64 {
	// Graviton typically saves ~20%
	return math.Round(monthlyCost * 0.20)
}

// getNodegroupInstanceIDs resolves backing ASG instances
func (a *RecommendationsAnalyzer) getNodegroupInstanceIDs(ctx context.Context, clusterName, nodegroupName string) ([]string, bool) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return a.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
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
	if len(asgNames) == 0 || a.asgClient == nil {
		return nil, false
	}

	// Describe each ASG to collect instance IDs
	var ids []string
	for _, name := range asgNames {
		asgOut, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
			return a.asgClient.DescribeAutoScalingGroups(rc, &autoscaling.DescribeAutoScalingGroupsInput{
				AutoScalingGroupNames: []string{name},
			})
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

// getInstanceDetails retrieves EC2 instance details
func (a *RecommendationsAnalyzer) getInstanceDetails(ctx context.Context, instanceIDs []string) []InstanceDetails {
	if a.ec2Client == nil || len(instanceIDs) == 0 {
		return nil
	}

	out, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		return nil
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
			results = append(results, InstanceDetails{
				InstanceID:   aws.ToString(inst.InstanceId),
				InstanceType: string(inst.InstanceType),
				LaunchTime:   aws.ToTime(inst.LaunchTime),
				Lifecycle:    lifecycle,
				State:        state,
				AZ:           az,
			})
		}
	}
	return results
}
