package live

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
	"github.com/dantech2000/refresh/internal/noderoll"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/tui/state"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// rollObserver is the part of noderoll.KubeObserver a live roll uses.
type rollObserver interface {
	StartInformers(ctx context.Context) error
	StopInformers()
	CaptureBaseline(ctx context.Context) error
	Snapshot(ctx context.Context) (noderoll.Snapshot, error)
}

// rollServices are the calls a live roll makes. Tests replace them.
type rollServices struct {
	// kubeFor returns a client for the cluster from a kubeconfig context
	// whose server matches the cluster endpoint, and a line saying which
	// context it used or why there is none. It never writes to the
	// terminal.
	kubeFor func(ctx context.Context, cfg aws.Config, cluster string) (kubernetes.Interface, string)
	// healthCheck runs the pre-flight health gate `nodegroup update` runs.
	healthCheck func(ctx context.Context, cfg aws.Config, cluster string, nodegroups []string, kube kubernetes.Interface) health.HealthSummary
	startRoll   func(ctx context.Context, cfg aws.Config, cluster, nodegroup, version string) (*ekstypes.Update, error)
	// waitUpdate polls the EKS update until it ends or ctx ends.
	waitUpdate func(ctx context.Context, cfg aws.Config, cluster, nodegroup, updateID string, timeout time.Duration) (ekstypes.UpdateStatus, string, error)
	observe    func(kube kubernetes.Interface, nodegroup string) rollObserver
}

func defaultRollServices(opts Options) rollServices {
	return rollServices{
		kubeFor: func(ctx context.Context, cfg aws.Config, cluster string) (kubernetes.Interface, string) {
			target, err := health.DescribeTarget(ctx, factory.NewEKSClient(cfg), cluster, cfg.Region)
			if err != nil {
				return nil, "no node view: " + err.Error()
			}
			client, sel, err := health.ConnectKubeClientForCluster(ctx, opts.Kubeconfig, opts.KubeContext, target, health.ProbeConnection)
			if err != nil {
				var mm *health.ClusterMismatchError
				if errors.As(err, &mm) {
					return nil, "no kubeconfig context for this cluster · add one: " + target.UpdateKubeconfigHint()
				}
				return nil, "no node view: " + err.Error()
			}
			return client, "kubeconfig context " + sel.Diag.Context
		},
		healthCheck: func(ctx context.Context, cfg aws.Config, cluster string, nodegroups []string, kube kubernetes.Interface) health.HealthSummary {
			checker := health.NewCheckerForConfig(cfg, kube, nil)
			if kube != nil {
				checker.SetTargetNodegroups(nodegroups)
			}
			return checker.RunAllChecks(ctx, cluster)
		},
		startRoll: func(ctx context.Context, cfg aws.Config, cluster, nodegroup, version string) (*ekstypes.Update, error) {
			return nodegroupsvc.StartNodegroupRoll(ctx, factory.NewEKSClient(cfg), cluster, nodegroup, version, false)
		},
		waitUpdate: func(ctx context.Context, cfg aws.Config, cluster, nodegroup, updateID string, timeout time.Duration) (ekstypes.UpdateStatus, string, error) {
			monitor := &refreshTypes.ProgressMonitor{
				Updates: []refreshTypes.UpdateProgress{{
					NodegroupName: nodegroup, UpdateID: updateID, ClusterName: cluster,
					Status: ekstypes.UpdateStatusInProgress, StartTime: time.Now(),
				}},
				StartTime: time.Now(),
			}
			// Quiet: the TUI owns the terminal.
			err := monitoring.MonitorUpdates(ctx, factory.NewEKSClient(cfg), monitor, refreshTypes.MonitorConfig{
				PollInterval: opts.PollInterval, Quiet: true, Timeout: timeout,
			})
			u := monitor.Updates[0]
			return u.Status, u.ErrorMessage, err
		},
		observe: func(kube kubernetes.Interface, nodegroup string) rollObserver {
			return noderoll.NewKubeObserver(kube, nodegroup, "")
		},
	}
}

