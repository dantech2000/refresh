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

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/diag"
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
	kubeFor func(ctx context.Context, cfg aws.Config, cluster string) (kubernetes.Interface, health.NodeMetricsLister, string)
	// decide reads the nodegroup from EKS right before a roll starts and
	// applies `nodegroup update`'s decision table to it (custom AMI, already
	// updating, already on the latest AMI).
	decide func(ctx context.Context, cfg aws.Config, cluster, nodegroup string) (*ekstypes.Nodegroup, nodegroupsvc.AMIUpdateDecision, error)
	// healthCheck runs the pre-flight health gate `nodegroup update` runs.
	healthCheck func(ctx context.Context, cfg aws.Config, cluster string, nodegroups []string, kube kubernetes.Interface, metrics health.NodeMetricsLister) health.HealthSummary
	// drainBlockers is `cluster upgrade`'s PDB gate before each nodegroup
	// roll: the PDBs and pods that would refuse an eviction from nodegroup.
	drainBlockers func(ctx context.Context, cfg aws.Config, cluster, nodegroup string, kube kubernetes.Interface) (health.DrainBlockerReport, error)
	// offerings lists the (instance type, AZ) pairs the nodegroup spans
	// where EC2 does not offer the type.
	offerings func(ctx context.Context, cfg aws.Config, cluster, nodegroup string) ([]nodegroupsvc.UnavailableOffering, error)
	// pendingPods and verify are the post-roll verification of `nodegroup
	// update`: pods newly stuck Pending, and the nodegroup back to ACTIVE.
	pendingPods func(ctx context.Context, kube kubernetes.Interface) (nodegroupsvc.PendingPods, bool)
	verify      func(ctx context.Context, cfg aws.Config, cluster, nodegroup string, kube kubernetes.Interface, pre nodegroupsvc.PendingPods, preOK bool) (nodegroupsvc.PostRollVerification, []diag.Failure)
	startRoll   func(ctx context.Context, cfg aws.Config, cluster, nodegroup, version string) (*ekstypes.Update, error)
	// waitUpdate polls the EKS update until it ends or ctx ends.
	waitUpdate func(ctx context.Context, cfg aws.Config, cluster, nodegroup, updateID string, timeout time.Duration) (ekstypes.UpdateStatus, string, error)
	observe    func(kube kubernetes.Interface, nodegroup string) rollObserver
}

