package backend

import (
	"slices"

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
	}
	if snap.Have.Cluster != fleet.Collected {
		c.Status = view.Unknown
		return c
	}
	c.Version = snap.Cluster.Version
	c.Support = apidoc.SupportUnknown
	if snap.Have.Support == fleet.Collected {
		c.Support = snap.Cluster.Support.Tier
	}
	if view.StatusOf(snap.Cluster.Status) == view.Progress {
		c.InFlight = append(c.InFlight, "control plane "+snap.Cluster.Status)
	}
	// A health issue of any part fails the row: the issues that the
	// HEALTH column of "refresh status" counts.
	failed := snap.HealthIssues() > 0 || view.StatusOf(snap.Cluster.Status) == view.Fail
	for _, n := range snap.Nodegroups {
		if n.State != fleet.Collected {
			c.Stale.Unknown++
			continue
		}
		switch view.StatusOf(n.Status) {
		case view.Progress:
			c.InFlight = append(c.InFlight, "nodegroup "+n.Name+" "+n.Status)
		case view.Fail:
			failed = true
		}
		if behind, known := snap.VersionBehind(n); known && behind {
			c.Stale.Behind++
		}
		switch status, known := n.AMIStatus(); {
		case !known:
			c.Stale.Unknown++
		case status == apidoc.AMIOutdated:
			c.Stale.AMIs++
		}
	}
	for _, a := range snap.Addons {
		if a.State != fleet.Collected {
			c.Stale.Unknown++
			continue
		}
		switch view.StatusOf(a.Status) {
		case view.Progress:
			c.InFlight = append(c.InFlight, "add-on "+a.Name+" "+a.Status)
		case view.Fail:
			failed = true
		}
		switch behind, known := a.Behind(); {
		case !known && a.Version != "":
			c.Stale.Unknown++
		case behind:
			c.Stale.Addons++
		}
	}
	slices.Sort(c.InFlight)
	c.Status = overall(c, failed)
	return c
}

// overall is the one status of a row: failed, then changing, then stale,
// then incomplete, else healthy. A cluster out of standard support has
// failed in the sense of the fleet: it needs an upgrade before anything.
func overall(c state.Cluster, failed bool) view.Status {
	switch {
	case failed, c.Support == apidoc.SupportExtended, c.Support == apidoc.SupportUnsupported:
		return view.Fail
	case c.Changing():
		return view.Progress
	case c.Stale.AMIs > 0, c.Stale.Behind > 0, c.Stale.Addons > 0:
		return view.Warn
	case c.Incomplete, c.Stale.Unknown > 0:
		return view.Unknown
	}
	return view.Healthy
}
