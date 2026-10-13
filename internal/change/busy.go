package change

import (
	"slices"
	"strings"

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
