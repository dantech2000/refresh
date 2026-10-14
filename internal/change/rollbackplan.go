package change

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// A version rollback moves the control plane back one minor version
// within 7 days of an in-place upgrade. It is UpdateClusterVersion with
// the previous version, and EKS types the update VersionRollback. EKS
// rolls back only the control plane and the nodes of EKS Auto Mode, so
// the documented order is: the managed nodegroups roll back, then the
// add-ons that the older version cannot run are downgraded, then the
// control plane rolls back.
// https://docs.aws.amazon.com/eks/latest/userguide/rollback-cluster.html

// rollbackWindowSlack covers the unknown length of the upgrade itself.
// EKS counts the window from the end of the upgrade, and an update
// records only when it was created. So the planner blocks only once even
// a slow control-plane update would have closed the window, and leaves
// the hours before that to EKS, with a notice.
const rollbackWindowSlack = 2 * time.Hour

// The EKS update types and the update status that the rollback planner
// reads in the history of a cluster.
const (
	updateVersion    = "VersionUpdate"
	updateRollback   = "VersionRollback"
	updateInProgress = "InProgress"
	updateSuccessful = "Successful"
	supportExtended  = "EXTENDED"
)

// RollbackInputs is what PlanRollback reads beside the snapshot. The
// planner makes no call of its own: its caller reads from AWS inside the
// three functions, and PlanRollback calls each one only when the plan
// needs it.
type RollbackInputs struct {
	// Now is the time of the plan. The rollback window is judged at it.
	Now time.Time
	// Version says whether EKS offers a Kubernetes version, and in which
	// support tier: one DescribeClusterVersions call. It is called once,
	// for the version to roll back to, and not at all when a rollback is
	// in progress or done.
	Version func(v fleet.Version) VersionOffer
	// AddonVersions returns the versions of an add-on that a Kubernetes
	// version can run, newest first, and none when EKS publishes none:
	// fleet.Fetcher.AddonVersions. It is called once for each add-on, for
	// the version to roll back to.
	AddonVersions func(addon string, kubernetes fleet.Version) ([]string, error)
	// Insights returns the ROLLBACK_READINESS insights of the cluster. No
	// refresh is started for them: EKS evaluates them anew when the
	// rollback starts. It is called under the same condition as Version.
	Insights func() InsightsRead
}

// PlanRollback derives the plan to roll a cluster back one minor version
// from a snapshot with the nodegroups, the add-ons, and the update
// history of the cluster. A step that live state already meets is Skip,
// so the same request after a stopped run goes on where it ended, and
// after a finished rollback it has only the work that is left at the
// version that the control plane is back at.
//
// The phases are Nodegroups, Addons, and ControlPlane, with no hop in
// their refs. The control-plane step carries Force when the request skips
// the insights. Like PlanUpgrade, it applies no busy rule and no health
// gate.
//
// The error is a RefusalError for a timeout out of bounds and for a
// cluster with no minor version to return to, and a NotFoundError for a
// --skip-nodegroup name that the cluster does not have.
func PlanRollback(snap *fleet.Snapshot, req RollbackRequest, in RollbackInputs) (*Plan, error) {
	if err := ValidateRollbackTimeout(req.RollbackTimeout); err != nil {
		return nil, err
	}
	target := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	live := snap.Cluster.Version
	if snap.Have.Cluster != fleet.Collected || live.IsZero() {
		return nil, &RefusalError{Reason: RefusalVersionUnknown, Message: "the Kubernetes version of cluster " + target.Cluster + " is not known"}
	}
	p := &rollbackPlanner{
		snap: snap, req: req, in: in, live: live,
		cp: &ClusterPlan{Target: target, From: live, Failures: slices.Clone(snap.Failures), snapshot: snap},
	}
	if s := snap.Have.Nodegroups; s == fleet.Collected || s == fleet.Partial {
		have := make([]string, len(snap.Nodegroups))
		for i, n := range snap.Nodegroups {
			have[i] = n.Name
		}
		if err := checkExists(target, StepNodegroup, req.SkipNodegroups, have); err != nil {
			return nil, err
		}
	}
	h := versionHistoryOf(snap)
	rollingBack := h.inFlight != nil && h.inFlight.Type == updateRollback
	if h.rolledBack() && !rollingBack {
		// The control plane is back at its target: a rerun after a
		// finished rollback. Only work that is left at that version
		// remains, and nothing is checked again.
		p.cp.To = live
		p.notice("the control plane rolled back from %s to %s on about %s (update %s)", nextMinor(live), live, aboutDate(h.rollback.CreatedAt), h.rollback.ID)
		p.workSteps()
		return p.plan(), nil
	}
	if live.Minor == 0 {
		return nil, &RefusalError{Reason: RefusalNoPreviousVersion, Message: fmt.Sprintf("version %s has no previous minor", live)}
	}
	p.cp.To = fleet.Version{Major: live.Major, Minor: live.Minor - 1}
	p.eligibility(h, rollingBack)
	control := StepRef{Phase: PhaseRef{Target: target, Kind: PhaseControlPlane}, Kind: StepControlPlane}
	if !rollingBack {
		p.targetOffer()
		p.insights(&control)
	}
	p.workSteps()

	step := Step{Ref: control, Action: Submit, Reason: VersionAhead, From: live.String(), To: p.cp.To.String(), Force: req.SkipInsights}
	if rollingBack {
		// The rollback in flight has its own request; this run sends none.
		step.Action, step.Reason, step.Force = Attach, InFlight, false
	}
	p.cp.Phases = append(p.cp.Phases, Phase{Ref: control.Phase, Steps: []Step{step}})
	p.notice("self-managed and hybrid nodes are not rolled back by EKS or refresh: move them to %s yourself before the control plane rolls back. Fargate pods at %s trigger the kubelet skew insight: delete them first, and they come back at %s",
		p.cp.To, live, p.cp.To)
	return p.plan(), nil
}

