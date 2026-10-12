package noderoll

import "context"

// ScriptedObserver replays a fixed sequence of snapshots. It backs the
// `nodegroup update --simulate` dev flag and tests, so the full live roll can
// be exercised with no AWS and no cluster. After the last frame it keeps
// returning the final state.
type ScriptedObserver struct {
	frames []Snapshot
	i      int
}

// NewScriptedObserver replays frames in order.
func NewScriptedObserver(frames []Snapshot) *ScriptedObserver {
	return &ScriptedObserver{frames: frames}
}

// Snapshot returns the next frame, advancing until the last (which then
// repeats).
func (o *ScriptedObserver) Snapshot(_ context.Context) (Snapshot, error) {
	if len(o.frames) == 0 {
		return Snapshot{}, nil
	}
	idx := o.i
	if idx >= len(o.frames) {
		idx = len(o.frames) - 1
	}
	if o.i < len(o.frames)-1 {
		o.i++
	}
	return o.frames[idx], nil
}

// AtEnd reports whether the last frame has been reached.
func (o *ScriptedObserver) AtEnd() bool { return o.i >= len(o.frames)-1 }

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

// DemoTimeline builds a realistic surge roll of 3 old nodes → 3 new nodes
// (max-surge 1): each new node joins and goes Ready, then an old node drains
// its pods and terminates. Used by --simulate.
func DemoTimeline() []Snapshot {
	const a, b, c = "ip-10-0-1-12", "ip-10-0-3-21", "ip-10-0-2-08"
	const d, e, f = "ip-10-0-1-44", "ip-10-0-2-77", "ip-10-0-3-05"
	return []Snapshot{
		snapOf(oldReady(a), oldReady(b), oldReady(c)),
		snapOf(oldReady(a), oldReady(b), oldReady(c), newJoining(d)),
		snapOf(draining(a, 4, 4), oldReady(b), oldReady(c), newReady(d)),
		snapOf(draining(a, 2, 4), oldReady(b), oldReady(c), newReady(d)),
		snapOf(draining(a, 0, 4), oldReady(b), oldReady(c), newReady(d)),
		snapOf(oldReady(b), oldReady(c), newReady(d)),
		snapOf(oldReady(b), oldReady(c), newReady(d), newJoining(e)),
		snapOf(draining(b, 3, 3), oldReady(c), newReady(d), newReady(e)),
		snapOf(draining(b, 0, 3), oldReady(c), newReady(d), newReady(e)),
		snapOf(oldReady(c), newReady(d), newReady(e)),
		snapOf(oldReady(c), newReady(d), newReady(e), newJoining(f)),
		snapOf(draining(c, 5, 5), newReady(d), newReady(e), newReady(f)),
		snapOf(draining(c, 0, 5), newReady(d), newReady(e), newReady(f)),
		snapOf(newReady(d), newReady(e), newReady(f)),
	}
}
