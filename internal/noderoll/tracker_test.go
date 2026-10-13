package noderoll

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func snapOf(nodes ...NodeView) Snapshot {
	s := Snapshot{Nodes: nodes, Total: len(nodes)}
	for _, n := range nodes {
		switch n.Phase {
		case PhaseDraining:
			s.Draining++
		case PhaseJoining:
			s.Joining++
		case PhaseReady:
			if n.OnTarget {
				s.ReadyTarget++
			}
		}
	}
	return s
}

func oldReady(name string) NodeView {
	return NodeView{Name: name, OnTarget: false, Ready: true, Phase: PhaseReady}
}

func newJoining(name string) NodeView {
	return NodeView{Name: name, OnTarget: true, Ready: false, Phase: PhaseJoining}
}

func newReady(name string) NodeView {
	return NodeView{Name: name, OnTarget: true, Ready: true, Phase: PhaseReady}
}

func draining(name string, remaining, total int) NodeView {
	return NodeView{Name: name, OnTarget: false, Ready: true, Phase: PhaseDraining, Pods: remaining, PodsTotal: total}
}

func TestTracker_DerivesLifecycleEvents(t *testing.T) {
	tr := NewTracker()
	// The starting point: three old nodes that are Ready. No events.
	tr.Observe(snapOf(oldReady("a"), oldReady("b"), oldReady("c")))
	if got := tr.Recent(0); len(got) != 0 {
		t.Fatalf("the first snapshot gave events: %v", got)
	}
	// A new node joins, and an old one is cordoned.
	tr.Observe(snapOf(draining("a", 4, 4), oldReady("b"), oldReady("c"), newJoining("d")))
	// The new node is Ready, and the drained node is gone.
	tr.Observe(snapOf(oldReady("b"), oldReady("c"), newReady("d")))

	want := []Event{
		{"a", EvtDraining},
		{"d", EvtJoining},
		{"d", EvtOnline},
		{"a", EvtTerminated},
	}
	if diff := cmp.Diff(want, tr.Recent(0)); diff != "" {
		t.Fatalf("events (-want +got):\n%s", diff)
	}
}

func TestRecent(t *testing.T) {
	tr := &Tracker{events: []Event{{"a", EvtJoining}, {"b", EvtOnline}, {"c", EvtDraining}}}
	if got := tr.Recent(2); len(got) != 2 || got[0].Node != "b" {
		t.Errorf("Recent(2) = %v, want the last two", got)
	}
	if got := tr.Recent(10); len(got) != 3 {
		t.Errorf("Recent(10) = %v, want all three", got)
	}
	// Recent returns a copy: a change of it does not reach the tracker.
	got := tr.Recent(1)
	got[0].Node = "mutated"
	if tr.Recent(1)[0].Node != "c" {
		t.Error("Recent returned the history of the tracker itself")
	}
}

// Several nodes that go in one snapshot give their events in the order of
// their names, and not in the order of a map.
func TestTracker_TerminatedEventsSorted(t *testing.T) {
	for range 20 { // the order of a map changes from run to run
		tr := NewTracker()
		tr.Observe(snapOf(oldReady("n-c"), oldReady("n-a"), oldReady("n-d"), oldReady("n-b")))
		tr.Observe(snapOf())
		want := []Event{
			{"n-a", EvtTerminated}, {"n-b", EvtTerminated},
			{"n-c", EvtTerminated}, {"n-d", EvtTerminated},
		}
		if diff := cmp.Diff(want, tr.Recent(0)); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
	}
}

// The history is bounded: the last trackerEventCapacity events stay, the
// newest last.
func TestTracker_EventHistoryBounded(t *testing.T) {
	tr := NewTracker()
	tr.Observe(snapOf(oldReady("a")))
	total := trackerEventCapacity*2 + 7
	for i := range total {
		if i%2 == 0 {
			tr.Observe(snapOf(draining("a", 1, 1)))
		} else {
			tr.Observe(snapOf(oldReady("a")))
		}
	}
	all := tr.Recent(0)
	if len(all) != trackerEventCapacity {
		t.Fatalf("%d events kept, want %d", len(all), trackerEventCapacity)
	}
	// total is odd, so the last snapshot had the node draining.
	if last := all[len(all)-1]; last.Kind != EvtDraining {
		t.Errorf("the newest event = %v, want draining", last)
	}
	if cap(tr.events) > 2*trackerEventCapacity {
		t.Errorf("the backing array grew to %d, want at most %d", cap(tr.events), 2*trackerEventCapacity)
	}
}
