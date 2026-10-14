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

	// up is the roll whose panel is on the screen, or nil. A fleet has a
	// nodegroup of one name in several clusters, so a roll is known by its
	// whole ref: region, cluster, and nodegroup.
	up *change.StepRef
	// desired is the number of nodes that each roll replaces: the old
	// nodes of its first snapshot.
	desired map[change.StepRef]int
	// moves has what each roll of the plan moves from and to, for the
	// header of its panel.
	moves map[change.StepRef]move
	held  []string
	// notes are the warnings that arrived while the panel is up, oldest
	// first. The panel shows the newest of them, so that a launch that
	// Auto Scaling refuses is seen while the roll waits on it, and not
	// only when the roll ends. last is the event of the panel, for the
	// redraw that a new note needs.
	notes []string
	last  *change.Event
	// frame is the last frame that was appended, and frameAt when.
	frame   []string
	frameAt time.Time
}

// NewRollRenderer returns the renderer of a roll that shows the nodes. It
// redraws the panel in place when stderr is a terminal, and appends
// frames otherwise.
func NewRollRenderer(ios *cmdutil.IOStreams) *RollRenderer {
	r := &RollRenderer{LineRenderer: NewRenderer(ios, false), desired: map[change.StepRef]int{}, moves: map[change.StepRef]move{}}
	if ios.ErrTTY {
		r.live = view.NewLive(ios.ErrOut, ios.Size)
	}
	return r
}

// Render implements Renderer.
func (r *RollRenderer) Render(e change.Event) {
	switch e.Kind {
	case change.PlanReady:
		for i := range e.Plan.Clusters {
			for _, s := range e.Plan.Clusters[i].Steps() {
				r.moves[s.Ref] = moveOf(s)
			}
		}
	case change.NodeProgress:
		r.last = &e
		r.draw(e)
		return
	case change.StepChanged:
		// The roll of the panel ended: the panel comes down before the
		// line of its result.
		if r.up != nil && e.Step == *r.up && e.Result.Status != change.StepStarted {
			r.down()
		}
	case change.Notice:
		// The line of the warning is held with the others and prints when
		// the panel comes down. Until then the panel shows it.
		if r.up != nil {
			r.notes = append(r.notes, e.Text)
			r.LineRenderer.Render(e)
			r.draw(*r.last)
			return
		}
	}
	r.LineRenderer.Render(e)
}

// move is what the header of a panel says that a roll moves from and to.
type move struct {
	from, to string
}

// moveOf returns the two ends of a roll, each named for what it is. A
// roll of an upgrade or a rollback changes the Kubernetes version. A roll
// onto the latest AMI keeps the version and changes the image: the header
// names the image IDs, and the release version that the nodes run when no
// image ID was read for them.
func moveOf(s change.Step) move {
	if s.From != s.To {
		return move{from: "Kubernetes " + s.From, to: "Kubernetes " + s.To}
	}
	var m move
	switch {
	case s.FromImage != "":
		m.from = "AMI " + s.FromImage
	case s.FromRelease != "":
		m.from = "AMI release " + s.FromRelease
	}
	if s.ToImage != "" {
		m.to = "AMI " + s.ToImage
	}
	return m
}

// draw shows the panel for one NodeProgress event.
func (r *RollRenderer) draw(e change.Event) {
	n := e.Nodes
	if _, ok := r.desired[e.Step]; !ok {
		old := 0
		for _, node := range n.Snapshot.Nodes {
			if !node.OnTarget {
				old++
			}
		}
		r.desired[e.Step] = old
	}
	progress := view.RollProgress{
		Nodegroup: n.Nodegroup, Snapshot: n.Snapshot, Recent: n.Recent, Watching: n.Watching,
		OldVersion: r.moves[e.Step].from, NewVersion: r.moves[e.Step].to, Notes: r.notes,
	}
	th := r.ios.ErrTheme()
	if r.live == nil {
		// The frames are appended, and so are the lines: a warning has
		// printed already, and the frame does not repeat it.
		progress.Notes = nil
		frame := view.RollPanel(th, progress, r.desired[e.Step], 0)
		if slices.Equal(frame, r.frame) || (r.frame != nil && e.At.Sub(r.frameAt) < frameInterval) {
			return
		}
		r.frame, r.frameAt = frame, e.At
		_ = cmdutil.WriteLines(r.ios.ErrOut, append(frame, ""))
		return
	}
	elapsed := max(e.At.Sub(n.StartedAt), time.Second)
	if !r.live.Draw(view.RollPanel(th, progress, r.desired[e.Step], elapsed)) {
		// The panel is taller than the terminal: the lines stand in.
		r.down()
		return
	}
	step := e.Step
	r.up = &step
	r.hold = func(line string) { r.held = append(r.held, line) }
}

// down takes the panel off the screen and prints the lines that it held.
func (r *RollRenderer) down() {
	if r.live != nil {
		r.live.Clear()
	}
	r.up, r.hold, r.last = nil, nil, nil
	for _, line := range r.held {
		_, _ = fmt.Fprintln(r.ios.ErrOut, line)
	}
	r.held, r.notes = nil, nil
}

// Pause implements Renderer: a prompt needs the screen.
func (r *RollRenderer) Pause() { r.down() }

// Close implements Renderer.
func (r *RollRenderer) Close() { r.down() }
