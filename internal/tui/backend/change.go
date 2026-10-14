package backend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// The errors of a change from the UI.
var (
	ErrStaleProposal = errors.New("the dry run is no longer the newest; run it again")
	ErrClusterBusy   = errors.New("the UI already runs or watches a change on this cluster")
	ErrUnknownChange = errors.New("the change is not one of this UI")
)

// keptFinished is how many finished changes the backend keeps, own and
// watched apart. Older ones are dropped with their logs.
const keptFinished = 20

// The default wait of each kind of change, from CONTRACT.md.
var defaultWait = map[change.Kind]time.Duration{
	change.NodegroupUpdate: 40 * time.Minute,
	change.NodegroupScale:  5 * time.Minute,
	change.AddonUpdate:     5 * time.Minute,
	change.ClusterUpgrade:  4 * time.Hour,
	change.ClusterRollback: 4 * time.Hour,
}

// entry is one handle the backend holds, with what the notifier read of it
// last.
type entry struct {
	id        state.ChangeID
	key       state.Key
	handle    *change.Handle
	elsewhere bool
	// resource is the resource a watched change follows, and announced
	// says that the feed has named the watch, once its step attached.
	resource  change.Resource
	announced bool
	view      change.View
	log       []change.LogEntry
	nodes     map[change.StepRef][]noderoll.Event
}

// proposal is the newest dry run, with the request it was computed for.
type proposal struct {
	state.Proposal
	request change.Request
}

// limits returns the budgets of one kind of change: --timeout for its
// reads, --wait-timeout or the default of the kind's command for its
// waits, and the default poll interval.
func (b *Live) limits(kind change.Kind) change.Limits {
	wait := defaultWait[kind]
	if b.opts.WaitTimeoutSet {
		wait = b.opts.WaitTimeout
	}
	return change.Limits{Read: b.opts.ReadTimeout, Wait: wait, Poll: change.DefaultPollInterval}
}

// request builds the request of an action. The policy is the zero value:
// the UI has no --force, no --require-healthy, and no --skip-health-check,
// and it never sets --yes.
func (b *Live) request(a state.Action) (change.Request, error) {
	target := change.Target{Region: a.Cluster.Region, Cluster: a.Cluster.Name}
	common := func(kind change.Kind) change.Common {
		return change.Common{Policy: health.Policy{}, Nodes: true, Limits: b.limits(kind)}
	}
	switch a.Kind {
	case state.ActionRoll:
		if a.Nodegroup == "" {
			return nil, errors.New("a roll names one nodegroup")
		}
		return change.RollRequest{Common: common(change.NodegroupUpdate), Target: target, Nodegroups: []string{a.Nodegroup}}, nil
	case state.ActionAddons:
		return change.AddonUpdateRequest{Common: common(change.AddonUpdate), Target: target, All: true}, nil
	case state.ActionUpgrade:
		req := change.UpgradeRequest{Common: common(change.ClusterUpgrade), Target: target, To: a.To, Only: a.Only}
		if a.Nodegroup != "" {
			req.Nodegroups = []string{a.Nodegroup}
		}
		return req, nil
	case state.ActionRollback:
		return change.RollbackRequest{Common: common(change.ClusterRollback), Target: target}, nil
	}
	return nil, fmt.Errorf("unknown action %q", a.Kind)
}

// withExpect returns req with the reviewed plan as its consent.
func withExpect(req change.Request, plan *change.Plan) change.Request {
	switch r := req.(type) {
	case change.RollRequest:
		r.Expect = plan
		return r
	case change.AddonUpdateRequest:
		r.Expect = plan
		return r
	case change.UpgradeRequest:
		r.Expect = plan
		return r
	case change.RollbackRequest:
		r.Expect = plan
		return r
	}
	return req
}

