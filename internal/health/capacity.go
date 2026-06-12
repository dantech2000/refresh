package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/services/common"
)

// CPU headroom thresholds for safe rolling updates: at least
// minSafeCPUHeadroomPercent free is a pass, at least
// minWarnCPUHeadroomPercent is a warning, anything less is a failure.
const (
	minSafeCPUHeadroomPercent = 30.0
	minWarnCPUHeadroomPercent = 15.0
)

// Per-node peak-CPU thresholds. The cluster mean can hide a single saturated
// node (one busy large instance averaged with idle small ones), so the peak
// node degrades — never upgrades — the mean-based verdict: a node at or above
// peakFailCPUPercent fails the check, and one at or above peakWarnCPUPercent
// downgrades a pass to a warning.
const (
	peakWarnCPUPercent = 85.0
	peakFailCPUPercent = 95.0
)

// CheckClusterCapacity validates that the cluster has sufficient capacity for rolling updates
func (hc *HealthChecker) CheckClusterCapacity(ctx context.Context, clusterName string) HealthResult {
	return hc.checkClusterCapacityWith(ctx, hc.newCPUSnapshot(clusterName))
}

// checkClusterCapacityWith is CheckClusterCapacity against a (possibly
// shared) CPU snapshot, so RunAllChecks fetches metrics once for both the
// capacity and balance checks.
func (hc *HealthChecker) checkClusterCapacityWith(ctx context.Context, snap *cpuSnapshot) HealthResult {
	result := HealthResult{
		Name:       "Cluster Capacity",
		IsBlocking: true, // Capacity is blocking
		Details:    []string{},
	}

	// Use default EC2 metrics (no prerequisites required)
	cpuByInstance, err := snap.get(ctx)
	if err != nil {
		result.Status = StatusWarn
		result.Score = 70 // Default score when metrics unavailable
		result.Message = fmt.Sprintf("Unable to fetch CPU metrics: %v", err)
		result.Details = append(result.Details, "EC2 CPU metrics unavailable - check EKS node group status")
		return result
	}

	result.Details = append(result.Details, "Using default EC2 metrics (CPU only)")
	result.Details = append(result.Details, "Memory metrics require Container Insights setup")

	// Calculate average and peak CPU utilization
	cpuMetrics := make([]float64, 0, len(cpuByInstance))
	for _, v := range cpuByInstance {
		cpuMetrics = append(cpuMetrics, v)
	}
	avgCPU := calculateAverage(cpuMetrics)
	maxCPU := maxFloat(cpuMetrics)

	result.Details = append(result.Details, fmt.Sprintf("Average CPU utilization: %.1f%% (peak node %.1f%%)", avgCPU, maxCPU))
	result.Details = append(result.Details, "Memory utilization: Not available (requires Container Insights)")

	// Mean-based verdict from cluster-wide CPU headroom.
	headroom := 100 - avgCPU
	switch {
	case headroom >= minSafeCPUHeadroomPercent:
		result.Status = StatusPass
		result.Score = 100
		result.Message = fmt.Sprintf("Sufficient CPU capacity (avg %.1f%%, peak %.1f%%)", avgCPU, maxCPU)
	case headroom >= minWarnCPUHeadroomPercent:
		result.Status = StatusWarn
		result.Score = 70
		result.Message = fmt.Sprintf("Limited CPU capacity (avg %.1f%%, peak %.1f%%)", avgCPU, maxCPU)
		result.Details = append(result.Details, "Consider scaling up nodes before update")
	default:
		result.Status = StatusFail
		result.Score = 30
		result.Message = fmt.Sprintf("Insufficient CPU capacity (avg %.1f%%, peak %.1f%%)", avgCPU, maxCPU)
		result.Details = append(result.Details, "Insufficient CPU headroom for safe rolling update")
	}

	// Peak-node guard: a single near-saturated node is a real bottleneck for a
	// rolling update even when the cluster mean looks healthy. Only ever
	// degrade the mean-based verdict, never improve it.
	switch {
	case maxCPU >= peakFailCPUPercent && result.Status != StatusFail:
		result.Status = StatusFail
		result.Score = 30
		result.Message = fmt.Sprintf("A node is near CPU saturation (peak %.1f%%, avg %.1f%%)", maxCPU, avgCPU)
		result.Details = append(result.Details, fmt.Sprintf("At least one node at/above %.0f%% CPU — rolling it could overload its peers", peakFailCPUPercent))
	case maxCPU >= peakWarnCPUPercent && result.Status == StatusPass:
		result.Status = StatusWarn
		result.Score = 70
		result.Message = fmt.Sprintf("Uneven CPU capacity (peak %.1f%%, avg %.1f%%)", maxCPU, avgCPU)
		result.Details = append(result.Details, fmt.Sprintf("At least one node at/above %.0f%% CPU despite a low cluster average", peakWarnCPUPercent))
	}

	return result
}

// cpuSnapshot lazily computes per-instance average CPU once, so the capacity
// and balance checks running in the same RunAllChecks pass share a single
// nodegroup discovery + CloudWatch fetch instead of issuing one
// GetMetricStatistics call per instance each.
type cpuSnapshot struct {
	hc          *HealthChecker
	clusterName string
	once        sync.Once
	byInstance  map[string]float64
	err         error
}

func (hc *HealthChecker) newCPUSnapshot(clusterName string) *cpuSnapshot {
	return &cpuSnapshot{hc: hc, clusterName: clusterName}
}

func (s *cpuSnapshot) get(ctx context.Context) (map[string]float64, error) {
	s.once.Do(func() {
		s.byInstance, s.err = s.hc.clusterCPUByInstance(ctx, s.clusterName)
	})
	return s.byInstance, s.err
}

