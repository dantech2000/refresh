package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// change is the change on the Change screen, or false with none.
func (m Model) change() (state.Change, bool) {
	if m.chg >= 0 && m.chg < len(m.st.Changes) {
		return m.st.Changes[m.chg], true
	}
	return state.Change{}, false
}

// changeScreen: the phases and the steps of the change on the left, with
// the nodes of the step that rolls; the log on the right.
func (m Model) changeScreen(w, h int) Block {
	c, ok := m.change()
	if !ok {
		return Block{{}, {sp(1), m.heading("Change")}, {},
			{sp(1), sub("No change yet.")},
			{sp(1), dim("On the fleet screen, select a cluster and press p, a, U, or B to preview one.")}}
	}
	top := m.changeHeader(c, w)
	bodyH := h - len(top)
	lw := max(44, w*48/100)
	rw := w - lw - 1
	return append(top, hjoin(bodyH, col{m.changeSteps(c, lw, bodyH), lw}, vrule(bodyH, m.bd), col{m.changeLog(c, rw, bodyH), rw})...)
}

// kindLabel names a kind of change for a person.
func kindLabel(k change.Kind) string {
	switch k {
	case change.NodegroupUpdate:
		return "Roll"
	case change.AddonUpdate:
		return "Add-on update"
	case change.ClusterUpgrade:
		return "Upgrade"
	case change.ClusterRollback:
		return "Rollback"
	case change.NodegroupScale:
		return "Scale"
	}
	return string(k)
}

// changeStatus is the one token of the state of a change.
func (m Model) changeStatus(c state.Change) Line {
	switch c.State {
	case change.Preparing:
		return Line{m.tok(view.Progress, "preparing")}
	case change.Running:
		return Line{m.tok(view.Progress, "running")}
	case change.Paused:
		return Line{statusSeg(view.Warn, m.th.Mark(view.Warn)+" paused")}
	case change.Asking:
		return Line{m.tok(view.Warn, "question")}
	}
	if c.Result == nil {
		return Line{m.tok(view.Unknown, "finished")}
	}
	return Line{m.tok(view.RunStatus(c.Result.Status), strings.ToLower(string(c.Result.Status)))}
}

func (m Model) changeHeader(c state.Change, w int) Block {
	title := Line{sp(1), m.heading(kindLabel(c.Kind) + " " + c.Target.Cluster), sp(2), sub(c.Target.Region)}
	if p := c.Plan; p != nil && len(p.Clusters) > 0 && !p.Clusters[0].To.IsZero() && p.Clusters[0].To != p.Clusters[0].From {
		title = append(title, sp(2), tx(p.Clusters[0].From.String()+" → "+p.Clusters[0].To.String()))
	}
	title = append(title, sp(2))
	title = append(title, m.changeStatus(c)...)
	if c.Elsewhere {
		title = append(title, sp(2), role(view.RoleWarn, "started elsewhere · watch only"))
	}
	if c.StopRequested && c.Running() {
		title = append(title, sp(2), role(view.RoleWarn, "stop requested"))
	} else if c.PauseRequested && c.State != change.Paused {
		title = append(title, sp(2), role(view.RoleWarn, "pause requested"))
	}
	elapsed := m.now.Sub(c.StartedAt)
	if c.Result != nil && len(c.Log) > 0 {
		elapsed = c.Log[len(c.Log)-1].At.Sub(c.StartedAt)
	}
	right := Line{sub("elapsed "), tx(dur(elapsed))}
	if n := len(m.st.Changes); n > 1 {
		right = append(right, sp(2), dim(fmt.Sprintf("change %d/%d · [ ]", m.chg+1, n)))
	}
	right = append(right, sp(1))
	out := Block{{}, joinRight(title, right, w)}
	if q := c.Question; q != nil {
		line := Line{sp(1), m.mark(view.Warn), sp(1), bold(view.RoleWarn, q.Text), sp(2)}
		if c.Controls.Answer {
			line = append(line, chip("y"), sp(1), sub("go on"), sp(2), chip("n"), sp(1), sub("stop"))
		} else {
			line = append(line, dim("answered elsewhere"))
		}
		out = append(out, line.Fit(w))
		for _, r := range q.Reasons {
			out = append(out, Line{sp(3), sub(r)}.Fit(w))
		}
	}
	return append(out, append(Line{sp(1)}, hrule(w-2, m.bd)...))
}

