package change

import (
	"fmt"
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// UpgradeInputs is what PlanUpgrade reads beside the snapshot. The
// planner makes no call of its own: its caller reads from AWS inside the
// three functions, and PlanUpgrade calls each one only for what the plan
// needs, in the order of the plan.
//
// The planner has no context. A read that fails because the context of
// the caller ended looks like any other failed read here, and blocks a
// step: the caller checks its context after PlanUpgrade returns, and an
// interrupted plan is an error, not a blocked upgrade.
type UpgradeInputs struct {
	// Now is the time of the plan.
	Now time.Time
	// Version says whether EKS offers a Kubernetes version: one
	// DescribeClusterVersions call. It is called once, for the target.
	Version func(v fleet.Version) VersionOffer
	// AddonVersions returns the versions of an add-on that a Kubernetes
	// version can run, newest first, and none when EKS publishes none:
	// fleet.Fetcher.AddonVersions. It is called for the live version and
	// for each hop, and never for an add-on that the request skips. One
	// plan can ask twice for the same add-on and version.
	AddonVersions func(addon string, kubernetes fleet.Version) ([]string, error)
	// Insights returns the UPGRADE_READINESS insights of the cluster. A
	// real run starts an insights refresh in it and waits for it; a dry
	// run reads only. It is called at most once, for the hop after the
	// live version, and not at all with SkipInsights, when the control
	// plane does not move, or when the kubelet skew blocks that hop.
	Insights func(hop fleet.Version) InsightsRead
	// Command holds the words around the commands that a notice names.
	Command FollowUp
}

// PlanUpgrade derives the plan of a cluster upgrade from a snapshot with
// the nodegroups and the add-ons of the cluster. It is also how a run
// resumes: a step whose goal the live cluster already meets is Skip, so
// the same request after a stopped run, or after a finished one, plans
// only what remains. There is no state file.
//
// The plan has one hop for each minor version up to the target. A hop is
// the phases ControlPlane, AddonsBefore, Nodegroups, AddonsAfter, with
// the hop target in the ref of each: the add-ons that the new control
// plane cannot run go before the rolls, and the rest after them. A hop at
// the live version comes first when work at that version must finish
// before the control plane moves: see catchUp. A request for the version
// that the control plane runs is that one hop alone.
//
// The plan has no busy rule and no health gate applied: the caller runs
// Busy and the gate over its steps. A blocker here is about the request
// and the versions: the readiness of a hop, an add-on with no usable
// version, a nodegroup that cannot roll.
//
// The error is a RefusalError for a request that cannot be planned, and
// a NotFoundError for a nodegroup or an add-on name that the cluster does
// not have.
func PlanUpgrade(snap *fleet.Snapshot, req UpgradeRequest, in UpgradeInputs) (*Plan, error) {
	target := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	live := snap.Cluster.Version
	if snap.Have.Cluster != fleet.Collected || live.IsZero() {
		return nil, &RefusalError{Reason: RefusalVersionUnknown, Message: "the Kubernetes version of cluster " + target.Cluster + " is not known"}
	}
	hops, err := Hops(live, req.To)
	if err != nil {
		return nil, err
	}
	if err := checkScope(req, target.Cluster, live, len(hops)); err != nil {
		return nil, err
	}
	if len(hops) == 0 {
		// The control plane is at the target. One hop at that version
		// still catches up the add-ons and the nodegroups that trail it.
		hops = []fleet.Version{req.To}
	}
	p := &upgradePlanner{
		snap: snap, req: req, in: in, live: live,
		cp: &ClusterPlan{Target: target, From: live, To: req.To, Failures: slices.Clone(snap.Failures), snapshot: snap},
	}
	switch offer := in.Version(req.To); {
	case offer.Err != nil:
		// The plan goes on: an account may lack the permission, and EKS
		// still refuses a version that it does not offer.
		p.fail(apidoc.FailureCluster, target.Cluster, offer.Err)
	case !offer.Offered:
		return nil, &RefusalError{Reason: RefusalNotOffered, Message: fmt.Sprintf("version %s is not offered by EKS", req.To)}
	}
	if err := p.checkTargets(); err != nil {
		return nil, err
	}
	p.simulate()

	var lag addonLag
	if hops[0] != live {
		lag = p.catchUp(hops[0])
	}
	for _, hop := range hops {
		p.hop(hop)
	}
	p.applyScope()
	p.blockOnLaggingAddons(lag)
	p.otherComputeNotice()
	return &Plan{Kind: ClusterUpgrade, Request: req, Clusters: []ClusterPlan{*p.cp}, At: in.Now}, nil
}

// upgradePlanner holds one run of PlanUpgrade.
type upgradePlanner struct {
	snap *fleet.Snapshot
	req  UpgradeRequest
	in   UpgradeInputs
	cp   *ClusterPlan
	// live is the version that the control plane runs.
	live fleet.Version
	// addons are the installed add-ons in dependency order.
	addons []fleet.Addon
	// simNodegroups and simAddons are the versions that the plan
	// predicts, hop by hop: a later hop is planned against where the
	// cluster will be, while a step is Skip from live state alone.
	simNodegroups map[string]fleet.Version
	simAddons     map[string]string
}

// otherComputeNotice says, when the plan moves the control plane, that
// the compute outside the managed nodegroups stays at the old version, as
// the notice of a rollback plan does for its direction.
func (p *upgradePlanner) otherComputeNotice() {
	moves := slices.ContainsFunc(p.cp.Steps(), func(s Step) bool {
		return s.Ref.Kind == StepControlPlane && (s.Action == Submit || s.Action == Attach)
	})
	if !moves {
		return
	}
	p.notice("self-managed and hybrid nodes are not upgraded by EKS or refresh: move them to %s yourself after the control plane upgrades. Fargate pods stay at %s: delete them after the upgrade, and they come back at %s",
		p.req.To, p.live, p.req.To)
}

func (p *upgradePlanner) notice(format string, args ...any) {
	p.cp.Notices = append(p.cp.Notices, fmt.Sprintf(format, args...))
}

func (p *upgradePlanner) block(kind BlockerKind, step *StepRef, format string, args ...any) {
	p.cp.Blockers = append(p.cp.Blockers, Blocker{Kind: kind, Step: step, Message: fmt.Sprintf(format, args...)})
}

// fail records a read that failed, once.
func (p *upgradePlanner) fail(kind apidoc.FailureKind, name string, err error) {
	f := fleet.FailureFromError(kind, name, err)
	f.Region = p.cp.Region
	if kind != apidoc.FailureCluster {
		f.Cluster = p.cp.Cluster
	}
	p.cp.Failures = appendFailure(p.cp.Failures, f)
}

// appendFailure adds a failure that the list does not have yet. Two hops
// can fail the same read.
func appendFailure(list []apidoc.Failure, f apidoc.Failure) []apidoc.Failure {
	if slices.Contains(list, f) {
		return list
	}
	return append(list, f)
}

// checkTargets refuses a name of -n, --skip-addon, or --skip-nodegroup
// that the cluster does not have. A list that was not read cannot show
// which names exist; the busy rule blocks the plan for it.
func (p *upgradePlanner) checkTargets() error {
	listed := func(s fleet.State) bool { return s == fleet.Collected || s == fleet.Partial }
	if listed(p.snap.Have.Nodegroups) {
		have := make([]string, len(p.snap.Nodegroups))
		for i, n := range p.snap.Nodegroups {
			have[i] = n.Name
		}
		if err := checkExists(p.cp.Target, StepNodegroup, slices.Concat(p.req.Nodegroups, p.req.SkipNodegroups), have); err != nil {
			return err
		}
	}
	if listed(p.snap.Have.Addons) {
		have := make([]string, len(p.snap.Addons))
		for i, a := range p.snap.Addons {
			have[i] = a.Name
		}
		return checkExists(p.cp.Target, StepAddon, p.req.SkipAddons, have)
	}
	return nil
}

// simulate orders the add-ons and starts the predicted versions at the
// live ones.
func (p *upgradePlanner) simulate() {
	names := make([]string, len(p.snap.Addons))
	byName := make(map[string]fleet.Addon, len(p.snap.Addons))
	p.simAddons = make(map[string]string, len(p.snap.Addons))
	for i, a := range p.snap.Addons {
		names[i], byName[a.Name], p.simAddons[a.Name] = a.Name, a, a.Version
	}
	for _, name := range sortByDependency(names) {
		p.addons = append(p.addons, byName[name])
	}
	p.simNodegroups = make(map[string]fleet.Version, len(p.snap.Nodegroups))
	for _, n := range p.snap.Nodegroups {
		p.simNodegroups[n.Name] = n.Version
	}
}

// rollable returns the nodegroups that the plan may roll: the ones that
// -n names, or all of them.
func (p *upgradePlanner) rollable() []fleet.Nodegroup {
	if len(p.req.Nodegroups) == 0 {
		return p.snap.Nodegroups
	}
	var out []fleet.Nodegroup
	for _, n := range p.snap.Nodegroups {
		if slices.Contains(p.req.Nodegroups, n.Name) {
			out = append(out, n)
		}
	}
	return out
}

func (p *upgradePlanner) skipsNodegroup(name string) bool {
	return slices.Contains(p.req.SkipNodegroups, name)
}

func (p *upgradePlanner) skipsAddon(name string) bool {
	return slices.Contains(p.req.SkipAddons, name)
}

// advance moves the predicted version of each nodegroup that a hop rolls
// up to a version. A custom-AMI nodegroup and a skipped one stay where
// they are.
func (p *upgradePlanner) advance(nodegroups []fleet.Nodegroup, to fleet.Version) {
	for _, n := range nodegroups {
		if n.AMIType != customAMIType && !p.skipsNodegroup(n.Name) && !atLeast(p.simNodegroups[n.Name], to) {
			p.simNodegroups[n.Name] = to
		}
	}
}

// phase adds a phase of a hop to the plan. A phase with no step is left
// out.
func (p *upgradePlanner) phase(kind PhaseKind, hop fleet.Version, steps []Step) {
	if len(steps) == 0 {
		return
	}
	ref := PhaseRef{Target: p.cp.Target, Kind: kind, Hop: hop}
	for i := range steps {
		steps[i].Ref.Phase = ref
	}
	p.cp.Phases = append(p.cp.Phases, Phase{Ref: ref, Steps: steps})
}

// hop plans one minor version: the readiness of the hop, the control
// plane, the add-ons that the new control plane cannot run, every
// nodegroup, and the other add-ons.
func (p *upgradePlanner) hop(to fleet.Version) {
	r := hopReadiness(p.snap, to, p.simNodegroups, p.req.SkipInsights, p.in.Insights)
	if r.Blocker != nil {
		p.cp.Blockers = append(p.cp.Blockers, *r.Blocker)
	}
	p.cp.Notices = append(p.cp.Notices, r.Notices...)
	for _, f := range r.Failures {
		p.cp.Failures = appendFailure(p.cp.Failures, f)
	}
	p.phase(PhaseControlPlane, to, []Step{p.controlPlaneStep(to)})
	before, after := p.addonSteps(to)
	p.phase(PhaseAddonsBefore, to, before)
	p.phase(PhaseNodegroups, to, p.nodegroupSteps(p.snap.Nodegroups, to, VersionBehind))
	p.phase(PhaseAddonsAfter, to, after)
	// After the hop, the nodegroups that the plan rolls are at its
	// target, unless --only leaves the nodegroups out.
	if p.req.includes(PartNodegroups) {
		p.advance(p.rollable(), to)
	}
}
