package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet"
)

// DiscoveryError is the error of a fleet roll in which no region returned
// its cluster list. Sweep has what each region answered, so a front end
// can tell bad credentials from closed regions.
type DiscoveryError struct {
	Sweep *fleet.SweepResult
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf("could not list clusters in any of %d region(s)", len(e.Sweep.Regions.Names))
}

// fleet reports whether the plan is the plan of a fleet roll: every
// cluster of a region sweep.
func (p *Plan) fleet() bool {
	r, ok := p.Request.(RollRequest)
	return ok && r.Fleet != nil
}

// planFleet builds the plan of a fleet roll.
//
//  1. Discover: one cluster list per region, all at once, under the read
//     budget. A region of a default sweep that is closed to the
//     credentials is skipped. A region that fails is a failure of the
//     plan. No region that answers is an error.
//  2. Plan: one plan per cluster, as planRollCluster builds it, with
//     bounded fan-out, in the order of the regions and then by name. Each
//     cluster has its own read budget. A cluster that cannot be planned
//     is in the plan with a blocker and a failure, and does not stop the
//     others.
//
// A run confirms the whole plan once, and then plans each cluster again
// at its turn: see runner.turn.
func planFleet(ctx context.Context, d Deps, req RollRequest) (*Plan, error) {
	sweep, targets := discover(ctx, d, req)
	if len(sweep.Answered) == 0 {
		return nil, &DiscoveryError{Sweep: sweep}
	}
	limit := d.AWS.MaxInFlight() / 2
	plans := fanout.ForEach(ctx, targets, limit, func(ctx context.Context, t Target) *ClusterPlan {
		cctx, read := d.budget(ctx, req.Limits.Read, ErrReadTimeout)
		read.start()
		defer read.stop()
		cp, err := planRollCluster(cctx, d, req, t)
		if err != nil {
			return unplannedCluster(t, err)
		}
		return cp
	})
	p := &Plan{Kind: NodegroupUpdate, Request: req, SkippedRegions: sweep.Skipped, Failures: sweep.Failures, At: d.now()}
	for i, cp := range plans {
		if cp == nil {
			// The run ended before this cluster started.
			cp = unplannedCluster(targets[i], context.Cause(ctx))
		}
		p.Clusters = append(p.Clusters, *cp)
	}
	return p, nil
}

// discover lists the clusters of every region of the request, under the
// read budget, and returns what each region answered and the clusters in
// the order of the regions and then by name.
func discover(ctx context.Context, d Deps, req RollRequest) (*fleet.SweepResult, []Target) {
	ctx, read := d.budget(ctx, req.Limits.Read, ErrReadTimeout)
	read.start()
	defer read.stop()
	regions := req.Fleet.Regions
	type outcome struct {
		ran   bool
		names []string
		err   error
	}
	outcomes := fanout.ForEach(ctx, regions.Names, max(len(regions.Names), 1), func(ctx context.Context, region string) outcome {
		// Only a default sweep gives up on a region after one failed
		// dial. A region that the user named gets the full retries.
		clients := d.AWS.Clients(region)
		if regions.Default {
			clients = d.AWS.SweepClients(region).Clients
		}
		names, err := d.Fetch.Clusters(ctx, clients)
		slices.Sort(names)
		return outcome{ran: true, names: names, err: err}
	})
	sweep := &fleet.SweepResult{Regions: regions}
	var targets []Target
	for i, region := range regions.Names {
		o := outcomes[i]
		if !o.ran {
			o.err = context.Cause(ctx)
		}
		switch {
		case o.err == nil:
			sweep.Answered = append(sweep.Answered, region)
			for _, name := range o.names {
				targets = append(targets, Target{Region: region, Cluster: name})
			}
		case o.ran && regions.Default && awsx.IsRegionInaccessible(o.err):
			sweep.Skipped = append(sweep.Skipped, region)
		default:
			sweep.Failures = append(sweep.Failures, fleet.FailureFromError(apidoc.FailureRegion, region, o.err))
			sweep.Errs = append(sweep.Errs, o.err)
		}
	}
	slices.Sort(sweep.Skipped)
	return sweep, targets
}

// unplannedCluster returns the plan of a cluster of a fleet that could
// not be planned: no step, a blocker, and the failure of the read.
func unplannedCluster(t Target, err error) *ClusterPlan {
	var read *ClusterReadError
	if errors.As(err, &read) {
		err = read.Err
	}
	f := fleet.FailureFromError(apidoc.FailureCluster, t.Cluster, err)
	f.Region = t.Region
	return &ClusterPlan{
		Target:   t,
		Failures: []apidoc.Failure{f},
		Blockers: []Blocker{{Kind: BlockerUnread, Message: "could not plan cluster " + t.Cluster + ": " + f.Error}},
	}
}

// single returns a plan with the one cluster, for the comparison of a
// cluster of a fleet with itself at another time.
func single(p *Plan, cp *ClusterPlan) *Plan {
	return &Plan{Kind: p.Kind, Request: p.Request, Clusters: []ClusterPlan{*cp}, At: p.At}
}

// turnOutcome is how the turn of a cluster of a fleet begins.
type turnOutcome int

const (
	// turnRun: the cluster runs its plan.
	turnRun turnOutcome = iota
	// turnBlocked: the cluster is not changed, and the next one goes on.
	// Its steps with work are Blocked.
	turnBlocked
	// turnUnread: the cluster could not be read at its turn. Its steps
	// are NotAttempted, with a failure, and the next one goes on.
	turnUnread
	// turnHalt: the run ends here. The steps of this cluster and of every
	// later one are NotAttempted.
	turnHalt
)

