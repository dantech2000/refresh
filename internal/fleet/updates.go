package fleet

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/awsx"
)

// Update is one EKS update of the cluster itself, as far as the rollback
// window needs it.
type Update struct {
	ID string
	// Type and Status are the EKS values, such as VersionUpdate and
	// Successful.
	Type, Status string
	// Version is the Version parameter of a version update or rollback.
	Version   Version
	CreatedAt time.Time
}

// RollbackWindow is how long after an in-place upgrade EKS lets a cluster
// roll back to the previous minor version.
const RollbackWindow = 7 * 24 * time.Hour

// Rollback says that a cluster can roll back, and until when.
type Rollback struct {
	// PreviousVersion is the version that a rollback returns to.
	PreviousVersion Version
	// UpgradedAt is when the upgrade to the current version started. EKS
	// counts the window from when it finished, so the dates are
	// approximate.
	UpgradedAt     time.Time
	AvailableUntil time.Time
}

// RollbackWindowOf returns the rollback that the update history of the
// snapshot allows at now: the newest successful in-place upgrade to the
// current version started less than RollbackWindow ago, with no rollback
// since. ok is false when there is none, and when the updates or the
// cluster were not read.
func RollbackWindowOf(snap *Snapshot, now time.Time) (Rollback, bool) {
	if snap.Have.Updates != Collected || snap.Have.Cluster != Collected || snap.Cluster.Version.IsZero() {
		return Rollback{}, false
	}
	var upgrade, rollback *Update
	for i := range snap.Updates {
		u := &snap.Updates[i]
		if u.Status != string(ekstypes.UpdateStatusSuccessful) || u.Version != snap.Cluster.Version {
			continue
		}
		switch u.Type {
		case string(ekstypes.UpdateTypeVersionUpdate):
			if upgrade == nil || u.CreatedAt.After(upgrade.CreatedAt) {
				upgrade = u
			}
		case string(ekstypes.UpdateTypeVersionRollback):
			if rollback == nil || u.CreatedAt.After(rollback.CreatedAt) {
				rollback = u
			}
		}
	}
	if upgrade == nil || (rollback != nil && rollback.CreatedAt.After(upgrade.CreatedAt)) || snap.Cluster.Version.Minor == 0 {
		return Rollback{}, false
	}
	until := upgrade.CreatedAt.Add(RollbackWindow)
	if !now.Before(until) {
		return Rollback{}, false
	}
	return Rollback{
		PreviousVersion: Version{Major: snap.Cluster.Version.Major, Minor: snap.Cluster.Version.Minor - 1},
		UpgradedAt:      upgrade.CreatedAt,
		AvailableUntil:  until,
	}, true
}

// fetchUpdates reads the update history of the cluster: the list, then
// every update in one wave. EKS promises no order, so every update is
// described. The read is advisory: a failure sets the state and adds no
// failure, because the rollback window is an extra that the output marks
// as not read. The error is the one of the read that failed, for a caller
// that needs the history and reports its failure.
func (f *Fetcher) fetchUpdates(ctx context.Context, c *awsx.Clients, cluster string) ([]Update, State, error) {
	ids, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListUpdatesOutput, error) {
			return c.EKS.ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String(cluster), NextToken: token})
		},
		func(out *eks.ListUpdatesOutput) ([]string, *string) { return out.UpdateIds, out.NextToken },
	)
	if err != nil {
		return nil, stateOf(err), err
	}
	type described struct {
		ran    bool
		update Update
		err    error
	}
	results := each(ctx, f.tickets(c), ids, func(ctx context.Context, id string) described {
		out, err := c.EKS.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String(cluster), UpdateId: aws.String(id)})
		if err != nil {
			return described{ran: true, err: err}
		}
		if out.Update == nil {
			return described{ran: true}
		}
		u := Update{ID: aws.ToString(out.Update.Id), Type: string(out.Update.Type), Status: string(out.Update.Status), CreatedAt: aws.ToTime(out.Update.CreatedAt)}
		for _, p := range out.Update.Params {
			if p.Type == ekstypes.UpdateParamTypeVersion {
				u.Version, _ = ParseVersion(aws.ToString(p.Value))
			}
		}
		return described{ran: true, update: u}
	})
	updates := make([]Update, 0, len(ids))
	for _, r := range results {
		if !r.ran {
			return nil, NotAttempted, nil
		}
		if r.err != nil {
			return nil, stateOf(r.err), r.err
		}
		if r.update.ID != "" {
			updates = append(updates, r.update)
		}
	}
	return updates, Collected, nil
}
