package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube"
)

// ValidateSizes checks the sizes of a scale against each other, before
// anything is read. A nil size was not given. At least one size is needed,
// none may be negative, the max size is 1 or more as EKS requires, and the
// ones that were given must hold min <= desired <= max.
func ValidateSizes(desired, minSize, maxSize *int32) error {
	if desired == nil && minSize == nil && maxSize == nil {
		return errors.New("give at least one of --desired, --min, --max")
	}
	for _, s := range []struct {
		flag string
		size *int32
	}{{"--desired", desired}, {"--min", minSize}, {"--max", maxSize}} {
		if s.size != nil && *s.size < 0 {
			return fmt.Errorf("%s must be 0 or more", s.flag)
		}
	}
	switch {
	case maxSize != nil && *maxSize < 1:
		return errors.New("--max must be 1 or more")
	case minSize != nil && maxSize != nil && *minSize > *maxSize:
		return fmt.Errorf("--min %d is above --max %d", *minSize, *maxSize)
	case desired != nil && minSize != nil && *desired < *minSize:
		return fmt.Errorf("--desired %d is below --min %d", *desired, *minSize)
	case desired != nil && maxSize != nil && *desired > *maxSize:
		return fmt.Errorf("--desired %d is above --max %d", *desired, *maxSize)
	}
	return nil
}

// validate rejects a request with no cluster, no nodegroup, or sizes that
// cannot hold among themselves.
func (r ScaleRequest) validate() error {
	switch {
	case r.Cluster == "":
		return errors.New("no cluster to scale a nodegroup of")
	case r.Nodegroup == "":
		return errors.New("no nodegroup to scale")
	}
	return ValidateSizes(r.Desired, r.Min, r.Max)
}

// resolve returns the sizes that the nodegroup has after the scale: the
// sizes that the request gives, and the current ones for the rest. It
// returns an error for sizes that cannot hold, so nothing is sent for
// them.
//
// A lone --min or --max that excludes the current desired size is an
// error that says to pass --desired too: EKS does not document that it
// moves the desired size into new bounds, so the node count is always the
// choice of the user.
func (r ScaleRequest) resolve(cur Sizes) (Sizes, error) {
	after := cur
	desired, lowest, highest := "the current desired size", "the current min size", "the current max size"
	if r.Desired != nil {
		after.Desired, desired = *r.Desired, "--desired"
	}
	if r.Min != nil {
		after.Min, lowest = *r.Min, "--min"
	}
	if r.Max != nil {
		after.Max, highest = *r.Max, "--max"
	}
	// hint names the flag that would make a size fit, when it was not
	// given.
	hint := func(given *int32, flag string) string {
		if given != nil {
			return ""
		}
		return "; pass " + flag + " too"
	}
	switch {
	case r.Desired == nil && after.Max < cur.Desired:
		return Sizes{}, fmt.Errorf("--max %d is below %s %d; pass --desired too", after.Max, desired, cur.Desired)
	case r.Desired == nil && after.Min > cur.Desired:
		return Sizes{}, fmt.Errorf("--min %d is above %s %d; pass --desired too", after.Min, desired, cur.Desired)
	case after.Min > after.Max && r.Min != nil:
		return Sizes{}, fmt.Errorf("--min %d is above %s %d%s", after.Min, highest, after.Max, hint(r.Max, "--max"))
	case after.Min > after.Max:
		return Sizes{}, fmt.Errorf("--max %d is below %s %d; pass --min too", after.Max, lowest, after.Min)
	case after.Desired < after.Min:
		return Sizes{}, fmt.Errorf("%s %d is below %s %d%s", desired, after.Desired, lowest, after.Min, hint(r.Min, "--min"))
	case after.Desired > after.Max:
		return Sizes{}, fmt.Errorf("%s %d is above %s %d%s", desired, after.Desired, highest, after.Max, hint(r.Max, "--max"))
	}
	return after, nil
}

// nodegroupInFlight reports whether EKS is changing a nodegroup with the
// status. A nodegroup that is DEGRADED, or whose create or delete failed,
// is not changing: nothing will settle it, so there is nothing to wait
// for.
func nodegroupInFlight(status string) bool {
	return status == "CREATING" || status == "UPDATING" || status == "DELETING"
}

// notActive is the message of a scale target that is not ACTIVE and not
// changing. The contract blocks a change of such a target, whatever the
// direction of the scale and whatever the flags.
func notActive(nodegroup, status string) string {
	return fmt.Sprintf("nodegroup %s is %s, not ACTIVE; it was not scaled", nodegroup, status)
}

