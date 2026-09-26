package runner

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// ClusterChanges reads what EKS is changing on cluster right now (see
// cluster.ChangesInProgress), minus the changes ignore accepts (nil keeps
// every change). A mutating command calls it before its confirmation prompt
// and before any change; a dry run does not.
//
// A read that fails is not a refusal: it prints a warning on stderr and
// returns no changes, and EKS still rejects a second update itself.
func ClusterChanges(ctx context.Context, api clustersvc.BusyAPI, cluster string, ignore func(clustersvc.Change) bool) clustersvc.Changes {
	changes, err := clustersvc.ChangesInProgress(ctx, api, cluster)
	if err != nil {
		if ctx.Err() == nil {
			render.Notef(ui.Stderr, render.Warn, "Could not check what EKS is changing on %s: %v", cluster, err)
		}
		return nil
	}
	var out clustersvc.Changes
	for _, c := range changes {
		if ignore == nil || !ignore(c) {
			out = append(out, c)
		}
	}
	return out
}

// BusyMessage says that cluster is busy with changes and that nothing was
// started.
func BusyMessage(cluster string, changes clustersvc.Changes) string {
	return fmt.Sprintf("%s is busy (%s); nothing was started. Run it again once that finishes", cluster, changes)
}

// BusyExit is the exit 3 error of a run that found cluster busy.
func BusyExit(cluster string, changes clustersvc.Changes) error {
	return cli.Exit(BusyMessage(cluster, changes), ExitBlocked)
}

// RefuseIfBusy returns BusyExit when ClusterChanges finds anything, else
// nil.
func RefuseIfBusy(ctx context.Context, api clustersvc.BusyAPI, cluster string, ignore func(clustersvc.Change) bool) error {
	if changes := ClusterChanges(ctx, api, cluster, ignore); len(changes) > 0 {
		return BusyExit(cluster, changes)
	}
	return nil
}