// command is the CLI command that makes the same change as the action,
// with the profile and the Kubernetes access of this UI, so it runs
// against the same account.
func (b *Live) command(a state.Action) string {
	parts := []string{"refresh"}
	if b.opts.Profile != "" {
		parts = append(parts, "--profile", shellWord(b.opts.Profile))
	}
	parts = append(parts, "--region", a.Cluster.Region)
	kube := false
	switch a.Kind {
	case state.ActionRoll:
		parts, kube = append(parts, "nodegroup", "update", a.Nodegroup, "-c", a.Cluster.Name), true
	case state.ActionAddons:
		parts = append(parts, "addon", "update", "--all", "-c", a.Cluster.Name)
	case state.ActionUpgrade:
		parts, kube = append(parts, "cluster", "upgrade", a.Cluster.Name, "--to", a.To.String()), true
		if len(a.Only) > 0 {
			names := make([]string, len(a.Only))
			for i, p := range a.Only {
				names[i] = string(p)
			}
			parts = append(parts, "--only", strings.Join(names, ","))
		}
		if a.Nodegroup != "" {
			parts = append(parts, "-n", a.Nodegroup)
		}
	case state.ActionRollback:
		parts, kube = append(parts, "cluster", "rollback", a.Cluster.Name), true
	}
	if kube {
		if b.opts.Kubeconfig != "" {
			parts = append(parts, "--kubeconfig", shellWord(b.opts.Kubeconfig))
		}
		if b.opts.KubeContext != "" {
			parts = append(parts, "--kube-context", shellWord(b.opts.KubeContext))
		}
	}
	return strings.Join(parts, " ")
}

// shellWord quotes s for a shell when it needs it.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Plan is the dry run of an action: the plan, its gate, and the CLI
// command, under the read budget. It changes nothing. The proposal it
// returns is the one that Start accepts, until the next Plan.
func (b *Live) Plan(ctx context.Context, a state.Action) (state.Proposal, error) {
	b.mu.Lock()
	known := slices.ContainsFunc(b.clusters, func(c state.Cluster) bool { return c.Key == a.Cluster })
	b.mu.Unlock()
	if !known {
		return state.Proposal{}, ErrUnknownCluster
	}
	req, err := b.request(a)
	if err != nil {
		return state.Proposal{}, err
	}
	ctx, cancel := b.withTimeoutOf(ctx)
	defer cancel()
	plan, err := change.NewPlan(ctx, b.deps, req)
	if err != nil {
		return state.Proposal{}, err
	}
	// A dry run that esc cancelled is not the newest: it must not take
	// the place of a later one.
	if err := ctx.Err(); err != nil {
		return state.Proposal{}, err
	}
	b.mu.Lock()
	b.nextProposal++
	p := &proposal{Proposal: state.Proposal{
		ID: b.nextProposal, Action: a, Plan: plan, Doc: plan.Document(), Command: b.command(a),
		Blocked: plan.Decision() == change.DecisionBlocked,
	}, request: req}
	b.proposal = p
	b.mu.Unlock()
	b.bump()
	return p.Proposal, nil
}

