package noderoll

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A fake cluster goes through the roll of a managed nodegroup, and the
// observer reports each node on its way: an old node drains and is gone,
// a new node joins and is Ready.
func TestObserver_RollTransitions(t *testing.T) {
	// Three old nodes, and one node of another nodegroup, which the label
	// selector leaves out.
	other := mkNode("ip-other", newAMI, true, false)
	other.Labels[LabelNodegroup] = "general"
	client := fake.NewClientset(
		mkNode("ip-1", oldAMI, true, false),
		mkNode("ip-2", oldAMI, true, false),
		mkNode("ip-3", oldAMI, true, false),
		other,
	)
	obs := listing(t, client, Identity{TargetAMI: newAMI}, newClock())

	// Before the roll.
	s := snapshot(t, obs)
	if s.Total != 3 {
		t.Fatalf("Total = %d, want 3: ip-other is of another nodegroup", s.Total)
	}
	if s.ReadyTarget != 0 {
		t.Fatalf("ReadyTarget = %d, want 0: every node is old", s.ReadyTarget)
	}
	for _, n := range s.Nodes {
		if n.Phase != PhaseReady || n.OnTarget {
			t.Fatalf("node %s = %+v, want Ready and old", n.Name, n)
		}
	}

	// A new node joins, and an old one is cordoned.
	createNode(t, client, mkNode("ip-4", newAMI, false, false))
	cordon(t, client, "ip-1")
	s = snapshot(t, obs)
	if s.Total != 4 {
		t.Fatalf("Total = %d, want 4", s.Total)
	}
	if s.Draining != 1 || s.Joining != 1 || s.ReadyTarget != 0 {
		t.Fatalf("draining %d, joining %d, new and ready %d; want 1, 1, 0", s.Draining, s.Joining, s.ReadyTarget)
	}
	if got := phaseOf(s, "ip-1"); got != PhaseDraining {
		t.Fatalf("ip-1 is %s, want Draining", got)
	}
	if got := phaseOf(s, "ip-4"); got != PhaseJoining {
		t.Fatalf("ip-4 is %s, want Joining", got)
	}

	// The new node is Ready, and the drained node is gone.
	setReady(t, client, "ip-4")
	deleteNode(t, client, "ip-1")
	s = snapshot(t, obs)
	if s.Total != 3 {
		t.Fatalf("Total = %d, want 3: ip-1 is gone", s.Total)
	}
	if s.ReadyTarget != 1 || s.Draining != 0 || s.Joining != 0 {
		t.Fatalf("new and ready %d, draining %d, joining %d; want 1, 0, 0", s.ReadyTarget, s.Draining, s.Joining)
	}
	if v := nodeOf(s, "ip-4"); !v.OnTarget || v.Phase != PhaseReady {
		t.Fatalf("ip-4 = %+v, want new and Ready", v)
	}
	// The nodes of a snapshot are sorted by name.
	for i := 1; i < len(s.Nodes); i++ {
		if s.Nodes[i-1].Name > s.Nodes[i].Name {
			t.Errorf("the nodes are not sorted: %s before %s", s.Nodes[i-1].Name, s.Nodes[i].Name)
		}
	}
}

// Found on a real cluster: a roll that was attached to a minute after it
// began counted the node that EKS had launched already as old. Only the
// nodes that were created before the start of the roll are old.
func TestStartedAtCountsLaunchedNodesAsNew(t *testing.T) {
	start := time.Date(2026, 9, 30, 12, 27, 23, 0, time.UTC)
	cordoned := versioned("old-2", "", start.Add(-time.Hour))
	cordoned.Spec.Unschedulable = true
	client := fake.NewClientset(
		versioned("old-1", "", start.Add(-time.Hour)),
		cordoned,
		versioned("launched", "", start.Add(44*time.Second)),
	)
	obs := listing(t, client, Identity{StartedAt: start}, newClock())
	snap := snapshot(t, obs)
	if !nodeOf(snap, "launched").OnTarget || nodeOf(snap, "old-1").OnTarget || nodeOf(snap, "old-2").OnTarget {
		t.Fatalf("nodes = %+v, want only launched new", snap.Nodes)
	}
}

// The time of creation can mislead: the clocks differ, or the nodegroup
// scales out during the roll. For a roll between two versions the kubelet
// version decides.
func TestTargetVersionBeatsTheBaseline(t *testing.T) {
	start := time.Date(2026, 9, 30, 12, 27, 23, 0, time.UTC)
	client := fake.NewClientset(
		versioned("old-after", "v1.34.6-eks-3c1d8b1", start.Add(2*time.Second)),   // the old version, created just after
		versioned("new-before", "v1.35.3-eks-7a2b9c0", start.Add(-2*time.Second)), // the new version, created just before
		versioned("unknown", "", start.Add(time.Minute)),                          // no kubelet version yet
	)
	obs := listing(t, client, Identity{TargetVersion: "1.35", StartedAt: start}, newClock())
	snap := snapshot(t, obs)
	if nodeOf(snap, "old-after").OnTarget || !nodeOf(snap, "new-before").OnTarget {
		t.Errorf("the kubelet version did not decide: %+v", snap.Nodes)
	}
	if !nodeOf(snap, "unknown").OnTarget {
		t.Errorf("a node with no kubelet version follows the time of its creation, which is after the start: new")
	}
}

