package runner

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

// ClusterChanges reads what EKS is changing on cluster right now (see
// cluster.ChangesInProgress), minus the changes ignore accepts (nil keeps
// every change). A mutating command calls it before its confirmation prompt
// and before any change; a dry run does not.
//
// A read that fails returns the error: a caller refuses to start, as the
// TUI does, since an add-on update EKS is running could go unseen.
func ClusterChanges(ctx context.Context, api clustersvc.BusyAPI, cluster string, ignore func(clustersvc.Change) bool) (clustersvc.Changes, error) {
	changes, err := clustersvc.ChangesInProgress(ctx, api, cluster)
	if err != nil {
		return nil, err
	}
	var out clustersvc.Changes
	for _, c := range changes {
		if ignore == nil || !ignore(c) {
			out = append(out, c)
		}
	}
	return out, nil
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

// RefuseIfBusy returns BusyExit when ClusterChanges finds anything,
// BusyUnknownExit when it could not read the cluster, else nil.
func RefuseIfBusy(ctx context.Context, api clustersvc.BusyAPI, cluster string, ignore func(clustersvc.Change) bool) error {
	changes, err := ClusterChanges(ctx, api, cluster, ignore)
	switch {
	case err != nil:
		return BusyUnknownExit(ctx, cluster, err)
	case len(changes) > 0:
		return BusyExit(cluster, changes)
	}
	return nil
}

// BusyUnknownExit is the exit 3 error of a run that could not check
// whether EKS is changing cluster (exit 1 when ctx ended).
func BusyUnknownExit(ctx context.Context, cluster string, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return cli.Exit(fmt.Sprintf("could not check what EKS is changing on %s; nothing was started\n%s", cluster, awserr.FormatAWSError(err, "checking "+cluster+" for changes in progress").Error()), ExitBlocked)
}