// Start runs the newest proposal. Without --allow-changes it is refused
// here, and nowhere else: this is the one function that calls
// change.Start. The proposal is consumed under the lock, so one
// confirmation starts one run; the run gets the reviewed plan as its
// consent and the backend's context, not the caller's.
func (b *Live) Start(_ context.Context, id state.ProposalID) (state.ChangeID, error) {
	if !b.opts.AllowChanges {
		return 0, ErrReadOnly
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.proposal
	switch {
	case p == nil || p.ID != id:
		return 0, ErrStaleProposal
	case p.Blocked:
		return 0, errors.New("the dry run is blocked; nothing can start")
	case len(b.byCluster[p.Action.Cluster]) > 0:
		return 0, ErrClusterBusy
	case b.ctx.Err() != nil:
		// Run has returned and waits for no goroutine any more.
		return 0, errors.New("the UI is closing; nothing starts")
	}
	b.proposal = nil
	h := change.Start(b.ctx, b.deps, withExpect(p.request, p.Plan))
	e := b.add(h, p.Action.Cluster, false, change.Resource{})
	b.say(p.Action.Cluster, view.Progress, "started "+describeAction(p.Action))
	b.bumpLocked()
	return e.id, nil
}

// add records a handle and claims its cluster. The caller holds b.mu.
func (b *Live) add(h *change.Handle, key state.Key, elsewhere bool, r change.Resource) *entry {
	b.nextChange++
	e := &entry{id: b.nextChange, key: key, handle: h, elsewhere: elsewhere, resource: r, view: h.View()}
	b.entries = append(b.entries, e)
	b.byCluster[key] = append(b.byCluster[key], e.id)
	b.wg.Go(func() { b.follow(e) })
	return e
}

// follow reads the handle on every change and publishes it, and at the
// end releases the claim and asks for a read of the cluster. It takes the
// channel before the read, so no change is missed.
func (b *Live) follow(e *entry) {
	after := uint64(0)
	for {
		ch := e.handle.Changed()
		after = b.store(e, after)
		b.bump()
		select {
		case <-ch:
			continue
		case <-e.handle.Done():
		}
		b.store(e, after)
		b.release(e)
		b.bump()
		return
	}
}

// store copies the view and the new log entries of the handle into the
// entry, and returns the sequence number of the last entry read.
func (b *Live) store(e *entry, after uint64) uint64 {
	v := e.handle.View()
	entries := e.handle.Events(after)
	b.mu.Lock()
	defer b.mu.Unlock()
	e.view = v
	if e.elsewhere && !e.announced && len(v.Steps) > 0 {
		// The watch attached to the update: it is a change, and the feed
		// says so once.
		e.announced = true
		b.say(e.key, view.Progress, "watching a change started elsewhere: "+e.resource.String())
	}
	if len(entries) > 0 {
		// The published slice is never written to: a longer copy replaces it.
		e.log = slices.Concat(e.log, entries)
		after = entries[len(entries)-1].Seq
	}
	for ref, p := range v.Progress {
		if p.Nodes != nil {
			if e.nodes == nil {
				e.nodes = map[change.StepRef][]noderoll.Event{}
			}
			e.nodes[ref] = p.Nodes.Recent
		}
	}
	return after
}

// release ends the claim of a finished change, says how it ended, and
// asks the sweep loop to read its cluster again. A watch that found
// nothing in flight is dropped without a word: the sweep that started
// it read a state that is gone.
func (b *Live) release(e *entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byCluster[e.key] = slices.DeleteFunc(b.byCluster[e.key], func(id state.ChangeID) bool { return id == e.id })
	if !e.elsewhere {
		b.ended[e.key] = b.opts.Now()
	}
	res := e.view.Result
	if e.elsewhere && res != nil && res.Status == change.NothingToDo {
		b.entries = slices.DeleteFunc(b.entries, func(x *entry) bool { return x == e })
		return
	}
	if res != nil {
		status := view.RunStatus(res.Status)
		text := "ended: " + strings.ToLower(string(res.Status))
		if res.Err != nil {
			text += ": " + res.Err.Error()
		}
		b.say(e.key, status, text)
	}
	b.trimLocked()
	select {
	case b.refetch <- e.key:
	default:
	}
}

// trimLocked drops the oldest finished changes past keptFinished, own and
// watched apart. The caller holds b.mu.
func (b *Live) trimLocked() {
	own, watched := 0, 0
	for i := len(b.entries) - 1; i >= 0; i-- {
		e := b.entries[i]
		if e.view.State != change.Finished {
			continue
		}
		n := &own
		if e.elsewhere {
			n = &watched
		}
		*n++
		if *n > keptFinished {
			b.entries = slices.Delete(b.entries, i, i+1)
		}
	}
}

// adopt starts a watch for each nodegroup and control plane that the
// sweep shows UPDATING and that this UI does not run. The caller holds
// b.mu. The feed names the watch once it has attached to the update: a
// watch that finds nothing, or an update that is not a change of
// refresh, ends without a word.
//
// A cluster with a change of ours is left alone: its handle reports what
// happens there. A snapshot read before our last change on the cluster
// ended is left alone too: what it shows may be that change. A watch
// of the same resource that still runs is not started twice. CREATING
// and DELETING are not adopted: nothing of refresh is behind them.
func (b *Live) adopt() {
	if b.ctx.Err() != nil {
		return
	}
	for _, c := range b.clusters {
		snap := c.Snapshot
		if snap == nil || snap.Have.Cluster != fleet.Collected || len(b.byCluster[c.Key]) > 0 {
			continue
		}
		if ended, ok := b.ended[c.Key]; ok && snap.FetchedAt.Before(ended) {
			continue
		}
		target := change.Target{Region: c.Region, Cluster: c.Name}
		var resources []change.Resource
		if snap.Cluster.Status == string(ekstypes.ClusterStatusUpdating) {
			resources = append(resources, change.Resource{Target: target, Kind: change.StepControlPlane})
		}
		rolling := 0
		for _, n := range snap.Nodegroups {
			if n.State == fleet.Collected && n.Status == string(ekstypes.NodegroupStatusUpdating) {
				resources = append(resources, change.Resource{Target: target, Kind: change.StepNodegroup, Name: n.Name})
				rolling++
			}
		}
		for _, r := range resources {
			if slices.ContainsFunc(b.entries, func(e *entry) bool { return e.elsewhere && e.resource == r && e.view.State != change.Finished }) {
				continue
			}
			kind := change.NodegroupUpdate
			if r.Kind == change.StepControlPlane {
				kind = change.ClusterUpgrade
			}
			// The node view is for one roll, as the run has it.
			h := change.Watch(b.ctx, b.deps, r, change.WatchOptions{Nodes: rolling == 1, Limits: b.limits(kind)})
			b.add(h, c.Key, true, r)
		}
	}
}

// Unfinished returns the changes of this session that have not ended,
// for the words on the way out: they keep running in EKS.
func (b *Live) Unfinished() []state.Change {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []state.Change
	for _, e := range b.entries {
		if e.view.State != change.Finished {
			out = append(out, state.Change{ID: e.id, View: e.view, Log: e.log, Nodes: e.nodes})
		}
	}
	return out
}

// changes returns the changes for a state: running first, then finished,
// newest last within each. The caller holds b.mu.
func (b *Live) changes() []state.Change {
	out := make([]state.Change, 0, len(b.entries))
	for _, e := range b.entries {
		if e.view.State != change.Finished {
			out = append(out, state.Change{ID: e.id, View: e.view, Log: e.log, Nodes: e.nodes})
		}
	}
	for _, e := range b.entries {
		if e.view.State == change.Finished {
			out = append(out, state.Change{ID: e.id, View: e.view, Log: e.log, Nodes: e.nodes})
		}
	}
	return out
}

// running returns the running change of a cluster, or 0. The caller holds
// b.mu.
func (b *Live) running(key state.Key) state.ChangeID {
	if ids := b.byCluster[key]; len(ids) > 0 {
		return ids[0]
	}
	return 0
}

// find returns the entry of a change.
func (b *Live) find(id state.ChangeID) (*entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.entries, func(e *entry) bool { return e.id == id })
	if i < 0 {
		return nil, ErrUnknownChange
	}
	return b.entries[i], nil
}

