package sim

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// Roll timings, in simulated time. A node takes about two minutes end to
// end, close to a real managed-nodegroup roll with max-unavailable 1.
const (
	joinLo, joinHi           = 45 * time.Second, 75 * time.Second
	evictLo, evictHi         = 2 * time.Second, 4 * time.Second
	graceLo, graceHi         = 4 * time.Second, 9 * time.Second
	terminateLo, terminateHi = 6 * time.Second, 12 * time.Second
	pdbHoldLo, pdbHoldHi     = 25 * time.Second, 40 * time.Second
	pdbRetry                 = 10 * time.Second
	describeEvery            = 15 * time.Second
	perNodeEstimate          = 100 * time.Second
	rollEventCap             = 400
)

type rollNode struct {
	name     string
	onTarget bool
	phase    noderoll.Phase
	// due is when a joining node turns Ready, or when an empty draining
	// node leaves the cluster.
	due        time.Time
	pods       int
	drainPods  []*rollPod
	drainTotal int
	daemonSets []string
	nextEvict  time.Time
}

type rollPod struct {
	name    string
	st      state.PodState
	until   time.Time
	retryAt time.Time
	reason  string
}

type roll struct {
	w        *World
	c        *cluster
	st       state.Roll
	nodes    []*rollNode
	launched int
	tracker  *noderoll.Tracker
	seen     int
	events   []state.Event
	nextDesc time.Time
	pdbUsed  bool
	updateID string
	// onEvent forwards node lifecycle events to a parent upgrade.
	onEvent func(state.Event)
	// done runs once when the roll ends.
	done func(ok bool)
}

func (w *World) findNodegroup(c *cluster, name string) *state.Nodegroup {
	for i := range c.Nodegroups {
		if c.Nodegroups[i].Name == name {
			return &c.Nodegroups[i]
		}
	}
	return nil
}

func (w *World) planRoll(c *cluster, ngName string) (state.Plan, error) {
	ng := w.findNodegroup(c, ngName)
	if ng == nil {
		return state.Plan{}, fmt.Errorf("nodegroup %q not found in %s", ngName, c.Name)
	}
	toAMI := latestAMI(c.Version)
	p := state.Plan{
		Action:  state.Action{Kind: state.ActionRoll, Cluster: c.Name, Nodegroup: ngName},
		Title:   "Patch nodegroup · " + c.Name + " / " + ngName,
		Command: "refresh nodegroup update -c " + c.Name + " -n " + ngName,
	}
	if ng.Version != c.Version {
		p.Changes = append(p.Changes, state.Change{Field: "version", From: ng.Version, To: c.Version})
	}
	p.Changes = append(p.Changes, state.Change{Field: "AMI release", From: ng.AMI, To: toAMI})
	est := time.Duration(ng.Nodes) * perNodeEstimate
	p.Facts = []state.Fact{
		{Key: "nodes", Value: strconv.Itoa(ng.Nodes) + " replaced · maxUnavailable 1", Note: "surge via ASG"},
		{Key: "launch template", Value: "unchanged"},
		{Key: "estimate", Value: "~" + roundMinutes(est), Note: "about " + perNodeEstimate.String() + " per node"},
	}
	p.Gates = w.rollGates(c, ng)
	switch {
	case c.Busy != "":
		p.Blocked = c.Name + " is busy: " + c.Busy
	case !ng.NeedsPatch(c.Version):
		p.Blocked = ngName + " already runs the newest AMI for " + c.Version
	}
	return p, nil
}

func (w *World) rollGates(c *cluster, ng *state.Nodegroup) []state.PlanGate {
	gates := []state.PlanGate{
		{Status: state.CheckPass, Text: fmt.Sprintf("nodes Ready %d/%d", ng.Nodes, ng.Nodes)},
		{Status: state.CheckPass, Text: "ASG healthy · capacity for +1 node"},
	}
	if c.scenario.quotaVCPU < 24 {
		gates = append(gates, state.PlanGate{Status: state.CheckWarn, Text: fmt.Sprintf("EC2 quota headroom %d vCPU", c.scenario.quotaVCPU), Note: "tight for a surge node"})
	} else {
		gates = append(gates, state.PlanGate{Status: state.CheckPass, Text: fmt.Sprintf("EC2 quota headroom %d vCPU", c.scenario.quotaVCPU)})
	}
	if c.scenario.tightPDB {
		gates = append(gates, state.PlanGate{Status: state.CheckWarn, Text: "pdb/checkout allows 0 disruptions", Note: "the roll will wait on it"})
	}
	if c.scenario.orphanPods > 0 {
		gates = append(gates, state.PlanGate{Status: state.CheckWarn, Text: fmt.Sprintf("%d pods with no controller", c.scenario.orphanPods), Note: "they will not come back"})
	}
	return gates
}

func roundMinutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 1 {
		return "1m"
	}
	return strconv.Itoa(m) + "m"
}

// startRoll begins a nodegroup roll to the cluster's version and newest AMI.
// parent, when set, names the upgrade that owns the roll; done runs when the
// roll ends. The caller holds w.mu.
func (w *World) startRoll(c *cluster, ngName string, done func(ok bool), parent string) (*roll, error) {
	ng := w.findNodegroup(c, ngName)
	if ng == nil {
		return nil, fmt.Errorf("nodegroup %q not found in %s", ngName, c.Name)
	}
	r := &roll{
		w:        w,
		c:        c,
		tracker:  noderoll.NewTracker(),
		updateID: w.id(),
		done:     done,
		st: state.Roll{
			Cluster:        c.Name,
			Nodegroup:      ngName,
			FromVersion:    ng.Version,
			ToVersion:      c.Version,
			FromAMI:        ng.AMI,
			ToAMI:          latestAMI(c.Version),
			MaxUnavailable: 1,
			StartedAt:      w.now,
			Planned:        ng.Nodes,
			UpgradeOf:      parent,
		},
	}
	for range ng.Nodes {
		r.nodes = append(r.nodes, &rollNode{name: w.nodeName(c.Region), phase: noderoll.PhaseReady, pods: 8 + w.rng.IntN(7)})
	}
	ng.Status = "UPDATING"
	if parent == "" {
		c.Busy = "rolling " + ngName
	}
	r.tracker.Observe(r.snapshotNodes())
	r.aws("UpdateNodegroupVersion", fmt.Sprintf("%s → %s · id=%s", ng.AMI, r.st.ToAMI, r.updateID))
	r.nextDesc = w.now.Add(describeEvery)
	w.emit(state.Event{Cluster: c.Name, Source: state.SourceRoll, Level: state.LevelProgress, Subject: ngName,
		Text: "roll started", Detail: fmt.Sprintf("%d nodes → %s", ng.Nodes, r.st.ToAMI)})
	w.rolls = append(w.rolls, r)
	return r, nil
}

// add records e in the roll's feed. An event that already has a sequence
// number (an AWS call from the fleet log) keeps it.
func (r *roll) add(e state.Event) {
	if e.Seq == 0 {
		e = r.w.stamp(e)
	}
	e.Cluster = r.c.Name
	r.events = appendCapped(r.events, e, rollEventCap)
}

func (r *roll) aws(op, text string) {
	r.add(r.w.api(r.c.Name, op, text))
}

func (r *roll) kube(warning bool, reason, object, msg string) {
	lvl := state.LevelInfo
	if warning {
		lvl = state.LevelWarn
	}
	r.add(state.Event{Source: state.SourceKube, Level: lvl, Subject: object, Text: reason, Detail: msg})
}

func (r *roll) step() {
	if !r.st.Running() {
		return
	}
	now := r.w.now
	if !now.Before(r.nextDesc) {
		r.nextDesc = now.Add(describeEvery)
		r.aws("DescribeUpdate", "id="+r.updateID+" status=InProgress")
	}
	for _, n := range r.nodes {
		if n.phase == noderoll.PhaseJoining && !now.Before(n.due) {
			n.phase = noderoll.PhaseReady
			r.kube(false, "NodeReady", "node/"+n.name, "kubelet is posting ready status")
		}
	}
	if d := r.draining(); d != nil {
		r.drain(d)
	} else if r.joining() == nil {
		old := r.oldReady()
		switch {
		case len(old) == 0:
			r.finish(true)
		case r.launched == r.st.Planned-len(old):
			r.launch()
		default:
			r.startDrain(old[0])
		}
	}
	r.observe()
}

func (r *roll) launch() {
	n := &rollNode{name: r.w.nodeName(r.c.Region), onTarget: true, phase: noderoll.PhaseJoining, due: r.w.now.Add(r.w.between(joinLo, joinHi))}
	r.nodes = append(r.nodes, n)
	r.launched++
	r.aws("autoscaling", "launched "+n.name+" · desired +1 (surge)")
	r.kube(false, "RegisteredNode", "node/"+n.name, "node registered with the cluster")
}

