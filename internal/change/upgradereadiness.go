package change

import (
	"fmt"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The EKS insight statuses that a planner tells apart. Any other value,
// UNKNOWN among them, is an insight that EKS could not evaluate.
const (
	insightPassing = "PASSING"
	insightWarning = "WARNING"
	insightError   = "ERROR"
)

// skipInsightsHint ends every insights blocker of an upgrade.
const skipInsightsHint = "re-run shortly, or pass --skip-insights-check to upgrade without this check (not recommended)"

// Readiness is the readiness of one hop of an upgrade: whether its
// control plane may move.
type Readiness struct {
	// Blocker is nil when the control plane may move.
	Blocker *Blocker
	Notices []string
	// Failures are the insight reads that failed.
	Failures []apidoc.Failure
}

// HopReadiness judges the readiness of a hop against the snapshot, which
// a run reads again right before the control plane of that hop moves: the
// plan judged later hops from versions that it only predicted, and EKS
// evaluates the insights for the minor version after the live one only.
// It is ready, with no check, when the control plane is at the hop
// already.
//
// insights is called only when the hop is the minor version after the
// live one and skipInsights is false, and only after the kubelet skew
// passed: a real run refreshes the insights in it, and a hop that the
// skew blocks sends no refresh.
func HopReadiness(snap *fleet.Snapshot, hop fleet.Version, skipInsights bool, insights func(hop fleet.Version) InsightsRead) Readiness {
	if atLeast(snap.Cluster.Version, hop) {
		return Readiness{}
	}
	versions := make(map[string]fleet.Version, len(snap.Nodegroups))
	for _, n := range snap.Nodegroups {
		versions[n.Name] = n.Version
	}
	return hopReadiness(snap, hop, versions, skipInsights, insights)
}

// hopReadiness is the readiness gate of one hop: the kubelet skew of
// every nodegroup against the hop target, then the upgrade insights.
// versions has the version that each nodegroup runs when the hop starts.
// Every nodegroup counts, also one that the run leaves alone.
//
// EKS evaluates the upgrade insights for the minor version after the live
// control plane only, so they are judged for that hop alone. A later hop
// gets the skew check here, and the whole gate again from HopReadiness.
func hopReadiness(snap *fleet.Snapshot, hop fleet.Version, versions map[string]fleet.Version, skipInsights bool, insights func(fleet.Version) InsightsRead) Readiness {
	target := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	ref := StepRef{Phase: PhaseRef{Target: target, Kind: PhaseControlPlane, Hop: hop}, Kind: StepControlPlane}
	blocked := func(kind BlockerKind, msg string) *Blocker { return &Blocker{Kind: kind, Step: &ref, Message: msg} }

	var skew []string
	for _, n := range snap.Nodegroups {
		if v := versions[n.Name]; beyondKubeletSkew(v, hop) {
			skew = append(skew, fmt.Sprintf("nodegroup %s at %s would exceed the kubelet skew limit (%d minors) against %s", n.Name, v, kubeletSkew, hop))
		}
	}
	if len(skew) > 0 {
		return Readiness{Blocker: blocked(BlockerNotReady, strings.Join(skew, "; "))}
	}
	live := snap.Cluster.Version
	switch {
	case hop.Compare(live) <= 0, hop.Compare(nextMinor(live)) > 0:
		// The control plane does not move in this hop, or the hop is one
		// that EKS has no insight for yet.
		return Readiness{}
	case skipInsights:
		return Readiness{Notices: []string{fmt.Sprintf(
			"cluster insights for %s were not checked (--skip-insights-check): deprecated APIs and kubelet skew of nodes outside managed nodegroups (Fargate, Karpenter, self-managed, hybrid) are unverified", hop)}}
	}
	read := insights(hop)
	fail := func(what string, err error) Readiness {
		f := fleet.FailureFromError(apidoc.FailureCluster, target.Cluster, err)
		f.Region = target.Region
		return Readiness{
			Blocker:  blocked(BlockerUnread, fmt.Sprintf("could not %s cluster insights for %s: %s; %s", what, hop, refreshSummary(err), skipInsightsHint)),
			Failures: []apidoc.Failure{f},
		}
	}
	switch {
	case read.RefreshErr != nil:
		return fail("refresh", read.RefreshErr)
	case read.Err != nil && read.Refreshed:
		return fail("read", read.Err)
	case read.Err != nil:
		// A dry run does not block on a read that a real run makes again.
		r := fail("read", read.Err)
		return Readiness{
			Notices:  []string{fmt.Sprintf("could not read cluster insights for %s (%s); a real run will refresh them and block until EKS has evaluated %s", hop, awsx.Summary(read.Err), hop)},
			Failures: r.Failures,
		}
	}
	return judgeInsights(hop, read, blocked)
}

// judgeInsights turns the upgrade insights for a hop into its readiness.
// An ERROR insight blocks. In a real run an UNKNOWN insight blocks too,
// and so does a hop with no insight at all: EKS has not evaluated it, so
// the checks for deprecated APIs and kubelet skew have not run. In the
// preview of a dry run those two are notices, because a real run
// refreshes the insights first. A WARNING insight is a notice.
func judgeInsights(hop fleet.Version, read InsightsRead, blocked func(BlockerKind, string) *Blocker) Readiness {
	var errs, unknown, warnings []string
	total := 0
	for _, in := range read.Insights {
		if v, err := fleet.ParseVersion(in.KubernetesVersion); err != nil || v != hop {
			continue
		}
		total++
		switch in.Status {
		case insightPassing:
		case insightWarning:
			warnings = append(warnings, insightName(in))
		case insightError:
			errs = append(errs, insightName(in))
		default:
			unknown = append(unknown, insightName(in))
		}
	}
	var r Readiness
	if len(warnings) > 0 {
		r.Notices = append(r.Notices, fmt.Sprintf("insight warnings for %s: %s", hop, strings.Join(warnings, ", ")))
	}
	realRun := fmt.Sprintf("a real run will refresh them and block until EKS has evaluated %s", hop)
	var parts []string
	if len(errs) > 0 {
		parts = append(parts, fmt.Sprintf("%d blocking insight(s): %s", len(errs), strings.Join(errs, ", ")))
	}
	if len(unknown) > 0 {
		parts = append(parts, fmt.Sprintf("%d insight(s) with UNKNOWN status (EKS could not evaluate them): %s", len(unknown), strings.Join(unknown, ", ")))
	}
	switch {
	case total == 0 && read.Refreshed:
		r.Blocker = blocked(BlockerNotReady, fmt.Sprintf("insights for %s not available yet: EKS hasn't evaluated this version; %s", hop, skipInsightsHint))
	case total == 0:
		r.Notices = append(r.Notices, fmt.Sprintf("insights for %s not evaluated yet; %s", hop, realRun))
	case len(errs) > 0, len(unknown) > 0 && read.Refreshed:
		msg := strings.Join(parts, "; ")
		if len(unknown) > 0 {
			msg += "; " + skipInsightsHint
		}
		r.Blocker = blocked(BlockerNotReady, msg)
	case len(unknown) > 0:
		r.Notices = append(r.Notices, strings.Join(parts, "; ")+"; "+realRun)
	}
	return r
}

// insightName returns the name of an insight, or its ID when it has
// none.
func insightName(in apidoc.Insight) string {
	if in.Name != "" {
		return in.Name
	}
	return in.ID
}