// scaleDecision is what the step table of a scale says about a nodegroup.
type scaleDecision struct {
	Action Action
	Reason Reason
	Sizes  ScaleSizes
	// NotActive is the message of the blocker of a Blocked step.
	NotActive string
	// Err fails the whole plan: the sizes cannot hold.
	Err error
}

// decideScale is the step table of a scale. The first row that matches
// decides.
//
//  1. The nodegroup is already changing (CREATING, UPDATING, DELETING):
//     Skip, InFlight. The target is busy, and its sizes are not settled,
//     so they are not checked.
//  2. The nodegroup is not ACTIVE (DEGRADED, CREATE_FAILED,
//     DELETE_FAILED): Blocked, for a scale in any direction and for the
//     bounds alone. No flag goes past it.
//  3. The resulting sizes cannot hold: the plan fails.
//  4. Every resulting size equals the current one: Skip, NoChange.
//  5. Otherwise: Submit.
//
// The fresh read before the submit applies rows 1 and 2 again.
func decideScale(n fleet.Nodegroup, req ScaleRequest) scaleDecision {
	cur := Sizes{Desired: n.Desired, Min: n.Min, Max: n.Max}
	switch {
	case nodegroupInFlight(n.Status):
		return scaleDecision{Action: Skip, Reason: InFlight, Sizes: ScaleSizes{Before: cur, After: cur}}
	case n.Status != "ACTIVE":
		return scaleDecision{Action: Blocked, Sizes: ScaleSizes{Before: cur, After: cur}, NotActive: notActive(n.Name, n.Status)}
	}
	after, err := req.resolve(cur)
	switch {
	case err != nil:
		return scaleDecision{Err: err}
	case after == cur:
		return scaleDecision{Action: Skip, Reason: NoChange, Sizes: ScaleSizes{Before: cur, After: cur}}
	}
	return scaleDecision{Action: Submit, Sizes: ScaleSizes{Before: cur, After: after}}
}

// planScale builds the plan of a scale: one read of the whole cluster, the
// step of the nodegroup, the busy rule, and the gate of the direction. A
// target that is not ACTIVE blocks the plan in every direction, in the
// step table. A scale that lowers the desired size runs the node check
// and the scale-down drain check. Any other scale runs the quota check,
// and a scale-up also looks for instance types that an availability zone
// of the nodegroup does not offer.
func planScale(ctx context.Context, d Deps, req ScaleRequest) (*Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	clients := d.AWS.Clients(req.Region)
	snap, err := d.Fetch.Fetch(ctx, clients, req.Cluster, fleet.Need{Nodegroups: true, Addons: true})
	if err != nil {
		return nil, &ClusterReadError{Target: Target{Region: clients.Region, Cluster: req.Cluster}, Err: err}
	}
	cp := ClusterPlan{
		Target:   Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name},
		Failures: slices.Clone(snap.Failures),
		snapshot: snap,
	}
	phase := Phase{Ref: PhaseRef{Target: cp.Target, Kind: PhaseScale}}
	step, err := cp.scaleStep(phase.Ref, req)
	if err != nil {
		return nil, err
	}
	phase.Steps = []Step{step}
	cp.Phases = []Phase{phase}

	refuse, notices := Busy(snap, NodegroupScale, phase.Steps)
	cp.Blockers = append(cp.Blockers, refuse...)
	cp.Notices = append(cp.Notices, notices...)
	cp.gateScale(ctx, d, req.Policy, step)
	return &Plan{Kind: NodegroupScale, Request: req, Clusters: []ClusterPlan{cp}, At: d.now()}, nil
}