// turn begins the turn of cluster i of a fleet, and returns how. why is
// the message of a cluster that is blocked.
//
// The clusters run one after another, and a cluster that is refused,
// fails, times out, or fails its verification does not stop the next one.
// Three things end the run: an interrupt, a stop from the checkpoint
// hook, and a question during the run that was answered no or could not
// be asked.
//
// Each cluster has its own read budget, which starts here. The clusters
// before this one took time, so every cluster after the first is planned
// again, and the new plan is compared with the one that was confirmed, by
// the table of Diff:
//
//   - a difference that only says that less is to do: a notice.
//   - a difference that needs an answer, such as a new AMI release: --yes
//     accepts it with a notice. Without it a person is asked, and after a
//     yes the cluster is planned once more. With nobody to ask, the
//     cluster is blocked.
//   - a difference that refuses, such as another Kubernetes version or a
//     new blocker: the cluster is blocked.
//
// The plan of the turn replaces the confirmed one in p.
func (r *runner) turn(p *Plan, i int) (outcome turnOutcome, why string) {
	cp := &p.Clusters[i]
	switch {
	case r.stop != nil || r.interrupted:
		return turnHalt, ""
	case r.interruptedNow() || r.ctx.Err() != nil:
		return turnHalt, ""
	}
	if i > 0 && r.hooks.Checkpoint != nil {
		answer, err := r.hooks.Checkpoint(r.ctx, PhaseRef{Target: cp.Target, Kind: PhaseRoll})
		switch {
		case err != nil && r.ctx.Err() != nil:
			r.interruptedNow()
			return turnHalt, ""
		case err != nil:
			r.stop = &stop{status: Failed, err: fmt.Errorf("the checkpoint before cluster %s failed: %w; nothing more was changed", cp.Cluster, err)}
			r.hookErr = err
			return turnHalt, ""
		case answer.Stop:
			r.stop = &stop{status: Stopped, err: fmt.Errorf("stopped before cluster %s; nothing more was changed", cp.Cluster)}
			return turnHalt, ""
		}
	}
	r.newRead(r.common.Limits.Read)
	if cp.Decision() == DecisionBlocked {
		return turnBlocked, ""
	}
	if i == 0 {
		return turnRun, ""
	}
	req, _ := r.req.(RollRequest)
	prev := *cp
	for asked := 0; ; asked++ {
		d := r.d
		if d.Fetch == nil {
			d.Fetch = &fleet.Fetcher{Now: d.Now}
		}
		d.Fetch = d.Fetch.Fresh()
		cur, err := planRollCluster(r.readCtx, d, req, prev.Target)
		if err != nil {
			if r.interruptedNow() {
				return turnHalt, ""
			}
			r.addFailures(unplannedCluster(prev.Target, err).Failures)
			return turnUnread, ""
		}
		diff := Diff(single(p, &prev), single(p, cur))
		p.Clusters[i] = *cur
		yes := r.common.Policy.Yes
		for _, c := range diff {
			if c.Effect() == Continue || (yes && c.Effect() == AskAgain) {
				r.notice(cp.Cluster + ": " + c.Message)
			}
		}
		switch {
		case cur.Decision() == DecisionBlocked:
			return turnBlocked, ""
		case worstEffect(diff) == Refuse:
			return turnBlocked, "the plan of " + cp.Cluster + " changed since it was confirmed (" + messages(diff, Refuse) + "); nothing was started there"
		}
		q := r.question(single(p, cur), diff, true)
		switch {
		case q == nil:
			acceptCluster(&p.Clusters[i])
			return turnRun, ""
		case r.hooks.Ask == nil:
			return turnBlocked, unanswered(q.Kind) + "; nothing was started in " + cp.Cluster
		case asked >= maxQuestions:
			return turnBlocked, cp.Cluster + " keeps changing (" + strings.Join(q.Reasons, "; ") + "); nothing was started there"
		}
		q.Text = cp.Cluster + ": " + q.Text
		// The budgets do not run while a person answers.
		r.read.pause()
		ok, err := r.hooks.Ask(r.ctx, *q)
		r.read.start()
		switch {
		case err != nil && r.ctx.Err() != nil:
			r.interruptedNow()
			return turnHalt, ""
		case err != nil:
			r.stop = &stop{status: Failed, err: fmt.Errorf("could not ask %q: %w; nothing more was changed", q.Text, err)}
			r.hookErr = err
			return turnHalt, ""
		case !ok:
			r.stop = &stop{status: Declined, err: errors.New("declined; nothing more was changed")}
			return turnHalt, ""
		}
		prev = *cur
	}
}

// blockedResults returns the results of a cluster of a fleet that is not
// changed: a step with work is Blocked, with why as its failure when
// there is a reason beside the blockers of the plan, and a target that is
// already changing keeps its Busy row.
func (r *runner) blockedResults(cp *ClusterPlan, why string) []StepResult {
	var out []StepResult
	for _, s := range cp.Steps() {
		res := StepResult{Ref: s.Ref, Status: StepSkipped}
		switch {
		case s.busy():
			res = cp.busyResult(s)
		case s.work() || s.Action == Blocked:
			res.Status = StepBlocked
			if why != "" {
				res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, why)
			}
		}
		out = append(out, res)
	}
	return out
}
