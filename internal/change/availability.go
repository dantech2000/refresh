package change

import (
	"context"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// availabilityNotices returns one notice for each target nodegroup with an
// instance type that EC2 does not offer in one of its availability zones.
// It runs health.Availability beside the gate: what it finds is advice and
// never part of the verdict, and a read that fails gives nothing.
func availabilityNotices(ctx context.Context, c *awsx.Clients, snap *fleet.Snapshot, targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	results := health.Run(ctx, []health.Check{health.Availability()}, &health.Input{
		Snapshot: snap, Targets: targets, Clients: c, Now: snap.FetchedAt,
	})
	if results[0].Outcome != health.Warn {
		return nil
	}
	return results[0].Details
}
