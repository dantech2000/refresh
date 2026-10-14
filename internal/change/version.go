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
	v, g, ok := parseVersions(version, goal)
	if !ok {
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

// addonDirection reports whether moving an add-on from the version live
// to goal goes the way of the change, or stays: down for a rollback, and
// up for an upgrade.
func addonDirection(kind Kind, live, goal string) bool {
	order := fleet.CompareAddonVersions(goal, live)
	if kind == ClusterRollback {
		return order <= 0
	}
	return order >= 0
}

// newestAtOrBelow returns the newest of versions that is at or below
// version. ok is false when every one of versions is newer.
func newestAtOrBelow(version string, versions []string) (newest string, ok bool) {
	for _, v := range versions {
		if fleet.CompareAddonVersions(v, version) <= 0 && (!ok || fleet.CompareAddonVersions(v, newest) > 0) {
			newest, ok = v, true
		}
	}
	return newest, ok
}

// adjacent reports whether a control plane at version live can take the
// request of a step to goal: an upgrade from the minor version before the
// goal, and a rollback from the minor version after it.
func adjacent(kind Kind, live, goal string) bool {
	n, ok := minorsLeft(kind, live, goal)
	return ok && n == 1
}

// rollDirection reports whether a roll from the version live to goal goes
// the way of the change: up for an upgrade, and down for a rollback. A
// version that is not known goes no way.
func rollDirection(kind Kind, live, goal string) bool {
	n, ok := minorsLeft(kind, live, goal)
	return ok && n > 0
}

// minorsLeft returns how many minor versions a version at live has to
// move in the direction of the change to reach goal: more than zero
// before the goal, zero at it, and less than zero past it. ok is false
// when a version is not known or the major versions differ.
func minorsLeft(kind Kind, live, goal string) (n int, ok bool) {
	v, g, ok := parseVersions(live, goal)
	if !ok || v.Major != g.Major {
		return 0, false
	}
	if kind == ClusterRollback {
		return v.Minor - g.Minor, true
	}
	return g.Minor - v.Minor, true
}

// parseVersions parses a Kubernetes version and the goal of a step. ok is
// false when either one does not parse.
func parseVersions(version, goal string) (v, g fleet.Version, ok bool) {
	v, err := fleet.ParseVersion(version)
	g, gerr := fleet.ParseVersion(goal)
	return v, g, err == nil && gerr == nil
}