func (r *roll) startDrain(n *rollNode) {
	n.phase = noderoll.PhaseDraining
	workloads := r.c.scenario.workloads
	for i := range n.pods {
		wl := workloads[(i+r.w.rng.IntN(len(workloads)))%len(workloads)]
		p := &rollPod{name: r.w.podName(wl), st: state.PodRunning}
		if wl == "checkout" && r.c.scenario.tightPDB && !r.pdbUsed {
			r.pdbUsed = true
			p.st = state.PodBlocked
			p.until = r.w.now.Add(r.w.between(pdbHoldLo, pdbHoldHi))
			p.reason = "blocked by pdb/checkout (0 disruptions allowed)"
		}
		n.drainPods = append(n.drainPods, p)
	}
	// Keep a blocked pod first so it shows at the top of the pod list.
	slices.SortStableFunc(n.drainPods, func(a, b *rollPod) int {
		if a.st == state.PodBlocked {
			return -1
		}
		if b.st == state.PodBlocked {
			return 1
		}
		return 0
	})
	n.drainTotal = len(n.drainPods)
	n.daemonSets = []string{"aws-node", "kube-proxy", "metrics-agent"}
	n.nextEvict = r.w.now.Add(r.w.between(evictLo, evictHi))
	r.kube(false, "NodeNotSchedulable", "node/"+n.name, "cordoned for the roll")
}

func (r *roll) drain(n *rollNode) {
	now := r.w.now
	live := n.drainPods[:0]
	for _, p := range n.drainPods {
		if p.st == state.PodTerminating && !now.Before(p.until) {
			continue
		}
		live = append(live, p)
	}
	n.drainPods = live
	for _, p := range n.drainPods {
		if p.st != state.PodBlocked || now.Before(p.retryAt) {
			continue
		}
		if !now.Before(p.until) {
			p.st = state.PodRunning
			p.reason = ""
			r.add(state.Event{Source: state.SourceRoll, Level: state.LevelOK, Subject: "pdb", Text: "checkout allows a disruption again"})
			continue
		}
		p.retryAt = now.Add(pdbRetry)
		r.kube(true, "EvictionBlocked", "pod/"+p.name, "Cannot evict pod as it would violate the pod's disruption budget")
		r.add(state.Event{Source: state.SourceRoll, Level: state.LevelWarn, Subject: "pdb", Text: "checkout refused eviction", Detail: "retry in 10s"})
		r.w.emit(state.Event{Cluster: r.c.Name, Source: state.SourceRoll, Level: state.LevelWarn, Subject: r.st.Nodegroup, Text: "pdb/checkout blocks eviction"})
	}
	if len(n.drainPods) == 0 {
		if n.due.IsZero() {
			n.due = now.Add(r.w.between(terminateLo, terminateHi))
			r.aws("autoscaling", "TerminateInstanceInAutoScalingGroup "+n.name)
		} else if !now.Before(n.due) {
			r.remove(n)
		}
		return
	}
	if now.Before(n.nextEvict) {
		return
	}
	n.nextEvict = now.Add(r.w.between(evictLo, evictHi))
	for _, p := range n.drainPods {
		if p.st != state.PodRunning {
			continue
		}
		p.st = state.PodTerminating
		p.until = now.Add(r.w.between(graceLo, graceHi))
		r.kube(false, "Killing", "pod/"+p.name, "Stopping container · evicted from "+n.name)
		if target := r.newestReady(); target != "" {
			wl := p.name[:max(0, len(p.name)-11)]
			r.kube(false, "Scheduled", "pod/"+r.w.podName(wl), "→ "+target)
			r.bumpPods(target)
		}
		if r.w.rng.IntN(9) == 0 {
			r.kube(true, "FailedScheduling", "pod/"+r.w.podName("search"), "0/"+strconv.Itoa(len(r.nodes))+" nodes available: 1 node(s) were unschedulable")
		}
		break
	}
}

func (r *roll) bumpPods(name string) {
	for _, n := range r.nodes {
		if n.name == name {
			n.pods++
		}
	}
}

func (r *roll) remove(n *rollNode) {
	r.nodes = slices.DeleteFunc(r.nodes, func(x *rollNode) bool { return x == n })
	r.kube(false, "RemovingNode", "node/"+n.name, "Node "+n.name+" event: Removing Node from Controller")
}

func (r *roll) draining() *rollNode {
	for _, n := range r.nodes {
		if n.phase == noderoll.PhaseDraining {
			return n
		}
	}
	return nil
}

func (r *roll) joining() *rollNode {
	for _, n := range r.nodes {
		if n.phase == noderoll.PhaseJoining {
			return n
		}
	}
	return nil
}

func (r *roll) oldReady() []*rollNode {
	var out []*rollNode
	for _, n := range r.nodes {
		if !n.onTarget {
			out = append(out, n)
		}
	}
	return out
}

func (r *roll) newestReady() string {
	for i := len(r.nodes) - 1; i >= 0; i-- {
		if n := r.nodes[i]; n.onTarget && n.phase == noderoll.PhaseReady {
			return n.name
		}
	}
	return ""
}