// liveRoll is a roll this backend started.
type liveRoll struct {
	key     string // fleet key of the cluster
	st      state.Roll
	tracker *noderoll.Tracker
	seen    int
	warned  map[string]bool
}

// healthGates turns a health verdict into plan gates. blocked names the
// blocking checks.
func healthGates(s health.HealthSummary) (gates []state.PlanGate, blocked []string) {
	for _, r := range s.Results {
		g := state.PlanGate{Text: r.Name, Note: r.Message}
		switch {
		case r.Skipped:
			g.Status = state.CheckPending
		case r.Status == health.StatusFail:
			g.Status = state.CheckFail
			if r.IsBlocking {
				blocked = append(blocked, r.Name)
			}
		case r.Status == health.StatusWarn:
			g.Status = state.CheckWarn
		default:
			g.Status = state.CheckPass
		}
		gates = append(gates, g)
	}
	if s.Decision == health.DecisionBlock && len(blocked) == 0 {
		blocked = []string{"health gate"}
	}
	return gates, blocked
}

// planRollLive adds what only a change needs to a roll dry run: the
// pre-flight health gate and the node view's Kubernetes access.
func (b *Backend) planRollLive(ctx context.Context, p *state.Plan, cfg aws.Config, t target, ng string) {
	kube, how := b.roll.kubeFor(ctx, cfg, t.name)
	summary := b.roll.healthCheck(ctx, cfg, t.name, []string{ng}, kube)
	gates, blocked := healthGates(summary)
	nodeView := state.PlanGate{Status: state.CheckPass, Text: "live node view", Note: how}
	if kube == nil {
		nodeView.Status = state.CheckWarn
	}
	p.Gates = append([]state.PlanGate{nodeView}, gates...) // the real gate replaces the placeholder
	if len(blocked) > 0 && p.Blocked == "" {
		p.Blocked = "the health gate blocks the roll: " + strings.Join(blocked, ", ")
	}
}

// startRoll checks the gates again and starts the roll. The caller has
// checked that changes are allowed. It returns once EKS accepted the update;
// the roll is then watched in the background.
func (b *Backend) startRoll(ctx context.Context, a state.Action) error {
	c, ok := b.cluster(a.Cluster)
	if !ok {
		return fmt.Errorf("cluster %q is not in the fleet", a.Cluster)
	}
	cfg, t, err := b.cfgFor(a.Cluster)
	if err != nil {
		return err
	}
	var ng *state.Nodegroup
	for i := range c.Nodegroups {
		if c.Nodegroups[i].Name == a.Nodegroup {
			ng = &c.Nodegroups[i]
		}
	}
	if ng == nil {
		return fmt.Errorf("nodegroup %q not found in %s", a.Nodegroup, a.Cluster)
	}
	if !ng.AMIStale {
		return fmt.Errorf("%s already runs the latest AMI for %s", a.Nodegroup, ng.Version)
	}
	// One change per cluster at a time: claim the cluster before any call.
	b.mu.Lock()
	if busy := b.busyOf(a.Cluster, c); busy != "" {
		b.mu.Unlock()
		return fmt.Errorf("%s is busy: %s", a.Cluster, busy)
	}
	b.claimed[a.Cluster] = "starting a roll of " + a.Nodegroup
	b.mu.Unlock()
	release := func() {
		b.mu.Lock()
		delete(b.claimed, a.Cluster)
		b.mu.Unlock()
	}

	sctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	defer cancel()
	kube, how := b.roll.kubeFor(sctx, cfg, t.name)
	summary := b.roll.healthCheck(sctx, cfg, t.name, []string{ng.Name}, kube)
	if _, blocked := healthGates(summary); len(blocked) > 0 {
		release()
		return fmt.Errorf("blocked: the health gate blocks the roll: %s", strings.Join(blocked, ", "))
	}
	// An AMI patch is pinned to the nodegroup's own version, as `nodegroup
	// update` does; a version change is `cluster upgrade`'s job.
	update, err := b.roll.startRoll(sctx, cfg, t.name, ng.Name, ng.Version)
	if err != nil {
		release()
		return fmt.Errorf("starting the roll of %s/%s: %w", t.name, ng.Name, err)
	}

	b.mu.Lock()
	r := &liveRoll{key: a.Cluster, tracker: noderoll.NewTracker(), warned: map[string]bool{}}
	r.st = state.Roll{
		Cluster: a.Cluster, Nodegroup: ng.Name,
		FromVersion: ng.Version, ToVersion: ng.Version,
		FromAMI: ng.AMI, ToAMI: "latest for " + ng.Version,
		MaxUnavailable: 1, StartedAt: b.now(), Planned: ng.Nodes,
	}
	id := aws.ToString(update.Id)
	b.rollEvent(r, state.Event{Source: state.SourceAWS, Level: state.LevelInfo, Subject: "UpdateNodegroupVersion", Text: "id=" + id + " · " + ng.Version})
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "node view", Text: how})
	b.emit(state.Event{Cluster: a.Cluster, Source: state.SourceRoll, Level: state.LevelProgress, Subject: ng.Name, Text: "roll started", Detail: "update " + id})
	b.rolls = append(b.rolls, r)
	b.claimed[a.Cluster] = "rolling " + ng.Name
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}

	b.work.Add(1)
	go func() {
		defer b.work.Done()
		b.watchRoll(runCtx, r, cfg, t, id, kube)
	}()
	return nil
}

