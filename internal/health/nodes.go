package health

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
)

// CheckNodeHealth validates that all nodes in the cluster are ready
func (hc *HealthChecker) CheckNodeHealth(ctx context.Context, clusterName string) HealthResult {
	result := HealthResult{
		Name:       "Node Health",
		IsBlocking: true, // Node health is blocking
		Details:    []string{},
	}

	// Get all nodegroups in the cluster (with pagination)
	nodegroupNames, err := hc.listNodegroupNames(ctx, clusterName)
	if err != nil {
		result.Message = fmt.Sprintf("Failed to list nodegroups: %s", awserr.Summary(err))
		result.failures = append(result.failures, clusterFailure(clusterName, diag.OpListNodegroups, err))
		unreadNodeHealth(&result, []error{err})
		return result
	}

	totalNodes := 0
	readyNodes := 0
	var problemNodes []string
	var inProgress []string
	var describeErrs []error

	// Prefer real node readiness from the Kubernetes API when available;
	// DesiredSize is only a proxy (an ACTIVE nodegroup can still have
	// NotReady/cordoned nodes, and an UPDATING one can be fully serving).
	realTotal, realReady, notReadyNodes, joiningNodes, haveRealCounts := hc.kubernetesNodeCounts(ctx)

	// Describe the nodegroups concurrently; results come back in input order.
	type ngDescribe struct {
		ng  *types.Nodegroup
		err error
	}
	described := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, ngName string) ngDescribe {
			ng, err := hc.describeNodegroup(fctx, clusterName, ngName)
			return ngDescribe{ng: ng, err: err}
		})

	// Check each nodegroup
	for i, ngName := range nodegroupNames {
		ngDesc := described[i]
		if ngDesc.err != nil {
			result.Details = append(result.Details, fmt.Sprintf("Failed to describe nodegroup %s: %s", ngName, awserr.Summary(ngDesc.err)))
			f := diag.FromError(diag.KindNodegroup, ngName, diag.OpDescribeNodegroup, ngDesc.err)
			f.Cluster = clusterName
			result.failures = append(result.failures, f)
			describeErrs = append(describeErrs, ngDesc.err)
			continue
		}
		if ctx.Err() != nil && ngDesc.ng == nil {
			// ForEachParallel stopped dispatching; the item never ran.
			result.Details = append(result.Details, fmt.Sprintf("Failed to describe nodegroup %s: %v", ngName, ctx.Err()))
			continue
		}

		nodegroup := ngDesc.ng
		if nodegroup == nil {
			result.Details = append(result.Details, fmt.Sprintf("Empty describe response for nodegroup %s", ngName))
			continue
		}

		desired := 0
		if nodegroup.ScalingConfig != nil && nodegroup.ScalingConfig.DesiredSize != nil {
			desired = int(*nodegroup.ScalingConfig.DesiredSize)
		}

		// Classify by nodegroup status. CREATING/UPDATING are benign,
		// in-progress states (scaling, rolling) — they are tracked separately
		// and must not be reported as readiness failures. Their desired size
		// says nothing about how many of their nodes are Ready, so the
		// estimate leaves them out of both counts: counting them in the total
		// only would score a healthy cluster mid-roll as half ready.
		switch nodegroup.Status {
		case types.NodegroupStatusActive:
			totalNodes += desired
			readyNodes += desired
		case types.NodegroupStatusCreating, types.NodegroupStatusUpdating:
			inProgress = append(inProgress, fmt.Sprintf("%s (%s)", ngName, string(nodegroup.Status)))
		case types.NodegroupStatusDegraded:
			totalNodes += desired
			problemNodes = append(problemNodes, fmt.Sprintf("%s (DEGRADED)", ngName))
		default:
			totalNodes += desired
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
		for _, name := range joiningNodes {
			problemNodes = append(problemNodes, fmt.Sprintf("%s (joining)", name))
		}
	}

	if len(inProgress) > 0 {
		result.Details = append(result.Details, fmt.Sprintf("Nodegroups scaling/updating (not a failure): %v", inProgress))
	}

	// Calculate score and status
	if totalNodes == 0 && len(inProgress) == 0 {
		switch {
		case haveRealCounts:
			result.Status = StatusFail
			result.Score = 0
			result.Message = "No nodes found in cluster"
		case len(describeErrs) > 0:
			result.Message = fmt.Sprintf("Could not describe %d of %d nodegroups", len(describeErrs), len(nodegroupNames))
			unreadNodeHealth(&result, describeErrs)
		case len(problemNodes) == 0:
			// No managed nodegroup capacity to estimate from. The nodes may
			// be Fargate, Karpenter, or self-managed, which only the
			// Kubernetes API can see.
			skipped := skippedResult(result.Name,
				"No managed nodegroup capacity; node readiness needs Kubernetes API access",
				append(result.Details, "Fargate, Karpenter, and self-managed nodes are visible only through the Kubernetes API")...)
			skipped.failures = result.failures
			return skipped
		default:
			result.Status = StatusFail
			result.Score = 0
			result.Message = fmt.Sprintf("No nodes found in cluster, issues: %v", problemNodes)
		}
		return result
	}

	scorePercentage := 0
	if totalNodes > 0 {
		scorePercentage = (readyNodes * 100) / totalNodes
	}

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

	result.Status, result.Message = nodeHealthVerdict(readyNodes, totalNodes, len(joiningNodes), haveRealCounts, problemNodes, inProgress)
	return result
}

