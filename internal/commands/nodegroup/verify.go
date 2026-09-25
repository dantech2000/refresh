package nodegroup

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/diag"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// Post-roll verification lives in the nodegroup service, shared with the
// TUI's roll; these names keep the command's code as it was.
type (
	pendingPodSet = nodegroupsvc.PendingPods
	// PostRollVerification is the result of verifying a completed AMI roll.
	PostRollVerification = nodegroupsvc.PostRollVerification
)

func snapshotPendingPods(ctx context.Context, k8sClient kubernetes.Interface) (pendingPodSet, bool) {
	return nodegroupsvc.SnapshotPendingPods(ctx, k8sClient)
}

func verifyPostRoll(ctx context.Context, eksClient nodegroupsvc.NodegroupDescriber, k8sClient kubernetes.Interface, clusterName string, nodegroups []string, preroll pendingPodSet, prerollOK bool) (PostRollVerification, []diag.Failure) {
	return nodegroupsvc.VerifyPostRoll(ctx, eksClient, k8sClient, clusterName, nodegroups, preroll, prerollOK)
}
