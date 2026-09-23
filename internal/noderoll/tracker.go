package noderoll

import "sort"

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

// trackerEventCapacity bounds how many events a Tracker retains. The panel
// only shows the last few, and a long roll of a large nodegroup would
// otherwise grow the history without bound.
const trackerEventCapacity = 256

// Tracker derives lifecycle events by diffing successive snapshots — turning
// the observer's point-in-time state into the "node came online / went offline"
// feed the live panel shows. It is pure: feed it snapshots, read Recent. It
// keeps the last trackerEventCapacity events; older ones are dropped.
type Tracker struct {
	prev   map[string]Phase
	seeded bool
	events []Event
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
	// Appeared or changed phase, in s.Nodes order (KubeObserver sorts by name).
	for _, n := range s.Nodes {
		old, existed := t.prev[n.Name]
		if existed && old == n.Phase {
			continue
		}
		if k := eventForPhase(n.Phase); k != "" {
			t.add(Event{Node: n.Name, Kind: k})
		}
	}
	// Disappeared → terminated, sorted by name (map order is random).
	var gone []string
	for name := range t.prev {
		if _, ok := cur[name]; !ok {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	for _, name := range gone {
		t.add(Event{Node: name, Kind: EvtTerminated})
	}
	t.prev = cur
}

// add appends e and drops the oldest events past trackerEventCapacity. The
// trim shifts in place, so the backing array stays bounded.
func (t *Tracker) add(e Event) {
	t.events = append(t.events, e)
	if over := len(t.events) - trackerEventCapacity; over > 0 {
		n := copy(t.events, t.events[over:])
		t.events = t.events[:n]
	}
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

// Recent returns a copy of the last n events (fewer if not enough have
// accrued; every retained event when n <= 0).
func (t *Tracker) Recent(n int) []Event {
	src := t.events
	if n > 0 && len(src) > n {
		src = src[len(src)-n:]
	}
	return append([]Event(nil), src...)
}