// rollbackPlanner holds one run of PlanRollback.
type rollbackPlanner struct {
	snap *fleet.Snapshot
	req  RollbackRequest
	in   RollbackInputs
	cp   *ClusterPlan
	// live is the version that the control plane runs.
	live fleet.Version
}

func (p *rollbackPlanner) plan() *Plan {
	return &Plan{Kind: ClusterRollback, Request: p.req, Clusters: []ClusterPlan{*p.cp}, At: p.in.Now}
}

func (p *rollbackPlanner) notice(format string, args ...any) {
	p.cp.Notices = append(p.cp.Notices, fmt.Sprintf(format, args...))
}

func (p *rollbackPlanner) block(kind BlockerKind, step *StepRef, format string, args ...any) {
	p.cp.Blockers = append(p.cp.Blockers, Blocker{Kind: kind, Step: step, Message: fmt.Sprintf(format, args...)})
}

func (p *rollbackPlanner) fail(kind apidoc.FailureKind, name string, err error) {
	f := fleet.FailureFromError(kind, name, err)
	f.Region = p.cp.Region
	if kind != apidoc.FailureCluster {
		f.Cluster = p.cp.Cluster
	}
	p.cp.Failures = appendFailure(p.cp.Failures, f)
}

// RollbackControlPlaneBlockers checks the snapshot that a run reads
// right before the control plane rolls back: every managed nodegroup must
// be at the target or below it, because nodes newer than the control
// plane are outside the Kubernetes version skew policy. A nodegroup that
// the run did not roll back and that is still above the target blocks the
// phase: a skipped one, one with a custom AMI, or one whose roll failed.
// A nodegroup that could not be read is an Unread blocker.
func RollbackControlPlaneBlockers(snap *fleet.Snapshot, target fleet.Version) []Blocker {
	ref := &StepRef{
		Phase: PhaseRef{Target: Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}, Kind: PhaseControlPlane},
		Kind:  StepControlPlane,
	}
	var out []Blocker
	if snap.Have.Nodegroups != fleet.Collected {
		out = append(out, Blocker{Kind: BlockerUnread, Step: ref, Message: "could not read every nodegroup of " + snap.Cluster.Name + " before the control plane rolls back"})
	}
	var above []string
	for _, n := range snap.Nodegroups {
		if n.State == fleet.Collected && !atLeast(target, n.Version) {
			above = append(above, n.Name+" at "+n.Version.String())
		}
	}
	if len(above) > 0 {
		out = append(out, Blocker{Kind: BlockerNotReady, Step: ref, Message: fmt.Sprintf(
			"the control plane has not changed: nodegroups above %s (%s) must move to %s first, then rerun", target, strings.Join(above, ", "), target)})
	}
	return out
}

// RollbackTargetChanged compares the plan that a person reviewed with
// the plan computed now, before the first change of a rollback. It
// returns a RefusalError when they return to different versions: the
// nodes and the add-ons would go to a version that nobody confirmed. No
// answer and no flag accepts that. Diff reports every other difference,
// and a blocker of the new plan refuses the run on its own.
func RollbackTargetChanged(reviewed, fresh *Plan) error {
	for i := range fresh.Clusters {
		cur := &fresh.Clusters[i]
		for j := range reviewed.Clusters {
			if prev := &reviewed.Clusters[j]; prev.Target == cur.Target && prev.To != cur.To {
				return &RefusalError{Reason: RefusalTargetChanged, Message: fmt.Sprintf(
					"the rollback target changed from %s to %s since the plan; nothing was changed: plan again", prev.To, cur.To)}
			}
		}
	}
	return nil
}
