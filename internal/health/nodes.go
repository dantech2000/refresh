package health

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// CheckNodeHealth validates that all nodes in the cluster are ready
func (hc *HealthChecker) CheckNodeHealth(ctx context.Context, clusterName string) HealthResult {
	result := HealthResult{
		Name:       "Node Health",
		IsBlocking: true, // Node health is blocking
		Details:    []string{},
	}

	// Get all nodegroups in the cluster (with pagination)
	var nodegroupNames []string
	var nextToken *string
	for {
		ngOut, err := hc.eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{
			ClusterName: aws.String(clusterName),
			NextToken:   nextToken,
		})
		if err != nil {
			result.Status = StatusFail
			result.Score = 0
			result.Message = fmt.Sprintf("Failed to list nodegroups: %v", err)
			return result
		}
		nodegroupNames = append(nodegroupNames, ngOut.Nodegroups...)
		if ngOut.NextToken == nil {
			break
		}
		nextToken = ngOut.NextToken
	}

	totalNodes := 0
	readyNodes := 0
	var problemNodes []string

	// Check each nodegroup
	for _, ngName := range nodegroupNames {
		ngDesc, err := hc.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ngName),
		})
		if err != nil {
			result.Details = append(result.Details, fmt.Sprintf("Failed to describe nodegroup %s: %v", ngName, err))
			continue
		}

		nodegroup := ngDesc.Nodegroup

		// Count total desired nodes
		if nodegroup.ScalingConfig != nil && nodegroup.ScalingConfig.DesiredSize != nil {
			totalNodes += int(*nodegroup.ScalingConfig.DesiredSize)
		}

		// Check nodegroup status
		switch nodegroup.Status {
		case types.NodegroupStatusActive:
			if nodegroup.ScalingConfig != nil && nodegroup.ScalingConfig.DesiredSize != nil {
				readyNodes += int(*nodegroup.ScalingConfig.DesiredSize)
			}
		case types.NodegroupStatusDegraded:
			problemNodes = append(problemNodes, fmt.Sprintf("%s (DEGRADED)", ngName))
		case types.NodegroupStatusCreating, types.NodegroupStatusUpdating:
			problemNodes = append(problemNodes, fmt.Sprintf("%s (%s)", ngName, string(nodegroup.Status)))
		default:
			problemNodes = append(problemNodes, fmt.Sprintf("%s (%s)", ngName, string(nodegroup.Status)))
		}

		result.Details = append(result.Details, fmt.Sprintf("Nodegroup %s: %s", ngName, string(nodegroup.Status)))
	}

	// Calculate score and status
	if totalNodes == 0 {
		result.Status = StatusFail
		result.Score = 0
		result.Message = "No nodes found in cluster"
		return result
	}

	scorePercentage := (readyNodes * 100) / totalNodes
	result.Score = scorePercentage

	if len(problemNodes) == 0 {
		result.Status = StatusPass
		result.Message = fmt.Sprintf("%d/%d nodes ready", readyNodes, totalNodes)
	} else if readyNodes > 0 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("%d/%d nodes ready, issues: %v", readyNodes, totalNodes, problemNodes)
	} else {
		result.Status = StatusFail
		result.Message = fmt.Sprintf("No ready nodes, issues: %v", problemNodes)
	}

	return result
}
