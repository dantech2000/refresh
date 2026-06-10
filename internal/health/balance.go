package health

import (
	"context"
	"fmt"
	"math"
)

// CheckResourceBalance validates resource distribution and utilization patterns
func (hc *HealthChecker) CheckResourceBalance(ctx context.Context, clusterName string) HealthResult {
	return hc.checkResourceBalanceWith(ctx, hc.newCPUSnapshot(clusterName))
}

// checkResourceBalanceWith is CheckResourceBalance against a (possibly
// shared) CPU snapshot, so RunAllChecks fetches metrics once for both the
// capacity and balance checks.
func (hc *HealthChecker) checkResourceBalanceWith(ctx context.Context, snap *cpuSnapshot) HealthResult {
	result := HealthResult{
		Name:       "Resource Balance",
		IsBlocking: false, // Resource balance is warning-level
		Details:    []string{},
	}

	// Get node-level metrics using default EC2 metrics (no prerequisites required)
	nodeMetrics, err := hc.nodeMetricsFromSnapshot(ctx, snap)
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

	// Clamp to the documented 0-100 range: extreme variance/utilization would
	// otherwise drive the score negative and skew the overall average.
	result.Score = int(math.Max(0, math.Min(balanceScore, utilizationScore)))

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

// nodeMetricsFromSnapshot converts the snapshot's per-instance averages into
// NodeMetrics entries (one batched CloudWatch fetch instead of one call per
// instance). Node names are the EC2 instance IDs.
func (hc *HealthChecker) nodeMetricsFromSnapshot(ctx context.Context, snap *cpuSnapshot) ([]NodeMetrics, error) {
	cpuByInstance, err := snap.get(ctx)
	if err != nil {
		return nil, err
	}

	nodeMetrics := make([]NodeMetrics, 0, len(cpuByInstance))
	for instanceID, cpuPercent := range cpuByInstance {
		nodeMetrics = append(nodeMetrics, NodeMetrics{
			NodeName:      instanceID,
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