// changeSteps lists the phases with their steps, and the nodes under the
// step that rolls.
func (m Model) changeSteps(c state.Change, w, h int) Block {
	out := Block{{}}
	if c.Plan == nil {
		return append(out, Line{sp(1), m.tok(view.Progress, "reading the cluster and computing the plan")}).fit(w, h)
	}
	var rolling *change.StepRef
	for _, ph := range c.Phases {
		line := Line{sp(1), m.mark(phaseStatus(ph.Status)), sp(1), bold(view.RoleText, phaseLabel(ph.Ref)), sp(1), dim(strings.ToLower(string(ph.Status)))}
		if !ph.StartedAt.IsZero() {
			end := m.now
			if !ph.EndedAt.IsZero() {
				end = ph.EndedAt
			}
			line = append(line, sp(1), dim(dur(end.Sub(ph.StartedAt))))
		}
		out = append(out, line.Fit(w))
		for _, s := range c.Steps {
			if s.Ref.Phase != ph.Ref {
				continue
			}
			out = append(out, m.stepLine(c, s, w))
			if s.Status == change.StepStarted && s.Ref.Kind == change.StepNodegroup && c.Progress[s.Ref].Nodes != nil && rolling == nil {
				ref := s.Ref
				rolling = &ref
			}
		}
	}
	if rolling != nil {
		out = append(out, Line{})
		out = append(out, m.nodeTable(c, *rolling, w)...)
	}
	return out.fit(w, h)
}

// stepLine is one step: its mark, its name, what it moves from and to,
// and what became of it.
func (m Model) stepLine(c state.Change, s change.StepResult, w int) Line {
	name := s.Ref.Name
	if name == "" {
		name = "control plane"
	}
	l := Line{sp(3), m.mark(view.StepStatus(s.Status)), sp(1), tx(name), sp(1), dim(strings.ToLower(string(s.Status)))}
	if step := planStep(c.Plan, s.Ref); step != nil && step.From != "" && step.To != "" && step.From != step.To {
		l = append(l, sp(1), sub(step.From+" → "+step.To))
	}
	if s.Attached {
		l = append(l, sp(1), dim("attached"))
	}
	if p := c.Progress[s.Ref].Update; p != nil && s.Status == change.StepStarted && p.Status != "" {
		l = append(l, sp(1), dim(p.Status))
	}
	switch {
	case s.Failure != nil:
		l = append(l, sp(1), role(view.RoleFail, s.Failure.Error))
	case s.VerifyIssue != "":
		l = append(l, sp(1), role(view.RoleFail, s.VerifyIssue))
	}
	return l.Fit(w)
}

// planStep returns the step of the plan with the ref, or nil.
func planStep(p *change.Plan, ref change.StepRef) *change.Step {
	if p == nil {
		return nil
	}
	for i := range p.Clusters {
		for _, ph := range p.Clusters[i].Phases {
			for j := range ph.Steps {
				if ph.Steps[j].Ref == ref {
					return &ph.Steps[j]
				}
			}
		}
	}
	return nil
}

func phaseLabel(ref change.PhaseRef) string {
	label := string(ref.Kind)
	switch ref.Kind {
	case change.PhaseControlPlane:
		label = "Control plane"
	case change.PhaseAddonsBefore:
		label = "Add-ons before the nodegroups"
	case change.PhaseAddonsAfter:
		label = "Add-ons after the nodegroups"
	case change.PhaseAddons:
		label = "Add-ons"
	case change.PhaseNodegroups:
		label = "Nodegroups"
	case change.PhaseRoll:
		label = "Roll"
	case change.PhaseScale:
		label = "Scale"
	}
	if !ref.Hop.IsZero() {
		label += " " + ref.Hop.String()
	}
	return label
}

func phaseStatus(s change.PhaseStatus) view.Status {
	switch s {
	case change.PhaseRunning:
		return view.Progress
	case change.PhaseDone:
		return view.Healthy
	case change.PhaseFailed:
		return view.Fail
	case change.PhaseStopped:
		return view.Warn
	case change.PhaseSkipped:
		return view.Neutral
	}
	return view.Unknown
}

