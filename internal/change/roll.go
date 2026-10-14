package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// validate rejects a request with no cluster and no fleet, or with both.
func (r RollRequest) validate() error {
	switch {
	case r.Fleet != nil && (r.Cluster != "" || len(r.Nodegroups) > 0):
		return errors.New("a fleet roll takes every cluster of its regions: name no cluster and no nodegroup, and select with a filter")
	case r.Fleet == nil && r.Cluster == "":
		return errors.New("no cluster to roll the nodegroups of")
	case r.Select != nil && r.Select.Match == nil:
		return errors.New("the selection of nodegroups has no match function")
	}
	return nil
}

// NoMatchError is the error of a roll of one cluster whose selection
// matches no nodegroup. A selection that selects nothing is a mistake to
// report, and not a run with nothing to do.
type NoMatchError struct {
	Target
	// Selection is the text of the selection.
	Selection string
	// Names are the nodegroups that the cluster has.
	Names []string
}

func (e *NoMatchError) Error() string {
	have := "the cluster has no nodegroup"
	if len(e.Names) > 0 {
		have = "its nodegroups are " + strings.Join(e.Names, ", ")
	}
	return fmt.Sprintf("no nodegroup of cluster %s matches %s; %s", e.Cluster, e.Selection, have)
}

// planRoll builds the plan of a roll: of one cluster, or of a fleet.
func planRoll(ctx context.Context, d Deps, req RollRequest) (*Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if req.Fleet != nil {
		return planFleet(ctx, d, req)
	}
	cp, err := planRollCluster(ctx, d, req, req.Target)
	if err != nil {
		return nil, err
	}
	return &Plan{Kind: NodegroupUpdate, Request: req, Clusters: []ClusterPlan{*cp}, At: d.now()}, nil
}

// planRollCluster builds the plan of the roll of one cluster, in the
// order of every plan:
//
//  1. One read of the whole cluster, with the AMIs and the instance IDs.
//  2. The step of every target, from DecideAMIUpdate.
//  3. The update in flight of each target that is already changing.
//  4. The busy rule, with every step as a step of the run.
//  5. The health gate over all the nodegroups that would be rolled, and
//     beside it the instance-type offerings. See gateRoll.
//
// The error is non-nil when there is no plan: the cluster could not be
// read, a name is not a nodegroup of it, or a selection of one cluster
// matches nothing.
func planRollCluster(ctx context.Context, d Deps, req RollRequest, t Target) (*ClusterPlan, error) {
	clients := d.AWS.Clients(t.Region)
	snap, err := d.Fetch.Fetch(ctx, clients, t.Cluster, fleet.Need{Nodegroups: true, Addons: true, AMI: true, InstanceIDs: true})
	if err != nil {
		return nil, &ClusterReadError{Target: Target{Region: clients.Region, Cluster: t.Cluster}, Err: err}
	}
	cp := &ClusterPlan{
		Target:   Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name},
		Failures: slices.Clone(snap.Failures),
		snapshot: snap,
	}
	names, err := rollTargets(snap, req)
	if err != nil {
		return nil, err
	}
	if req.Select != nil && len(names) > 0 {
		// The selection is by pattern, so the plan says what it took.
		cp.Notices = append(cp.Notices, req.Select.Text+" selects the nodegroups "+strings.Join(names, ", "))
	}
	phase := Phase{Ref: PhaseRef{Target: cp.Target, Kind: PhaseRoll}, Batch: true}
	for _, name := range names {
		phase.Steps = append(phase.Steps, cp.rollStep(phase.Ref, name, req))
	}
	cp.markBusy(ctx, clients, phase.Steps)
	cp.Phases = []Phase{phase}

	refuse, notices := Busy(snap, NodegroupUpdate, phase.Steps)
	cp.Blockers = append(cp.Blockers, refuse...)
	cp.Notices = append(cp.Notices, notices...)
	// A plan that the busy rule refuses runs no gate: nothing would be
	// submitted, so its reads would be for nothing.
	if len(refuse) == 0 {
		cp.gateRoll(ctx, d, req.Policy)
	}
	return cp, nil
}

