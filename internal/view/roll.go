package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/noderoll"
)

// RollProgress is what the panel of a roll shows: the nodes of one
// nodegroup at one moment, and what happened to them last.
type RollProgress struct {
	Nodegroup string
	Snapshot  noderoll.Snapshot
	// Recent are the last events of the roll, oldest first. The panel
	// shows the last rollEvents of them.
	Recent []noderoll.Event
	// Watching says that the snapshot comes from a watch. Without it the
	// panel says that the nodes are polled, so a reader knows that it lags.
	Watching bool
	// OldVersion and NewVersion name what the roll moves from and to, in
	// the words of the caller, which say what each one is: "Kubernetes
	// 1.33", "AMI ami-0abc", or "AMI release 1.32.0-20250101". Either may
	// be "".
	OldVersion, NewVersion string
	// Notes are problems that were found beside the roll while the panel
	// is up, oldest first, such as a node that Auto Scaling could not
	// launch. The panel shows the last rollNotes of them under the bar.
	Notes []string
}

const (
	// rollEvents is how many events the panel shows.
	rollEvents = 6
	// rollNotes is how many notes the panel shows: the newest.
	rollNotes = 2
	// rollBarWidth and evictionBarWidth are the cells of the bar of
	// replaced nodes and of the bar of evicted pods.
	rollBarWidth     = 16
	evictionBarWidth = 5
	// warningWidth is the most cells of the message of a warning. A long
	// message would make the panel wider than the terminal.
	warningWidth = 80
	// nodeColumnWidth and stateColumnWidth are the least widths of two
	// columns of the node table, so the table does not jump as nodes come
	// and go.
	nodeColumnWidth  = 14
	stateColumnWidth = 10
)

// rollSpinner are the frames of the mark of a roll that is running.
var rollSpinner = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// RollPanel returns the lines of the panel of a roll: a header with the
// nodegroup and the old and new version, a bar of the nodes that are
// replaced, the newest notes about the roll, a table of the nodes with the progress of each drain and the
// pressure on each node, the last events, and the Warning events about
// the nodes.
//
// desired is the number of nodes that the roll replaces. elapsed is how
// long the roll has run: the header shows it, and it turns the mark of
// the header. A caller that appends frames, and does not redraw in place,
// passes 0: the panel of a roll that did not change is then the same
// lines, and the caller can leave it out.
func RollPanel(th Theme, p RollProgress, desired int, elapsed time.Duration) []string {
	snap := p.Snapshot
	old := 0
	for _, n := range snap.Nodes {
		if !n.OnTarget {
			old++
		}
	}
	replaced := max(desired-old, 0)
	active := snap.Joining > 0 || snap.Draining > 0 || replaced < desired

	head := th.Glyph(Healthy)
	if active {
		head = th.spin(elapsed)
	}
	head += " " + th.Strong("rolling "+p.Nodegroup)
	switch {
	case p.OldVersion != "" && p.NewVersion != "":
		head += "   " + th.paint(colorPeach, p.OldVersion, false) + th.Dim(" "+th.arrow()+" ") + th.paint(colorGreen, p.NewVersion, false)
	case p.NewVersion != "":
		head += "   " + th.Dim(th.arrow()+" ") + th.paint(colorGreen, p.NewVersion, false)
	}
	if elapsed > 0 {
		head += th.Dim("   " + elapsed.Truncate(time.Second).String())
	}
	progress := th.bar(replaced, desired, rollBarWidth, colorGreen) + "  " +
		th.Strong(fmt.Sprintf("%d/%d replaced", replaced, desired)) + th.Dim(", ") + th.Strong(fmt.Sprintf("%d new ready", snap.ReadyTarget))
	if !p.Watching {
		progress += th.Dim(", polled")
	}
	out := []string{head, progress}
	notes := p.Notes
	if len(notes) > rollNotes {
		notes = notes[len(notes)-rollNotes:]
	}
	for _, note := range notes {
		out = append(out, th.Token(Warn, strings.Join(strings.Fields(note), " ")))
	}
	out = append(out, "")
	out = append(out, rollNodeTable(th, snap.Nodes)...)

	out = append(out, "", th.Dim("events"))
	recent := p.Recent
	if len(recent) > rollEvents {
		recent = recent[len(recent)-rollEvents:]
	}
	for _, e := range recent {
		status, text := rollEventText(e.Kind)
		out = append(out, indent+indent+th.Glyph(status)+" "+th.Strong(e.Node)+th.Dim("  "+text))
	}
	if snap.WarningsCapped > 0 {
		out = append(out, indent+indent+th.Dim(fmt.Sprintf("(showing first %d warning events)", snap.WarningsCapped)))
	}
	if len(snap.Warnings) > 0 {
		out = append(out, "", th.Dim("warnings"))
		for _, w := range snap.Warnings {
			line := indent + indent + th.Token(Warn, w.Reason) + " " + th.Strong(w.Node)
			if msg := oneLineWarning(w.Message, th.Unicode); msg != "" {
				line += th.Dim("  " + msg)
			}
			out = append(out, line)
		}
	}
	return out
}

