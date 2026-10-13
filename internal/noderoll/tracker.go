package noderoll

import "slices"

// EventKind is a step of a node through a roll.
type EventKind string

// The kinds of event.
const (
	// EvtJoining: a node appeared and is not Ready yet.
	EvtJoining EventKind = "joining"
	// EvtOnline: a node became Ready.
	EvtOnline EventKind = "online"
	// EvtDraining: a node was cordoned, or tainted for removal.
	EvtDraining EventKind = "draining"
	// EvtTerminated: a node is gone.
	EvtTerminated EventKind = "terminated"
)

// Event is one step of one node.
type Event struct {
	Node string
	Kind EventKind
}

// trackerEventCapacity is how many events a Tracker keeps. A panel shows
// the last few, and a long roll of a large nodegroup would grow the
// history without end.
const trackerEventCapacity = 256

// Tracker turns successive snapshots into the events of a roll: it
// compares each snapshot with the one before. It reads nothing itself. It
// keeps the last trackerEventCapacity events.
type Tracker struct {
	prev   map[string]Phase
	seeded bool
	events []Event
}

// NewTracker returns a Tracker with no events.
func NewTracker() *Tracker { return &Tracker{prev: map[string]Phase{}} }

// Observe compares s with the snapshot before it and adds an event for
// each difference. The first snapshot is the starting point and gives no
// event, so a roll that is already running does not replay its history.
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
	// A node that appeared or changed its phase, in the order of s.Nodes.
	for _, n := range s.Nodes {
		old, existed := t.prev[n.Name]
		if existed && old == n.Phase {
			continue
		}
		if k := eventForPhase(n.Phase); k != "" {
			t.add(Event{Node: n.Name, Kind: k})
		}
	}
	// A node that is gone, by name: the order of a map is random.
	var gone []string
	for name := range t.prev {
		if _, ok := cur[name]; !ok {
			gone = append(gone, name)
		}
	}
	slices.Sort(gone)
	for _, name := range gone {
		t.add(Event{Node: name, Kind: EvtTerminated})
	}
	t.prev = cur
}

// add appends e and drops the oldest events beyond the capacity. It moves
// the events in place, so the backing array stays bounded.
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
	}
	return ""
}

// Recent returns a copy of the last n events, or of every event that the
// Tracker keeps when n is 0 or less or there are fewer.
func (t *Tracker) Recent(n int) []Event {
	src := t.events
	if n > 0 && len(src) > n {
		src = src[len(src)-n:]
	}
	return slices.Clone(src)
}
