package noderoll

import (
	"context"
	"testing"
)

func TestTracker_DerivesLifecycleEvents(t *testing.T) {
	tr := NewTracker()
	// Baseline: 3 old nodes ready — no events.
	tr.Observe(snapOf(oldReady("a"), oldReady("b"), oldReady("c")))
	if len(tr.Events) != 0 {
		t.Fatalf("baseline should emit no events, got %v", tr.Events)
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
	if len(tr.Events) != len(want) {
		t.Fatalf("events = %v, want %v", tr.Events, want)
	}
	for i, w := range want {
		if tr.Events[i] != w {
			t.Errorf("event %d = %v, want %v", i, tr.Events[i], w)
		}
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
	for i := 0; i < 100; i++ {
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
	tr := &Tracker{Events: []Event{{"a", EvtJoining}, {"b", EvtOnline}, {"c", EvtDraining}}}
	if got := tr.Recent(2); len(got) != 2 || got[0].Node != "b" {
		t.Errorf("Recent(2) = %v, want last two", got)
	}
	if got := tr.Recent(10); len(got) != 3 {
		t.Errorf("Recent(10) = %v, want all 3", got)
	}
}
