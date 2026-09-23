package noderoll

import (
	"context"
	"reflect"
	"testing"
)

func TestTracker_DerivesLifecycleEvents(t *testing.T) {
	tr := NewTracker()
	// Baseline: 3 old nodes ready — no events.
	tr.Observe(snapOf(oldReady("a"), oldReady("b"), oldReady("c")))
	if got := tr.Recent(0); len(got) != 0 {
		t.Fatalf("baseline should emit no events, got %v", got)
	}
	// Surge a new node (joining) + cordon an old one (draining).
	tr.Observe(snapOf(draining("a", 4, 4), oldReady("b"), oldReady("c"), newJoining("d")))
	// New node goes Ready; drained node terminates.
	tr.Observe(snapOf(oldReady("b"), oldReady("c"), newReady("d")))

	want := []Event{
		{"a", EvtDraining},
		{"d", EvtJoining},
		{"d", EvtOnline},
		{"a", EvtTerminated},
	}
	if got := tr.Recent(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestScriptedObserver_RunsToCompletion(t *testing.T) {
	obs := NewScriptedObserver(DemoTimeline())
	ctx := context.Background()
	// Mirror the driver: pull frames until the snapshot reports the roll done.
	done := func(s Snapshot) bool {
		return s.Total > 0 && s.ReadyTarget == 3 && s.Draining == 0 && s.Joining == 0
	}
	var last Snapshot
	finished := false
	for range 100 {
		s, err := obs.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		last = s
		if done(s) {
			finished = true
			break
		}
	}
	if !finished {
		t.Fatalf("scripted roll never completed; last = %+v", last)
	}
}

func TestRecent(t *testing.T) {
	tr := &Tracker{events: []Event{{"a", EvtJoining}, {"b", EvtOnline}, {"c", EvtDraining}}}
	if got := tr.Recent(2); len(got) != 2 || got[0].Node != "b" {
		t.Errorf("Recent(2) = %v, want last two", got)
	}
	if got := tr.Recent(10); len(got) != 3 {
		t.Errorf("Recent(10) = %v, want all 3", got)
	}
	// Recent hands out a copy: mutating it must not touch the tracker.
	got := tr.Recent(1)
	got[0].Node = "mutated"
	if tr.Recent(1)[0].Node != "c" {
		t.Error("Recent returned an alias of the tracker's history")
	}
}

// Several nodes leaving in one snapshot must emit terminated events in name
// order, not map order.
func TestTracker_TerminatedEventsSorted(t *testing.T) {
	for range 20 { // map order varies per run; repeat to catch it
		tr := NewTracker()
		tr.Observe(snapOf(oldReady("n-c"), oldReady("n-a"), oldReady("n-d"), oldReady("n-b")))
		tr.Observe(snapOf())
		want := []Event{
			{"n-a", EvtTerminated}, {"n-b", EvtTerminated},
			{"n-c", EvtTerminated}, {"n-d", EvtTerminated},
		}
		if got := tr.Recent(0); !reflect.DeepEqual(got, want) {
			t.Fatalf("terminated events = %v, want %v", got, want)
		}
	}
}

// The event history is bounded: only the last trackerEventCapacity events
// survive, newest last.
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
		t.Fatalf("retained %d events, want %d", len(all), trackerEventCapacity)
	}
	// total is odd, so the last observation (index total-1, even) was draining.
	if last := all[len(all)-1]; last.Kind != EvtDraining {
		t.Errorf("newest event = %v, want draining", last)
	}
	if cap(tr.events) > 2*trackerEventCapacity {
		t.Errorf("backing array grew to %d, want <= %d", cap(tr.events), 2*trackerEventCapacity)
	}
}
