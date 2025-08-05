package health

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// CheckResourceBalance validates resource distribution and utilization patterns
func (hc *HealthChecker) CheckResourceBalance(ctx context.Context, clusterName string) HealthResult {
	result := HealthResult{
		Name:       "Resource Balance",
		IsBlocking: false, // Resource balance is warning-level
		Details:    []string{},
	}

	// Get node-level metrics using default EC2 metrics (no prerequisites required)
	nodeMetrics, err := hc.getNodeLevelEC2Metrics(ctx, clusterName)
	if err != nil {
		result.Status = StatusWarn
		result.Score = 80
		result.Message = fmt.Sprintf("Unable to fetch detailed metrics: %v", err)
		result.Details = append(result.Details, "Node-level EC2 metrics unavailable")
		return result
	}

	// Analyze resource distribution
	analysis := hc.analyzeResourceDistribution(nodeMetrics)

	result.Details = append(result.Details, fmt.Sprintf("Analyzed %d nodes", len(nodeMetrics)))
	result.Details = append(result.Details, fmt.Sprintf("CPU variance: %.1f%%", analysis.CPUVariance))
	result.Details = append(result.Details, "Memory variance: Not available (requires CloudWatch agent)")
	result.Details = append(result.Details, fmt.Sprintf("Max CPU utilization: %.1f%%", analysis.MaxCPU))
	result.Details = append(result.Details, "Max Memory utilization: Not available (requires CloudWatch agent)")

	// Determine status based on CPU balance and utilization only
	maxVariance := analysis.CPUVariance
	maxUtilization := analysis.MaxCPU

	// Score based on both balance and peak utilization
	balanceScore := 100.0
	if maxVariance > 30 {
		balanceScore -= (maxVariance - 30) * 2 // Penalize high variance
	}

	utilizationScore := 100.0
	if maxUtilization > 85 {
		utilizationScore -= (maxUtilization - 85) * 3 // Penalize high utilization
	}

	result.Score = int(math.Min(balanceScore, utilizationScore))

	// Determine status based on CPU-only analysis
	if maxUtilization > 90 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("High CPU utilization detected (max: %.1f%%)", maxUtilization)
		result.Details = append(result.Details, "Consider scaling before update")
	} else if maxVariance > 40 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("Uneven CPU distribution (variance: %.1f%%)", maxVariance)
		result.Details = append(result.Details, "Workload distribution may cause issues during rolling update")
	} else if maxUtilization > 80 || maxVariance > 25 {
		result.Status = StatusWarn
		result.Message = "Moderate CPU utilization detected"
		result.Details = append(result.Details, "Monitor closely during update")
	} else {
		result.Status = StatusPass
		result.Message = "CPU distribution and utilization within acceptable ranges"
		result.Details = append(result.Details, "Memory analysis requires Container Insights setup")
	}

	return result
}

// NodeMetrics represents resource metrics for a single node
type NodeMetrics struct {
	NodeName      string
	CPUPercent    float64
	MemoryPercent float64
}

// ResourceAnalysis contains analysis of resource distribution
type ResourceAnalysis struct {
	CPUVariance    float64
	MemoryVariance float64
	MaxCPU         float64
	MaxMemory      float64
	MinCPU         float64
	MinMemory      float64
}

// getNodeLevelEC2Metrics retrieves per-node resource metrics using default EC2 metrics
func (hc *HealthChecker) getNodeLevelEC2Metrics(ctx context.Context, clusterName string) ([]NodeMetrics, error) {
	if hc.cwClient == nil {
		return nil, fmt.Errorf("CloudWatch client not available")
	}

	// Get instance IDs and names from EKS
	instanceData, err := hc.getClusterInstanceData(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance data: %v", err)
	}

	if len(instanceData) == 0 {
		return nil, fmt.Errorf("no instances found in cluster")
	}

	var nodeMetrics []NodeMetrics

	// Get metrics for each instance using default EC2 metrics
	for _, instance := range instanceData {
		cpuPercent, err := hc.getEC2InstanceCPU(ctx, instance.InstanceID)
		if err != nil {
			// Skip instances without metrics but continue
			continue
		}

		nodeMetrics = append(nodeMetrics, NodeMetrics{
			NodeName:      instance.NodeName,
			CPUPercent:    cpuPercent,
			MemoryPercent: 0, // Memory not available without CloudWatch agent
		})
	}

	if len(nodeMetrics) == 0 {
		return nil, fmt.Errorf("no EC2 CPU metrics available for cluster instances")
	}

	return nodeMetrics, nil
}

