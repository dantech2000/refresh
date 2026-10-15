package change

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// Busy applies the busy rule to a snapshot and returns the refusals and
// the notices. ours are the steps of the caller: the steps of the whole
// plan in the plan step, and the steps of a phase before that phase.
//
// In flight means a cluster that is not ACTIVE, a nodegroup that is
// neither ACTIVE nor DEGRADED, and an add-on that is CREATING, UPDATING,
// or DELETING.
//
// For nodegroup update, nodegroup scale, and addon update: anything in
// flight that is not a target refuses the run. A target in flight does
// not: its own step attaches to it, reports it as busy, or is blocked
// with a blocker of its own.
//
// For cluster upgrade and cluster rollback: a cluster in flight refuses,
// unless it is UPDATING and the control-plane step attaches. A target
// that EKS is deleting refuses. Anything else in flight that is not a
// target is a notice, and the run goes on.
//
// A cluster, a nodegroup list, or an add-on list that was not read in
// full cannot show that nothing is changing: an Unread blocker.
func Busy(snap *fleet.Snapshot, kind Kind, ours []Step) (refuse []Blocker, notices []string) {
	cluster := snap.Cluster.Name
	for _, g := range []struct {
		state fleet.State
		what  string
	}{
		{snap.Have.Cluster, "the cluster"}, {snap.Have.Nodegroups, "every nodegroup"}, {snap.Have.Addons, "every add-on"},
	} {
		if g.state != fleet.Collected {
			refuse = append(refuse, Blocker{Kind: BlockerUnread, Message: "could not read " + g.what + " of " + cluster + ", so nothing shows that no change is running"})
		}
	}
	mine := func(k StepKind, name string) (Step, bool) {
		i := slices.IndexFunc(ours, func(s Step) bool {
			sameKind := s.Ref.Kind == k || (k == StepNodegroup && s.Ref.Kind == StepScale)
			// A Blocked step has its own blocker, which names the target.
			return sameKind && s.Ref.Name == name && (s.work() || s.busy() || s.Action == Blocked)
		})
		if i < 0 {
			return Step{}, false
		}
		return ours[i], true
	}
	engine := !kind.independent()
	var busy, deleting []string
	if snap.Have.Cluster == fleet.Collected && snap.Cluster.Status != "ACTIVE" {
		step, ok := mine(StepControlPlane, "")
		if !engine || !ok || step.Action != Attach || snap.Cluster.Status != "UPDATING" {
			busy = append(busy, "cluster "+snap.Cluster.Status)
		}
	}
	other := func(k StepKind, noun, name, status string) {
		_, ok := mine(k, name)
		switch {
		case ok && engine && status == "DELETING":
			deleting = append(deleting, noun+" "+name+" "+status)
		case ok:
		case engine:
			notices = append(notices, noun+" "+name+" is "+status+"; the run does not wait for it")
		default:
			busy = append(busy, noun+" "+name+" "+status)
		}
	}
	for _, n := range snap.Nodegroups {
		if n.State == fleet.Collected && n.Status != "ACTIVE" && n.Status != "DEGRADED" {
			other(StepNodegroup, "nodegroup", n.Name, n.Status)
		}
	}
	for _, a := range snap.Addons {
		if a.State == fleet.Collected && addonInFlight(a.Status) {
			other(StepAddon, "add-on", a.Name, a.Status)
		}
	}
	if all := slices.Concat(busy, deleting); len(all) > 0 {
		refuse = append(refuse, Blocker{Kind: BlockerBusy, Message: cluster + " is busy (" + strings.Join(all, ", ") + "); nothing was started. Run it again once that finishes"})
	}
	return refuse, notices
}

// addonInFlight reports whether EKS is changing an add-on with the
// status.
func addonInFlight(status string) bool {
	return status == "CREATING" || status == "UPDATING" || status == "DELETING"
}

// markBusy completes the steps of the targets that are already changing.
// For each it looks up the update in flight, sets its type on the step,
// and adds the Busy failure of the target to the plan. The lookup is a
// read, so the plan of a dry run has the type too.
func (cp *ClusterPlan) markBusy(ctx context.Context, c *awsx.Clients, steps []Step) {
	for i := range steps {
		s := &steps[i]
		if !s.busy() {
			continue
		}
		res := s.Ref.Resource()
		kind, note := lookupInFlight(ctx, c, res)
		s.InFlight = kind
		status, version := cp.statusOf(res)
		cp.Failures = append(cp.Failures, cp.busyFailure(res, status, version, note))
	}
}

// statusOf returns the status of a target in the snapshot of the plan,
// and for an add-on the version that it reports.
func (cp *ClusterPlan) statusOf(res Resource) (status, version string) {
	if res.Kind == StepAddon {
		for _, a := range cp.snapshot.Addons {
			if a.Name == res.Name {
				return a.Status, a.Version
			}
		}
		return "", ""
	}
	for _, n := range cp.snapshot.Nodegroups {
		if n.Name == res.Name {
			return n.Status, ""
		}
	}
	return "", ""
}

// busyFailure returns the failure of a target that is already changing:
// an add-on or a nodegroup, of any kind of change. version is the version
// that an add-on reports, and "" for a nodegroup. inFlight names the
// update: its type, unknownUpdate when the lookup failed, or "" when EKS
// lists no update in progress.
func (cp *ClusterPlan) busyFailure(res Resource, status, version, inFlight string) apidoc.Failure {
	msg := res.String() + " is " + status
	if version != "" && status != "DELETING" {
		msg += " to " + version
	}
	if inFlight != "" {
		msg += " (" + inFlight + ")"
	}
	ref := StepRef{Kind: res.Kind, Name: res.Name}
	f := apidoc.NewFailure(resourceKind(ref), res.Name, apidoc.ReasonBusy, msg+"; it was not changed. Run the command again once that finishes")
	f.Cluster, f.Region = cp.Cluster, cp.Region
	return f
}