// maxMetricDataQueries is the GetMetricData per-request limit.
const maxMetricDataQueries = 500

// clusterCPUByInstance returns the average CPUUtilization over the last 10
// minutes for every instance backing the cluster's nodegroups, fetched in
// batched GetMetricData calls (500 queries per request) instead of one
// GetMetricStatistics call per instance.
func (hc *HealthChecker) clusterCPUByInstance(ctx context.Context, clusterName string) (map[string]float64, error) {
	if hc.cwClient == nil {
		return nil, fmt.Errorf("CloudWatch client not available")
	}

	instanceIDs, err := hc.getClusterInstanceIDs(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster instances: %w", err)
	}
	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("no instances found in cluster")
	}

	endTime := time.Now()
	startTime := endTime.Add(-10 * time.Minute)

	sums := make(map[string]float64, len(instanceIDs))
	counts := make(map[string]int, len(instanceIDs))

	for offset := 0; offset < len(instanceIDs); offset += maxMetricDataQueries {
		chunk := instanceIDs[offset:min(offset+maxMetricDataQueries, len(instanceIDs))]
		queries := make([]types.MetricDataQuery, 0, len(chunk))
		for i, id := range chunk {
			queries = append(queries, types.MetricDataQuery{
				Id: aws.String(fmt.Sprintf("cpu%d", i)),
				MetricStat: &types.MetricStat{
					Metric: &types.Metric{
						Namespace:  aws.String("AWS/EC2"),
						MetricName: aws.String("CPUUtilization"),
						Dimensions: []types.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(id)}},
					},
					Period: aws.Int32(300), // 5 minute periods
					Stat:   aws.String("Average"),
				},
			})
		}

		var nextToken *string
		for {
			out, err := hc.cwClient.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
				StartTime:         aws.Time(startTime),
				EndTime:           aws.Time(endTime),
				MetricDataQueries: queries,
				NextToken:         nextToken,
			})
			if err != nil {
				return nil, fmt.Errorf("fetching CPU metrics: %w", err)
			}
			for _, r := range out.MetricDataResults {
				var idx int
				if r.Id == nil {
					continue
				}
				if _, err := fmt.Sscanf(*r.Id, "cpu%d", &idx); err != nil || idx < 0 || idx >= len(chunk) {
					continue
				}
				id := chunk[idx]
				for _, v := range r.Values {
					sums[id] += v
					counts[id]++
				}
			}
			if out.NextToken == nil {
				break
			}
			nextToken = out.NextToken
		}
	}

	byInstance := make(map[string]float64, len(counts))
	for id, c := range counts {
		if c > 0 {
			byInstance[id] = sums[id] / float64(c)
		}
	}
	if len(byInstance) == 0 {
		return nil, fmt.Errorf("no CPU metrics available from EC2 instances")
	}
	return byInstance, nil
}

// calculateAverage calculates the average of a slice of float64 values
func calculateAverage(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

// maxFloat returns the largest value in the slice, or 0 for an empty slice.
func maxFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// getClusterInstanceIDs retrieves all EC2 instance IDs for the cluster
func (hc *HealthChecker) getClusterInstanceIDs(ctx context.Context, clusterName string) ([]string, error) {
	// List all nodegroups in the cluster (with pagination)
	var nodegroupNames []string
	var nextToken *string
	for {
		listInput := &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName), NextToken: nextToken}
		ngOutput, err := hc.eksClient.ListNodegroups(ctx, listInput)
		if err != nil {
			return nil, fmt.Errorf("failed to list nodegroups: %w", err)
		}
		nodegroupNames = append(nodegroupNames, ngOutput.Nodegroups...)
		if ngOutput.NextToken == nil {
			break
		}
		nextToken = ngOutput.NextToken
	}

	// For each nodegroup (concurrently), get the associated Auto Scaling
	// Groups and their instances.
	perNodegroup := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, ngName string) []string {
			descOutput, err := hc.eksClient.DescribeNodegroup(fctx, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(ngName),
			})
			if err != nil || descOutput.Nodegroup == nil || descOutput.Nodegroup.Resources == nil {
				return nil // Skip failed nodegroups
			}
			var ids []string
			for _, asg := range descOutput.Nodegroup.Resources.AutoScalingGroups {
				if asg.Name != nil {
					asgInstanceIDs, err := hc.getASGInstanceIDs(fctx, *asg.Name)
					if err != nil {
						continue // Skip failed ASGs
					}
					ids = append(ids, asgInstanceIDs...)
				}
			}
			return ids
		})

	var instanceIDs []string
	for _, ids := range perNodegroup {
		instanceIDs = append(instanceIDs, ids...)
	}

	return instanceIDs, nil
}

// getASGInstanceIDs retrieves instance IDs from an Auto Scaling Group
func (hc *HealthChecker) getASGInstanceIDs(ctx context.Context, asgName string) ([]string, error) {
	if hc.asgClient == nil {
		return nil, fmt.Errorf("auto Scaling client not available")
	}

	input := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{asgName},
	}

	output, err := hc.asgClient.DescribeAutoScalingGroups(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe ASG %s: %w", asgName, err)
	}

	if len(output.AutoScalingGroups) == 0 {
		return nil, fmt.Errorf("ASG %s not found", asgName)
	}

	var instanceIDs []string
	for _, instance := range output.AutoScalingGroups[0].Instances {
		if instance.InstanceId != nil {
			instanceIDs = append(instanceIDs, *instance.InstanceId)
		}
	}

	return instanceIDs, nil
}