// unreadNodeHealth sets result for a Node Health check whose EKS reads failed,
// after the retries, with errs. The message is left to the caller. A read that
// only failed on throttling or another transient fault says nothing about the
// nodes, so it warns and does not block; the failures report the missing data.
// A permanent error (the cluster does not exist, access is denied) still fails
// and blocks.
func unreadNodeHealth(result *HealthResult, errs []error) {
	result.Score = 0
	for _, err := range errs {
		if common.IsPermanentAPIError(err) {
			result.Status = StatusFail
			return
		}
	}
	result.Status = StatusWarn
	result.IsBlocking = false
}

// nodeHealthVerdict returns the Node Health status and message. measured is
// true when the counts come from the Kubernetes API. An estimate from
// nodegroup desired capacity is too coarse for the minReadyNodePercent rule,
// so it only fails when no node is ready. joining nodes (see nodeJoining) are
// left out of the minReadyNodePercent rule: they only warn.
func nodeHealthVerdict(readyNodes, totalNodes, joining int, measured bool, problemNodes, inProgress []string) (HealthStatus, string) {
	estimatedSuffix := ""
	if !measured {
		estimatedSuffix = " (estimated)"
	}
	joiningNote := ""
	if joining > 0 {
		joiningNote = fmt.Sprintf(", %d joining not counted", joining)
	}
	settled := totalNodes - joining

	switch {
	case len(problemNodes) == 0 && readyNodes == 0 && len(inProgress) > 0:
		// Everything is mid-scale and nothing is wrong — warn, don't fail.
		return StatusWarn, fmt.Sprintf("Nodegroups still scaling: %v", inProgress)
	case len(problemNodes) == 0:
		return StatusPass, fmt.Sprintf("%d/%d nodes ready%s", readyNodes, totalNodes, estimatedSuffix)
	case readyNodes == 0 && settled > 0:
		return StatusFail, fmt.Sprintf("No ready nodes, issues: %v", problemNodes)
	case readyNodes == 0:
		return StatusWarn, fmt.Sprintf("No ready nodes yet, %d joining: %v", joining, problemNodes)
	case !measured:
		return StatusWarn, fmt.Sprintf("%d/%d nodes ready%s, issues: %v", readyNodes, totalNodes, estimatedSuffix, problemNodes)
	case readyNodes*100 < settled*minReadyNodePercent:
		return StatusFail, fmt.Sprintf("%d/%d nodes ready, below the %d%% ready minimum%s; issues: %v",
			readyNodes, totalNodes, minReadyNodePercent, joiningNote, problemNodes)
	default:
		return StatusWarn, fmt.Sprintf("%d/%d nodes ready (fails below %d%% ready%s), issues: %v",
			readyNodes, totalNodes, minReadyNodePercent, joiningNote, problemNodes)
	}
}

// listNodegroupNames lists every managed nodegroup in the cluster, with the
// shared retry and error-formatting policy.
func (hc *HealthChecker) listNodegroupNames(ctx context.Context, clusterName string) ([]string, error) {
	return awserr.ListAllPages(ctx, fmt.Sprintf("listing nodegroups for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return hc.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
}

// describeNodegroup describes one nodegroup with retry, formatting a failure.
// A nil nodegroup with a nil error means an empty describe response.
func (hc *HealthChecker) describeNodegroup(ctx context.Context, clusterName, ngName string) (*types.Nodegroup, error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return hc.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ngName),
		})
	})
	if err != nil {
		return nil, awserr.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s", ngName))
	}
	if out == nil {
		return nil, nil
	}
	return out.Nodegroup, nil
}