func defaultRollServices(opts Options) rollServices {
	return rollServices{
		kubeFor: func(ctx context.Context, cfg aws.Config, cluster string) (kubernetes.Interface, health.NodeMetricsLister, string) {
			target, err := health.DescribeTarget(ctx, factory.NewEKSClient(cfg), cluster, cfg.Region)
			if err != nil {
				return nil, nil, "no node view: " + awsinternal.FormatAWSError(err, "describing cluster "+cluster).Error()
			}
			client, sel, err := health.ConnectKubeClientForCluster(ctx, opts.Kubeconfig, opts.KubeContext, target, health.ProbeConnection)
			if err != nil {
				var mm *health.ClusterMismatchError
				if errors.As(err, &mm) {
					return nil, nil, "no kubeconfig context for this cluster · add one: " + target.UpdateKubeconfigHint()
				}
				return nil, nil, "no node view: " + err.Error()
			}
			// metrics-server, best effort, for the drain-headroom check.
			metrics, merr := health.BuildMetricsClient(sel)
			if merr != nil {
				metrics = nil
			}
			return client, metrics, "kubeconfig context " + sel.Diag.Context
		},
		decide: func(ctx context.Context, cfg aws.Config, cluster, nodegroup string) (*ekstypes.Nodegroup, nodegroupsvc.AMIUpdateDecision, error) {
			svc := factory.NewNodegroupService(cfg, false, opts.Logger)
			live, err := svc.DescribeNodegroup(ctx, cluster, nodegroup)
			if err != nil {
				return nil, nodegroupsvc.AMIUpdateDecision{}, err
			}
			return live, svc.AMIUpdateDecider(cluster, nodegroupsvc.AMIUpdateOptions{})(ctx, live), nil
		},
		healthCheck: func(ctx context.Context, cfg aws.Config, cluster string, nodegroups []string, kube kubernetes.Interface, metrics health.NodeMetricsLister) health.HealthSummary {
			checker := health.NewCheckerForConfig(cfg, kube, nil)
			if kube != nil {
				checker.SetTargetNodegroups(nodegroups)
				if metrics != nil {
					checker.SetNodeMetrics(metrics)
				}
			}
			return checker.RunAllChecks(ctx, cluster)
		},
		drainBlockers: func(ctx context.Context, cfg aws.Config, cluster, nodegroup string, kube kubernetes.Interface) (health.DrainBlockerReport, error) {
			return health.NewCheckerForConfig(cfg, kube, nil).DrainBlockers(ctx, cluster, []string{nodegroup})
		},
		offerings: func(ctx context.Context, cfg aws.Config, cluster, nodegroup string) ([]nodegroupsvc.UnavailableOffering, error) {
			return factory.NewNodegroupService(cfg, false, opts.Logger).CheckInstanceTypeAvailability(ctx, cluster, nodegroup)
		},
		pendingPods: nodegroupsvc.SnapshotPendingPods,
		verify: func(ctx context.Context, cfg aws.Config, cluster, nodegroup string, kube kubernetes.Interface, pre nodegroupsvc.PendingPods, preOK bool) (nodegroupsvc.PostRollVerification, []diag.Failure) {
			return nodegroupsvc.VerifyPostRoll(ctx, factory.NewEKSClient(cfg), kube, cluster, []string{nodegroup}, pre, preOK)
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
	t       target // the cluster, by region and name
	st      state.Roll
	tracker *noderoll.Tracker
	seen    int
	warned  map[string]bool
	// viewed is set once the node view read the nodes.
	viewed bool
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
	kube, metrics, how := b.roll.kubeFor(ctx, cfg, t.name)
	summary := b.roll.healthCheck(ctx, cfg, t.name, []string{ng}, kube, metrics)
	gates, blocked := healthGates(summary)
	nodeView := state.PlanGate{Status: state.CheckPass, Text: "live node view", Note: how}
	if kube == nil {
		nodeView.Status = state.CheckWarn
	}
	p.Gates = append([]state.PlanGate{nodeView}, gates...) // the real gate replaces the placeholder
	// Instance types EC2 does not offer in some of the nodegroup's AZs: new
	// nodes may fail to launch there. Advisory, as in `nodegroup update`.
	if offs, err := b.roll.offerings(ctx, cfg, t.name, ng); err == nil {
		for _, o := range offs {
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: o.InstanceType + " not offered in " + o.AvailabilityZone, Note: "new nodes may fail to launch there"})
		}
	}
	if len(blocked) > 0 && p.Blocked == "" {
		p.Blocked = "the health gate blocks the roll: " + strings.Join(blocked, ", ")
	}
	// What the user sees here and confirms with y: Start refuses a roll
	// whose gate has found anything more since.
	b.mu.Lock()
	b.accepted[acceptKey(t, ng)] = findings(summary)
	b.mu.Unlock()
}

func acceptKey(t target, ng string) string { return t.region + "/" + t.name + "/" + ng }

// findings names the health results that did not pass, and the ones that
// did not run: a check the dry run measured and Start could not (the node
// view is gone) is a new finding, not a pass.
func findings(s health.HealthSummary) []string {
	var out []string
	for _, r := range s.Results {
		switch {
		case r.Skipped:
			out = append(out, r.Name+" (Skipped)")
		case r.Status != health.StatusPass:
			out = append(out, r.Name+" ("+string(r.Status)+")")
		}
	}
	return out
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
	// One change per cluster: claim the cluster, by region and name, before
	// any call.
	b.mu.Lock()
	if busy := b.busyOf(a.Cluster, c); busy != "" {
		b.mu.Unlock()
		return fmt.Errorf("%s is busy: %s", a.Cluster, busy)
	}
	b.claimed[t] = "starting a roll of " + a.Nodegroup
	accepted, planned := b.accepted[acceptKey(t, ng.Name)]
	b.mu.Unlock()
	started := false
	defer func() {
		if !started {
			b.mu.Lock()
			delete(b.claimed, t)
			b.mu.Unlock()
		}
	}()
	if !planned {
		return errors.New("no dry run for this roll: open its dry run (p) first")
	}

	gctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	defer cancel()
	// The nodegroup as EKS has it now, not as the last sweep saw it, through
	// the decision table of `nodegroup update`.
	live, d, err := b.roll.decide(gctx, cfg, t.name, ng.Name)
	if err != nil {
		return err
	}
	if !d.Starts() {
		return fmt.Errorf("%s: %s", ng.Name, d.Reason)
	}
	version := aws.ToString(live.Version)

	kube, metrics, how := b.roll.kubeFor(gctx, cfg, t.name)
	summary := b.roll.healthCheck(gctx, cfg, t.name, []string{ng.Name}, kube, metrics)
	if _, blocked := healthGates(summary); len(blocked) > 0 {
		return fmt.Errorf("blocked: the health gate blocks the roll: %s", strings.Join(blocked, ", "))
	}
	if added := newFindings(accepted, findings(summary)); len(added) > 0 {
		return fmt.Errorf("the health gate found more since the dry run: %s; open the dry run again (p)", strings.Join(added, ", "))
	}

	// Pods already Pending now are not the roll's doing (post-roll check).
	pre, preOK := b.roll.pendingPods(gctx, kube)

	// The start call gets its own budget, so a slow gate cannot leave it a
	// nearly expired context.
	sctx, scancel := context.WithTimeout(ctx, b.opts.CallTimeout)
	defer scancel()
	// An AMI patch is pinned to the nodegroup's own version, as `nodegroup
	// update` does; a version change is `cluster upgrade`'s job.
	update, err := b.roll.startRoll(sctx, cfg, t.name, ng.Name, version)
	if err != nil {
		return awsinternal.FormatAWSError(err, fmt.Sprintf("starting the roll of %s/%s", t.name, ng.Name))
	}
	started = true

	b.mu.Lock()
	r := &liveRoll{t: t, tracker: noderoll.NewTracker(), warned: map[string]bool{}}
	r.st = state.Roll{
		Nodegroup: ng.Name, FromVersion: version, ToVersion: version,
		FromAMI: ng.AMI, ToAMI: "latest for " + version,
		MaxUnavailableText: maxUnavailableText(live.UpdateConfig),
		StartedAt:          b.now(), Planned: int(aws.ToInt32(scalingDesired(live))),
	}
	id := aws.ToString(update.Id)
	b.rollEvent(r, state.Event{Source: state.SourceAWS, Level: state.LevelInfo, Subject: "UpdateNodegroupVersion", Text: "id=" + id + " · " + version})
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "node view", Text: how})
	b.emit(state.Event{Cluster: a.Cluster, Source: state.SourceRoll, Level: state.LevelProgress, Subject: ng.Name, Text: "roll started", Detail: "update " + id})
	b.rolls = append(b.rolls, r)
	b.claimed[t] = "rolling " + ng.Name
	delete(b.accepted, acceptKey(t, ng.Name))
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}

	b.work.Add(1)
	go func() {
		defer b.work.Done()
		b.watchRoll(runCtx, r, cfg, t, id, kube, pre, preOK)
	}()
	return nil
}

