package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/dantech2000/refresh/internal/services/common"
)

type UtilizationCollector struct {
	cw       *cloudwatch.Client
	logger   *slog.Logger
	cache    *Cache
	cacheTTL time.Duration
}

func NewUtilizationCollector(cw *cloudwatch.Client, logger *slog.Logger, cache *Cache) *UtilizationCollector {
	return &UtilizationCollector{cw: cw, logger: logger, cache: cache, cacheTTL: 2 * time.Minute}
}

// CollectBasicCPU returns a coarse CPU utilization over timeframe for a nodegroup (cluster-level namespace metric)
// This is a placeholder using a generic metric and will be refined later.
func (u *UtilizationCollector) CollectBasicCPU(ctx context.Context, clusterName, nodegroupName, timeframe string) (UtilizationData, bool) {
	// Cache key
	key := "cpu:" + clusterName + ":" + nodegroupName + ":" + timeframe
	if v, ok := u.cache.Get(key); ok {
		if data, ok2 := v.(UtilizationData); ok2 {
			return data, true
		}
	}

	// Map timeframe
	end := time.Now()
	start := end.Add(-7 * 24 * time.Hour)
	switch timeframe {
	case "30d":
		start = end.Add(-30 * 24 * time.Hour)
	case "90d":
		start = end.Add(-90 * 24 * time.Hour)
	}

	// Placeholder: fetch any metric to avoid AccessDenied on accounts without Container Insights
	// If AccessDenied or metric missing, return zeroed data.
	stat := cwtypes.StatisticAverage
	period := int32(300)
	input := &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/EKS"),
		MetricName: aws.String("ClusterSuccessfulProvisionedNodeCount"), // benign metric
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(end),
		Period:     aws.Int32(period),
		Statistics: []cwtypes.Statistic{stat},
		Dimensions: []cwtypes.Dimension{
			{Name: aws.String("ClusterName"), Value: aws.String(clusterName)},
		},
	}
	_, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricStatisticsOutput, error) {
		return u.cw.GetMetricStatistics(rc, input)
	})
	if err != nil {
		u.logger.Debug("utilization metrics unavailable", "error", err)
		return UtilizationData{}, false
	}

	// Placeholder values
	data := UtilizationData{Current: 0, Average: 0, Peak: 0, Trend: "stable"}
	u.cache.Set(key, data, u.cacheTTL)
	return data, true
}

// CollectEC2CPUForInstances aggregates EC2 CPUUtilization for instance IDs over a short window
func (u *UtilizationCollector) CollectEC2CPUForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	if len(instanceIDs) == 0 {
		return UtilizationData{}, false
	}
	// Default to 1 hour, support 3h and 24h windows
	end := time.Now()
	start := end.Add(-1 * time.Hour)
	switch window {
	case "3h":
		start = end.Add(-3 * time.Hour)
	case "24h":
		start = end.Add(-24 * time.Hour)
	}

	// Build a GetMetricData request with one query per instance (batching efficiently)
	queries := make([]cwtypes.MetricDataQuery, 0, len(instanceIDs))
	for i, id := range instanceIDs {
		idstr := aws.String(id)
		q := cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("m%d", i)),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EC2"),
					MetricName: aws.String("CPUUtilization"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: idstr}},
				},
				Period: aws.Int32(300),
				Stat:   aws.String("Average"),
				Unit:   cwtypes.StandardUnitPercent,
			},
		}
		queries = append(queries, q)
	}

	input := &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
		ScanBy:            cwtypes.ScanByTimestampAscending,
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricDataOutput, error) {
		return u.cw.GetMetricData(rc, input)
	})
	if err != nil || len(out.MetricDataResults) == 0 {
		u.logger.Debug("ec2 cpu metrics unavailable", "error", err)
		return UtilizationData{}, false
	}

	// Average across instances and datapoints
	var sum float64
	var count int
	var peak float64
	var last float64
	var lastTs time.Time
	for _, r := range out.MetricDataResults {
		for _, v := range r.Values {
			sum += v
			count++
			if v > peak {
				peak = v
			}
		}
		// Track latest datapoint per series for current
		for i := range r.Timestamps {
			ts := r.Timestamps[i]
			val := r.Values[i]
			if ts.After(lastTs) {
				lastTs = ts
				last = val
			}
		}
	}
	if count == 0 {
		return UtilizationData{}, false
	}

	// Calculate trend based on time series data
	trend := u.calculateTrend(out.MetricDataResults)

	data := UtilizationData{Average: sum / float64(count), Peak: peak, Current: last, Trend: trend}
	return data, true
}