// scaleStep returns the step of the nodegroup, and records its blocker or
// its busy failure in the plan. A name that is not a nodegroup of the
// cluster is an error that lists the close matches.
func (cp *ClusterPlan) scaleStep(phase PhaseRef, req ScaleRequest) (Step, error) {
	snap := cp.snapshot
	step := Step{Ref: StepRef{Phase: phase, Kind: StepScale, Name: req.Nodegroup}}
	i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == req.Nodegroup })
	// Without the list, nothing shows which names exist. The busy rule
	// blocks the plan for the list that was not read.
	listed := snap.Have.Nodegroups == fleet.Collected || snap.Have.Nodegroups == fleet.Partial
	if i < 0 && listed {
		names := make([]string, len(snap.Nodegroups))
		for j, n := range snap.Nodegroups {
			names[j] = n.Name
		}
		return Step{}, &NotFoundError{Target: cp.Target, Kind: StepNodegroup, Name: req.Nodegroup, Names: names}
	}
	if i < 0 || snap.Nodegroups[i].State != fleet.Collected {
		step.Action = Blocked
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: "could not read nodegroup " + req.Nodegroup})
		return step, nil
	}
	n := snap.Nodegroups[i]
	// A size that EKS did not return is not 0. Nothing can be planned
	// from sizes that are not known.
	if !n.SizesKnown {
		step.Action = Blocked
		msg := "EKS did not return every scaling size of nodegroup " + req.Nodegroup
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: msg})
		f := apidoc.NewFailure(apidoc.FailureNodegroup, req.Nodegroup, apidoc.ReasonUnknown, msg)
		f.Cluster, f.Region = cp.Cluster, cp.Region
		cp.Failures = append(cp.Failures, f)
		return step, nil
	}
	d := decideScale(n, req)
	if d.Err != nil {
		return Step{}, d.Err
	}
	step.Action, step.Reason, step.Sizes = d.Action, d.Reason, &d.Sizes
	if d.NotActive != "" {
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerNotReady, Step: &step.Ref, Message: d.NotActive})
	}
	if step.busy() {
		cp.Failures = append(cp.Failures, cp.busyNodegroupFailure(n.Name, n.Status, ""))
	}
	return step, nil
}

// busyNodegroupFailure returns the failure of a nodegroup target that is
// already changing. inFlight names the update: its type, or
// unknownUpdate. It is "" in a plan, which makes no lookup.
func (cp *ClusterPlan) busyNodegroupFailure(name, status, inFlight string) apidoc.Failure {
	msg := fmt.Sprintf("nodegroup %s is %s", name, status)
	if inFlight != "" {
		msg += " (" + inFlight + ")"
	}
	f := apidoc.NewFailure(apidoc.FailureNodegroup, name, apidoc.ReasonBusy, msg+"; it was not changed. Run the command again once that finishes")
	f.Cluster, f.Region = cp.Cluster, cp.Region
	return f
}

// gateScale runs the health gate of a scale step that would be submitted.
// The step table has blocked a target that is not ACTIVE before it, for
// every direction. The direction picks the checks. A scale that removes
// nodes reads the nodes and the PodDisruptionBudgets, so it needs
// Kubernetes access. Any other scale asks nothing of Kubernetes. Beside the gate, a scale-up
// reads the instance-type offerings, and what it finds is a notice.
func (cp *ClusterPlan) gateScale(ctx context.Context, d Deps, policy health.Policy, s Step) {
	if s.Action != Submit {
		return
	}
	i := slices.IndexFunc(cp.snapshot.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == s.Ref.Name })
	clients := d.AWS.Clients(cp.Region)
	// The offerings are read while the checks run, and joined before the
	// plan is returned.
	var (
		wg      sync.WaitGroup
		offered string
	)
	if s.Sizes.After.Desired > s.Sizes.Before.Desired {
		wg.Go(func() { offered = availabilityNotice(ctx, clients, cp.snapshot.Nodegroups[i]) })
	}
	defer func() {
		wg.Wait()
		if offered != "" {
			cp.Notices = append(cp.Notices, offered)
		}
	}()
	if policy.Skip {
		cp.Gate = health.Gate(nil, policy)
		cp.Notices = append(cp.Notices, "the health checks were skipped (--skip-health-check)")
		return
	}
	in := &health.Input{Snapshot: cp.snapshot, Targets: []string{s.Ref.Name}, Clients: clients, Now: cp.snapshot.FetchedAt}
	checks := []health.Check{health.Quotas()}
	if remove := s.Sizes.Before.Desired - s.Sizes.After.Desired; remove > 0 {
		checks = []health.Check{health.Nodes(), health.ScaleDownBlockers(s.Ref.Name, remove)}
		in.Kube, in.Metrics, in.NoKube = kubeAccess(d, cp.snapshot)
	}
	cp.Checks = health.Run(ctx, checks, in)
	cp.Gate = health.Gate(cp.Checks, policy)
}

// kubeAccess returns the Kubernetes clients of the cluster of snap, or
// why there are none, in the words of a message. It makes no request.
func kubeAccess(d Deps, snap *fleet.Snapshot) (kubernetes.Interface, metricsv1beta1.NodeMetricsInterface, string) {
	if d.Kube == nil {
		return nil, nil, "no Kubernetes client"
	}
	client, err := d.Kube(kube.Target{Name: snap.Cluster.Name, Region: snap.Cluster.Region, Endpoint: snap.Cluster.Endpoint})
	if err != nil {
		return nil, nil, err.Error()
	}
	return client.Clientset, client.Metrics, ""
}
