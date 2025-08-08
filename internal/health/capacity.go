package health

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// CheckClusterCapacity validates that the cluster has sufficient capacity for rolling updates
func (hc *HealthChecker) CheckClusterCapacity(ctx context.Context, clusterName string) HealthResult {
	result := HealthResult{
		Name:       "Cluster Capacity",
		IsBlocking: true, // Capacity is blocking
		Details:    []string{},
	}

	// Use default EC2 metrics (no prerequisites required)
	cpuMetrics, err := hc.getEC2ClusterCPUMetrics(ctx, clusterName)
	if err != nil {
		result.Status = StatusWarn
		result.Score = 70 // Default score when metrics unavailable
		result.Message = fmt.Sprintf("Unable to fetch CPU metrics: %v", err)
		result.Details = append(result.Details, "EC2 CPU metrics unavailable - check EKS node group status")
		return result
	}

	result.Details = append(result.Details, "Using default EC2 metrics (CPU only)")
	result.Details = append(result.Details, "Memory metrics require Container Insights setup")

	// Calculate average CPU utilization
	avgCPU := calculateAverage(cpuMetrics)

	result.Details = append(result.Details, fmt.Sprintf("Average CPU utilization: %.1f%%", avgCPU))
	result.Details = append(result.Details, "Memory utilization: Not available (requires Container Insights)")

	// Calculate score based on CPU headroom only
	// We want at least 30% headroom for safe rolling updates
	headroom := 100 - avgCPU
	if headroom >= 30 {
		result.Status = StatusPass
		result.Score = 100
		result.Message = fmt.Sprintf("Sufficient CPU capacity (%.1f%% utilization)", avgCPU)
	} else if headroom >= 15 {
		result.Status = StatusWarn
		result.Score = 70
		result.Message = fmt.Sprintf("Limited CPU capacity (%.1f%% utilization)", avgCPU)
		result.Details = append(result.Details, "Consider scaling up nodes before update")
	} else {
		result.Status = StatusFail
		result.Score = 30
		result.Message = fmt.Sprintf("Insufficient CPU capacity (%.1f%% utilization)", avgCPU)
		result.Details = append(result.Details, "Insufficient CPU headroom for safe rolling update")
	}

	return result
}

// getEC2ClusterCPUMetrics gets CPU utilization from all EKS worker nodes using default EC2 metrics
func (hc *HealthChecker) getEC2ClusterCPUMetrics(ctx context.Context, clusterName string) ([]float64, error) {
	if hc.cwClient == nil {
		return nil, fmt.Errorf("CloudWatch client not available")
	}

	// Get instance IDs from nodegroups
	instanceIDs, err := hc.getClusterInstanceIDs(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster instances: %v", err)
	}

	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("no instances found in cluster")
	}

	// Get CPU metrics from each instance using default AWS/EC2 namespace
	var allValues []float64
	endTime := time.Now()
	startTime := endTime.Add(-10 * time.Minute)

	for _, instanceID := range instanceIDs {
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
			continue // Skip failed instances but log the issue
		}

		for _, datapoint := range output.Datapoints {
			if datapoint.Average != nil {
				allValues = append(allValues, *datapoint.Average)
			}
		}
	}

	if len(allValues) == 0 {
		return nil, fmt.Errorf("no CPU metrics available from EC2 instances")
	}

	return allValues, nil
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

// getClusterInstanceIDs retrieves all EC2 instance IDs for the cluster
func (hc *HealthChecker) getClusterInstanceIDs(ctx context.Context, clusterName string) ([]string, error) {
	// List all nodegroups in the cluster (with pagination)
	var nodegroupNames []string
	var nextToken *string
	for {
		listInput := &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName), NextToken: nextToken}
		ngOutput, err := hc.eksClient.ListNodegroups(ctx, listInput)
		if err != nil {
			return nil, fmt.Errorf("failed to list nodegroups: %v", err)
		}
		nodegroupNames = append(nodegroupNames, ngOutput.Nodegroups...)
		if ngOutput.NextToken == nil {
			break
		}
		nextToken = ngOutput.NextToken
	}

	var instanceIDs []string

	// For each nodegroup, get the associated Auto Scaling Group and its instances
	for _, ngName := range nodegroupNames {
		descInput := &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ngName),
		}

		descOutput, err := hc.eksClient.DescribeNodegroup(ctx, descInput)
		if err != nil {
			continue // Skip failed nodegroups
		}

		// Get Auto Scaling Group name from nodegroup resources
		if descOutput.Nodegroup.Resources != nil && descOutput.Nodegroup.Resources.AutoScalingGroups != nil {
			for _, asg := range descOutput.Nodegroup.Resources.AutoScalingGroups {
				if asg.Name != nil {
					asgInstanceIDs, err := hc.getASGInstanceIDs(ctx, *asg.Name)
					if err != nil {
						continue // Skip failed ASGs
					}
					instanceIDs = append(instanceIDs, asgInstanceIDs...)
				}
			}
		}
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
		return nil, fmt.Errorf("failed to describe ASG %s: %v", asgName, err)
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