// rollNodeTable returns the table of the nodes: a mark for the phase, the
// name, old or new, and the state. The cells hold colors, so the widths
// are the visible ones.
func rollNodeTable(th Theme, nodes []noderoll.NodeView) []string {
	rows := [][]string{{"", th.Dim("NODE"), th.Dim("OLD/NEW"), th.Dim("STATE")}}
	for _, n := range nodes {
		age := th.paint(colorPeach, "old", false)
		if n.OnTarget {
			age = th.paint(colorGreen, "new", false)
		}
		rows = append(rows, []string{th.Glyph(rollPhaseStatus(n.Phase)), th.Strong(n.Name), age, rollNodeState(th, n)})
	}
	widths := []int{1, nodeColumnWidth, 0, stateColumnWidth}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], visibleWidth(cell))
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = cell + strings.Repeat(" ", widths[i]-visibleWidth(cell))
		}
		// The mark stands before the name with one space, as in a list.
		out = append(out, strings.TrimRight(indent+cells[0]+" "+strings.Join(cells[1:], columnGap), " "))
	}
	return out
}

// rollPhaseStatus returns the status that draws the phase of a node.
func rollPhaseStatus(p noderoll.Phase) Status {
	switch p {
	case noderoll.PhaseReady:
		return Healthy
	case noderoll.PhaseJoining:
		return Progress
	case noderoll.PhaseDraining:
		return Warn
	}
	return Unknown
}

// rollEventText returns the status and the words of an event of a roll.
func rollEventText(k noderoll.EventKind) (Status, string) {
	switch k {
	case noderoll.EvtJoining:
		return Progress, "scaling up, joining"
	case noderoll.EvtOnline:
		return Healthy, "came online (Ready)"
	case noderoll.EvtDraining:
		return Warn, "cordoned, draining"
	case noderoll.EvtTerminated:
		return Neutral, "drained and terminated"
	}
	return Neutral, string(k)
}

// rollNodeState returns the state cell of a node: its phase, for a
// draining node the pods that were evicted of the ones it had, and the
// pressure on it. A node can be Ready and under pressure, when the new
// instance is too small.
func rollNodeState(th Theme, n noderoll.NodeView) string {
	var s string
	switch n.Phase {
	case noderoll.PhaseReady:
		s = th.paint(colorGreen, "Ready", false)
	case noderoll.PhaseJoining:
		s = th.Dim("joining (NotReady)")
	case noderoll.PhaseDraining:
		s = th.Caution("draining")
		if n.PodsTotal > 0 {
			evicted := max(n.PodsTotal-n.Pods, 0)
			s += th.Dim(", evicting ") + th.bar(evicted, n.PodsTotal, evictionBarWidth, colorYellow) +
				th.Dim(fmt.Sprintf(" %d/%d pods", evicted, n.PodsTotal))
		}
	default:
		s = th.Dim("unknown")
	}
	if len(n.Pressure) > 0 {
		s += th.Dim(", ") + th.Token(Warn, strings.Join(n.Pressure, "+"))
	}
	return s
}

// oneLineWarning returns the message of a warning on one line, cut to
// warningWidth cells.
func oneLineWarning(s string, unicode bool) string {
	return truncate(strings.Join(strings.Fields(s), " "), warningWidth, unicode)
}

// spin returns the mark of something that is running: a frame of the
// spinner for each second of elapsed, or a star without Unicode.
func (t Theme) spin(elapsed time.Duration) string {
	if !t.Unicode {
		return t.paint(colorTeal, "*", false)
	}
	frame := int(elapsed/time.Second) % len(rollSpinner)
	return t.paint(colorTeal, rollSpinner[max(frame, 0)], false)
}

// arrow returns the arrow between an old and a new value.
func (t Theme) arrow() string {
	if t.Unicode {
		return "→"
	}
	return "->"
}

// bar returns a bar of width cells of which done of total are filled, in
// the color c. A total of 0 or less counts as 1.
func (t Theme) bar(done, total, width int, c rgb) string {
	filled := min(max(done*width/max(total, 1), 0), width)
	fill, empty, left, right := "#", "-", "[", "]"
	if t.Unicode {
		fill, empty, left, right = "█", "░", "▕", "▏"
	}
	return left + t.paint(c, strings.Repeat(fill, filled), false) + t.Dim(strings.Repeat(empty, width-filled)) + right
}
