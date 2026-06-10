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

// defaultUtilizationCacheTTL is how long CloudWatch utilization metrics are
// cached; kept short so repeated commands still reflect recent load.
const defaultUtilizationCacheTTL = 2 * time.Minute

type UtilizationCollector struct {
	cw       *cloudwatch.Client
	logger   *slog.Logger
	cache    *Cache
	cacheTTL time.Duration
}

func NewUtilizationCollector(cw *cloudwatch.Client, logger *slog.Logger, cache *Cache) *UtilizationCollector {
	return &UtilizationCollector{cw: cw, logger: logger, cache: cache, cacheTTL: defaultUtilizationCacheTTL}
}

// metricSpec describes one CloudWatch metric to fetch per instance.
type metricSpec struct {
	namespace string
	metric    string
	idPrefix  string
	extraDims []cwtypes.Dimension
	unit      cwtypes.StandardUnit
}

var (
	cpuSpec     = []metricSpec{{namespace: "AWS/EC2", metric: "CPUUtilization", idPrefix: "m", unit: cwtypes.StandardUnitPercent}}
	memorySpec  = []metricSpec{{namespace: "CWAgent", metric: "mem_used_percent", idPrefix: "mem", unit: cwtypes.StandardUnitPercent}}
	networkSpec = []metricSpec{
		{namespace: "AWS/EC2", metric: "NetworkIn", idPrefix: "netin", unit: cwtypes.StandardUnitBytes},
		{namespace: "AWS/EC2", metric: "NetworkOut", idPrefix: "netout", unit: cwtypes.StandardUnitBytes},
	}
	// Root-volume disk usage. EKS-optimized AMIs use xfs (AL2/AL2023); older
	// or custom nodes may use ext4 — query both rather than pinning one
	// fstype and silently matching nothing.
	diskSpec = []metricSpec{
		{
			namespace: "CWAgent", metric: "disk_used_percent", idPrefix: "diskx", unit: cwtypes.StandardUnitPercent,
			extraDims: []cwtypes.Dimension{
				{Name: aws.String("path"), Value: aws.String("/")},
				{Name: aws.String("fstype"), Value: aws.String("xfs")},
			},
		},
		{
			namespace: "CWAgent", metric: "disk_used_percent", idPrefix: "diske", unit: cwtypes.StandardUnitPercent,
			extraDims: []cwtypes.Dimension{
				{Name: aws.String("path"), Value: aws.String("/")},
				{Name: aws.String("fstype"), Value: aws.String("ext4")},
			},
		},
	}
)

// windowDuration maps a window label to its lookback duration.
func windowDuration(window string) time.Duration {
	switch window {
	case "3h":
		return 3 * time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

// identity is the default no-op value transform.
func identity(v float64) float64 { return v }

// bytesToMB converts CloudWatch byte totals to megabytes.
func bytesToMB(v float64) float64 { return v / (1024 * 1024) }

// collect fetches CloudWatch metrics per spec×instance, aggregates them, and
// returns average/peak/current/trend. transform is applied to each datapoint.
func (u *UtilizationCollector) collect(ctx context.Context, specs []metricSpec, instanceIDs []string, window string, transform func(float64) float64, unavailableMsg string) (UtilizationData, bool) {
	if len(instanceIDs) == 0 {
		return UtilizationData{}, false
	}
	if transform == nil {
		transform = identity
	}

	end := time.Now()
	start := end.Add(-windowDuration(window))

	queries := make([]cwtypes.MetricDataQuery, 0, len(instanceIDs)*len(specs))
	for _, spec := range specs {
		for i, id := range instanceIDs {
			dims := []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(id)}}
			dims = append(dims, spec.extraDims...)
			queries = append(queries, cwtypes.MetricDataQuery{
				Id: aws.String(fmt.Sprintf("%s%d", spec.idPrefix, i)),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{
						Namespace:  aws.String(spec.namespace),
						MetricName: aws.String(spec.metric),
						Dimensions: dims,
					},
					Period: aws.Int32(300),
					Stat:   aws.String("Average"),
					Unit:   spec.unit,
				},
			})
		}
	}

	results, err := u.getMetricData(ctx, queries, start, end)
	if err != nil || len(results) == 0 {
		u.logger.Debug(unavailableMsg, "error", err)
		return UtilizationData{}, false
	}

	var sum, peak, last float64
	var count int
	var lastTs time.Time
	for _, r := range results {
		for i, v := range r.Values {
			tv := transform(v)
			sum += tv
			count++
			if tv > peak {
				peak = tv
			}
			if ts := r.Timestamps[i]; ts.After(lastTs) {
				lastTs = ts
				last = tv
			}
		}
	}
	if count == 0 {
		return UtilizationData{}, false
	}
	return UtilizationData{
		Average: sum / float64(count),
		Peak:    peak,
		Current: last,
		Trend:   u.calculateTrend(results),
	}, true
}

// maxMetricDataQueries is the GetMetricData per-request limit.
const maxMetricDataQueries = 500

// getMetricData fetches all results for the given queries, chunking requests
// at the API's 500-query limit and following pagination. Without chunking, a
// nodegroup with >500 instance×metric combinations would fail the entire
// request.
func (u *UtilizationCollector) getMetricData(ctx context.Context, queries []cwtypes.MetricDataQuery, start, end time.Time) ([]cwtypes.MetricDataResult, error) {
	var results []cwtypes.MetricDataResult
	for offset := 0; offset < len(queries); offset += maxMetricDataQueries {
		chunk := queries[offset:min(offset+maxMetricDataQueries, len(queries))]
		var nextToken *string
		for {
			token := nextToken
			out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*cloudwatch.GetMetricDataOutput, error) {
				return u.cw.GetMetricData(rc, &cloudwatch.GetMetricDataInput{
					StartTime:         aws.Time(start),
					EndTime:           aws.Time(end),
					MetricDataQueries: chunk,
					ScanBy:            cwtypes.ScanByTimestampAscending,
					NextToken:         token,
				})
			})
			if err != nil {
				return nil, err
			}
			results = append(results, out.MetricDataResults...)
			if out.NextToken == nil {
				break
			}
			nextToken = out.NextToken
		}
	}
	return results, nil
}

func (u *UtilizationCollector) CollectEC2CPUForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	return u.collect(ctx, cpuSpec, instanceIDs, window, nil, "ec2 cpu metrics unavailable")
}

func (u *UtilizationCollector) CollectMemoryForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	return u.collect(ctx, memorySpec, instanceIDs, window, nil, "memory metrics unavailable (CloudWatch agent may not be installed)")
}

func (u *UtilizationCollector) CollectNetworkForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	return u.collect(ctx, networkSpec, instanceIDs, window, bytesToMB, "network metrics unavailable")
}

func (u *UtilizationCollector) CollectDiskForInstances(ctx context.Context, instanceIDs []string, window string) (UtilizationData, bool) {
	return u.collect(ctx, diskSpec, instanceIDs, window, nil, "disk metrics unavailable (CloudWatch agent may not be installed)")
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
