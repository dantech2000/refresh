package changecmd

import (
	"fmt"
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/view"
)

// frameInterval is the least time between two panel frames that are
// appended, for a roll whose stderr is not a terminal.
const frameInterval = 15 * time.Second

// RollRenderer is the renderer of a roll with the node panel. It writes
// the lines of a LineRenderer, and draws the panel from the NodeProgress
// events of the run.
//
// On a terminal the panel is redrawn in place, and the progress lines of
// the wait are held while it is up: a line written under a block that is
// redrawn would be erased with it. The lines print when the panel comes
// down: at the end of the roll, at a prompt, and at the end of the run.
//
// Without a terminal the panel cannot be redrawn. Its frames are
// appended, at most one each frameInterval, and only a frame that differs
// from the one before.
type RollRenderer struct {
	*LineRenderer
	// live redraws the panel in place. It is nil when frames are appended.
	live *view.Live

	// nodegroup is the roll whose panel is up, or "".
	nodegroup string
	// desired is the number of nodes that the roll replaces: the old
	// nodes of its first snapshot.
	desired map[string]int
	// releases has the AMI that each roll of the plan moves to.
	releases map[string]string
	held     []string
	// frame is the last frame that was appended, and frameAt when.
	frame   []string
	frameAt time.Time
}

// NewRollRenderer returns the renderer of a roll that shows the nodes. It
// redraws the panel in place when stderr is a terminal, and appends
// frames otherwise.
func NewRollRenderer(ios *cmdutil.IOStreams) *RollRenderer {
	r := &RollRenderer{LineRenderer: NewRenderer(ios, false), desired: map[string]int{}, releases: map[string]string{}}
	if ios.ErrTTY {
		r.live = view.NewLive(ios.ErrOut, ios.Size)
	}
	return r
}

// Render implements Renderer.
func (r *RollRenderer) Render(e change.Event) {
	switch e.Kind {
	case change.PlanReady:
		for _, c := range e.Plan.Document().Clusters {
			for _, s := range c.Steps {
				r.releases[s.Name] = s.ToRelease
			}
		}
	case change.NodeProgress:
		r.draw(e)
		return
	case change.StepChanged:
		// The roll of the panel ended: the panel comes down before the
		// line of its result.
		if e.Step.Name == r.nodegroup && e.Result.Status != change.StepStarted {
			r.down()
		}
	}
	r.LineRenderer.Render(e)
}

// draw shows the panel for one NodeProgress event.
func (r *RollRenderer) draw(e change.Event) {
	n := e.Nodes
	if _, ok := r.desired[n.Nodegroup]; !ok {
		old := 0
		for _, node := range n.Snapshot.Nodes {
			if !node.OnTarget {
				old++
			}
		}
		r.desired[n.Nodegroup] = old
	}
	progress := view.RollProgress{
		Nodegroup: n.Nodegroup, Snapshot: n.Snapshot, Recent: n.Recent, Watching: n.Watching,
		NewVersion: r.releases[n.Nodegroup],
	}
	th := r.ios.ErrTheme()
	if r.live == nil {
		frame := view.RollPanel(th, progress, r.desired[n.Nodegroup], 0)
		if slices.Equal(frame, r.frame) || (r.frame != nil && e.At.Sub(r.frameAt) < frameInterval) {
			return
		}
		r.frame, r.frameAt = frame, e.At
		_ = cmdutil.WriteLines(r.ios.ErrOut, append(frame, ""))
		return
	}
	elapsed := max(e.At.Sub(n.StartedAt), time.Second)
	if !r.live.Draw(view.RollPanel(th, progress, r.desired[n.Nodegroup], elapsed)) {
		// The panel is taller than the terminal: the lines stand in.
		r.down()
		return
	}
	r.nodegroup = n.Nodegroup
	r.hold = func(line string) { r.held = append(r.held, line) }
}

// down takes the panel off the screen and prints the lines that it held.
func (r *RollRenderer) down() {
	if r.live != nil {
		r.live.Clear()
	}
	r.nodegroup, r.hold = "", nil
	for _, line := range r.held {
		_, _ = fmt.Fprintln(r.ios.ErrOut, line)
	}
	r.held = nil
}

// Pause implements Renderer: a prompt needs the screen.
func (r *RollRenderer) Pause() { r.down() }

// Close implements Renderer.
func (r *RollRenderer) Close() { r.down() }