func (r *roll) finish(ok bool) {
	r.st.EndedAt = r.w.now
	ng := r.w.findNodegroup(r.c, r.st.Nodegroup)
	if ng != nil {
		ng.Status = "ACTIVE"
		if ok {
			ng.Version = r.st.ToVersion
			ng.AMI = r.st.ToAMI
			ng.LatestAMI = latestAMI(ng.Version)
		}
	}
	if r.st.UpgradeOf == "" {
		r.c.Busy = ""
	}
	r.aws("DescribeUpdate", "id="+r.updateID+" status=Successful")
	took := r.st.EndedAt.Sub(r.st.StartedAt).Round(time.Second)
	e := state.Event{Source: state.SourceRoll, Level: state.LevelOK, Subject: r.st.Nodegroup, Text: "roll complete",
		Detail: fmt.Sprintf("%d nodes on %s · %s", r.st.Planned, r.st.ToAMI, took)}
	r.add(e)
	e.Cluster = r.c.Name
	r.w.emit(e)
	if r.onEvent != nil {
		r.onEvent(e)
	}
	if r.done != nil {
		r.done(ok)
	}
}

func (r *roll) snapshotNodes() noderoll.Snapshot {
	s := noderoll.Snapshot{}
	for _, n := range r.nodes {
		v := noderoll.NodeView{Name: n.name, OnTarget: n.onTarget, Ready: n.phase != noderoll.PhaseJoining, Phase: n.phase}
		if n.phase == noderoll.PhaseDraining {
			v.Pods = len(n.drainPods)
			v.PodsTotal = n.drainTotal
		}
		s.Nodes = append(s.Nodes, v)
		switch n.phase {
		case noderoll.PhaseDraining:
			s.Draining++
		case noderoll.PhaseJoining:
			s.Joining++
		case noderoll.PhaseReady:
			if n.onTarget {
				s.ReadyTarget++
			}
		}
	}
	slices.SortFunc(s.Nodes, func(a, b noderoll.NodeView) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	s.Total = len(s.Nodes)
	return s
}

// observe feeds the node snapshot to the Tracker and turns new lifecycle
// transitions into feed events.
func (r *roll) observe() {
	r.tracker.Observe(r.snapshotNodes())
	evs := r.tracker.Recent(0)
	if len(evs) < r.seen {
		// The tracker dropped old events at its cap; resync.
		r.seen = len(evs)
	}
	for _, e := range evs[r.seen:] {
		ev := lifecycleEvent(e)
		r.add(ev)
		ev.Cluster = r.c.Name
		if e.Kind != noderoll.EvtJoining {
			r.w.emit(ev)
		}
		if r.onEvent != nil {
			r.onEvent(ev)
		}
	}
	r.seen = len(evs)
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

func (r *roll) snapshot() state.Roll {
	st := r.st
	st.Snapshot = r.snapshotNodes()
	st.Events = slices.Clone(r.events)
	st.Pods = map[string][]state.Pod{}
	st.NodePods = map[string]int{}
	blocked := false
	for _, n := range r.nodes {
		if n.phase != noderoll.PhaseDraining {
			if n.phase == noderoll.PhaseReady {
				st.NodePods[n.name] = n.pods
			}
			continue
		}
		var pods []state.Pod
		for _, p := range n.drainPods {
			reason := p.reason
			if p.st == state.PodTerminating {
				reason = "terminating · grace 30s"
			}
			if p.st == state.PodBlocked {
				blocked = true
			}
			pods = append(pods, state.Pod{Name: p.name, State: p.st, Reason: reason})
		}
		for _, ds := range n.daemonSets {
			pods = append(pods, state.Pod{Name: ds, State: state.PodDaemonSet, Reason: "daemonset · skipped"})
		}
		st.Pods[n.name] = pods
	}
	if st.Running() {
		remaining := len(r.oldReady())
		st.ETA = time.Duration(remaining) * perNodeEstimate
	}
	ready := 0
	for _, n := range st.Snapshot.Nodes {
		if n.Ready {
			ready++
		}
	}
	st.Gates = []state.Gate{
		{Name: fmt.Sprintf("nodes Ready %d/%d", ready, st.Snapshot.Total), Status: state.CheckPass},
		{Name: "ASG in service", Status: state.CheckPass},
		{Name: "no failed pods", Status: state.CheckPass},
	}
	if blocked {
		st.Gates = slices.Insert(st.Gates, 1, state.Gate{Name: "pdb 1 at limit", Status: state.CheckWarn})
	} else {
		st.Gates = slices.Insert(st.Gates, 1, state.Gate{Name: "pdbs allow disruption", Status: state.CheckPass})
	}
	return st
}