// busyOf names the change on a cluster, from this backend or from EKS. The
// caller holds b.mu.
func (b *Backend) busyOf(key string, c state.Cluster) string {
	if s := b.claimed[key]; s != "" {
		return s
	}
	return c.Busy
}

// rollEvent records e in r's feed. The caller holds b.mu.
func (b *Backend) rollEvent(r *liveRoll, e state.Event) {
	e.Cluster = r.key
	r.st.Events = appendCapped(r.st.Events, b.stamp(e), logCap)
}

// watchRoll follows the roll until EKS says it ended: the EKS update is the
// authority on the result, and the node view is best effort beside it.
func (b *Backend) watchRoll(ctx context.Context, r *liveRoll, cfg aws.Config, t target, updateID string, kube kubernetes.Interface) {
	type result struct {
		status ekstypes.UpdateStatus
		msg    string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		s, m, err := b.roll.waitUpdate(ctx, cfg, t.name, r.st.Nodegroup, updateID, b.opts.WaitTimeout)
		done <- result{s, m, err}
	}()

	var obs rollObserver
	if kube != nil {
		obs = b.roll.observe(kube, r.st.Nodegroup)
		if err := obs.StartInformers(ctx); err == nil {
			defer obs.StopInformers()
		}
		if err := obs.CaptureBaseline(ctx); err != nil {
			b.mu.Lock()
			b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelWarn, Subject: "node view", Text: "could not read the nodes: " + err.Error()})
			b.mu.Unlock()
			obs = nil
		}
	}
	tick := time.NewTicker(b.opts.ObserveInterval)
	defer tick.Stop()
	b.observeOnce(ctx, r, obs, true)
	for {
		select {
		case res := <-done:
			b.observeOnce(ctx, r, obs, false)
			b.finishRoll(r, res.status, res.msg, res.err)
			return
		case <-tick.C:
			b.observeOnce(ctx, r, obs, false)
		}
	}
}

