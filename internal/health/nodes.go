package health

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	var inProgress []string

	// Prefer real node readiness from the Kubernetes API when available;
	// DesiredSize is only a proxy (an ACTIVE nodegroup can still have
	// NotReady/cordoned nodes, and an UPDATING one can be fully serving).
	realTotal, realReady, notReadyNodes, haveRealCounts := hc.kubernetesNodeCounts(ctx)

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

		// Classify by nodegroup status. CREATING/UPDATING are benign,
		// in-progress states (scaling, rolling) — they are tracked separately
		// and must not be reported as readiness failures.
		switch nodegroup.Status {
		case types.NodegroupStatusActive:
			if nodegroup.ScalingConfig != nil && nodegroup.ScalingConfig.DesiredSize != nil {
				readyNodes += int(*nodegroup.ScalingConfig.DesiredSize)
			}
		case types.NodegroupStatusCreating, types.NodegroupStatusUpdating:
			inProgress = append(inProgress, fmt.Sprintf("%s (%s)", ngName, string(nodegroup.Status)))
		case types.NodegroupStatusDegraded:
			problemNodes = append(problemNodes, fmt.Sprintf("%s (DEGRADED)", ngName))
		default:
			problemNodes = append(problemNodes, fmt.Sprintf("%s (%s)", ngName, string(nodegroup.Status)))
		}

		result.Details = append(result.Details, fmt.Sprintf("Nodegroup %s: %s", ngName, string(nodegroup.Status)))
	}

	// Real node counts supersede the DesiredSize proxy.
	if haveRealCounts {
		totalNodes = realTotal
		readyNodes = realReady
		for _, name := range notReadyNodes {
			problemNodes = append(problemNodes, fmt.Sprintf("%s (NotReady)", name))
		}
	}

	if len(inProgress) > 0 {
		result.Details = append(result.Details, fmt.Sprintf("Nodegroups scaling/updating (not a failure): %v", inProgress))
	}

	// Calculate score and status
	if totalNodes == 0 {
		result.Status = StatusFail
		result.Score = 0
		result.Message = "No nodes found in cluster"
		return result
	}

	scorePercentage := (readyNodes * 100) / totalNodes

	// Without real Kubernetes counts the score is an estimate derived from
	// nodegroup desired capacity: an ACTIVE nodegroup can still hold
	// NotReady/cordoned nodes, so a "100%" here is not a confident measurement.
	// Cap it below 100 and label it estimated.
	estimated := !haveRealCounts
	if estimated {
		result.Details = append(result.Details, "Node readiness estimated from nodegroup desired capacity (no Kubernetes API access)")
		if scorePercentage > maxEstimatedNodeHealthScore {
			scorePercentage = maxEstimatedNodeHealthScore
		}
	}
	result.Score = scorePercentage

	estimatedSuffix := ""
	if estimated {
		estimatedSuffix = " (estimated)"
	}

	switch {
	case len(problemNodes) == 0 && readyNodes == 0 && len(inProgress) > 0:
		// Everything is mid-scale and nothing is wrong — warn, don't fail.
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("Nodegroups still scaling: %v", inProgress)
	case len(problemNodes) == 0:
		result.Status = StatusPass
		result.Message = fmt.Sprintf("%d/%d nodes ready%s", readyNodes, totalNodes, estimatedSuffix)
	case readyNodes > 0:
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("%d/%d nodes ready%s, issues: %v", readyNodes, totalNodes, estimatedSuffix, problemNodes)
	default:
		result.Status = StatusFail
		result.Message = fmt.Sprintf("No ready nodes, issues: %v", problemNodes)
	}

	return result
}

// maxEstimatedNodeHealthScore caps the Node Health score when it is derived
// from the nodegroup DesiredSize proxy rather than real Kubernetes node counts,
// so an estimate never reads as a confident perfect 100.
const maxEstimatedNodeHealthScore = 90

// kubernetesNodeCounts returns real node readiness from the Kubernetes API:
// total node count, ready count, and names of NotReady nodes. ok is false when
// no Kubernetes client is available or the list fails (callers fall back to
// the nodegroup DesiredSize proxy).
func (hc *HealthChecker) kubernetesNodeCounts(ctx context.Context) (total, ready int, notReady []string, ok bool) {
	if hc.k8sClient == nil {
		return 0, 0, nil, false
	}
	nodes, err := hc.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, nil, false
	}
	for _, node := range nodes.Items {
		isReady := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				isReady = true
				break
			}
		}
		if isReady {
			ready++
		} else {
			notReady = append(notReady, node.Name)
		}
	}
	return len(nodes.Items), ready, notReady, true
}