// phaseOrder sorts the node table: draining, joining, old, new.
func phaseOrder(n noderoll.NodeView) int {
	switch {
	case n.Phase == noderoll.PhaseDraining:
		return 0
	case n.Phase == noderoll.PhaseJoining:
		return 1
	case !n.OnTarget:
		return 2
	default:
		return 3
	}
}

// nodeTable shows the nodes of the roll of a step: a bar of the replaced
// nodes, then one row per node with its state and the pods it still has
// to drain.
func (m Model) nodeTable(c state.Change, ref change.StepRef, w int) Block {
	p := c.Progress[ref].Nodes
	snap := p.Snapshot
	nodes := slices.Clone(snap.Nodes)
	slices.SortStableFunc(nodes, func(a, b noderoll.NodeView) int { return phaseOrder(a) - phaseOrder(b) })
	old := 0
	for _, n := range nodes {
		if !n.OnTarget {
			old++
		}
	}
	planned := max(snap.Total, old+snap.ReadyTarget+snap.Joining)
	source := "watched"
	if !p.Watching {
		source = "polled"
	}
	head := Line{sp(1), m.heading("Nodes of " + ref.Name), sp(2), sub(fmt.Sprintf("%d/%d replaced", snap.ReadyTarget, max(1, planned))), sp(2), dim(source)}
	out := Block{head.Fit(w), append(Line{sp(1)}, bar(min(40, w-4), float64(snap.ReadyTarget)/float64(max(1, planned)), m.bd)...)}
	nameW := len("NODE")
	for _, n := range nodes {
		nameW = max(nameW, width(n.Name))
	}
	nameW = min(nameW, 30) + 2
	out = append(out, Line{sp(1), bold(view.RoleDim, padRight("NODE", nameW)+padRight("OLD/NEW", 9)+"STATE")})
	for _, n := range nodes {
		l := Line{sp(1)}
		l = append(l, Line{tx(n.Name)}.Fit(nameW)...)
		age := "old"
		if n.OnTarget {
			age = "new"
		}
		l = append(l, Line{sub(age)}.Fit(9)...)
		l = append(l, m.nodeState(n)...)
		out = append(out, l.Fit(w))
	}
	for _, warn := range snap.Warnings {
		out = append(out, Line{sp(1), m.mark(view.Warn), sp(1), sub(warn.Node + ": " + warn.Message)}.Fit(w))
	}
	return out
}

// nodeState is the state cell of a node.
func (m Model) nodeState(n noderoll.NodeView) Line {
	var l Line
	switch n.Phase {
	case noderoll.PhaseReady:
		l = Line{m.tok(view.Healthy, "Ready")}
	case noderoll.PhaseJoining:
		l = Line{m.tok(view.Progress, "joining (NotReady)")}
	case noderoll.PhaseDraining:
		l = Line{m.tok(view.Warn, "draining")}
		if n.PodsTotal > 0 {
			evicted := max(n.PodsTotal-n.Pods, 0)
			l = append(l, sp(1), dim(fmt.Sprintf("%d/%d pods evicted", evicted, n.PodsTotal)))
			if len(n.PodList) > 0 {
				names := make([]string, 0, len(n.PodList))
				for _, pod := range n.PodList {
					names = append(names, pod.Name)
				}
				l = append(l, sp(1), dim("left: "+strings.Join(names, ", ")))
			}
		}
	default:
		l = Line{dim("unknown")}
	}
	if len(n.Pressure) > 0 {
		l = append(l, sp(1), m.tok(view.Warn, strings.Join(n.Pressure, "+")))
	}
	return l
}

// logLine is one line of the merged log of a change.
type logLine struct {
	at     time.Time
	status view.Status
	text   string
}

