package change

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/fleet"
)

// kubeletSkew is the supported kubelet version skew: the nodes may trail
// the control plane by at most this many minor versions.
const kubeletSkew = 3

// maxHops caps how many minor versions one plan may cross. It is far
// above any span that EKS supports, so a real target never reaches it. It
// exists so that a target such as 1.2000000000 fails at once, and does
// not allocate one hop for each minor version.
const maxHops = 100

// Hops returns the minor versions that an upgrade from one version to
// another goes through, one after another: 1.31 to 1.33 gives 1.32 and
// 1.33. Equal versions give no hop, which is how the upgrade of a cluster
// that is at its target becomes a catch-up and not an error. EKS upgrades
// one minor version at a time.
func Hops(from, to fleet.Version) ([]fleet.Version, error) {
	switch {
	case from.IsZero() || to.IsZero():
		return nil, &RefusalError{Reason: RefusalVersionUnknown, Message: "the Kubernetes version to upgrade from or to is not known"}
	case from.Major != to.Major:
		return nil, &RefusalError{Reason: RefusalOtherMajor, Message: fmt.Sprintf("target version %s is not a minor version of Kubernetes %d", to, from.Major)}
	case to.Minor < from.Minor:
		return nil, &RefusalError{Reason: RefusalOlderTarget, Message: fmt.Sprintf("target version %s is older than current version %s", to, from)}
	case to.Minor-from.Minor > maxHops:
		return nil, &RefusalError{
			Reason:  RefusalTooManyHops,
			Message: fmt.Sprintf("target version %s is %d minor versions ahead of %s; one plan crosses at most %d", to, to.Minor-from.Minor, from, maxHops),
		}
	}
	// The loop counts the hops and not the minor versions: a loop up to
	// to.Minor would never end for the largest int.
	hops := make([]fleet.Version, 0, to.Minor-from.Minor)
	for i := 1; i <= to.Minor-from.Minor; i++ {
		hops = append(hops, fleet.Version{Major: from.Major, Minor: from.Minor + i})
	}
	return hops, nil
}

// beyondKubeletSkew reports whether a nodegroup trails a control plane
// by more than the supported kubelet skew. It is false when a version is
// not known.
func beyondKubeletSkew(nodegroup, controlPlane fleet.Version) bool {
	if nodegroup.IsZero() || controlPlane.IsZero() || nodegroup.Major != controlPlane.Major {
		return false
	}
	return controlPlane.Minor-nodegroup.Minor > kubeletSkew
}

// nextMinor returns the minor version after v.
func nextMinor(v fleet.Version) fleet.Version {
	return fleet.Version{Major: v.Major, Minor: v.Minor + 1}
}
