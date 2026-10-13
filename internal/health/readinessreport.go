package health

import (
	"context"
	"strings"
	"sync"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// ReadinessReport is what the readiness checks of one cluster found: the
// report behind "cluster upgrade-check", which the command and the TUI
// both show.
type ReadinessReport struct {
	// Snapshot is the cluster as the report read it.
	Snapshot  *fleet.Snapshot
	Readiness apidoc.Readiness
	// Reasons are the reasons behind a verdict other than Ready.
	Reasons []string
	// Checks are the results of Insights, VersionSkew, and ControlPlane.
	Checks []Result
	// Insights are the insights of the category, every status included.
	// InsightsEvaluated is false when the list could not be read.
	Insights          []apidoc.Insight
	InsightsEvaluated bool
	// Rollback is the open rollback window, or nil. RollbackInsights are
	// the ROLLBACK_READINESS insights, read only inside the window, and
	// nil when they could not be read.
	Rollback         *fleet.Rollback
	RollbackInsights []apidoc.Insight
}

// CheckReadiness runs the readiness checks of a cluster: the snapshot,
// the insights, and the control-plane metrics in one wave, the rollback
// insights in a second one inside a rollback window, then the Insights,
// VersionSkew, and ControlPlane checks. "" is the UPGRADE_READINESS
// category. The error is the error of DescribeCluster: the cluster could
// not be read at all.
func CheckReadiness(ctx context.Context, c *awsx.Clients, f *fleet.Fetcher, cluster, category string) (ReadinessReport, error) {
	var (
		wg       sync.WaitGroup
		snap     *fleet.Snapshot
		fetchErr error
		insights []apidoc.Insight
		listErr  error
		cp       []Result
	)
	// The control-plane metrics need only the name of the cluster.
	stub := &fleet.Snapshot{Cluster: fleet.Cluster{Name: cluster, Region: c.Region}}
	wg.Go(func() {
		snap, fetchErr = f.Fetch(ctx, c, cluster, fleet.Need{Nodegroups: true, Addons: true, LatestAddons: true, Support: true, Updates: true})
	})
	wg.Go(func() { insights, listErr = ListInsights(ctx, c, cluster, category) })
	wg.Go(func() {
		cp = Run(ctx, []Check{ControlPlane()}, &Input{Snapshot: stub, Clients: c, Now: f.Time()})
	})
	wg.Wait()
	if fetchErr != nil {
		return ReadinessReport{}, fetchErr
	}
	report := ReadinessReport{Snapshot: snap, Insights: insights, InsightsEvaluated: listErr == nil}
	if window, ok := fleet.RollbackWindowOf(snap, snap.FetchedAt); ok {
		report.Rollback = &window
		if list, err := ListInsights(ctx, c, cluster, string(ekstypes.CategoryRollbackReadiness)); err == nil {
			report.RollbackInsights = list
		}
	}
	checks := []Check{Insights(InsightsInput{Insights: insights, Err: listErr, Category: category}), VersionSkew()}
	report.Checks = append(Run(ctx, checks, &Input{Snapshot: snap, Clients: c, Now: snap.FetchedAt}), cp...)
	report.Readiness, report.Reasons = readinessOf(report.Checks, snap.ClusterDoc().Support)
	return report, nil
}

// readinessOf folds the results into the verdict: the gate with an empty
// policy, where a block from unread data alone is Incomplete. A support
// window that is not standard is a reason to review.
func readinessOf(results []Result, support *apidoc.Support) (apidoc.Readiness, []string) {
	v := Gate(results, Policy{})
	reasons := make([]string, 0, len(v.Reasons)+1)
	for _, r := range v.Reasons {
		reasons = append(reasons, r.String())
	}
	readiness := apidoc.ReadinessReady
	switch v.Decision {
	case Block:
		readiness = apidoc.ReadinessIncomplete
		for _, b := range v.Blockers {
			if b.Outcome == Fail {
				readiness = apidoc.ReadinessBlocked
			}
		}
	case Ask:
		readiness = apidoc.ReadinessReview
	}
	if support != nil && support.Tier != apidoc.SupportStandard && support.Tier != apidoc.SupportUnknown {
		reasons = append(reasons, "Support: the cluster is in "+strings.ToLower(string(support.Tier))+" support")
		if readiness == apidoc.ReadinessReady {
			readiness = apidoc.ReadinessReview
		}
	}
	return readiness, reasons
}
