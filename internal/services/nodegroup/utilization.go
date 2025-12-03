package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
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
	data := UtilizationData{Average: sum / float64(count), Peak: peak, Current: last, Trend: "stable"}
	return data, true
}