// Whether a roll changes the version is read from the nodes. With every
// node on the target version, as for a new AMI, the time of creation
// decides. With a node on another minor version it is a roll between two
// versions, and it stays one as the old nodes go.
func TestTargetVersionOnlyDecidesAVersionRoll(t *testing.T) {
	start := time.Date(2026, 9, 30, 12, 27, 23, 0, time.UTC)
	// A new AMI: both nodes run 1.35.
	client := fake.NewClientset(
		versioned("patched-old", "v1.35.3-eks-1", start.Add(-time.Hour)),
		versioned("patched-new", "v1.35.3-eks-2", start.Add(time.Minute)),
	)
	obs := listing(t, client, Identity{TargetVersion: "1.35", StartedAt: start}, newClock())
	snap := snapshot(t, obs)
	if nodeOf(snap, "patched-old").OnTarget || !nodeOf(snap, "patched-new").OnTarget {
		t.Errorf("a new AMI: %+v", snap.Nodes)
	}

	// A roll between two versions that ends: the kubelet still decides
	// after the old node is gone, so a new node that was created "before"
	// the start, by a clock that differs, stays new.
	client = fake.NewClientset(
		versioned("old", "v1.34.6-eks-1", start.Add(-time.Hour)),
		versioned("new-skewed", "v1.35.3-eks-2", start.Add(-2*time.Second)),
	)
	obs = listing(t, client, Identity{TargetVersion: "1.35", StartedAt: start}, newClock())
	snapshot(t, obs)
	deleteNode(t, client, "old")
	snap = snapshot(t, obs)
	if !nodeOf(snap, "new-skewed").OnTarget {
		t.Errorf("after the old node went, the new node counts as old: %+v", snap.Nodes)
	}
}

// TestAttach_Modes has one row for each way in which Attach tells the new
// nodes of a roll from the old ones. Attach chooses once, from its first
// list of the nodes.
func TestAttach_Modes(t *testing.T) {
	start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	before, after := start.Add(-time.Hour), start.Add(time.Minute)
	tests := []struct {
		name string
		id   Identity
		// nodes are there as Attach lists, and later joins afterwards.
		nodes []string
		later string
		// kubelet and created give the kubelet version and the time of
		// creation of each node.
		kubelet map[string]string
		created map[string]time.Time
		// wantNew are the nodes that count as new, of all of them.
		wantNew []string
	}{
		{
			name:    "a target version, and a node on another minor: the kubelet decides",
			id:      Identity{TargetVersion: "1.35", StartedAt: start},
			nodes:   []string{"a", "b"},
			later:   "c",
			kubelet: map[string]string{"a": "v1.34.6-eks-1", "b": "v1.35.3-eks-1", "c": "v1.34.6-eks-1"},
			// b was created before the start and is new. c came later and
			// is old: the time of creation does not count.
			created: map[string]time.Time{"a": before, "b": before, "c": after},
			wantNew: []string{"b"},
		},
		{
			name:    "a target version, and a node with no kubelet version: that node follows its creation",
			id:      Identity{TargetVersion: "1.35", StartedAt: start},
			nodes:   []string{"a", "b"},
			later:   "c",
			kubelet: map[string]string{"a": "v1.34.6-eks-1"},
			created: map[string]time.Time{"a": before, "b": before, "c": after},
			wantNew: []string{"c"},
		},
		{
			name:    "a target version that every node runs: created before the start is old",
			id:      Identity{TargetVersion: "1.35", StartedAt: start},
			nodes:   []string{"a", "b"},
			later:   "c",
			kubelet: map[string]string{"a": "v1.35.3-eks-1", "b": "v1.35.3-eks-2", "c": "v1.34.6-eks-1"},
			// c runs another minor, and Attach has chosen already: it
			// counts by its creation.
			created: map[string]time.Time{"a": before, "b": after, "c": after},
			wantNew: []string{"b", "c"},
		},
		{
			name:    "no target version, a start: created before the start is old",
			id:      Identity{StartedAt: start},
			nodes:   []string{"a", "b"},
			later:   "c",
			kubelet: map[string]string{"a": "v1.34.6-eks-1", "b": "v1.35.3-eks-1"},
			created: map[string]time.Time{"a": before, "b": after, "c": before},
			// c was created before the start by its own clock, and was not
			// there when Attach listed: new.
			wantNew: []string{"b", "c"},
		},
		{
			name:    "no target version, no start: there as Attach lists is old",
			id:      Identity{},
			nodes:   []string{"a", "b"},
			later:   "c",
			created: map[string]time.Time{"a": before, "b": after, "c": before},
			wantNew: []string{"c"},
		},
		{
			name:    "a target AMI changes nothing: the image label is not read",
			id:      Identity{TargetAMI: oldAMI},
			nodes:   []string{"a", "b"},
			later:   "c",
			created: map[string]time.Time{"a": before, "b": before, "c": after},
			wantNew: []string{"c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := func(name string) *corev1.Node {
				n := versioned(name, tt.kubelet[name], tt.created[name])
				// Every node has the image label of the old AMI.
				n.Labels[LabelImage] = oldAMI
				return n
			}
			client := fake.NewClientset()
			for _, name := range tt.nodes {
				createNode(t, client, node(name))
			}
			obs := watching(t, client, tt.id, newClock())
			changed := cacheChanges(t, obs)
			createNode(t, client, node(tt.later))
			s := waitForSnapshot(t, obs, changed, func(s Snapshot) bool { return s.Total == len(tt.nodes)+1 })
			var got []string
			for _, n := range s.Nodes {
				if n.OnTarget {
					got = append(got, n.Name)
				}
			}
			if diff := cmp.Diff(tt.wantNew, got); diff != "" {
				t.Errorf("new nodes (-want +got):\n%s", diff)
			}
		})
	}
}
