package change

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet"
)

// TestBusy is the table of the busy rule: each status of each resource,
// as a target and not, for a standalone command and for an engine, and
// each group that was not read.
func TestBusy(t *testing.T) {
	read := fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected}
	snapshot := func(edit func(s *fleet.Snapshot)) *fleet.Snapshot {
		s := &fleet.Snapshot{
			Cluster:    fleet.Cluster{Name: "prod", Status: "ACTIVE"},
			Nodegroups: []fleet.Nodegroup{{Name: "web", State: fleet.Collected, Status: "ACTIVE"}},
			Addons:     []fleet.Addon{{Name: "vpc-cni", State: fleet.Collected, Status: "ACTIVE"}, {Name: "coredns", State: fleet.Collected, Status: "ACTIVE"}},
			Have:       read,
		}
		edit(s)
		return s
	}
	phase := PhaseRef{Target: Target{Cluster: "prod"}}
	step := func(kind StepKind, name string, action Action, reason Reason) Step {
		return Step{Ref: StepRef{Phase: phase, Kind: kind, Name: name}, Action: action, Reason: reason}
	}
	busy := func(what string) []Blocker {
		return []Blocker{{Kind: BlockerBusy, Message: "prod is busy (" + what + "); nothing was started. Run it again once that finishes"}}
	}
	unread := func(what string) []Blocker {
		return []Blocker{{Kind: BlockerUnread, Message: "could not read " + what + " of prod, so nothing shows that no change is running"}}
	}
	addon := func(status string) func(*fleet.Snapshot) {
		return func(s *fleet.Snapshot) { s.Addons[0].Status = status }
	}
	nodegroup := func(status string) func(*fleet.Snapshot) {
		return func(s *fleet.Snapshot) { s.Nodegroups[0].Status = status }
	}
	cluster := func(status string) func(*fleet.Snapshot) {
		return func(s *fleet.Snapshot) { s.Cluster.Status = status }
	}
	submitCNI := []Step{step(StepAddon, "vpc-cni", Submit, VersionBehind)}
	submitDNS := []Step{step(StepAddon, "coredns", Submit, VersionBehind)}
	rollWeb := []Step{step(StepNodegroup, "web", Submit, AMIOutdated)}

	tests := []struct {
		name    string
		edit    func(*fleet.Snapshot)
		kind    Kind
		ours    []Step
		refuse  []Blocker
		notices []string
	}{
		{name: "nothing in flight", edit: func(*fleet.Snapshot) {}, kind: AddonUpdate, ours: submitCNI},

		// A standalone command.
		{name: "standalone: cluster UPDATING", edit: cluster("UPDATING"), kind: AddonUpdate, ours: submitCNI, refuse: busy("cluster UPDATING")},
		{name: "standalone: cluster CREATING", edit: cluster("CREATING"), kind: NodegroupScale, ours: rollWeb, refuse: busy("cluster CREATING")},
		{name: "standalone: another add-on UPDATING", edit: addon("UPDATING"), kind: AddonUpdate, ours: submitDNS, refuse: busy("add-on vpc-cni UPDATING")},
		{name: "standalone: another add-on CREATING", edit: addon("CREATING"), kind: AddonUpdate, ours: submitDNS, refuse: busy("add-on vpc-cni CREATING")},
		{name: "standalone: another add-on DELETING", edit: addon("DELETING"), kind: NodegroupScale, ours: rollWeb, refuse: busy("add-on vpc-cni DELETING")},
		{name: "standalone: another add-on DEGRADED", edit: addon("DEGRADED"), kind: AddonUpdate, ours: submitDNS},
		{name: "standalone: another add-on UPDATE_FAILED", edit: addon("UPDATE_FAILED"), kind: AddonUpdate, ours: submitDNS},
		{
			name: "standalone: a target that attaches", edit: addon("UPDATING"), kind: AddonUpdate,
			ours: []Step{step(StepAddon, "vpc-cni", Attach, InFlight)},
		},
		{
			name: "standalone: a busy target", edit: addon("UPDATING"), kind: AddonUpdate,
			ours: []Step{step(StepAddon, "vpc-cni", Skip, InFlight)},
		},
		{
			name: "standalone: a target that is skipped as current is not ours", edit: addon("UPDATING"), kind: AddonUpdate,
			ours: []Step{step(StepAddon, "vpc-cni", Skip, AlreadyAtTarget)}, refuse: busy("add-on vpc-cni UPDATING"),
		},
		{name: "standalone: a nodegroup UPDATING", edit: nodegroup("UPDATING"), kind: AddonUpdate, ours: submitCNI, refuse: busy("nodegroup web UPDATING")},
		{name: "standalone: a nodegroup CREATING", edit: nodegroup("CREATING"), kind: AddonUpdate, ours: submitCNI, refuse: busy("nodegroup web CREATING")},
		{name: "standalone: a nodegroup DELETING", edit: nodegroup("DELETING"), kind: AddonUpdate, ours: submitCNI, refuse: busy("nodegroup web DELETING")},
		{name: "standalone: a nodegroup DEGRADED", edit: nodegroup("DEGRADED"), kind: AddonUpdate, ours: submitCNI},
		{
			name: "standalone: a target nodegroup of a scale", edit: nodegroup("UPDATING"), kind: NodegroupScale,
			ours: []Step{step(StepScale, "web", Skip, InFlight)},
		},
		{
			name: "standalone: several", kind: AddonUpdate, ours: submitDNS,
			edit: func(s *fleet.Snapshot) {
				s.Cluster.Status, s.Nodegroups[0].Status, s.Addons[0].Status = "UPDATING", "UPDATING", "DELETING"
			},
			refuse: busy("cluster UPDATING, nodegroup web UPDATING, add-on vpc-cni DELETING"),
		},

		// An engine.
		{name: "engine: cluster UPDATING, not ours", edit: cluster("UPDATING"), kind: testUpgrade, ours: rollWeb, refuse: busy("cluster UPDATING")},
		{
			name: "engine: cluster UPDATING, the control plane attaches", edit: cluster("UPDATING"), kind: testUpgrade,
			ours: []Step{step(StepControlPlane, "", Attach, InFlight)},
		},
		{
			name: "engine: cluster UPDATING, the control plane submits", edit: cluster("UPDATING"), kind: testUpgrade,
			ours: []Step{step(StepControlPlane, "", Submit, VersionBehind)}, refuse: busy("cluster UPDATING"),
		},
		{
			name: "engine: cluster PENDING, the control plane attaches", edit: cluster("PENDING"), kind: testRollback,
			ours: []Step{step(StepControlPlane, "", Attach, InFlight)}, refuse: busy("cluster PENDING"),
		},
		{
			name: "engine: a target nodegroup UPDATING", edit: nodegroup("UPDATING"), kind: testUpgrade,
			ours: []Step{step(StepNodegroup, "web", Attach, InFlight)},
		},
		{
			name: "engine: a target nodegroup DELETING", edit: nodegroup("DELETING"), kind: testUpgrade,
			ours: rollWeb, refuse: busy("nodegroup web DELETING"),
		},
		{
			name: "engine: a target add-on DELETING", edit: addon("DELETING"), kind: testRollback,
			ours: submitCNI, refuse: busy("add-on vpc-cni DELETING"),
		},
		{
			name: "engine: a nodegroup that is not ours", edit: nodegroup("UPDATING"), kind: testUpgrade, ours: submitCNI,
			notices: []string{"nodegroup web is UPDATING; the run does not wait for it"},
		},
		{
			name: "engine: an add-on that is not ours", edit: addon("DELETING"), kind: testUpgrade, ours: rollWeb,
			notices: []string{"add-on vpc-cni is DELETING; the run does not wait for it"},
		},

		// A group that was not read in full.
		{name: "unread: the cluster", edit: func(s *fleet.Snapshot) { s.Have.Cluster = fleet.Failed }, kind: AddonUpdate, ours: submitCNI, refuse: unread("the cluster")},
		{name: "unread: the nodegroups", edit: func(s *fleet.Snapshot) { s.Have.Nodegroups = fleet.Partial }, kind: AddonUpdate, ours: submitCNI, refuse: unread("every nodegroup")},
		{name: "unread: the add-ons", edit: func(s *fleet.Snapshot) { s.Have.Addons = fleet.NotAttempted }, kind: testUpgrade, ours: submitCNI, refuse: unread("every add-on")},
		{
			name: "unread: an add-on, and another one busy", kind: AddonUpdate, ours: submitDNS,
			edit: func(s *fleet.Snapshot) {
				s.Have.Addons = fleet.Partial
				s.Addons[0].Status = "UPDATING"
				s.Addons = append(s.Addons, fleet.Addon{Name: "kube-proxy", State: fleet.Failed})
			},
			refuse: append(unread("every add-on"), busy("add-on vpc-cni UPDATING")...),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refuse, notices := Busy(snapshot(tt.edit), tt.kind, tt.ours)
			if diff := cmp.Diff(tt.refuse, refuse); diff != "" {
				t.Errorf("refusals (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.notices, notices); diff != "" {
				t.Errorf("notices (-want +got):\n%s", diff)
			}
		})
	}
}
