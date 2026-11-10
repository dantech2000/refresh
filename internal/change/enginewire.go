package change

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// categoryRollbackReadiness is the insight category that a rollback
// reads.
const categoryRollbackReadiness = string(ekstypes.CategoryRollbackReadiness)

// engineNeed is what the plan and each phase of an upgrade or a rollback
// read of the cluster: the nodegroups and the add-ons. A rollback also
// reads the update history. instances reads the instance IDs of the
// nodegroups too, for a read whose snapshot goes to the health checks of
// a nodegroup: the Capacity and Quotas checks count the instances.
func engineNeed(history, instances bool) fleet.Need {
	return fleet.Need{Nodegroups: true, Addons: true, InstanceIDs: instances, Updates: history}
}

// planUpgrade builds the plan of a cluster upgrade. It reads the cluster
// once, hands the snapshot to PlanUpgrade with the reads that the planner
// asks for one by one, and then applies the busy rule and the health gate
// to the steps.
//
// The reads of the planner stay lazy. An add-on that the request skips is
// not looked up. The insights are read only for the hop after the live
// version, and only when the kubelet skew lets that hop through, so a hop
// that the skew blocks sends no insights refresh.
//
// With refresh, a real run, the insights are refreshed before they are
// read: the block decision needs fresh ones. It is the one request of the
// plan that is not a read. Without it, a dry run, the insights are read
// as they are.
func planUpgrade(ctx context.Context, d Deps, req UpgradeRequest, refresh *insightsRefresher) (*Plan, error) {
	if req.Cluster == "" {
		return nil, errors.New("no cluster to upgrade")
	}
	clients := d.AWS.Clients(req.Region)
	// The two plans share the catalogue reads: the catalogue is a fact
	// about EKS, and the refresh between them does not change it.
	cat := newCatalogue(d.Fetch, clients)
	var p *Plan
	for again := false; ; again = true {
		snap, err := d.Fetch.Fetch(ctx, clients, req.Cluster, engineNeed(false, true))
		if err != nil {
			return nil, &ClusterReadError{Target: Target{Region: clients.Region, Cluster: req.Cluster}, Err: err}
		}
		ran := 0
		if refresh != nil {
			ran = refresh.ran
		}
		p, err = PlanUpgrade(snap, req, upgradeInputs(ctx, d, clients, snap, req, refresh, cat))
		if err := planEnded(ctx, err); err != nil {
			return nil, err
		}
		// A refresh can take minutes, and the read of the cluster is from
		// before it. The plan that is shown, gated, and confirmed comes
		// from a read after the refresh: the cluster is read once more,
		// and the second plan reuses the refresh. A refresh that failed
		// is not tried a second time: the plan blocks on it.
		if refresh == nil || refresh.ran == ran || refresh.failed || again {
			break
		}
		d.Fetch = d.Fetch.Fresh()
	}
	p.Clusters[0].gateEngine(ctx, d, ClusterUpgrade, req.Policy)
	return p, planEnded(ctx, nil)
}

// planEnded returns the error of a plan whose context ended while the
// planner read: the planner has no context, so a read that the end of the
// run failed looks to it like any failed read, and would block a step. An
// interrupted plan is an error, and never a blocked change.
func planEnded(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("the plan was not finished: %w", context.Cause(ctx))
	}
	return err
}

// upgradeInputs returns the reads of PlanUpgrade and of HopReadiness
// against live AWS. The add-on catalogue is read through cat.
func upgradeInputs(ctx context.Context, d Deps, clients *awsx.Clients, snap *fleet.Snapshot, req UpgradeRequest, refresh *insightsRefresher, cat *catalogue) UpgradeInputs {
	t := Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}
	return UpgradeInputs{
		Now:           d.now(),
		Version:       versionOffer(ctx, clients),
		AddonVersions: cat.lookup(ctx, upgradeCatalogueKeys(snap, req)),
		Insights:      upgradeInsights(ctx, clients, t, snap.Cluster.Version.String(), refresh),
		Command:       d.FollowUp,
	}
}

