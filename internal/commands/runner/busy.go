package runner

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
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

// Busy is a refusal to start: what EKS is changing on the cluster, or the
// read that failed when refresh could not tell. Exit is the exit 3 error;
// a nil *Busy means the cluster is clear.
type Busy struct {
	// Changes are the changes in progress ("add-on vpc-cni UPDATING").
	Changes []string
	// Failure is the read that failed, when refresh could not tell.
	Failure *diag.Failure
	Exit    error
}

// CheckBusy is ClusterChanges for a command that prints a document when it
// refuses (-o json|yaml): it returns what to put in the document and the
// exit error, or nil when the cluster is clear.
func CheckBusy(ctx context.Context, api clustersvc.BusyAPI, cluster, region string, ignore func(clustersvc.Change) bool) *Busy {
	changes, err := ClusterChanges(ctx, api, cluster, ignore)
	switch {
	case err != nil:
		f := diag.FromError(diag.KindCluster, cluster, diag.OperationOf(err), err)
		f.Region = region
		return &Busy{Failure: &f, Exit: BusyUnknownExit(ctx, cluster, err)}
	case len(changes) > 0:
		out := make([]string, len(changes))
		for i, c := range changes {
			out[i] = c.String()
		}
		return &Busy{Changes: out, Exit: BusyExit(cluster, changes)}
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