// analyzeResourceDistribution analyzes the distribution of resources across nodes
func (hc *HealthChecker) analyzeResourceDistribution(metrics []NodeMetrics) ResourceAnalysis {
	if len(metrics) == 0 {
		return ResourceAnalysis{}
	}

	// Calculate min, max, and variance
	analysis := ResourceAnalysis{
		MaxCPU:    metrics[0].CPUPercent,
		MinCPU:    metrics[0].CPUPercent,
		MaxMemory: metrics[0].MemoryPercent,
		MinMemory: metrics[0].MemoryPercent,
	}

	cpuSum := 0.0
	memorySum := 0.0

	// Find min/max and calculate sum
	for _, metric := range metrics {
		if metric.CPUPercent > analysis.MaxCPU {
			analysis.MaxCPU = metric.CPUPercent
		}
		if metric.CPUPercent < analysis.MinCPU {
			analysis.MinCPU = metric.CPUPercent
		}
		if metric.MemoryPercent > analysis.MaxMemory {
			analysis.MaxMemory = metric.MemoryPercent
		}
		if metric.MemoryPercent < analysis.MinMemory {
			analysis.MinMemory = metric.MemoryPercent
		}

		cpuSum += metric.CPUPercent
		memorySum += metric.MemoryPercent
	}

	// Calculate averages
	cpuAvg := cpuSum / float64(len(metrics))
	memoryAvg := memorySum / float64(len(metrics))

	// Calculate variance
	cpuVarianceSum := 0.0
	memoryVarianceSum := 0.0

	for _, metric := range metrics {
		cpuDiff := metric.CPUPercent - cpuAvg
		memoryDiff := metric.MemoryPercent - memoryAvg
		cpuVarianceSum += cpuDiff * cpuDiff
		memoryVarianceSum += memoryDiff * memoryDiff
	}

	analysis.CPUVariance = math.Sqrt(cpuVarianceSum / float64(len(metrics)))
	analysis.MemoryVariance = math.Sqrt(memoryVarianceSum / float64(len(metrics)))

	return analysis
}


// InstanceData represents an EC2 instance with its associated node name
type InstanceData struct {
	InstanceID string
	NodeName   string
}

// getClusterInstanceData retrieves instance IDs with their associated node names
func (hc *HealthChecker) getClusterInstanceData(ctx context.Context, clusterName string) ([]InstanceData, error) {
	// Get instance IDs from nodegroups
	instanceIDs, err := hc.getClusterInstanceIDs(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	var instanceData []InstanceData
	for i, instanceID := range instanceIDs {
		// For now, generate synthetic node names since we don't have a direct mapping
		// In a real implementation, you might want to use EC2 tags or other methods
		instanceData = append(instanceData, InstanceData{
			InstanceID: instanceID,
			NodeName:   fmt.Sprintf("node-%d", i+1),
		})
	}

	return instanceData, nil
}

// getEC2InstanceCPU retrieves CPU utilization for a specific EC2 instance
func (hc *HealthChecker) getEC2InstanceCPU(ctx context.Context, instanceID string) (float64, error) {
	endTime := time.Now()
	startTime := endTime.Add(-10 * time.Minute) // Look at last 10 minutes

	input := &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/EC2"),
		MetricName: aws.String("CPUUtilization"),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String("InstanceId"),
				Value: aws.String(instanceID),
			},
		},
		StartTime:  aws.Time(startTime),
		EndTime:    aws.Time(endTime),
		Period:     aws.Int32(300), // 5 minute periods
		Statistics: []types.Statistic{types.StatisticAverage},
	}

	output, err := hc.cwClient.GetMetricStatistics(ctx, input)
	if err != nil {
		return 0, err
	}

	// Calculate average from available datapoints
	if len(output.Datapoints) == 0 {
		return 0, fmt.Errorf("no CPU datapoints available for instance %s", instanceID)
	}

	sum := 0.0
	count := 0
	for _, datapoint := range output.Datapoints {
		if datapoint.Average != nil {
			sum += *datapoint.Average
			count++
		}
	}

	if count == 0 {
		return 0, fmt.Errorf("no valid CPU datapoints for instance %s", instanceID)
	}

	return sum / float64(count), nil
}