// changeLog shows the log of the change, newest first: the lifecycle
// events and the node events of its rolls, merged by time. A frozen pane
// shows the entries up to the freeze.
func (m Model) changeLog(c state.Change, w, h int) Block {
	follow := Line{m.tok(view.Healthy, "following")}
	if m.frozen != 0 {
		follow = Line{statusSeg(view.Warn, m.th.Mark(view.Warn)+" frozen")}
	}
	if m.warnOnly {
		follow = append(follow, sp(2), m.tok(view.Warn, "warnings only"))
	}
	out := Block{{}, joinRight(Line{sp(1), m.heading("Log")}, append(follow, sp(1)), w)}
	lines := m.logLines(c)
	shown := 0
	for i := len(lines) - 1; i >= 0 && shown < h-len(out); i-- {
		l := lines[i]
		if m.warnOnly && l.status != view.Warn && l.status != view.Fail {
			continue
		}
		shown++
		out = append(out, Line{sp(1), dim(clock(l.at)), sp(1), m.mark(l.status), sp(1), sub(l.text)}.Fit(w))
	}
	if shown == 0 {
		out = append(out, Line{sp(1), dim("nothing yet")})
	}
	return out.fit(w, h)
}

// logLines merges the log and the node events of a change into lines by
// time, up to the frozen sequence number.
func (m Model) logLines(c state.Change) []logLine {
	var lines []logLine
	for _, e := range c.Log {
		if m.frozen != 0 && e.Seq > m.frozen {
			break
		}
		status, text := eventText(e.Event)
		if text == "" {
			continue
		}
		lines = append(lines, logLine{at: e.At, status: status, text: text})
	}
	for _, events := range c.Nodes {
		for _, e := range events {
			if m.frozen != 0 && len(lines) > 0 && e.At.After(lines[len(lines)-1].at) {
				continue
			}
			s, text := nodeEventText(e.Kind)
			lines = append(lines, logLine{at: e.At, status: s, text: e.Node + " " + text})
		}
	}
	slices.SortStableFunc(lines, func(a, b logLine) int { return a.at.Compare(b.at) })
	return lines
}

// eventText words one lifecycle event, or "" for one the log does not
// show.
func eventText(e change.Event) (view.Status, string) {
	switch e.Kind {
	case change.PlanReady:
		return view.Neutral, "plan computed"
	case change.GateReport:
		if e.Verdict == nil {
			return view.Neutral, "health gate ran"
		}
		return gateStatus(e.Verdict.Decision), "health gate: " + strings.ToLower(string(e.Verdict.Decision))
	case change.PhaseStarted:
		return view.Progress, phaseLabel(e.Phase) + " started"
	case change.PhaseEnded:
		if e.Skipped {
			return view.Neutral, phaseLabel(e.Phase) + " skipped"
		}
		return view.Neutral, phaseLabel(e.Phase) + " ended"
	case change.StepChanged:
		if e.Result == nil {
			return view.Neutral, ""
		}
		name := e.Step.Name
		if name == "" {
			name = "control plane"
		}
		text := name + " " + strings.ToLower(string(e.Result.Status))
		if e.Result.UpdateID != "" && e.Result.Status == change.StepStarted {
			text += " (update " + e.Result.UpdateID + ")"
		}
		if e.Result.Failure != nil {
			text += ": " + e.Result.Failure.Error
		}
		return view.StepStatus(e.Result.Status), text
	case change.Notice:
		return view.Warn, e.Text
	case change.Progress:
		return view.Progress, e.Text
	case change.VerifyReport:
		worst := view.Healthy
		var parts []string
		for _, r := range e.Checks {
			parts = append(parts, r.Name+": "+strings.ToLower(string(r.Outcome)))
			if s := outcomeStatus(r.Outcome); s == view.Fail || (s == view.Unknown && worst != view.Fail) {
				worst = s
			}
		}
		return worst, "verification: " + strings.Join(parts, ", ")
	}
	return view.Neutral, ""
}

func gateStatus(d health.Decision) view.Status {
	switch d {
	case health.Go:
		return view.Healthy
	case health.Ask:
		return view.Warn
	case health.Block:
		return view.Fail
	}
	return view.Unknown
}

func nodeEventText(k noderoll.EventKind) (view.Status, string) {
	switch k {
	case noderoll.EvtJoining:
		return view.Progress, "joining"
	case noderoll.EvtOnline:
		return view.Healthy, "came online (Ready)"
	case noderoll.EvtDraining:
		return view.Warn, "cordoned, draining"
	case noderoll.EvtTerminated:
		return view.Neutral, "drained and terminated"
	}
	return view.Neutral, string(k)
}