// observeOnce reads the nodes and turns changes into feed events.
func (b *Backend) observeOnce(ctx context.Context, r *liveRoll, obs rollObserver, first bool) {
	if obs == nil {
		return
	}
	snap, err := obs.Snapshot(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelWarn, Subject: "node view", Text: err.Error()})
		return
	}
	if first {
		old := 0
		for _, n := range snap.Nodes {
			if !n.OnTarget {
				old++
			}
		}
		if old > 0 {
			r.st.Planned = old
		}
	}
	r.st.Snapshot = snap
	r.tracker.Observe(snap)
	evs := r.tracker.Recent(0)
	if len(evs) < r.seen {
		r.seen = len(evs)
	}
	for _, e := range evs[r.seen:] {
		ev := lifecycleEvent(e)
		b.rollEvent(r, ev)
		if e.Kind != noderoll.EvtJoining {
			ev.Cluster = r.key
			b.emit(ev)
		}
	}
	r.seen = len(evs)
	for _, w := range snap.Warnings {
		k := w.Object + "|" + w.Reason + "|" + w.Message
		if r.warned[k] {
			continue
		}
		r.warned[k] = true
		b.rollEvent(r, state.Event{Source: state.SourceKube, Level: state.LevelWarn, Subject: w.Object, Text: w.Reason, Detail: w.Message})
	}
	ready := 0
	for _, n := range snap.Nodes {
		if n.Ready {
			ready++
		}
	}
	r.st.Gates = []state.Gate{
		{Name: fmt.Sprintf("nodes Ready %d/%d", ready, snap.Total), Status: gateStatus(ready == snap.Total)},
		{Name: fmt.Sprintf("%d warning event(s)", len(snap.Warnings)), Status: gateStatus(len(snap.Warnings) == 0)},
	}
}

func gateStatus(ok bool) state.CheckStatus {
	if ok {
		return state.CheckPass
	}
	return state.CheckWarn
}

func lifecycleEvent(e noderoll.Event) state.Event {
	ev := state.Event{Source: state.SourceRoll, Subject: e.Node}
	switch e.Kind {
	case noderoll.EvtJoining:
		ev.Level, ev.Text = state.LevelProgress, "joining"
	case noderoll.EvtOnline:
		ev.Level, ev.Text = state.LevelOK, "online"
	case noderoll.EvtDraining:
		ev.Level, ev.Text = state.LevelProgress, "draining"
	case noderoll.EvtTerminated:
		ev.Level, ev.Text = state.LevelDone, "terminated"
	}
	return ev
}

// finishRoll records the EKS result and frees the cluster.
func (b *Backend) finishRoll(r *liveRoll, status ekstypes.UpdateStatus, msg string, err error) {
	b.mu.Lock()
	r.st.EndedAt = b.now()
	lvl, text := state.LevelOK, "roll complete"
	switch {
	case err != nil && status != ekstypes.UpdateStatusSuccessful:
		// The watch stopped (timeout, quit, lost access); the EKS update may
		// still be running.
		lvl, text = state.LevelWarn, "stopped watching: "+err.Error()
		r.st.Failed = "outcome unknown · the EKS update may still be running"
	case status != ekstypes.UpdateStatusSuccessful:
		lvl, text = state.LevelError, "roll "+strings.ToLower(string(status))
		r.st.Failed = strings.TrimSpace(string(status) + " " + msg)
	}
	took := r.st.EndedAt.Sub(r.st.StartedAt).Round(time.Second)
	b.rollEvent(r, state.Event{Source: state.SourceAWS, Level: lvl, Subject: "DescribeUpdate", Text: string(status), Detail: msg})
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: lvl, Subject: r.st.Nodegroup, Text: text, Detail: took.String()})
	b.emit(state.Event{Cluster: r.key, Source: state.SourceRoll, Level: lvl, Subject: r.st.Nodegroup, Text: text, Detail: took.String()})
	delete(b.claimed, r.key)
	b.mu.Unlock()
	b.Refresh() // read the nodegroup's new AMI
}

// snapshot copies a roll for State.
func (r *liveRoll) snapshot() state.Roll {
	st := r.st
	st.Events = slices.Clone(r.st.Events)
	st.Gates = slices.Clone(r.st.Gates)
	st.Snapshot.Nodes = slices.Clone(r.st.Snapshot.Nodes)
	for i := range st.Snapshot.Nodes {
		st.Snapshot.Nodes[i].Pressure = slices.Clone(st.Snapshot.Nodes[i].Pressure)
	}
	st.Snapshot.Warnings = slices.Clone(r.st.Snapshot.Warnings)
	st.Pods = map[string][]state.Pod{}
	st.NodePods = map[string]int{}
	if r.st.Running() && st.Planned > 0 {
		left := st.Planned - st.Replaced()
		st.ETA = time.Duration(left) * 2 * time.Minute
	}
	return st
}
