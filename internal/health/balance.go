package health

import (
	"context"
	"fmt"
	"math"

	"github.com/dantech2000/refresh/internal/aws/awserr"
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
		result.Message = fmt.Sprintf("Unable to fetch detailed metrics: %s", awserr.Summary(err))
		result.Details = append(result.Details, "Node-level EC2 metrics unavailable")
		return result
	}

	// Analyze resource distribution
	analysis := hc.analyzeResourceDistribution(nodeMetrics)

	result.Details = append(result.Details, fmt.Sprintf("Analyzed %d nodes", len(nodeMetrics)))
	result.Details = append(result.Details, fmt.Sprintf("CPU spread (std dev): %.1f pts", analysis.CPUStdDev))
	result.Details = append(result.Details, "Memory spread: Not available (requires CloudWatch agent)")
	result.Details = append(result.Details, fmt.Sprintf("Max CPU utilization: %.1f%%", analysis.MaxCPU))
	result.Details = append(result.Details, "Max Memory utilization: Not available (requires CloudWatch agent)")

	// Determine status based on CPU balance and utilization only. Thresholds
	// below are expressed in std-dev of per-node CPU, in percentage points.
	cpuSpread := analysis.CPUStdDev
	maxUtilization := analysis.MaxCPU

	// Score based on both balance (CPU std dev) and peak utilization.
	balanceScore := 100.0
	if cpuSpread > 30 {
		balanceScore -= (cpuSpread - 30) * 2 // Penalize a wide CPU spread
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
	} else if cpuSpread > 40 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("Uneven CPU distribution (std dev: %.1f pts)", cpuSpread)
		result.Details = append(result.Details, "Workload distribution may cause issues during rolling update")
	} else if maxUtilization > 80 || cpuSpread > 25 {
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

// NodeMetrics is the CPU utilization of a single node.
type NodeMetrics struct {
	CPUPercent float64
}

// ResourceAnalysis describes how CPU load is spread across nodes. CPUStdDev
// is the population standard deviation of per-node CPU utilization, in
// percentage points (a spread measure, not statistical variance).
type ResourceAnalysis struct {
	CPUStdDev float64
	MaxCPU    float64
}

// nodeMetricsFromSnapshot converts the snapshot's per-instance averages into
// NodeMetrics entries (one batched CloudWatch fetch instead of one call per
// instance).
func (hc *HealthChecker) nodeMetricsFromSnapshot(ctx context.Context, snap *cpuSnapshot) ([]NodeMetrics, error) {
	cpuByInstance, err := snap.get(ctx)
	if err != nil {
		return nil, err
	}

	nodeMetrics := make([]NodeMetrics, 0, len(cpuByInstance))
	for _, cpuPercent := range cpuByInstance {
		nodeMetrics = append(nodeMetrics, NodeMetrics{CPUPercent: cpuPercent})
	}

	if len(nodeMetrics) == 0 {
		return nil, fmt.Errorf("no EC2 CPU metrics available for cluster instances")
	}

	return nodeMetrics, nil
}

// analyzeResourceDistribution computes the peak and spread of per-node CPU.
func (hc *HealthChecker) analyzeResourceDistribution(metrics []NodeMetrics) ResourceAnalysis {
	if len(metrics) == 0 {
		return ResourceAnalysis{}
	}

	analysis := ResourceAnalysis{MaxCPU: metrics[0].CPUPercent}
	cpuSum := 0.0
	for _, metric := range metrics {
		if metric.CPUPercent > analysis.MaxCPU {
			analysis.MaxCPU = metric.CPUPercent
		}
		cpuSum += metric.CPUPercent
	}
	cpuAvg := cpuSum / float64(len(metrics))

	// Population standard deviation (sqrt of mean squared deviation) of
	// per-node CPU: a spread measure in percentage points.
	cpuSquaredDevSum := 0.0
	for _, metric := range metrics {
		cpuDiff := metric.CPUPercent - cpuAvg
		cpuSquaredDevSum += cpuDiff * cpuDiff
	}
	analysis.CPUStdDev = math.Sqrt(cpuSquaredDevSum / float64(len(metrics)))

	return analysis
}
