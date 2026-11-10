package fleettest

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// The types in this file are the Kubernetes side of a cluster: what refresh
// reads through the Kubernetes API for its health gates, its post-roll
// checks, and its live view. kubetest serves them.

// Node is a Kubernetes node of a managed nodegroup.
type Node struct {
	Name string
	// Nodegroup is the managed nodegroup the node belongs to, which the
	// eks.amazonaws.com/nodegroup label carries. "" is a node of no
	// managed nodegroup, such as a self-managed, Karpenter, Fargate, or
	// hybrid node: it has no such label.
	Nodegroup string
	// Labels are more labels of the node, such as
	// eks.amazonaws.com/compute-type: fargate or karpenter.sh/nodepool.
	Labels map[string]string
	// ImageID is the node's AMI, which the eks.amazonaws.com/nodegroup-image
	// label carries.
	ImageID string
	// NotReady and Unschedulable are the node's Ready condition, inverted,
	// and its cordon. The zero Node is Ready and schedulable. ReadyReason
	// is the reason of the Ready condition: KubeletReady for a Ready node,
	// and KubeletNotReady for a NotReady one, unless set (a node whose
	// kubelet stopped posting is NodeStatusUnknown).
	NotReady      bool
	Unschedulable bool
	ReadyReason   string
	// MilliCPU and MemoryMi are the node's allocatable capacity. They
	// default to 2000 and 8192. UsedMilliCPU and UsedMemoryMi are what the
	// metrics API reports as in use.
	MilliCPU, MemoryMi         int64
	UsedMilliCPU, UsedMemoryMi int64
	// CreatedAt defaults to the world's time at the Edit that added the
	// node.
	CreatedAt time.Time
}

// Pod is a Kubernetes pod.
type Pod struct {
	Namespace string
	Name      string
	// Node is the node the pod runs on, or "" for a pod that is not
	// scheduled.
	Node   string
	Labels map[string]string
	// Phase defaults to Running. NotReady marks a Running pod whose
	// containers are not ready. Reason is the status reason of a Failed
	// pod, such as Evicted, and DisruptionTarget sets the condition of a
	// pod that the kubelet ended for a node shutdown. Deleting gives the
	// pod a deletion timestamp.
	Phase            string
	NotReady         bool
	Reason           string
	DisruptionTarget bool
	Deleting         bool
	// CreatedAt defaults to the world's time at the Edit that added the
	// pod.
	CreatedAt time.Time
}

// PDB is a PodDisruptionBudget, with the status its controller reports.
type PDB struct {
	Namespace string
	Name      string
	// Selector selects the pods by label. An empty selector selects every
	// pod of the namespace.
	Selector map[string]string
	// MinAvailable is the budget, as a pod count.
	MinAvailable int32
	// MaxUnavailable, when not nil, is the budget in place of
	// MinAvailable: the spec has maxUnavailable and no minAvailable.
	// maxUnavailable: 0 allows no voluntary disruption at all.
	MaxUnavailable *int32
	// DisruptionsAllowed, CurrentHealthy, DesiredHealthy, and ExpectedPods
	// are the status. A budget blocks a drain when DisruptionsAllowed is 0
	// or less, or when a rule of evicts below refuses its pods whatever
	// the count.
	DisruptionsAllowed, CurrentHealthy, DesiredHealthy, ExpectedPods int32
	// Unsynced leaves the observed generation behind the generation, and
	// SyncFailed sets the DisruptionAllowed condition to SyncFailed: the
	// controller could not compute the status. AlwaysAllow sets the
	// unhealthy pod eviction policy. NoSelector gives the budget no
	// selector at all, which matches no pod.
	Unsynced    bool
	SyncFailed  bool
	AlwaysAllow bool
	NoSelector  bool
	// DisruptedPods is the number of evictions that the controller has
	// not confirmed yet (status.disruptedPods). Above 2000 the eviction
	// API refuses every pod of the budget.
	DisruptedPods int
}

// Deployment is a Kubernetes deployment, as far as refresh reads one.
type Deployment struct {
	Namespace string
	Name      string
	Replicas  int32
	// Selector selects the deployment's pods.
	Selector map[string]string
}

