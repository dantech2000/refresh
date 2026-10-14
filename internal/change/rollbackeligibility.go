package change

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// versionHistory is what the update history of a cluster says about the
// version that it runs.
type versionHistory struct {
	// read is false when the history could not be read.
	read bool
	// upgrade is the newest successful in-place upgrade to the current
	// version, and rollback the newest successful rollback to it.
	upgrade, rollback *fleet.Update
	// inFlight is the newest version update or rollback in progress.
	inFlight *fleet.Update
}

// rolledBack reports whether the cluster reached its version by a
// rollback, after any upgrade to it.
func (h versionHistory) rolledBack() bool {
	return h.rollback != nil && (h.upgrade == nil || h.rollback.CreatedAt.After(h.upgrade.CreatedAt))
}

// versionHistoryOf reads the update history of the snapshot. EKS lists
// the updates in no promised order, so the newest of each kind is found
// by its time.
func versionHistoryOf(snap *fleet.Snapshot) versionHistory {
	h := versionHistory{read: snap.Have.Updates == fleet.Collected}
	if !h.read {
		return h
	}
	newer := func(u, than *fleet.Update) bool { return than == nil || u.CreatedAt.After(than.CreatedAt) }
	for i := range snap.Updates {
		u := &snap.Updates[i]
		if u.Type != updateVersion && u.Type != updateRollback {
			continue
		}
		switch {
		case u.Status == updateInProgress:
			if newer(u, h.inFlight) {
				h.inFlight = u
			}
		case u.Status != updateSuccessful || u.Version != snap.Cluster.Version:
		case u.Type == updateVersion:
			if newer(u, h.upgrade) {
				h.upgrade = u
			}
		default:
			if newer(u, h.rollback) {
				h.rollback = u
			}
		}
	}
	return h
}

// aboutDate formats a time as a day. The dates of the rollback window
// are approximate.
func aboutDate(t time.Time) string { return t.UTC().Format("2006-01-02") }

// eligibility decides whether the cluster can roll back now. The first
// row that matches decides:
//
//  1. The update history could not be read: an Unread blocker.
//  2. A rollback to another version is in progress: a Busy blocker.
//     Resuming it would move the nodes and the add-ons to a version that
//     the control plane is not going to.
//  3. The rollback to the target is in progress: eligible. The
//     control-plane step attaches.
//  4. The cluster is not ACTIVE, or a version update is in progress: a
//     Busy blocker.
//  5. The history has no in-place upgrade to the current version: an
//     Unavailable blocker. A cluster created at its version cannot roll
//     back.
//  6. The upgrade started 7 days and 2 hours ago or longer: an
//     Unavailable blocker.
//  7. The upgrade started 7 days ago or longer: eligible, with a notice.
//     EKS decides.
//  8. Otherwise: eligible.
//
// Rows 6 to 8 set the Rollback of the plan: the upgrade was found.
func (p *rollbackPlanner) eligibility(h versionHistory, rollingBack bool) {
	target := p.cp.To
	switch {
	case !h.read:
		p.cp.Failures = appendFailure(p.cp.Failures, p.unreadHistory())
		p.block(BlockerUnread, nil, "could not read the update history of %s, so nothing shows that it can roll back: rerun to retry", p.cp.Cluster)
	case rollingBack && h.inFlight.Version != target:
		p.block(BlockerBusy, nil, "a rollback to %s is in progress (update %s), not to %s: wait for it to finish, then rerun", h.inFlight.Version, h.inFlight.ID, target)
	case rollingBack:
	case p.snap.Cluster.Status != clusterActive || h.inFlight != nil:
		p.block(BlockerBusy, nil, "the cluster is %s with an update in progress; EKS rolls back only an ACTIVE cluster with no update in progress: wait for it to finish, then rerun", p.snap.Cluster.Status)
	case h.upgrade == nil:
		p.block(BlockerUnavailable, nil, "the update history has no in-place upgrade to %s: a cluster created at its version cannot roll back", p.live)
	default:
		started := h.upgrade.CreatedAt
		until := started.Add(fleet.RollbackWindow)
		p.cp.Rollback = &fleet.Rollback{PreviousVersion: target, UpgradedAt: started, AvailableUntil: until}
		switch {
		case !p.in.Now.Before(until.Add(rollbackWindowSlack)):
			p.block(BlockerUnavailable, nil, "the upgrade to %s started about %s; the 7-day rollback window closed about %s", p.live, aboutDate(started), aboutDate(until))
		case !p.in.Now.Before(until):
			p.notice("the 7-day rollback window may have just closed (the upgrade to %s started about %s, and EKS counts from when it finished); EKS decides", p.live, aboutDate(started))
		}
	}
}