// newFindings lists what now has that before did not.
func newFindings(before, now []string) []string {
	var out []string
	for _, f := range now {
		if !slices.Contains(before, f) {
			out = append(out, f)
		}
	}
	return out
}

func scalingDesired(ng *ekstypes.Nodegroup) *int32 {
	if ng.ScalingConfig == nil {
		return nil
	}
	return ng.ScalingConfig.DesiredSize
}

func maxUnavailableText(u *ekstypes.NodegroupUpdateConfig) string {
	switch {
	case u == nil:
		return "1"
	case u.MaxUnavailable != nil:
		return fmt.Sprint(*u.MaxUnavailable)
	case u.MaxUnavailablePercentage != nil:
		return fmt.Sprintf("%d%%", *u.MaxUnavailablePercentage)
	default:
		return "unknown"
	}
}

// busyOf names the change on a cluster, from this backend or from EKS. The
// caller holds b.mu.
func (b *Backend) busyOf(key string, c state.Cluster) string {
	if t, ok := b.targets[key]; ok {
		if s := b.claimed[t]; s != "" {
			return s
		}
	}
	return c.Busy
}

// keyOf is the fleet key of t now; keys change when a second region gains a
// cluster of the same name. The caller holds b.mu.
func (b *Backend) keyOf(t target) string {
	for k, tt := range b.targets {
		if tt == t {
			return k
		}
	}
	return t.name
}

// rollEvent records e in r's feed. The caller holds b.mu.
func (b *Backend) rollEvent(r *liveRoll, e state.Event) {
	e.Cluster = b.keyOf(r.t)
	r.st.Events = appendCapped(r.st.Events, b.stamp(e), logCap)
}