// Answer answers the open question of a change. The handle refuses the
// answer for a change started elsewhere, a stale question, and a change
// that has finished.
func (b *Live) Answer(_ context.Context, id state.ChangeID, q change.QuestionID, yes bool) error {
	e, err := b.find(id)
	if err != nil {
		return err
	}
	return e.handle.Answer(q, yes)
}

// Pause holds a change at its next checkpoint.
func (b *Live) Pause(_ context.Context, id state.ChangeID) error {
	e, err := b.find(id)
	if err != nil {
		return err
	}
	return e.handle.Pause()
}

// Resume lets a paused change go on.
func (b *Live) Resume(_ context.Context, id state.ChangeID) error {
	e, err := b.find(id)
	if err != nil {
		return err
	}
	return e.handle.Resume()
}

// Stop ends a change at its next checkpoint.
func (b *Live) Stop(_ context.Context, id state.ChangeID) error {
	e, err := b.find(id)
	if err != nil {
		return err
	}
	return e.handle.Stop()
}

// describeAction names an action in a feed line.
func describeAction(a state.Action) string {
	switch a.Kind {
	case state.ActionRoll:
		return "the roll of nodegroup " + a.Nodegroup
	case state.ActionAddons:
		return "the update of the add-ons"
	case state.ActionUpgrade:
		return "the upgrade to " + a.To.String()
	case state.ActionRollback:
		return "the rollback"
	}
	return string(a.Kind)
}
