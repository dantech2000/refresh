package backend

import (
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// need is what a sweep reads of each cluster: what "refresh status" reads.
// Not the update history, which costs one call per update of every
// cluster; the readiness run reads it for one cluster on demand.
var need = fleet.Need{AMI: true, LatestAddons: true, Support: true}

// row derives the fleet row of a snapshot. It runs once per sweep for
// each cluster, so the frames that follow read fields and compute
// nothing.
func row(snap *fleet.Snapshot) state.Cluster {
	c := state.Cluster{
		Key:        state.Key{Region: snap.Cluster.Region, Name: snap.Cluster.Name},
		Snapshot:   snap,
		Incomplete: snap.Incomplete(),
		Findings:   snap.Findings(),
	}
	if snap.Have.Cluster != fleet.Collected {
		c.Status = view.Unknown
		return c
	}
	c.Version = snap.Cluster.Version
	c.Support = c.Findings.Support
	if view.StatusOf(snap.Cluster.Status) == view.Progress {
		c.InFlight = append(c.InFlight, state.InFlight{Kind: apidoc.StepControlPlane, Status: snap.Cluster.Status})
	}
	for _, n := range snap.Nodegroups {
		if n.State == fleet.Collected && view.StatusOf(n.Status) == view.Progress {
			c.InFlight = append(c.InFlight, state.InFlight{Kind: apidoc.StepNodegroup, Name: n.Name, Status: n.Status})
		}
	}
	for _, a := range snap.Addons {
		if a.State == fleet.Collected && view.StatusOf(a.Status) == view.Progress {
			c.InFlight = append(c.InFlight, state.InFlight{Kind: apidoc.StepAddon, Name: a.Name, Status: a.Status})
		}
	}
	slices.SortFunc(c.InFlight, func(a, b state.InFlight) int { return strings.Compare(a.String(), b.String()) })
	c.Status = overall(c)
	return c
}

// overall is the one status of a row: the standing of its findings, the
// rule of "refresh status". A row with something in flight shows the
// change over what the change may bring up to date, and over what is not
// read yet, but not over a health issue or the support tier.
func overall(c state.Cluster) view.Status {
	f := c.Findings
	if c.Changing() && f.HealthIssues == 0 && !f.OutOfStandardSupport() {
		return view.Progress
	}
	return view.StandingStatus(f.Standing())
}