// minReadyNodePercent is the share of nodes that must be Ready, per the
// Kubernetes API, for Node Health to pass or warn. Below it the check fails
// and blocks: too little capacity is left to take the pods of the nodes a roll
// drains. The estimate from nodegroup desired capacity never applies it.
const minReadyNodePercent = 50

// maxEstimatedNodeHealthScore caps the Node Health score when it is derived
// from the nodegroup DesiredSize proxy rather than real Kubernetes node counts,
// so an estimate never reads as a confident perfect 100.
const maxEstimatedNodeHealthScore = 90

// kubernetesNodeCounts returns real node readiness from the Kubernetes API:
// total node count, ready count, and the names of the nodes that are not
// Ready, split into joining nodes (see nodeJoining) and the rest. ok is false
// when no Kubernetes client is available or the list fails (callers fall back
// to the nodegroup DesiredSize proxy).
func (hc *HealthChecker) kubernetesNodeCounts(ctx context.Context) (total, ready int, notReady, joining []string, ok bool) {
	if hc.k8sClient == nil {
		return 0, 0, nil, nil, false
	}
	nodes, err := hc.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, nil, nil, false
	}
	now := time.Now()
	for _, node := range nodes.Items {
		switch {
		case nodeReady(&node):
			ready++
		case nodeJoining(&node, now):
			joining = append(joining, node.Name)
		default:
			notReady = append(notReady, node.Name)
		}
	}
	return len(nodes.Items), ready, notReady, joining, true
}

// nodeJoinGrace is how long after its creation a NotReady node counts as still
// joining the cluster. A scale-up returns when the nodegroup is ACTIVE, which
// can be before the kubelets of the new nodes report Ready.
const nodeJoinGrace = 10 * time.Minute

// nodeJoining reports whether a NotReady node looks like one that is still
// joining: created less than nodeJoinGrace ago, and its Ready condition is
// missing or set by a kubelet that is still starting (KubeletNotReady) or has
// never posted status (NodeStatusNeverUpdated). A node whose kubelet stopped
// posting (NodeStatusUnknown) is broken, not joining.
func nodeJoining(node *corev1.Node, now time.Time) bool {
	if node.CreationTimestamp.IsZero() || now.Sub(node.CreationTimestamp.Time) > nodeJoinGrace {
		return false
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Reason == "KubeletNotReady" || cond.Reason == "NodeStatusNeverUpdated"
		}
	}
	return true
}

// nodeLabelNodegroup is the EKS-managed label that scopes a node to its managed
// nodegroup. (Kept local to avoid coupling health to the noderoll package, which
// owns the same constant for the live-roll observer.)
const nodeLabelNodegroup = "eks.amazonaws.com/nodegroup"

// nodeReady reports whether a node's Kubernetes Ready condition is True. A
// Ready condition of False or Unknown (kubelet not reporting) both count as not
// ready — the research-backed honest reading: a running EC2 instance is not
// necessarily a Ready node.
func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// NodegroupReadyCounts lists the cluster's nodes once and returns the number of
// Ready=True nodes per managed nodegroup, keyed by nodegroup name (the
// eks.amazonaws.com/nodegroup label). ok is false when no Kubernetes client is
// wired or the list fails, so callers fall back to an honest "unknown" rather
// than the DesiredSize proxy. A nodegroup with nodes present but none Ready
// appears with a count of 0; a nodegroup absent from the map (no nodes observed)
// is treated by callers as 0 ready.
func (hc *HealthChecker) NodegroupReadyCounts(ctx context.Context) (map[string]int32, bool) {
	if hc.k8sClient == nil {
		return nil, false
	}
	nodes, err := hc.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false
	}
	counts := make(map[string]int32)
	for _, node := range nodes.Items {
		ng := node.Labels[nodeLabelNodegroup]
		if ng == "" {
			continue
		}
		// Ensure nodegroups whose nodes are all NotReady still register (count 0).
		if _, seen := counts[ng]; !seen {
			counts[ng] = 0
		}
		if nodeReady(&node) {
			counts[ng]++
		}
	}
	return counts, true
}

// clusterFailure is the failure of a cluster-wide read made for a check, such
// as listing the cluster's nodegroups or PodDisruptionBudgets. op is the IAM
// action, or "" for a Kubernetes API call.
func clusterFailure(clusterName, op string, err error) diag.Failure {
	return diag.FromError(diag.KindCluster, clusterName, op, err)
}
