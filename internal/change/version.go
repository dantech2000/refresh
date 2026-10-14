package change

import "github.com/dantech2000/refresh/internal/fleet"

// atLeast reports whether a is b or newer. A version that is not known
// is at least nothing, and nothing is at least it.
func atLeast(a, b fleet.Version) bool {
	return !a.IsZero() && !b.IsZero() && a.Compare(b) >= 0
}

// versionDone reports whether a Kubernetes version meets the goal of a
// step, by the direction of the change: at the goal or above it for an
// upgrade, and at the goal or below it for a rollback. A version that is
// not known is never done.
func versionDone(kind Kind, version, goal string) bool {
	v, err := fleet.ParseVersion(version)
	g, gerr := fleet.ParseVersion(goal)
	if err != nil || gerr != nil {
		return false
	}
	if kind == ClusterRollback {
		return atLeast(g, v)
	}
	return atLeast(v, g)
}

// addonDone reports whether an add-on version meets the goal of a step
// with no further read: at the goal or newer for an upgrade, and exactly
// the goal for a rollback. A rollback also keeps a version that the
// catalogue of its target lists, which the step itself reads.
func addonDone(kind Kind, version, goal string) bool {
	if version == "" || goal == "" {
		return false
	}
	order := fleet.CompareAddonVersions(version, goal)
	if kind == ClusterRollback {
		return order == 0
	}
	return order >= 0
}

// adjacent reports whether a control plane at version live can take the
// request of a step to goal: an upgrade from the minor version before the
// goal, and a rollback from the minor version after it.
func adjacent(kind Kind, live, goal string) bool {
	v, err := fleet.ParseVersion(live)
	g, gerr := fleet.ParseVersion(goal)
	if err != nil || gerr != nil || v.Major != g.Major {
		return false
	}
	if kind == ClusterRollback {
		return v.Minor == g.Minor+1
	}
	return v.Minor+1 == g.Minor
}

// rollDirection reports whether a roll from the version live to goal goes
// the way of the change: up for an upgrade, and down for a rollback. A
// version that is not known goes no way.
func rollDirection(kind Kind, live, goal string) bool {
	v, err := fleet.ParseVersion(live)
	g, gerr := fleet.ParseVersion(goal)
	if err != nil || gerr != nil || v.Major != g.Major {
		return false
	}
	if kind == ClusterRollback {
		return v.Minor > g.Minor
	}
	return v.Minor < g.Minor
}
