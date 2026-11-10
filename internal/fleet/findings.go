package fleet

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// Standing is the one judgement of a cluster of the fleet.
type Standing uint8

// The standings, from the best to the worst.
const (
	// StandingCurrent: nothing is out of date, and everything was read.
	StandingCurrent Standing = iota
	// StandingIncomplete: nothing that was read is out of date, but a read
	// failed.
	StandingIncomplete
	// StandingNeedsAttention: something is out of date or has a health
	// issue, or the cluster is in extended support.
	StandingNeedsAttention
	// StandingUnsupported: the cluster is past the end of extended support.
	StandingUnsupported
)

func (s Standing) String() string {
	switch s {
	case StandingCurrent:
		return "Current"
	case StandingIncomplete:
		return "Incomplete"
	case StandingNeedsAttention:
		return "NeedsAttention"
	case StandingUnsupported:
		return "Unsupported"
	}
	return "Standing(?)"
}

// Findings is what is out of date, unhealthy, or unread in one cluster.
// It is the one rule for a row of the fleet: "refresh status" and the
// fleet view of "refresh ui" both read it.
type Findings struct {
	// Support is the support tier, or apidoc.SupportUnknown when it was
	// not read.
	Support apidoc.SupportTier
	// AMIs counts the nodegroups with an AMI status, StaleAMIs those whose
	// AMI is not the latest, and UncomparedAMIs those with nothing to
	// compare: no instance, or an AMI type with no recommended AMI.
	AMIs, StaleAMIs, UncomparedAMIs int
	// OldestStaleAMI is when the oldest outdated AMI was created, or zero
	// when no creation time was read.
	OldestStaleAMI time.Time
	// UnreadAMIs counts the nodegroups with no AMI status because a read
	// failed.
	UnreadAMIs int
	// BehindNodegroups counts the nodegroups on an older Kubernetes minor
	// than the control plane.
	BehindNodegroups int
	// BehindAddons names the add-ons with a newer compatible version, in
	// the order of the snapshot.
	BehindAddons []string
	// UnreadAddons counts the add-ons whose newest version is not known
	// because a read failed.
	UnreadAddons int
	// HealthIssues counts the health issues that EKS reports for the
	// control plane, the nodegroups, and the add-ons that were read. An
	// add-on that is DEGRADED with no issue listed counts as one.
	HealthIssues int
	// Incomplete reports that a read of the cluster failed.
	Incomplete bool
}

// Findings judges the snapshot. It reads only what the document of the
// snapshot shows, so a count here is a count of "refresh status -o json".
func (s *Snapshot) Findings() Findings {
	f := Findings{Support: apidoc.SupportUnknown, Incomplete: s.Incomplete()}
	if s.Have.Support == Collected {
		f.Support = s.Cluster.Support.Tier
	}
	if s.Have.Cluster == Collected {
		f.HealthIssues = len(s.Cluster.HealthIssues)
	}
	if s.Have.Nodegroups.Listed() {
		for _, n := range s.Nodegroups {
			f.nodegroup(s, n)
		}
	}
	if s.Have.Addons.Listed() {
		for _, a := range s.Addons {
			f.addon(a)
		}
	}
	return f
}

// nodegroup adds the findings of one nodegroup.
func (f *Findings) nodegroup(s *Snapshot, n Nodegroup) {
	if n.State != Collected {
		f.UnreadAMIs++
		return
	}
	f.HealthIssues += len(n.HealthIssues)
	if behind, known := s.VersionBehind(n); known && behind {
		f.BehindNodegroups++
	}
	status, known := n.AMIStatus()
	if !known || n.AMI.State == NotRequested {
		// No AMI status. It is unread when a read of the nodegroup failed.
		if failed(n.AMI.State) || failed(n.InstancesState) || failed(n.ReadyState) || failed(n.WorkloadsState) {
			f.UnreadAMIs++
		}
		return
	}
	f.AMIs++
	switch status {
	case apidoc.AMIOutdated:
		f.StaleAMIs++
		if t := n.AMI.CurrentCreatedAt; !t.IsZero() && (f.OldestStaleAMI.IsZero() || t.Before(f.OldestStaleAMI)) {
			f.OldestStaleAMI = t
		}
	case apidoc.AMIUnknown:
		f.UncomparedAMIs++
	}
}

// addon adds the findings of one add-on.
func (f *Findings) addon(a Addon) {
	if a.State != Collected {
		f.UnreadAddons++
		return
	}
	switch {
	case len(a.Issues) > 0:
		f.HealthIssues += len(a.Issues)
	case a.Status == "DEGRADED":
		f.HealthIssues++
	}
	if a.LatestState != Collected {
		if failed(a.LatestState) {
			f.UnreadAddons++
		}
		return
	}
	if behind, known := a.Behind(); known && behind {
		f.BehindAddons = append(f.BehindAddons, a.Name)
	}
}

// Stale counts what there is to bring up to date: the stale AMIs, the
// nodegroups behind the control plane, and the add-ons behind.
func (f Findings) Stale() int {
	return f.StaleAMIs + f.BehindNodegroups + len(f.BehindAddons)
}

// NeedsAttention reports whether something is out of date or has a health
// issue.
func (f Findings) NeedsAttention() bool {
	return f.Stale() > 0 || f.HealthIssues > 0
}

// OutOfStandardSupport reports whether the cluster left standard support:
// it is in extended support, or unsupported.
func (f Findings) OutOfStandardSupport() bool {
	return f.Support == apidoc.SupportExtended || f.Support == apidoc.SupportUnsupported
}

// Standing is the judgement of the cluster. The first row that matches
// decides:
//
//  1. Unsupported: past the end of extended support.
//  2. NeedsAttention: in extended support, something out of date, or a
//     health issue.
//  3. Incomplete: a read failed.
//  4. Current.
func (f Findings) Standing() Standing {
	switch {
	case f.Support == apidoc.SupportUnsupported:
		return StandingUnsupported
	case f.OutOfStandardSupport(), f.NeedsAttention():
		return StandingNeedsAttention
	case f.Incomplete:
		return StandingIncomplete
	}
	return StandingCurrent
}