// Event is a Kubernetes event.
type Event struct {
	Namespace string
	Name      string
	// Type is Normal or Warning.
	Type    string
	Reason  string
	Message string
	// Kind and Object name what the event is about, such as Node and a
	// node name.
	Kind   string
	Object string
	At     time.Time
}

// RollScript says what happens to the nodes and pods of a nodegroup while a
// version update of it runs. The zero value is a clean roll.
//
// A roll moves through four stages, at fixed shares of the update's
// duration (UpdateScript.After), as the world's clock advances:
//
//	at the start  one new node per old node joins, not Ready, and one new
//	              instance per old instance is launched
//	at 1/4        the new nodes are Ready, and the old nodes are cordoned
//	at 1/2        the pods of the old nodes are replaced by pods with new
//	              names on the new nodes
//	at 3/4        the old nodes and the old instances are gone
//
// An update with no duration goes through all of them at once.
//
// The drain at 1/2 evicts the pods as the Kubernetes eviction API does
// (pkg/registry/core/pod/storage/eviction.go): a pod on an old node that a
// PodDisruptionBudget of its namespace protects, whose budget allows no
// disruption, blocks it (see evictionBlocked). An update without force
// then stops after the cordon, and ends Failed with PodEvictionFailure when
// its duration ends, as EKS does after its 15 minutes of retries
// (https://docs.aws.amazon.com/eks/latest/userguide/managed-node-update-behavior.html).
// A budget that allows the disruption again before then lets the roll go
// on. An update with force deletes the pods whatever the budgets say
// (https://docs.aws.amazon.com/eks/latest/APIReference/API_UpdateNodegroupVersion.html).
//
// The new nodes and instances run LatestImage of the update's Kubernetes
// version, which is the image the world's SSM parameters recommend, so what
// EC2, the node labels, and SSM say after a roll agree. The instances are
// those of the nodegroup's first Auto Scaling group; each launch and
// termination is a scaling activity. A nodegroup with no nodes and no
// instances in the world has no roll.
type RollScript struct {
	// StuckDrain stops the roll after the old nodes are cordoned: their pods
	// stay, and the old nodes stay. Pair it with a Failed result to model a
	// PodEvictionFailure.
	StuckDrain bool
	// NodesNeverReady keeps the new nodes NotReady.
	NodesNeverReady bool
	// PendingPods leaves the pods of the old nodes Pending and unscheduled
	// when they are evicted.
	PendingPods bool
}

// roll is the state of a nodegroup roll in progress.
type roll struct {
	script RollScript
	// force is the update's force flag: the drain ignores the budgets.
	force bool
	// blocked are the old nodes whose drain a budget blocked at the last
	// advance, or nil.
	blocked  []string
	started  time.Time
	duration time.Duration
	// number is the update's number, which names what the roll creates.
	number    int
	nodegroup string
	// image is the AMI of the new nodes and instances.
	image string
	// old and new are the names of the nodes that leave and that join.
	old, new []string
	// group is the Auto Scaling group whose instances are replaced, and
	// oldInstances and newInstances are their IDs.
	group                      string
	oldInstances, newInstances []string
	stage                      int
}

// startRoll plans the roll of ng for an update: of its Kubernetes nodes,
// and of the instances of its first Auto Scaling group. It returns nil for
// a nodegroup that has neither.
func (w *World) startRoll(c *Cluster, ng *Nodegroup, u *Update, version string) *roll {
	r := &roll{
		script: u.script.Roll, force: u.Force, started: w.now, duration: u.script.After,
		number: w.lastUpdate, nodegroup: ng.Name, image: LatestImage(version),
	}
	for _, n := range c.Nodes {
		if n.Nodegroup == ng.Name {
			r.old = append(r.old, n.Name)
		}
	}
	for i := range r.old {
		r.new = append(r.new, fmt.Sprintf("%s-u%d-%d", ng.Name, r.number, i+1))
	}
	if len(ng.AutoScalingGroups) > 0 {
		r.group = ng.AutoScalingGroups[0]
		for _, g := range w.state.AutoScalingGroups {
			if g.Name == r.group && Visible(g.Region, c.Region) {
				r.oldInstances = slices.Clone(g.InstanceIDs)
			}
		}
		for i := range r.oldInstances {
			r.newInstances = append(r.newInstances, fmt.Sprintf("i-%s-u%d-%d", ng.Name, r.number, i+1))
		}
	}
	if len(r.old) == 0 && len(r.oldInstances) == 0 {
		return nil
	}
	return r
}