// catalogueKey is one read of the add-on catalogue: an add-on at a
// Kubernetes version.
type catalogueKey struct {
	addon   string
	version fleet.Version
}

// catalogue holds the add-on catalogue reads of a plan. A planner asks
// for them one at a time, so its first ask reads every pair that the plan
// will ask for in one bounded fan-out, and the asks after it are answered
// from those reads. A pair that was not foreseen, and a pair whose read
// failed, is read when the planner asks for it: a failure is not kept.
type catalogue struct {
	fetch   *fleet.Fetcher
	clients *awsx.Clients
	reads   map[catalogueKey][]string
}

func newCatalogue(fetch *fleet.Fetcher, clients *awsx.Clients) *catalogue {
	return &catalogue{fetch: fetch, clients: clients, reads: map[catalogueKey][]string{}}
}

// lookup returns the AddonVersions read of one plan. keys returns the
// pairs that the plan will ask for, given the version of its first ask.
// The planner asks from one goroutine.
func (c *catalogue) lookup(ctx context.Context, keys func(first fleet.Version) []catalogueKey) func(string, fleet.Version) ([]string, error) {
	first := true
	return func(addon string, kubernetes fleet.Version) ([]string, error) {
		if first {
			first = false
			c.read(ctx, keys(kubernetes))
		}
		key := catalogueKey{addon: addon, version: kubernetes}
		if versions, ok := c.reads[key]; ok {
			return versions, nil
		}
		versions, err := c.fetch.AddonVersions(ctx, c.clients, addon, kubernetes)
		if err == nil {
			c.reads[key] = versions
		}
		return versions, err
	}
}

// read reads the pairs that c does not have yet, side by side, and keeps
// the ones that succeeded.
func (c *catalogue) read(ctx context.Context, keys []catalogueKey) {
	var missing []catalogueKey
	for _, k := range keys {
		if _, ok := c.reads[k]; !ok && !slices.Contains(missing, k) {
			missing = append(missing, k)
		}
	}
	type result struct {
		versions []string
		ok       bool
	}
	got := fanout.ForEach(ctx, missing, 0, func(ctx context.Context, k catalogueKey) result {
		versions, err := c.fetch.AddonVersions(ctx, c.clients, k.addon, k.version)
		return result{versions: versions, ok: err == nil}
	})
	for i, r := range got {
		if r.ok {
			c.reads[missing[i]] = r.versions
		}
	}
}

// upgradeCatalogueKeys returns the catalogue reads that PlanUpgrade asks
// for: each installed add-on that was read and that the request does not
// skip, at the live version when the plan has a catch-up hop, and at each
// hop.
func upgradeCatalogueKeys(snap *fleet.Snapshot, req UpgradeRequest) func(fleet.Version) []catalogueKey {
	return func(fleet.Version) []catalogueKey {
		live := snap.Cluster.Version
		hops, _ := Hops(live, req.To)
		if len(hops) == 0 {
			hops = []fleet.Version{req.To}
		}
		if hops[0] != live {
			hops = append([]fleet.Version{live}, hops...)
		}
		var keys []catalogueKey
		for _, a := range snap.Addons {
			if a.State != fleet.Collected || slices.Contains(req.SkipAddons, a.Name) {
				continue
			}
			for _, v := range hops {
				keys = append(keys, catalogueKey{addon: a.Name, version: v})
			}
		}
		return keys
	}
}

// rollbackCatalogueKeys returns the catalogue reads that PlanRollback
// asks for: each installed add-on that was read, at the target of the
// rollback, which is the version of the first ask.
func rollbackCatalogueKeys(addons []fleet.Addon) func(fleet.Version) []catalogueKey {
	return func(target fleet.Version) []catalogueKey {
		var keys []catalogueKey
		for _, a := range addons {
			if a.State == fleet.Collected {
				keys = append(keys, catalogueKey{addon: a.Name, version: target})
			}
		}
		return keys
	}
}