// CollectMemoryForInstances collects memory utilization using CloudWatch agent metrics
// Note: This requires CloudWatch agent to be installed and configured on nodes
func (u *UtilizationCollector) CollectMemoryForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	if len(instanceIDs) == 0 {
		return UtilizationData{}, false
	}

	// Calculate time range
	end := time.Now()
	start := end.Add(-1 * time.Hour)
	switch window {
	case "3h":
		start = end.Add(-3 * time.Hour)
	case "24h":
		start = end.Add(-24 * time.Hour)
	}

	// Build queries for memory metrics (CloudWatch agent namespace)
	queries := make([]cwtypes.MetricDataQuery, 0, len(instanceIDs))
	for i, id := range instanceIDs {
		idstr := aws.String(id)
		q := cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("mem%d", i)),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("CWAgent"),
					MetricName: aws.String("mem_used_percent"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: idstr}},
				},
				Period: aws.Int32(300),
				Stat:   aws.String("Average"),
				Unit:   cwtypes.StandardUnitPercent,
			},
		}
		queries = append(queries, q)
	}

	input := &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
		ScanBy:            cwtypes.ScanByTimestampAscending,
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricDataOutput, error) {
		return u.cw.GetMetricData(rc, input)
	})
	if err != nil || len(out.MetricDataResults) == 0 {
		u.logger.Debug("memory metrics unavailable (CloudWatch agent may not be installed)", "error", err)
		return UtilizationData{}, false
	}

	// Aggregate memory metrics
	var sum float64
	var count int
	var peak float64
	var last float64
	var lastTs time.Time

	for _, r := range out.MetricDataResults {
		for _, v := range r.Values {
			sum += v
			count++
			if v > peak {
				peak = v
			}
		}
		for i := range r.Timestamps {
			ts := r.Timestamps[i]
			val := r.Values[i]
			if ts.After(lastTs) {
				lastTs = ts
				last = val
			}
		}
	}

	if count == 0 {
		return UtilizationData{}, false
	}

	trend := u.calculateTrend(out.MetricDataResults)
	data := UtilizationData{Average: sum / float64(count), Peak: peak, Current: last, Trend: trend}
	return data, true
}

// CollectNetworkForInstances collects network utilization metrics
func (u *UtilizationCollector) CollectNetworkForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	if len(instanceIDs) == 0 {
		return UtilizationData{}, false
	}

	end := time.Now()
	start := end.Add(-1 * time.Hour)
	switch window {
	case "3h":
		start = end.Add(-3 * time.Hour)
	case "24h":
		start = end.Add(-24 * time.Hour)
	}

	// Collect NetworkIn and NetworkOut
	queries := make([]cwtypes.MetricDataQuery, 0, len(instanceIDs)*2)
	for i, id := range instanceIDs {
		idstr := aws.String(id)
		// Network In
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("netin%d", i)),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EC2"),
					MetricName: aws.String("NetworkIn"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: idstr}},
				},
				Period: aws.Int32(300),
				Stat:   aws.String("Average"),
				Unit:   cwtypes.StandardUnitBytes,
			},
		})
		// Network Out
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("netout%d", i)),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EC2"),
					MetricName: aws.String("NetworkOut"),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: idstr}},
				},
				Period: aws.Int32(300),
				Stat:   aws.String("Average"),
				Unit:   cwtypes.StandardUnitBytes,
			},
		})
	}

	input := &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
		ScanBy:            cwtypes.ScanByTimestampAscending,
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricDataOutput, error) {
		return u.cw.GetMetricData(rc, input)
	})
	if err != nil || len(out.MetricDataResults) == 0 {
		u.logger.Debug("network metrics unavailable", "error", err)
		return UtilizationData{}, false
	}

	// Aggregate network metrics (combine in+out in MB/s)
	var sum float64
	var count int
	var peak float64
	var last float64
	var lastTs time.Time

	for _, r := range out.MetricDataResults {
		for _, v := range r.Values {
			// Convert bytes to MB (divide by 1024*1024)
			vMB := v / (1024 * 1024)
			sum += vMB
			count++
			if vMB > peak {
				peak = vMB
			}
		}
		for i := range r.Timestamps {
			ts := r.Timestamps[i]
			val := r.Values[i] / (1024 * 1024)
			if ts.After(lastTs) {
				lastTs = ts
				last = val
			}
		}
	}

	if count == 0 {
		return UtilizationData{}, false
	}

	trend := u.calculateTrend(out.MetricDataResults)
	data := UtilizationData{Average: sum / float64(count), Peak: peak, Current: last, Trend: trend}
	return data, true
}