// advance moves the roll to the stage the clock has reached. The caller
// holds w.mu.
func (r *roll) advance(s *State, c *Cluster, now time.Time) {
	reached := 4
	if r.duration > 0 {
		reached = min(4, 1+int(4*now.Sub(r.started)/r.duration))
	}
	if r.script.StuckDrain {
		reached = min(reached, 2)
	}
	for r.stage < reached {
		if r.stage == 2 && !r.force {
			if r.blocked = r.evictionBlocked(c); len(r.blocked) > 0 {
				return
			}
		}
		r.stage++
		switch r.stage {
		case 1:
			for _, name := range r.new {
				c.Nodes = append(c.Nodes, &Node{Name: name, Nodegroup: r.nodegroup, ImageID: r.image, NotReady: true, CreatedAt: now, MilliCPU: 2000, MemoryMi: 8192})
			}
			r.launch(s, now)
		case 2:
			for _, n := range c.Nodes {
				switch {
				case slices.Contains(r.new, n.Name):
					n.NotReady = r.script.NodesNeverReady
				case slices.Contains(r.old, n.Name):
					n.Unschedulable = true
				}
			}
		case 3:
			r.evict(c, now)
		case 4:
			c.Nodes = slices.DeleteFunc(c.Nodes, func(n *Node) bool { return slices.Contains(r.old, n.Name) })
			r.terminate(s, now)
		}
	}
}

// evictionBlocked returns the old nodes that hold a pod the eviction API
// would refuse to evict. The rules are those of the API:
//
//   - a pod that is Pending, Succeeded, Failed, or being deleted ignores
//     every budget;
//   - a pod that more than one budget selects cannot be evicted at all;
//   - a pod that is not ready is evicted when its budget's policy is
//     AlwaysAllow, or, by default, when the budget has CurrentHealthy at
//     or above a DesiredHealthy above zero;
//   - otherwise the budget must have a status for its current generation
//     (not Unsynced), no more than 2000 DisruptedPods, and
//     DisruptionsAllowed above zero. A SyncFailed budget allows none: the
//     disruption controller writes 0 when it fails.
//
// One simplification: each pod is checked against the budget as it is, so
// two evictions under a budget that allows one both pass. The world
// replaces an evicted pod at once and Ready, so the budget's controller
// would allow the next one soon after.
func (r *roll) evictionBlocked(c *Cluster) []string {
	var nodes []string
	for _, p := range c.Pods {
		if !slices.Contains(r.old, p.Node) || slices.Contains(nodes, p.Node) {
			continue
		}
		if p.Deleting || p.Phase == "Pending" || p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		var budgets []PDB
		for _, b := range c.PDBs {
			if b.selects(p) {
				budgets = append(budgets, b)
			}
		}
		if len(budgets) == 0 {
			continue
		}
		if len(budgets) > 1 || !budgets[0].evicts(p) {
			nodes = append(nodes, p.Node)
		}
	}
	return nodes
}

// selects reports whether the budget's selector selects the pod. An empty
// selector selects every pod of the namespace, and no selector none.
func (b PDB) selects(p *Pod) bool {
	if b.NoSelector || b.Namespace != p.Namespace {
		return false
	}
	for k, v := range b.Selector {
		if p.Labels[k] != v {
			return false
		}
	}
	return true
}

// evicts reports whether the eviction API evicts a pod that this budget
// alone protects. See evictionBlocked.
func (b PDB) evicts(p *Pod) bool {
	if p.NotReady && (b.AlwaysAllow || b.CurrentHealthy >= b.DesiredHealthy && b.DesiredHealthy > 0) {
		return true
	}
	return !b.Unsynced && !b.SyncFailed && b.DisruptedPods <= maxDisruptedPods && b.DisruptionsAllowed > 0
}

// maxDisruptedPods is MaxDisruptedPodSize of the eviction API
// (kubernetes/kubernetes pkg/registry/core/pod/storage/eviction.go).
const maxDisruptedPods = 2000