// watchRoll follows the roll until EKS says it ended: the EKS update is the
// authority on the result, and the node view is best effort beside it.
func (b *Backend) watchRoll(ctx context.Context, r *liveRoll, cfg aws.Config, t target, updateID string, kube kubernetes.Interface, pre nodegroupsvc.PendingPods, preOK bool) {
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
			var v *nodegroupsvc.PostRollVerification
			var vf []diag.Failure
			if res.status == ekstypes.UpdateStatusSuccessful && ctx.Err() == nil {
				vctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
				got, fs := b.roll.verify(vctx, cfg, t.name, r.st.Nodegroup, kube, pre, preOK)
				cancel()
				v, vf = &got, fs
			}
			b.finishRoll(r, res.status, res.msg, res.err, v, vf)
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
	r.viewed = true
	r.tracker.Observe(snap)
	evs := r.tracker.Recent(0)
	if len(evs) < r.seen {
		r.seen = len(evs)
	}
	for _, e := range evs[r.seen:] {
		ev := lifecycleEvent(e)
		b.rollEvent(r, ev)
		if e.Kind != noderoll.EvtJoining {
			ev.Cluster = b.keyOf(r.t)
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

// finishRoll records the EKS result and frees the cluster. The EKS status
// decides: a Failed or Cancelled update is a failed roll even though the
// monitor also returns an error for it; only a watch that stopped before a
// final status is "outcome unknown".
func (b *Backend) finishRoll(r *liveRoll, status ekstypes.UpdateStatus, msg string, err error, v *nodegroupsvc.PostRollVerification, vf []diag.Failure) {
	b.mu.Lock()
	r.st.EndedAt = b.now()
	lvl, text := state.LevelOK, "roll complete"
	switch status {
	case ekstypes.UpdateStatusSuccessful:
		// The post-roll verification of `nodegroup update`.
		if v != nil {
			r.st.Gates = nil
			for _, c := range v.Checks {
				st := state.CheckPass
				if v.Skipped(c) {
					st = state.CheckPending
				}
				r.st.Gates = append(r.st.Gates, state.Gate{Name: c, Status: st})
				b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: levelOfCheck(st), Subject: "verify", Text: c})
			}
			for _, is := range v.Issues {
				r.st.Gates = append(r.st.Gates, state.Gate{Name: is, Status: state.CheckFail})
				b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelError, Subject: "verify", Text: is})
			}
			for _, f := range vf {
				r.st.Gates = append(r.st.Gates, state.Gate{Name: "could not read " + f.Name, Status: state.CheckWarn})
				b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelWarn, Subject: "verify", Text: f.Error})
			}
			if !v.OK() {
				lvl, text = state.LevelError, "roll complete · verification found issues"
				r.st.Failed = "post-roll verification: " + strings.Join(v.Issues, "; ")
			}
		}
	case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
		lvl, text = state.LevelError, "roll "+strings.ToLower(string(status))
		r.st.Failed = strings.TrimSpace(string(status) + " " + msg)
	default:
		why := "the watch ended"
		if err != nil {
			why = err.Error()
		}
		lvl, text = state.LevelWarn, "stopped watching: "+why
		r.st.Failed = "outcome unknown · the EKS update may still be running"
	}
	took := r.st.EndedAt.Sub(r.st.StartedAt).Round(time.Second)
	key := b.keyOf(r.t)
	b.rollEvent(r, state.Event{Source: state.SourceAWS, Level: lvl, Subject: "DescribeUpdate", Text: string(status), Detail: msg})
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: lvl, Subject: r.st.Nodegroup, Text: text, Detail: took.String()})
	b.emit(state.Event{Cluster: key, Source: state.SourceRoll, Level: lvl, Subject: r.st.Nodegroup, Text: text, Detail: took.String()})
	delete(b.claimed, r.t)
	b.mu.Unlock()
	b.Refresh() // read the nodegroup's new AMI
}

// rollSnapshot copies a roll for State. The caller holds b.mu.
func (b *Backend) rollSnapshot(r *liveRoll) state.Roll {
	st := r.st
	st.Cluster = b.keyOf(r.t)
	st.Events = slices.Clone(r.st.Events)
	st.Gates = slices.Clone(r.st.Gates)
	st.Snapshot.Nodes = slices.Clone(r.st.Snapshot.Nodes)
	for i := range st.Snapshot.Nodes {
		st.Snapshot.Nodes[i].Pressure = slices.Clone(st.Snapshot.Nodes[i].Pressure)
	}
	st.Snapshot.Warnings = slices.Clone(r.st.Snapshot.Warnings)
	st.Pods = map[string][]state.Pod{}
	st.NodePods = map[string]int{}
	// The pods still on each draining node, from the observer's per-node
	// read, for the drain card (a real roll showed only counts).
	for i, n := range st.Snapshot.Nodes {
		st.Snapshot.Nodes[i].PodList = slices.Clone(n.PodList)
		for _, p := range n.PodList {
			ps := state.Pod{Name: p.Name, State: state.PodRunning}
			if p.Terminating {
				ps.State = state.PodTerminating
			}
			st.Pods[n.Name] = append(st.Pods[n.Name], ps)
		}
	}
	// An estimate only from the pace this roll has shown: the time per
	// replaced node so far, times the nodes left.
	if done := st.Replaced(); r.st.Running() && r.viewed && done > 0 && st.Planned > done {
		per := b.now().Sub(r.st.StartedAt) / time.Duration(done)
		st.ETA = per * time.Duration(st.Planned-done)
	}
	return st
}

func levelOfCheck(s state.CheckStatus) state.Level {
	switch s {
	case state.CheckPass:
		return state.LevelOK
	case state.CheckFail:
		return state.LevelError
	case state.CheckWarn:
		return state.LevelWarn
	default:
		return state.LevelInfo
	}
}