// upgradeInsights returns the read of the UPGRADE_READINESS insights for
// a planner. With refresh it first asks EKS to evaluate them anew and
// waits for that. live is the version that the control plane runs.
func upgradeInsights(ctx context.Context, clients *awsx.Clients, t Target, live string, refresh *insightsRefresher) func(fleet.Version) InsightsRead {
	return func(fleet.Version) InsightsRead {
		read := InsightsRead{Refreshed: refresh != nil}
		if refresh != nil {
			if err := refresh.refresh(ctx, t, live); err != nil {
				read.RefreshErr = err
				return read
			}
		}
		read.Insights, read.Err = health.ListInsights(ctx, clients, t.Cluster, health.CategoryUpgradeReadiness)
		return read
	}
}

// versionOffer returns the read of what EKS says about one Kubernetes
// version: one DescribeClusterVersions call for that version.
func versionOffer(ctx context.Context, clients *awsx.Clients) func(fleet.Version) VersionOffer {
	return func(v fleet.Version) VersionOffer {
		out, err := clients.EKS.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{v.String()}})
		if err != nil {
			return VersionOffer{Err: err}
		}
		for _, info := range out.ClusterVersions {
			if aws.ToString(info.ClusterVersion) != v.String() {
				continue
			}
			offer := VersionOffer{Offered: true, Tier: apidoc.SupportStandard}
			switch {
			case info.VersionStatus == ekstypes.VersionStatusUnsupported || info.Status == ekstypes.ClusterVersionStatusUnsupported:
				offer.Tier = apidoc.SupportUnsupported
			case info.VersionStatus == ekstypes.VersionStatusExtendedSupport || info.Status == ekstypes.ClusterVersionStatusExtendedSupport:
				offer.Tier = apidoc.SupportExtended
			}
			return offer
		}
		return VersionOffer{}
	}
}

// planRollback builds the plan of a cluster rollback: one read of the
// cluster with its update history, PlanRollback with the reads that it
// asks for, and then the busy rule and the health gate. A rollback starts
// no insights refresh: EKS evaluates the rollback insights when the
// rollback starts.
func planRollback(ctx context.Context, d Deps, req RollbackRequest) (*Plan, error) {
	if req.Cluster == "" {
		return nil, errors.New("no cluster to roll back")
	}
	if err := ValidateRollbackTimeout(req.RollbackTimeout); err != nil {
		return nil, err
	}
	clients := d.AWS.Clients(req.Region)
	snap, err := d.Fetch.Fetch(ctx, clients, req.Cluster, engineNeed(true, true))
	if err != nil {
		return nil, &ClusterReadError{Target: Target{Region: clients.Region, Cluster: req.Cluster}, Err: err}
	}
	p, err := PlanRollback(snap, req, rollbackInputs(ctx, d, clients, snap.Cluster.Name, snap.Addons))
	if err := planEnded(ctx, err); err != nil {
		return nil, err
	}
	cp := &p.Clusters[0]
	// The plan shows every field of the control-plane request: the
	// version and force are on the step, and the timeout is said here.
	if req.RollbackTimeout > 0 && slices.ContainsFunc(cp.Steps(), func(s Step) bool { return s.Ref.Kind == StepControlPlane && s.Action == Submit }) {
		cp.Notices = append(cp.Notices, fmt.Sprintf("the control-plane rollback is sent with a rollback timeout of %d minutes", rollbackMinutes(req)))
	}
	cp.gateEngine(ctx, d, ClusterRollback, req.Policy)
	return p, planEnded(ctx, nil)
}

// rollbackInputs returns the reads of PlanRollback and of
// RollbackStillEligible against live AWS. The first catalogue read reads
// the catalogue of each of addons with it.
func rollbackInputs(ctx context.Context, d Deps, clients *awsx.Clients, cluster string, addons []fleet.Addon) RollbackInputs {
	return RollbackInputs{
		Now:           d.now(),
		Version:       versionOffer(ctx, clients),
		AddonVersions: newCatalogue(d.fetcher(), clients).lookup(ctx, rollbackCatalogueKeys(addons)),
		Insights: func() InsightsRead {
			insights, err := health.ListInsights(ctx, clients, cluster, categoryRollbackReadiness)
			return InsightsRead{Insights: insights, Err: err}
		},
	}
}

