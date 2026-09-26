package nodegroup

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/commands/runner"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

// updateBusyChanges reads what EKS is changing on cluster before `nodegroup
// update` starts anything. A nodegroup the pattern selects is left out: the
// run skips a target that is already UPDATING (skipAlreadyUpdating). A
// read that fails returns the error: the caller refuses the cluster.
func updateBusyChanges(ctx context.Context, eksClient *eks.Client, clusterName, pattern string) (clustersvc.Changes, error) {
	targets := healthTargetNodegroups(ctx, eksClient, clusterName, pattern)
	return runner.ClusterChanges(ctx, eksClient, clusterName, func(c clustersvc.Change) bool {
		return c.Kind == clustersvc.ChangeNodegroup && slices.Contains(targets, c.Name)
	})
}

// busyStrings is changes as text, one entry per change.
func busyStrings(changes clustersvc.Changes) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.String()
	}
	return out
}