// RollbackStillEligible judges a rollback again on a read of the cluster
// from right before one of its requests, against target, the version
// that the plan returns to. The plan is old by then: a question was open,
// or a nodegroup rolled for an hour. It makes the checks of PlanRollback
// that say whether EKS will take the control-plane rollback, and none
// about the steps: the update history and the rollback window
// (eligibility), the offer of the target version (targetOffer), and the
// rollback insights (insights). Without them a run could roll nodegroups
// and add-ons back for a control-plane rollback that EKS then refuses.
//
// The error is a RefusalError when the cluster would now return to
// another version than target: no answer and no flag accepts that. The
// blockers are what stops the rollback now, with the failures of the reads
// behind them. A cluster whose control plane is back at target already has
// nothing left to judge.
func RollbackStillEligible(snap *fleet.Snapshot, req RollbackRequest, in RollbackInputs, target fleet.Version) ([]Blocker, []apidoc.Failure, error) {
	t := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	live := snap.Cluster.Version
	p := &rollbackPlanner{snap: snap, req: req, in: in, live: live, cp: &ClusterPlan{Target: t, From: live, To: target}}
	if snap.Have.Cluster != fleet.Collected || live.IsZero() {
		p.block(BlockerUnread, nil, "the Kubernetes version of cluster %s is not known", t.Cluster)
		return p.cp.Blockers, p.cp.Failures, nil
	}
	h := versionHistoryOf(snap)
	if !h.read {
		p.eligibility(h, false)
		return p.cp.Blockers, p.cp.Failures, nil
	}
	rollingBack := h.inFlight != nil && h.inFlight.Type == updateRollback
	to := live
	if (!h.rolledBack() || rollingBack) && live.Minor > 0 {
		to = fleet.Version{Major: live.Major, Minor: live.Minor - 1}
	}
	if to != target {
		return nil, nil, &RefusalError{Reason: RefusalTargetChanged, Message: fmt.Sprintf(
			"the rollback target changed from %s to %s since the plan: plan again", target, to)}
	}
	if to == live {
		return nil, nil, nil
	}
	p.eligibility(h, rollingBack)
	if !rollingBack {
		control := StepRef{Phase: PhaseRef{Target: t, Kind: PhaseControlPlane}, Kind: StepControlPlane}
		p.targetOffer()
		p.insights(&control)
	}
	return p.cp.Blockers, p.cp.Failures, nil
}

// unreadHistory is the failure of an update history that the snapshot
// did not read. The read is advisory in a snapshot and leaves no failure
// there, and a rollback cannot be planned without it.
func (p *rollbackPlanner) unreadHistory() apidoc.Failure {
	f := apidoc.NewFailure(apidoc.FailureCluster, p.cp.Cluster, apidoc.ReasonUnknown, "the update history of the cluster was not read")
	f.Region = p.cp.Region
	return f
}

// targetOffer checks that EKS offers the version to roll back to, and
// that the upgrade policy of the cluster allows it: a version in
// extended support needs the EXTENDED policy. A check that could not be
// made blocks: unchecked, the run could roll nodegroups and add-ons back
// before EKS refuses the control plane.
func (p *rollbackPlanner) targetOffer() {
	target := p.cp.To
	offer := p.in.Version(target)
	policy := p.snap.Cluster.UpgradePolicy
	switch {
	case offer.Err != nil:
		p.fail(apidoc.FailureCluster, p.cp.Cluster, offer.Err)
		p.block(BlockerUnread, nil, "could not check that EKS still supports %s (rerun to retry): %s", target, awsx.Summary(offer.Err))
	case !offer.Offered:
		p.block(BlockerUnavailable, nil, "EKS does not offer %s", target)
	case offer.Tier == apidoc.SupportUnsupported:
		p.block(BlockerUnavailable, nil, "%s is no longer supported by EKS", target)
	case offer.Tier == apidoc.SupportExtended && policy != supportExtended:
		if policy == "" {
			policy = "not set"
		}
		p.block(BlockerUnavailable, nil,
			"%s is in extended support, and the cluster's upgrade policy is %s: change it to EXTENDED first (aws eks update-cluster-config --name %s --upgrade-policy supportType=EXTENDED); extended support charges apply",
			target, policy, shellQuote(p.cp.Cluster))
	case offer.Tier == apidoc.SupportExtended:
		p.notice("%s is in extended support: extended support charges apply after the rollback", target)
	}
}

// insights judges the ROLLBACK_READINESS insights. An ERROR insight and
// an UNKNOWN one block the control-plane step, and a read that failed
// blocks too. With SkipInsights none of them blocks: the plan has a
// notice, and the control-plane request carries force, so EKS skips the
// same checks. A WARNING insight is a notice. No insight at all is
// neither: EKS evaluates them when the rollback starts.
func (p *rollbackPlanner) insights(control *StepRef) {
	read := p.in.Insights()
	if read.Err != nil {
		p.fail(apidoc.FailureCluster, p.cp.Cluster, read.Err)
		if p.req.SkipInsights {
			p.notice("could not read the rollback readiness insights; --skip-insights-check: EKS skips them too")
			return
		}
		p.block(BlockerUnread, control, "could not read the rollback readiness insights (rerun to retry, or pass --skip-insights-check to roll back without them, not recommended): %s", awsx.Summary(read.Err))
		return
	}
	var blocking, warnings []string
	for _, in := range read.Insights {
		switch in.Status {
		case insightPassing:
		case insightWarning:
			warnings = append(warnings, insightName(in))
		case insightError:
			blocking = append(blocking, insightName(in)+" (ERROR)")
		default:
			blocking = append(blocking, insightName(in)+" (UNKNOWN)")
		}
	}
	if len(warnings) > 0 {
		p.notice("rollback insight warnings: %s", strings.Join(warnings, ", "))
	}
	switch {
	case len(blocking) > 0 && p.req.SkipInsights:
		p.notice("%d blocking insight(s) bypassed with --skip-insights-check: %s; EKS cannot guarantee a safe rollback", len(blocking), strings.Join(blocking, ", "))
	case len(blocking) > 0:
		p.block(BlockerNotReady, control,
			"%d blocking insight(s): %s; resolve them (refresh cluster upgrade-check --category ROLLBACK_READINESS), or pass --skip-insights-check to roll back anyway (not recommended)",
			len(blocking), strings.Join(blocking, ", "))
	}
}
