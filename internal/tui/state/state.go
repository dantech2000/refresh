// Package state holds what the TUI draws: the fleet as the last sweep read
// it, the readiness runs, and the feed. A State is immutable: the backend
// builds a new one for each version and never writes to a published one,
// so the model reads it without a lock.
package state

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// Key names one cluster of the fleet.
type Key struct{ Region, Name string }

// ProposalID names a dry run that a dialog shows.
type ProposalID uint64

// State is what the model draws.
type State struct {
	Version  uint64
	Now      time.Time // the clock of the backend at the publish
	ReadOnly bool
	Profile  string
	Context  string
	Sweep    Sweep
	Clusters []Cluster // sorted by region, then name
	// Changes are the changes the UI started or watches: running first,
	// then finished, newest last.
	Changes []Change
	Feed    []Line // the fleet feed, oldest first
}

// Sweep is the last sweep of the fleet, or the one that runs.
type Sweep struct {
	Regions  int
	Answered []string
	Skipped  []string
	Failed   []apidoc.Failure
	// StartedAt is when the sweep began. EndedAt is zero while it runs,
	// and Count how many sweeps have ended.
	StartedAt time.Time
	EndedAt   time.Time
	Count     int
	Interval  time.Duration
	// Error is set when no region answered: the fleet is empty for a
	// reason, and the fleet screen says it.
	Error string
}

// Cluster is one row of the fleet, derived once per sweep.
type Cluster struct {
	Key
	// Snapshot is the read of the last sweep. The backend never changes it
	// after the publish.
	Snapshot *fleet.Snapshot
	Status   view.Status
	Version  fleet.Version
	Support  apidoc.SupportTier
	Stale    Stale
	// InFlight names the resources that the snapshot shows changing, such
	// as "nodegroup ng-a", sorted.
	InFlight   []string
	Incomplete bool
	// Readiness is the last readiness run of the cluster, or nil.
	Readiness *Readiness
	// Change is the running change of this UI on the cluster, own or
	// watched, or 0 for none.
	Change ChangeID
	// Rollback is the rollback window that the last readiness run found,
	// or nil.
	Rollback *fleet.Rollback
}

// Stale counts what is behind in a cluster.
type Stale struct {
	// AMIs counts the nodegroups whose AMI is not the latest, Behind the
	// nodegroups on an older minor than the control plane, and Addons the
	// add-ons with a newer compatible version.
	AMIs, Behind, Addons int
	// Unknown counts the nodegroups and add-ons whose state of staleness
	// could not be read.
	Unknown int
}

// Changing reports whether the snapshot shows something of the cluster in
// flight.
func (c Cluster) Changing() bool { return len(c.InFlight) > 0 }

// Readiness is one readiness run of a cluster.
type Readiness struct {
	StartedAt time.Time
	// EndedAt is zero while the run goes on.
	EndedAt time.Time
	Report  *health.ReadinessReport
	// Err is set when the cluster could not be read at all.
	Err string
}

// Running reports whether the run goes on.
func (r Readiness) Running() bool { return r.EndedAt.IsZero() }

// Line is one line of the fleet feed.
type Line struct {
	At time.Time
	// Cluster is the zero value for a line about the fleet or a region.
	Cluster Key
	Status  view.Status
	Text    string
}
