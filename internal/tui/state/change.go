package state

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// ChangeID names one change that the UI started or watches.
type ChangeID uint64

// ActionKind names what a proposal would do.
type ActionKind string

// The actions a user can propose.
const (
	ActionRoll     ActionKind = "NodegroupRoll"
	ActionAddons   ActionKind = "AddonUpdate"
	ActionUpgrade  ActionKind = "ClusterUpgrade"
	ActionRollback ActionKind = "ClusterRollback"
)

// Action is what a key proposes: the kind, the cluster, and the parts
// that the kind takes.
type Action struct {
	Kind    ActionKind
	Cluster Key
	// Nodegroup is the one nodegroup of a roll.
	Nodegroup string
	// To is the version of an upgrade: the next minor, or the live version
	// for a catch-up of the nodegroups and the add-ons.
	To fleet.Version
	// Only limits an upgrade to these parts, as --only does. Empty means
	// every part.
	Only []change.Part
}

// Proposal is a dry run that the dialog shows. Starting it is consent to
// this plan and to no other: the backend passes it to the run as the
// reviewed plan.
type Proposal struct {
	ID     ProposalID
	Action Action
	Plan   *change.Plan
	Doc    apidoc.Plan
	// Command is the CLI command that makes the same change, with the
	// profile and the Kubernetes access of this UI.
	Command string
	// Blocked says that the plan cannot run; the dialog offers no start.
	Blocked bool
}

// Change is one handle as the backend last read it.
type Change struct {
	ID ChangeID
	change.View
	// Log is the whole log of the handle as the backend has read it,
	// oldest first, each entry with its Seq. It is append-only across
	// versions: a pane frozen at a Seq shows the same lines on every later
	// version.
	Log []change.LogEntry
	// Nodes is the node lifecycle feed of each roll step: every tracker
	// event that the latest node view carried.
	Nodes map[change.StepRef][]noderoll.Event
}

// Running reports whether the change has not finished.
func (c Change) Running() bool { return c.State != change.Finished }
