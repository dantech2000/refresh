package live

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/dantech2000/refresh/internal/commands/factory"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

// changesInProgress reads, right now, what EKS is changing on cluster (see
// cluster.ChangesInProgress). The fleet sweep can be a minute old, and
// between two steps of an upgrade run elsewhere (the CLI, the console) the
// cluster reads ACTIVE for a moment: Start checks this before it changes
// anything.
func changesInProgress(ctx context.Context, cfg aws.Config, cluster string) ([]string, error) {
	changes, err := clustersvc.ChangesInProgress(ctx, factory.NewEKSClient(cfg), cluster)
	if err != nil {
		return nil, err
	}
	busy := make([]string, len(changes))
	for i, c := range changes {
		busy[i] = c.String()
	}
	return busy, nil
}