// rollbackMinutes returns the rollback timeout of the request in whole
// minutes. ValidateRollbackTimeout bounds it to 120..10080.
func rollbackMinutes(req RollbackRequest) int32 {
	return int32(req.RollbackTimeout.Minutes()) //nolint:gosec // bounded by ValidateRollbackTimeout
}

// rollTargetsOf returns the nodegroups that the steps would roll, each
// once, in plan order. A nodegroup of an upgrade over several hops has a
// step in each.
func rollTargetsOf(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if s.Ref.Kind == StepNodegroup && s.Action == Submit && !slices.Contains(out, s.Ref.Name) {
			out = append(out, s.Ref.Name)
		}
	}
	return out
}

// gateEngine applies the busy rule and the health gate to the plan of an
// upgrade or a rollback.
//
// The busy rule takes every step of the plan as a step of the run: a
// control plane that someone else is changing refuses the plan, and the
// run attaches to its own work. A nodegroup at the goal of its step and
// UPDATING is its own work only when the update in flight rolls it to
// that goal (foreignRolls).
//
// The run applies the rule before each phase with the steps of that phase
// alone (phaseBusy), so the plan applies it to the first phase with work
// too: a cluster that is UPDATING is the work of a control-plane step that
// attaches in a later phase, and it still blocks the phase before that
// one. A dry run then reports the block that the real run would meet.
//
// The gate is health.Preflight over every nodegroup that the plan would
// roll, in any hop, so a drain blocker of the last nodegroup is known
// before the control plane moves. Each nodegroup step runs the gate again
// right before its roll. A plan that the busy rule refuses, and a plan
// that rolls no nodegroup, run no gate.
func (cp *ClusterPlan) gateEngine(ctx context.Context, d Deps, kind Kind, policy health.Policy) {
	refuse, notices := Busy(cp.snapshot, kind, cp.Steps())
	if i := slices.IndexFunc(cp.Phases, func(ph Phase) bool { return slices.ContainsFunc(ph.Steps, Step.work) }); i >= 0 {
		first, _ := Busy(cp.snapshot, kind, cp.Phases[i].Steps)
		for _, b := range first {
			if !slices.ContainsFunc(refuse, func(have Blocker) bool { return have.Message == b.Message }) {
				refuse = append(refuse, b)
			}
		}
	}
	refuse = append(refuse, cp.foreignRolls(ctx, d.AWS.Clients(cp.Region), kind)...)
	cp.Blockers = append(cp.Blockers, refuse...)
	cp.Notices = append(cp.Notices, notices...)
	if len(refuse) > 0 {
		return
	}
	cp.gateNodegroups(ctx, d, policy, rollTargetsOf(cp.Steps()), false)
}

// foreignRolls applies foreignRoll to the plan: for each Verify step of
// a nodegroup that the plan read as UPDATING, it looks up the update in
// flight, once for each nodegroup, and returns a Busy blocker when that
// update is not a roll to the goal of the step. The lookup is a read, so
// a dry run refuses too.
func (cp *ClusterPlan) foreignRolls(ctx context.Context, c *awsx.Clients, kind Kind) []Blocker {
	type lookup struct {
		u   InFlightUpdate
		err error
	}
	seen := map[string]lookup{}
	var out []Blocker
	for _, ph := range cp.Phases {
		for _, s := range ph.Steps {
			if s.Ref.Kind != StepNodegroup || s.Action != Verify {
				continue
			}
			i := slices.IndexFunc(cp.snapshot.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == s.Ref.Name })
			if i < 0 || cp.snapshot.Nodegroups[i].State != fleet.Collected || cp.snapshot.Nodegroups[i].Status != nodegroupUpdating {
				continue
			}
			l, ok := seen[s.Ref.Name]
			if !ok {
				l.u, l.err = inFlightUpdate(ctx, c, s.Ref.Resource())
				seen[s.Ref.Name] = l
			}
			if msg := foreignRoll(kind, s.Ref.Name, s.To, l.u, l.err); msg != "" {
				ref := s.Ref
				out = append(out, busyBlocker(cp.Cluster+" is busy: "+msg, &ref))
			}
		}
	}
	return out
}