// rollTargets returns the nodegroups that the request targets, in the
// order of their steps: the names as given, or every nodegroup in the
// order of the snapshot, less the ones that the selection does not match.
// A name that is not a nodegroup of the cluster is an error that lists
// the close matches. A selection that matches nothing is an error for one
// cluster, and no target for a cluster of a fleet.
func rollTargets(snap *fleet.Snapshot, req RollRequest) ([]string, error) {
	t := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	all := make([]string, len(snap.Nodegroups))
	for i, n := range snap.Nodegroups {
		all[i] = n.Name
	}
	// Without the list, nothing shows which names exist. The busy rule
	// blocks the plan for the list that was not read.
	listed := snap.Have.Nodegroups == fleet.Collected || snap.Have.Nodegroups == fleet.Partial
	names := all
	if len(req.Nodegroups) > 0 {
		names = nil
		for _, name := range req.Nodegroups {
			if listed && !slices.Contains(all, name) {
				return nil, &NotFoundError{Target: t, Kind: StepNodegroup, Name: name, Names: all}
			}
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	if req.Select == nil {
		return names, nil
	}
	var selected []string
	for _, name := range names {
		i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
		// A nodegroup that was not read cannot be matched. It stays a
		// target, and its step blocks the plan: leaving it out would hide
		// that the selection could not be applied.
		if i < 0 || snap.Nodegroups[i].State != fleet.Collected || req.Select.Match(snap.Nodegroups[i]) {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 && listed && req.Fleet == nil {
		return nil, &NoMatchError{Target: t, Selection: req.Select.Text, Names: all}
	}
	return selected, nil
}

// rollStep returns the step of one nodegroup, and records its notice and
// its blocker in the plan. markBusy completes a step whose nodegroup is
// already changing.
//
// The step pins the Kubernetes version of the nodegroup itself: From and
// To are both that version, so a roll never moves a nodegroup to another
// minor. FromRelease is the AMI release version that the nodegroup runs,
// for the fresh read before the submit. FromImage is the ID of the AMI
// that its nodes run, and ToImage the ID of the latest recommended AMI,
// both as the snapshot read them. Force is --force. The four are set only
// on a step that submits.
//
// Two rows come after the table of DecideAMIUpdate, for a step that it
// would submit. A nodegroup with no Kubernetes version cannot be pinned:
// Blocked, with an Unread blocker. A nodegroup that is not ACTIVE is not
// rolled, whatever the flags: Blocked, with a NotReady blocker. The fresh
// read before the submit applies both again.
func (cp *ClusterPlan) rollStep(phase PhaseRef, name string, req RollRequest) Step {
	snap := cp.snapshot
	step := Step{Ref: StepRef{Phase: phase, Kind: StepNodegroup, Name: name}}
	i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
	if i < 0 || snap.Nodegroups[i].State != fleet.Collected {
		step.Action = Blocked
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: "could not read nodegroup " + name})
		return step
	}
	n := snap.Nodegroups[i]
	d := DecideAMIUpdate(n, req.Reroll)
	step.Action, step.Reason = d.Action, d.Reason
	step.From, step.To = n.Version.String(), n.Version.String()
	switch {
	case d.Action == Manual:
		cp.Notices = append(cp.Notices, "nodegroup "+name+" has a custom AMI (AMI type CUSTOM) and is not rolled; publish a new launch template version to roll it")
	case d.Action != Submit:
	case n.Version.IsZero():
		step.Action, step.Reason = Blocked, ""
		msg := "EKS did not return the Kubernetes version of nodegroup " + name + ", so its roll cannot be pinned to it"
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: msg})
		f := apidoc.NewFailure(apidoc.FailureNodegroup, name, apidoc.ReasonUnknown, msg)
		f.Cluster, f.Region = cp.Cluster, cp.Region
		cp.Failures = append(cp.Failures, f)
	case n.Status != "ACTIVE":
		step.Action, step.Reason = Blocked, ""
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerNotReady, Step: &step.Ref, Message: notRollable(name, n.Status)})
	default:
		step.Force = req.Policy.Force
		step.FromRelease = n.ReleaseVersion
		if n.AMI.State == fleet.Collected {
			step.FromImage, step.ToImage = n.AMI.Current, n.AMI.Latest
		}
	}
	return step
}

// notRollable is the message of a roll target that is not ACTIVE and not
// changing. The contract blocks a change of such a target, whatever the
// flags.
func notRollable(nodegroup, status string) string {
	return fmt.Sprintf("nodegroup %s is %s, not ACTIVE; it was not rolled", nodegroup, status)
}