// busyTarget returns the result of a target that the fresh read before
// its submit shows changing, with the status of that read: it is left
// alone, and its failure names the type of the update in flight. It is
// the one place for a target that turned busy after the plan, for every
// standalone change.
func (r *runner) busyTarget(cp *ClusterPlan, res StepResult, c *awsx.Clients, resource Resource, status string) StepResult {
	res.Status = StepBusy
	var note string
	res.InFlight, note = lookupInFlight(r.readCtx, c, resource)
	_, version := cp.statusOf(resource)
	f := cp.busyFailure(resource, status, version, note)
	res.Failure = &f
	return res
}

// busyResult returns the result of a target that the plan found changing
// already: it is left alone. The plan looked up the update in flight, so
// the step has its type and the plan has its failure, and the run reads
// nothing again.
func (cp *ClusterPlan) busyResult(s Step) StepResult {
	res := StepResult{Ref: s.Ref, Status: StepBusy, InFlight: s.InFlight}
	i := slices.IndexFunc(cp.Failures, func(f apidoc.Failure) bool {
		return f.Reason == apidoc.ReasonBusy && f.Kind == resourceKind(s.Ref) && f.Name == s.Ref.Name
	})
	if i >= 0 {
		f := cp.Failures[i]
		res.Failure = &f
	}
	return res
}

// decided returns the result of a step that the plan decided and that a
// run sends nothing for, and false for a step with work. A step that the
// plan blocked is Blocked, a target that is already changing has its Busy
// row, and every other step with no work is Skipped. It is the one mapping
// from the plan to these rows, for a batch and for a step that runs alone.
func (cp *ClusterPlan) decided(s Step) (StepResult, bool) {
	switch {
	case s.Action == Blocked:
		return StepResult{Ref: s.Ref, Status: StepBlocked}, true
	case s.busy():
		return cp.busyResult(s), true
	case !s.work():
		return StepResult{Ref: s.Ref, Status: StepSkipped}, true
	}
	return StepResult{}, false
}

// unknownUpdate is what a busy failure says when the update in flight
// could not be looked up.
const unknownUpdate = "an update of unknown type"

// lookupInFlight looks up the update that makes a target busy. It returns
// the type for the step, and the words for the failure: the type,
// unknownUpdate when the lookup failed, and "" when EKS lists no update
// in progress, as for a resource that it is deleting. The lookup is two
// reads that happen only for a busy target: in the plan, and at the fresh
// read before a submit that finds a target changing since the plan.
func lookupInFlight(ctx context.Context, c *awsx.Clients, res Resource) (kind, note string) {
	u, err := inFlightUpdate(ctx, c, res)
	if err != nil {
		return "", unknownUpdate
	}
	return u.Type, u.Type
}

// InFlightUpdate is an EKS update of a resource that is in progress.
type InFlightUpdate struct {
	ID string
	// Type is the EKS update type, such as VersionUpdate.
	Type      string
	CreatedAt time.Time
	// Version is the Kubernetes version that the update moves to: its
	// Version parameter, or "" when it has none.
	Version string
}

// inFlightUpdate returns the update of a resource that is in progress:
// its ID, its type, such as AddonUpdate, when it was created, and the
// version that it moves to. It
// returns the zero value when EKS lists none. EKS lists the update IDs
// with no status, so each one is described, newest first.
func inFlightUpdate(ctx context.Context, c *awsx.Clients, r Resource) (InFlightUpdate, error) {
	list := &eks.ListUpdatesInput{Name: aws.String(r.Cluster)}
	describe := &eks.DescribeUpdateInput{Name: aws.String(r.Cluster)}
	switch r.Kind {
	case StepAddon:
		list.AddonName, describe.AddonName = aws.String(r.Name), aws.String(r.Name)
	case StepNodegroup, StepScale:
		list.NodegroupName, describe.NodegroupName = aws.String(r.Name), aws.String(r.Name)
	}
	ids, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListUpdatesOutput, error) {
			list.NextToken = token
			return c.EKS.ListUpdates(ctx, list)
		},
		func(out *eks.ListUpdatesOutput) ([]string, *string) { return out.UpdateIds, out.NextToken },
	)
	if err != nil {
		return InFlightUpdate{}, err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		describe.UpdateId = aws.String(ids[i])
		out, err := c.EKS.DescribeUpdate(ctx, describe)
		switch {
		case err != nil:
			return InFlightUpdate{}, err
		case out.Update == nil:
			return InFlightUpdate{}, fmt.Errorf("EKS returned no update for %s", ids[i])
		case out.Update.Status == ekstypes.UpdateStatusInProgress:
			u := InFlightUpdate{ID: aws.ToString(out.Update.Id), Type: string(out.Update.Type), CreatedAt: aws.ToTime(out.Update.CreatedAt)}
			for _, p := range out.Update.Params {
				if p.Type == ekstypes.UpdateParamTypeVersion {
					u.Version = aws.ToString(p.Value)
				}
			}
			return u, nil
		}
	}
	return InFlightUpdate{}, nil
}
