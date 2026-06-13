package noderoll

// EventKind classifies a node lifecycle transition during a roll.
type EventKind string

const (
	EvtJoining    EventKind = "joining"    // a new node appeared, not yet Ready
	EvtOnline     EventKind = "online"     // a node became Ready
	EvtDraining   EventKind = "draining"   // a node was cordoned / tainted for removal
	EvtTerminated EventKind = "terminated" // a node left the cluster
)

// Event is a single observed lifecycle transition.
type Event struct {
	Node string    `json:"node"`
	Kind EventKind `json:"kind"`
}

// Tracker derives lifecycle events by diffing successive snapshots — turning
// the observer's point-in-time state into the "node came online / went offline"
// feed the live panel shows. It is pure: feed it snapshots, read Events.
type Tracker struct {
	prev   map[string]Phase
	seeded bool
	Events []Event
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker { return &Tracker{prev: map[string]Phase{}} }

// Observe diffs s against the previous snapshot and appends any transitions.
// The first snapshot is taken as the baseline (no events), so a roll that is
// already in flight on attach doesn't replay its whole history.
func (t *Tracker) Observe(s Snapshot) {
	cur := make(map[string]Phase, len(s.Nodes))
	for _, n := range s.Nodes {
		cur[n.Name] = n.Phase
	}
	if !t.seeded {
		t.prev = cur
		t.seeded = true
		return
	}
	// Appeared or changed phase (s.Nodes is name-sorted → deterministic order).
	for _, n := range s.Nodes {
		old, existed := t.prev[n.Name]
		if existed && old == n.Phase {
			continue
		}
		if k := eventForPhase(n.Phase); k != "" {
			t.Events = append(t.Events, Event{Node: n.Name, Kind: k})
		}
	}
	// Disappeared → terminated.
	for name := range t.prev {
		if _, ok := cur[name]; !ok {
			t.Events = append(t.Events, Event{Node: name, Kind: EvtTerminated})
		}
	}
	t.prev = cur
}

func eventForPhase(p Phase) EventKind {
	switch p {
	case PhaseJoining:
		return EvtJoining
	case PhaseReady:
		return EvtOnline
	case PhaseDraining:
		return EvtDraining
	default:
		return ""
	}
}

// Recent returns the last n events (fewer if not enough have accrued).
func (t *Tracker) Recent(n int) []Event {
	if n <= 0 || len(t.Events) <= n {
		return t.Events
	}
	return t.Events[len(t.Events)-n:]
}