// evictionFailure is the error of an update whose drain the budgets kept
// blocked until it ended, in the words EKS uses.
func (r *roll) evictionFailure() UpdateError {
	return UpdateError{
		Code:        "PodEvictionFailure",
		Message:     "Reached max retries while trying to evict pods from nodes in node group " + r.nodegroup,
		ResourceIDs: slices.Clone(r.blocked),
	}
}

// evict replaces each pod of the old nodes, as its controller would: the
// old pod is gone, and a pod with a new name runs on a new node, or is
// Pending when the script says so. A roll with no new node leaves the
// replacements Pending.
func (r *roll) evict(c *Cluster, now time.Time) {
	moved := 0
	for _, p := range c.Pods {
		if !slices.Contains(r.old, p.Node) {
			continue
		}
		p.Name = fmt.Sprintf("%s-u%d", p.Name, r.number)
		p.CreatedAt, p.NotReady = now, false
		if r.script.PendingPods || len(r.new) == 0 {
			p.Node, p.Phase = "", "Pending"
			continue
		}
		p.Node = r.new[moved%len(r.new)]
		moved++
	}
}

// launch adds the new instances to the roll's Auto Scaling group, with the
// new image. Each takes the type, zone, tags, and CPU metrics of the
// instance it replaces.
func (r *roll) launch(s *State, now time.Time) {
	for i, id := range r.newInstances {
		old := r.oldInstances[i]
		for _, in := range s.Instances {
			if in.ID == old {
				in.ID, in.ImageID, in.LaunchTime, in.State = id, r.image, now, ""
				in.Tags = maps.Clone(in.Tags)
				s.Instances = append(s.Instances, in)
				break
			}
		}
		for _, m := range s.Metrics {
			if m.Dimensions["InstanceId"] == old {
				m.Dimensions, m.Values = map[string]string{"InstanceId": id}, slices.Clone(m.Values)
				s.Metrics = append(s.Metrics, m)
			}
		}
		r.activity(s, now, "launch", i, "Launching a new EC2 instance: "+id)
	}
	for i := range s.AutoScalingGroups {
		if s.AutoScalingGroups[i].Name == r.group {
			s.AutoScalingGroups[i].InstanceIDs = slices.Concat(s.AutoScalingGroups[i].InstanceIDs, r.newInstances)
		}
	}
}

// terminate removes the old instances from the group and from EC2.
func (r *roll) terminate(s *State, now time.Time) {
	gone := func(id string) bool { return slices.Contains(r.oldInstances, id) }
	for i, id := range r.oldInstances {
		r.activity(s, now, "terminate", i, "Terminating EC2 instance: "+id)
	}
	for i := range s.AutoScalingGroups {
		if s.AutoScalingGroups[i].Name == r.group {
			s.AutoScalingGroups[i].InstanceIDs = slices.DeleteFunc(slices.Clone(s.AutoScalingGroups[i].InstanceIDs), gone)
		}
	}
	s.Instances = slices.DeleteFunc(s.Instances, func(in Instance) bool { return gone(in.ID) })
	s.Metrics = slices.DeleteFunc(s.Metrics, func(m Metric) bool { return gone(m.Dimensions["InstanceId"]) })
}

// activity records a successful scaling activity of the roll's group,
// newest first, as Auto Scaling lists them.
func (r *roll) activity(s *State, now time.Time, kind string, i int, description string) {
	a := ScalingActivity{
		ID: fmt.Sprintf("act-u%d-%s-%d", r.number, kind, i+1), Group: r.group,
		Status: "Successful", Description: description, Start: now,
	}
	s.ScalingActivities = slices.Insert(s.ScalingActivities, 0, a)
}

// applyDefaults fills the Kubernetes fields that have a documented default.
func (c *Cluster) applyKubeDefaults(now time.Time) {
	setDefault(&c.Endpoint, "https://"+c.Name+".eks.fake.invalid")
	for _, n := range c.Nodes {
		setDefault(&n.MilliCPU, 2000)
		setDefault(&n.MemoryMi, 8192)
		setDefault(&n.CreatedAt, now)
	}
	for _, p := range c.Pods {
		setDefault(&p.Phase, "Running")
		setDefault(&p.CreatedAt, now)
	}
}
