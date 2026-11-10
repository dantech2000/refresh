package health

import (
	"context"
	"slices"
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
	// Reasons are the reasons behind a verdict other than Ready, worst
	// first. A support window that is not standard is a Warn reason of
	// SupportReason, which no check has.
	Reasons []Reason
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
		metrics  controlPlaneMetrics
		cpErr    error
	)
	// The control-plane metrics need only the name of the cluster. The
	// check judges them after the fetch: the etcd limit depends on the
	// tier of the control plane.
	stub := &Input{Snapshot: &fleet.Snapshot{Cluster: fleet.Cluster{Name: cluster, Region: c.Region}}, Clients: c, Now: f.Time()}
	wg.Go(func() {
		snap, fetchErr = f.Fetch(ctx, c, cluster, fleet.Need{Nodegroups: true, Addons: true, LatestAddons: true, Support: true, Updates: true})
	})
	wg.Go(func() { insights, listErr = ListInsights(ctx, c, cluster, category) })
	wg.Go(func() { metrics, cpErr = fetchControlPlaneMetrics(ctx, stub, cluster) })
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
	cp := ControlPlane()
	cp.Run = func(context.Context, *Input) Result { return controlPlaneResult(snap, metrics, cpErr) }
	checks := []Check{Insights(InsightsInput{Insights: insights, Err: listErr, Category: category}), VersionSkew(), cp}
	report.Checks = Run(ctx, checks, &Input{Snapshot: snap, Clients: c, Now: snap.FetchedAt})
	report.Readiness, report.Reasons = readinessOf(report.Checks, snap.ClusterDoc().Support)
	return report, nil
}

// SupportReason is the name of the reason that a support window gives.
const SupportReason = "Support"

// ReasonTexts returns the reasons of the report as the document words
// them, such as "VersionSkew: ...".
func (r ReadinessReport) ReasonTexts() []string { return reasonStrings(r.Reasons) }

// readinessOf folds the results into the verdict: the gate with an empty
// policy, where a block from unread data alone is Incomplete. A support
// window that is not standard is a reason to review.
func readinessOf(results []Result, support *apidoc.Support) (apidoc.Readiness, []Reason) {
	v := Gate(results, Policy{})
	reasons := slices.Clone(v.Reasons)
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
		reasons = append(reasons, Reason{Check: SupportReason, Outcome: Warn, Message: "the cluster is in " + strings.ToLower(string(support.Tier)) + " support"})
		if readiness == apidoc.ReadinessReady {
			readiness = apidoc.ReadinessReview
		}
	}
	return readiness, reasons
}
