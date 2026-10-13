package fleettest

import (
	"fmt"
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
	// eks.amazonaws.com/nodegroup label carries.
	Nodegroup string
	// ImageID is the node's AMI, which the eks.amazonaws.com/nodegroup-image
	// label carries.
	ImageID string
	// NotReady and Unschedulable are the node's Ready condition, inverted,
	// and its cordon. The zero Node is Ready and schedulable.
	NotReady      bool
	Unschedulable bool
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
	// containers are not ready.
	Phase    string
	NotReady bool
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
	// DisruptionsAllowed, CurrentHealthy, DesiredHealthy, and ExpectedPods
	// are the status. A budget blocks a drain when DisruptionsAllowed is 0.
	DisruptionsAllowed, CurrentHealthy, DesiredHealthy, ExpectedPods int32
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
//	at the start  one new node per old node joins, not Ready
//	at 1/4        the new nodes are Ready, and the old nodes are cordoned
//	at 1/2        the pods of the old nodes run on the new nodes
//	at 3/4        the old nodes are gone
//
// An update with no duration goes through all of them at once. A nodegroup
// with no nodes in the world has no roll.
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
	script   RollScript
	started  time.Time
	duration time.Duration
	image    string
	old, new []string
	stage    int
}

// startRoll plans the roll of ng's nodes for an update. It returns nil for a
// nodegroup with no nodes.
func (w *World) startRoll(c *Cluster, ng *Nodegroup, u *Update, release string) *roll {
	r := &roll{script: u.script.Roll, started: w.now, duration: u.script.After, image: "ami-" + release}
	for _, n := range c.Nodes {
		if n.Nodegroup == ng.Name {
			r.old = append(r.old, n.Name)
		}
	}
	if len(r.old) == 0 {
		return nil
	}
	for i := range r.old {
		r.new = append(r.new, fmt.Sprintf("%s-u%d-%d", ng.Name, w.lastUpdate, i+1))
	}
	return r
}

// advance moves the roll to the stage the clock has reached. The caller
// holds w.mu.
func (r *roll) advance(c *Cluster, nodegroup string, now time.Time) {
	reached := 4
	if r.duration > 0 {
		reached = min(4, 1+int(4*now.Sub(r.started)/r.duration))
	}
	if r.script.StuckDrain {
		reached = min(reached, 2)
	}
	for r.stage < reached {
		r.stage++
		switch r.stage {
		case 1:
			for _, name := range r.new {
				c.Nodes = append(c.Nodes, &Node{Name: name, Nodegroup: nodegroup, ImageID: r.image, NotReady: true, CreatedAt: now, MilliCPU: 2000, MemoryMi: 8192})
			}
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
			moved := 0
			for _, p := range c.Pods {
				if !slices.Contains(r.old, p.Node) {
					continue
				}
				if r.script.PendingPods {
					p.Node, p.Phase, p.NotReady = "", "Pending", false
					continue
				}
				p.Node = r.new[moved%len(r.new)]
				moved++
			}
		case 4:
			c.Nodes = slices.DeleteFunc(c.Nodes, func(n *Node) bool { return slices.Contains(r.old, n.Name) })
		}
	}
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
