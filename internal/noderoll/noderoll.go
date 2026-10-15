// Package noderoll observes the roll of a managed nodegroup as it happens:
// which nodes drain, which join, and which are gone. It reads the nodes of
// the cluster, because EKS reports one status for the whole update and
// nothing for a node.
//
// Attach returns an Observer for the nodes of one nodegroup, which gives a
// Snapshot on each call. A Tracker turns successive snapshots into the
// events of the roll. The package imports client-go and nothing of
// refresh, and it draws nothing: internal/view draws the panel.
package noderoll

// The labels that EKS sets on the nodes of a managed nodegroup.
const (
	// LabelNodegroup names the nodegroup of a node.
	LabelNodegroup = "eks.amazonaws.com/nodegroup"
	// LabelImage is the ID of the AMI that a node was launched with.
	LabelImage = "eks.amazonaws.com/nodegroup-image"
)

// Phase is where a node is in a roll.
type Phase string

// The phases.
const (
	// PhaseReady: the node is Ready and takes pods.
	PhaseReady Phase = "Ready"
	// PhaseJoining: the node is not Ready yet.
	PhaseJoining Phase = "Joining"
	// PhaseDraining: the node is cordoned, or has a taint that says it is
	// being removed.
	PhaseDraining Phase = "Draining"
)

// NodeView is one node as a snapshot saw it.
type NodeView struct {
	Name string
	// OnTarget is true for a new node of the roll: see Identity.
	OnTarget bool
	Ready    bool
	Phase    Phase
	// Pods is the number of pods that still have to leave a Draining
	// node, and PodsTotal the number when the node was first seen
	// Draining. Both are 0 for a node that is not Draining, and when the
	// pods could not be read.
	Pods      int
	PodsTotal int
	// PodList names the pods behind Pods.
	PodList []PodView
	// Pressure lists the pressure conditions of the node that are true,
	// such as MemoryPressure. A node can be Ready and under pressure: the
	// phase does not show it.
	Pressure []string
}

// PodView is one pod that a drain has to evict.
type PodView struct {
	// Name is the namespace and the name: "default/web-1".
	Name string
	// Terminating is true for a pod that has a deletion timestamp.
	Terminating bool
}

// Snapshot is the nodes of a nodegroup at one moment, with their counts.
type Snapshot struct {
	// Nodes are sorted by name.
	Nodes []NodeView
	Total int
	// ReadyTarget counts the new nodes that are Ready.
	ReadyTarget int
	Draining    int
	Joining     int
	// Warnings are the recent Warning events about the nodes of the
	// nodegroup: why a node is stuck, which the phases cannot say.
	Warnings []WarnEvent
	// WarningsCapped is the number of Warning events that were read when
	// the read stopped at its page cap, and 0 when every event was read.
	// Warnings may then miss events beyond the cap.
	WarningsCapped int
}

// WarnEvent is a Warning event about a node of the nodegroup.
type WarnEvent struct {
	// Node is the node that the event is about.
	Node string
	// Object is the involved object, as "Kind/name".
	Object string
	// Reason is the reason of the event, such as FailedDraining.
	Reason  string
	Message string
}
