package nodegroup

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// rollMeta is the static context for a live roll panel.
type rollMeta struct {
	Nodegroup      string
	OldAMI, NewAMI string
	Desired        int
	Frame          int // spinner tick, advanced by the driver
}

var rollSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func phaseStatus(p noderoll.Phase) render.Status {
	switch p {
	case noderoll.PhaseReady:
		return render.Healthy
	case noderoll.PhaseJoining:
		return render.Progress
	case noderoll.PhaseDraining:
		return render.Warn
	default:
		return render.Unknown
	}
}

func eventToken(kind noderoll.EventKind) (render.Status, string) {
	switch kind {
	case noderoll.EvtJoining:
		return render.Progress, "scaling up, joining"
	case noderoll.EvtOnline:
		return render.Healthy, "came online (Ready)"
	case noderoll.EvtDraining:
		return render.Warn, "cordoned, draining"
	case noderoll.EvtTerminated:
		return render.Neutral, "drained & terminated"
	default:
		return render.Neutral, string(kind)
	}
}

// rollPanelLines builds the live node-roll panel as a slice of lines (pure, so
// it is golden-testable): a header with overall progress, a per-node table
// (AMI + lifecycle state + pod-eviction bar), and a recent-events feed.
func rollPanelLines(th *render.Theme, snap noderoll.Snapshot, events []noderoll.Event, m rollMeta) []string {
	pal := th.Pal

	oldRemaining := 0
	for _, n := range snap.Nodes {
		if !n.OnTarget {
			oldRemaining++
		}
	}
	replaced := m.Desired - oldRemaining
	if replaced < 0 {
		replaced = 0
	}
	active := snap.Joining > 0 || snap.Draining > 0 || replaced < m.Desired

	head := th.Glyph(render.Healthy)
	if active {
		head = spin(th, m.Frame)
	}
	out := []string{
		head + " " + th.Paint(pal.White, "rolling "+m.Nodegroup) +
			th.Paint(pal.Dim, "   ") + th.Paint(pal.Peach, m.OldAMI) +
			th.Paint(pal.Dim, " → ") + th.Paint(pal.Green, m.NewAMI),
		th.Bar(replaced, m.Desired, 16, pal.Green) + "  " +
			th.Paint(pal.White, fmt.Sprintf("%d/%d replaced", replaced, m.Desired)) +
			th.Paint(pal.Dim, " · ") + th.Paint(pal.White, fmt.Sprintf("%d new ready", snap.ReadyTarget)),
		"",
	}

	tbl := th.NewTable(
		ui.Column{Title: "", Min: 1},
		ui.Column{Title: "NODE", Min: 14},
		ui.Column{Title: "AMI", Min: 3},
		ui.Column{Title: "STATE", Min: 10},
	)
	for _, n := range snap.Nodes {
		tbl.Row(
			th.Glyph(phaseStatus(n.Phase)),
			th.Paint(pal.White, n.Name),
			amiWord(th, n.OnTarget),
			nodeStateCell(th, n),
		)
	}
	for _, l := range tbl.Render() {
		out = append(out, "  "+l)
	}

	out = append(out, "", th.Paint(pal.Dim, "events"))
	for _, e := range events {
		st, text := eventToken(e.Kind)
		out = append(out, "    "+th.Glyph(st)+" "+th.Paint(pal.White, e.Node)+th.Paint(pal.Dim, "  "+text))
	}
	return out
}

func spin(th *render.Theme, frame int) string {
	if !th.Unicode {
		return th.Paint(th.Pal.Teal, "*")
	}
	return th.Paint(th.Pal.Teal, rollSpinner[frame%len(rollSpinner)])
}

func amiWord(th *render.Theme, onTarget bool) string {
	if onTarget {
		return th.Paint(th.Pal.Green, "new")
	}
	return th.Paint(th.Pal.Peach, "old")
}

func nodeStateCell(th *render.Theme, n noderoll.NodeView) string {
	switch n.Phase {
	case noderoll.PhaseReady:
		return th.Paint(th.Pal.Green, "Ready")
	case noderoll.PhaseJoining:
		return th.Paint(th.Pal.Dim, "joining (NotReady)")
	case noderoll.PhaseDraining:
		s := th.Paint(th.Pal.Yellow, "draining")
		if n.PodsTotal > 0 {
			evicted := n.PodsTotal - n.Pods
			if evicted < 0 {
				evicted = 0
			}
			s += th.Paint(th.Pal.Dim, " · evicting ") + th.Bar(evicted, n.PodsTotal, 5, th.Pal.Yellow) +
				th.Paint(th.Pal.Dim, fmt.Sprintf(" %d/%d pods", evicted, n.PodsTotal))
		}
		return s
	default:
		return th.Paint(th.Pal.Dim, "unknown")
	}
}

// runRoll drives the live panel from obs until done(snapshot) is true or ctx is
// cancelled, repainting in place on a TTY and appending snapshots when piped.
func runRoll(ctx context.Context, th *render.Theme, w io.Writer, obs noderoll.Observer, m rollMeta, interval time.Duration, done func(noderoll.Snapshot) bool) error {
	tr := noderoll.NewTracker()
	lr := th.NewLiveRegion(w)
	frame := 0
	return lr.Run(ctx, interval, func() ([]string, bool) {
		snap, err := obs.Snapshot(ctx)
		if err != nil {
			return []string{th.Token(render.Fail, "observer error: "+err.Error())}, true
		}
		tr.Observe(snap)
		frame++
		m.Frame = frame
		return rollPanelLines(th, snap, tr.Recent(6), m), done(snap)
	})
}

// rollComplete reports whether every node is the desired count, Ready, and on
// the target AMI.
func rollComplete(desired int) func(noderoll.Snapshot) bool {
	return func(s noderoll.Snapshot) bool {
		return s.Total > 0 && s.ReadyTarget >= desired && s.Draining == 0 && s.Joining == 0
	}
}

// runSimulatedRoll renders the live node-roll panel against a scripted observer
// (no AWS / no cluster). Backs `nodegroup update --simulate` for demos and QA.
func runSimulatedRoll(ctx context.Context, nodegroup string) error {
	if nodegroup == "" {
		nodegroup = "spot-burst"
	}
	th := render.Default(os.Stdout)
	m := rollMeta{Nodegroup: nodegroup, OldAMI: "ami-0a1b…", NewAMI: "ami-0f9e…", Desired: 3}
	obs := noderoll.NewScriptedObserver(noderoll.DemoTimeline())

	fmt.Println()
	if err := runRoll(ctx, th, os.Stdout, obs, m, 160*time.Millisecond, rollComplete(m.Desired)); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(th.Token(render.Healthy, fmt.Sprintf("%s rolled — 3/3 on %s", nodegroup, m.NewAMI)))
	return nil
}