// CollectDiskForInstances collects disk utilization metrics (EBS)
func (u *UtilizationCollector) CollectDiskForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	if len(instanceIDs) == 0 {
		return UtilizationData{}, false
	}

	end := time.Now()
	start := end.Add(-1 * time.Hour)
	switch window {
	case "3h":
		start = end.Add(-3 * time.Hour)
	case "24h":
		start = end.Add(-24 * time.Hour)
	}

	// EBS metrics use volume IDs, not instance IDs
	// We'll use the CloudWatch agent metric for disk used percent if available
	queries := make([]cwtypes.MetricDataQuery, 0, len(instanceIDs))
	for i, id := range instanceIDs {
		idstr := aws.String(id)
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("disk%d", i)),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("CWAgent"),
					MetricName: aws.String("disk_used_percent"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("InstanceId"), Value: idstr},
						{Name: aws.String("path"), Value: aws.String("/")},
						{Name: aws.String("fstype"), Value: aws.String("ext4")},
					},
				},
				Period: aws.Int32(300),
				Stat:   aws.String("Average"),
				Unit:   cwtypes.StandardUnitPercent,
			},
		})
	}

	input := &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
		ScanBy:            cwtypes.ScanByTimestampAscending,
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricDataOutput, error) {
		return u.cw.GetMetricData(rc, input)
	})
	if err != nil || len(out.MetricDataResults) == 0 {
		u.logger.Debug("disk metrics unavailable (CloudWatch agent may not be installed)", "error", err)
		return UtilizationData{}, false
	}

	var sum float64
	var count int
	var peak float64
	var last float64
	var lastTs time.Time

	for _, r := range out.MetricDataResults {
		for _, v := range r.Values {
			sum += v
			count++
			if v > peak {
				peak = v
			}
		}
		for i := range r.Timestamps {
			ts := r.Timestamps[i]
			val := r.Values[i]
			if ts.After(lastTs) {
				lastTs = ts
				last = val
			}
		}
	}

	if count == 0 {
		return UtilizationData{}, false
	}

	trend := u.calculateTrend(out.MetricDataResults)
	data := UtilizationData{Average: sum / float64(count), Peak: peak, Current: last, Trend: trend}
	return data, true
}

// calculateTrend analyzes time series data to determine trend direction
func (u *UtilizationCollector) calculateTrend(results []cwtypes.MetricDataResult) string {
	if len(results) == 0 {
		return "stable"
	}

	// Collect all datapoints with timestamps
	type datapoint struct {
		timestamp time.Time
		value     float64
	}
	var points []datapoint

	for _, r := range results {
		for i := range r.Timestamps {
			points = append(points, datapoint{
				timestamp: r.Timestamps[i],
				value:     r.Values[i],
			})
		}
	}

	if len(points) < 4 {
		return "stable"
	}

	// Sort by timestamp
	sort.Slice(points, func(i, j int) bool {
		return points[i].timestamp.Before(points[j].timestamp)
	})

	// Simple linear regression for trend detection
	// Using least squares method
	n := float64(len(points))
	var sumX, sumY, sumXY, sumX2 float64

	for i, p := range points {
		x := float64(i)
		y := p.value
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	// Calculate slope
	denominator := n*sumX2 - sumX*sumX
	if denominator == 0 {
		return "stable"
	}
	slope := (n*sumXY - sumX*sumY) / denominator

	// Calculate average for context
	avgY := sumY / n

	// Determine trend based on slope relative to average
	// If slope is more than 2% of average per datapoint, consider it a trend
	threshold := avgY * 0.02
	if math.Abs(threshold) < 0.5 {
		threshold = 0.5
	}

	if slope > threshold {
		return "increasing"
	} else if slope < -threshold {
		return "decreasing"
	}
	return "stable"
}

// CollectAllMetrics collects CPU, memory, network, and disk metrics for instances
func (u *UtilizationCollector) CollectAllMetrics(ctx context.Context, instanceIDs []string, window string) UtilizationMetrics {
	var metrics UtilizationMetrics
	metrics.TimeRange = window

	// Collect CPU (always available)
	if cpu, ok := u.CollectEC2CPUForInstances(ctx, instanceIDs, window); ok {
		metrics.CPU = cpu
	}

	// Collect memory (requires CloudWatch agent)
	if mem, ok := u.CollectMemoryForInstances(ctx, instanceIDs, window); ok {
		metrics.Memory = mem
	}

	// Collect network
	if net, ok := u.CollectNetworkForInstances(ctx, instanceIDs, window); ok {
		metrics.Network = net
	}

	// Collect disk (requires CloudWatch agent)
	if disk, ok := u.CollectDiskForInstances(ctx, instanceIDs, window); ok {
		metrics.Storage = disk
	}

	return metrics
}
