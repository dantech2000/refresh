package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// rollScreen: a live nodegroup roll. Nodes and the draining node's pods on
// the left; the roll feed, Kubernetes events, AWS calls, and health gates on
// the right.
func (m Model) rollScreen(w, h int) Block {
	r, ok := m.roll()
	if !ok {
		return Block{{}, append(Line{sp(1)}, section("Rolls")...), {},
			{sp(1), sub("No nodegroup roll yet.")},
			{sp(1), dimS("On the fleet screen, select a cluster and press p to patch a nodegroup.")}}
	}
	top := m.rollHeader(r, w)
	bodyH := h - len(top)
	lw := w * 44 / 100
	rw := w - lw - 1
	return append(top, hjoin(bodyH, col{m.nodeList(r, lw, bodyH), lw}, vrule(bodyH), col{m.rollFeed(r, rw, bodyH), rw})...)
}

func (m Model) rollHeader(r state.Roll, w int) Block {
	title := Line{sp(1), bold(colMauve, heading(r.Cluster+" / "+r.Nodegroup)), sp(2)}
	if r.FromVersion != r.ToVersion {
		title = append(title, sub(r.FromVersion+" → "), tx(r.ToVersion), sp(2))
	}
	title = append(title, dimS("AMI "+r.FromAMI+" → "+r.ToAMI))
	if r.UpgradeOf != "" {
		title = append(title, sp(2), fg(colBlue, "part of the upgrade"))
	}
	elapsed := m.st.Now.Sub(r.StartedAt)
	if !r.Running() {
		elapsed = r.EndedAt.Sub(r.StartedAt)
	}
	right := Line{sub("elapsed "), tx(dur(elapsed))}
	if r.Running() && r.ETA > 0 {
		right = append(right, sp(2), sub("eta "), tx(fmt.Sprintf("~%dm", max(1, int(r.ETA.Round(time.Minute).Minutes())))))
	}
	right = append(right, sp(2), sub("maxUnavailable "), tx(fmt.Sprint(r.MaxUnavailable)))
	if len(m.st.Rolls) > 1 {
		right = append(right, sp(2), dimS(fmt.Sprintf("roll %d/%d · [ ]", m.rollIdx+1, len(m.st.Rolls))))
	}
	right = append(right, sp(1))

	s := r.Snapshot
	old := 0
	for _, n := range s.Nodes {
		if !n.OnTarget && n.Phase != noderoll.PhaseDraining {
			old++
		}
	}
	frac := float64(r.Replaced()) / float64(max(1, r.Planned))
	active := 0.0
	if s.Draining+s.Joining > 0 {
		active = 0.5 / float64(max(1, r.Planned))
	}
	prog := Line{sp(1)}
	prog = append(prog, bar(min(56, w/3), frac, active)...)
	prog = append(prog, sp(2), bold(colText, fmt.Sprintf("%d/%d replaced", r.Replaced(), r.Planned)), sp(2))
	switch {
	case r.Failed != "":
		prog = append(prog, tok(state.LevelError, ""+r.Failed))
	case !r.Running():
		prog = append(prog, tok(state.LevelOK, "done in "+dur(r.EndedAt.Sub(r.StartedAt))))
	default:
		prog = append(prog,
			tok(state.LevelOK, fmt.Sprintf("%d new", s.ReadyTarget)), sp(2),
			tok(state.LevelProgress, fmt.Sprintf("%d draining", s.Draining)), sp(2),
			tok(state.LevelProgress, fmt.Sprintf("%d joining", s.Joining)), sp(2),
			tok(state.LevelInfo, fmt.Sprintf("%d old", old)))
	}
	return Block{{}, joinRight(title, right, w), prog, append(Line{sp(1)}, hrule(w-2)...)}
}

// phaseOrder sorts the node list: draining, joining, old, new.
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

func (m Model) nodeList(r state.Roll, w, h int) Block {
	nodes := slices.Clone(r.Snapshot.Nodes)
	slices.SortStableFunc(nodes, func(a, b noderoll.NodeView) int { return phaseOrder(a) - phaseOrder(b) })
	head := Line{sp(3)}
	for _, c := range []struct {
		n string
		w int
	}{{"NODE", 18}, {"PHASE", 13}, {"PODS", 0}} {
		head = append(head, Seg{Text: padRight(c.n, c.w), FG: colDim, Bold: true})
	}
	out := Block{{}, head}

	var drainNode string
	card := Block{}
	for _, n := range nodes {
		if n.Phase == noderoll.PhaseDraining {
			drainNode = n.Name
		}
	}
	if drainNode != "" {
		card = m.podCard(r, drainNode, w-2)
	}
	listH := h - len(out) - len(card) - 1
	prevNew := false
	shown := 0
	for i, n := range nodes {
		isNew := n.OnTarget && n.Phase == noderoll.PhaseReady
		if isNew && !prevNew && i > 0 {
			out = append(out, append(Line{sp(1)}, hrule(w-2)...))
			shown++
		}
		prevNew = isNew
		if shown >= listH-1 && i < len(nodes)-1 {
			out = append(out, Line{sp(3), dimS(fmt.Sprintf("+%d more", len(nodes)-i))})
			break
		}
		l := Line{sp(1)}
		if n.Phase == noderoll.PhaseDraining {
			l = append(l, fg(colMauve, "▶ "))
		} else {
			l = append(l, sp(2))
		}
		l = append(l, tx(padRight(n.Name, 18)))
		var phase, pods Line
		switch {
		case n.Phase == noderoll.PhaseDraining:
			phase = Line{tok(state.LevelProgress, "draining")}
			evicted := n.PodsTotal - n.Pods
			pods = append(bar(10, float64(evicted)/float64(max(1, n.PodsTotal)), 0), sp(1), sub(fmt.Sprintf("%d/%d evicted", evicted, n.PodsTotal)))
		case n.Phase == noderoll.PhaseJoining:
			phase = Line{tok(state.LevelProgress, "joining")}
			pods = Line{dimS("kubelet up · CNI pending")}
		case !n.OnTarget:
			phase = Line{tok(state.LevelInfo, "old")}
			pods = Line{sub(fmt.Sprintf("%d pods", r.NodePods[n.Name]))}
		default:
			phase = Line{tok(state.LevelOK, "new")}
			pods = Line{sub(fmt.Sprintf("%d pods", r.NodePods[n.Name]))}
		}
		if len(n.Pressure) > 0 {
			pods = append(pods, sp(1), tok(state.LevelWarn, ""+strings.Join(n.Pressure, ",")))
		}
		l = append(l, phase.Fit(13)...)
		l = append(l, pods...)
		if n.Phase == noderoll.PhaseDraining {
			l = l.Fit(w).WithBG(colSurface0)
		}
		out = append(out, l)
		shown++
	}
	out = out.fit(w, h-len(card))
	return append(out, card.indent(1)...)
}

// podCard lists the pods still on the draining node.
func (m Model) podCard(r state.Roll, node string, w int) Block {
	pods := r.Pods[node]
	var body Block
	const maxPods = 6
	nameW := 0
	for _, p := range pods {
		nameW = max(nameW, width(p.Name))
	}
	nameW = min(nameW+2, 30)
	for i, p := range pods {
		if i == maxPods {
			body = append(body, Line{tok(state.LevelInfo, fmt.Sprintf("+%d more", len(pods)-maxPods))})
			break
		}
		l := Line{}
		switch p.State {
		case state.PodBlocked:
			l = append(l, levelGlyph(state.LevelWarn), sp(1), tx(padRight(p.Name, nameW)), fg(colYellow, p.Reason))
		case state.PodTerminating:
			l = append(l, levelGlyph(state.LevelProgress), sp(1), tx(padRight(p.Name, nameW)), sub(p.Reason))
		case state.PodDaemonSet:
			l = append(l, levelGlyph(state.LevelInfo), sp(1), sub(padRight(p.Name, nameW)), dimS(p.Reason))
		default:
			l = append(l, levelGlyph(state.LevelInfo), sp(1), tx(padRight(p.Name, nameW)), dimS("waiting"))
		}
		body = append(body, l)
	}
	return box(Line{bold(colMauve, node), sp(1), dimS("still evicting")}, body, w, colSurface1)
}

// logTabs draws the log-source tabs of a live pane.
func (m Model) logTabs(first string) Line {
	l := Line{sp(1)}
	for i, name := range []string{first, "Kube events", "AWS API", "All"} {
		if logSource(i) == m.src {
			l = append(l, Seg{Text: " " + name + " ", FG: colCrust, BG: colMauve, Bold: true})
		} else {
			l = append(l, sub(" "+name+" "))
		}
		l = append(l, sp(1))
	}
	return l
}

func (m Model) keepSource(e state.Event) bool {
	switch m.src {
	case logKube:
		return e.Source == state.SourceKube
	case logAWS:
		return e.Source == state.SourceAWS
	case logAll:
		return true
	default:
		return e.Source != state.SourceKube && e.Source != state.SourceAWS
	}
}

func (m Model) rollFeed(r state.Roll, w, h int) Block {
	right := append(m.followToken(), sp(1))
	out := Block{{}, joinRight(m.logTabs("Roll"), right, w)}
	gates := Line{sub("health gates"), sp(2)}
	for _, g := range r.Gates {
		gates = append(gates, levelGlyph(checkLevel(g.Status)), sp(1), fg(levelColor(checkLevel(g.Status)), g.Name), sp(2))
	}
	gateBox := box(nil, Block{gates}, w-2, colSurface1)

	var kubeSide Block
	if m.src == logRoll {
		kubeSide = m.kubeSummary(r, w, 5)
	}
	feedH := h - len(out) - len(gateBox) - len(kubeSide) - 1
	evs := m.visible(m.rollEvents(r), m.keepSource)
	for i := 0; i < len(evs) && i < feedH; i++ {
		out = append(out, eventLine(evs[i]))
	}
	switch {
	case m.pausedSeq != 0 && m.rollEvents(r) == nil:
		out = append(out, Line{sp(1), dimS("this roll started after the freeze · space to follow")})
	case len(evs) == 0:
		out = append(out, Line{sp(1), dimS("nothing yet")})
	}
	out = out.fit(w, feedH+2)
	out = append(out, kubeSide...)
	out = out.fit(w, h-len(gateBox))
	return append(out, gateBox.indent(1)...)
}

// kubeSummary is the "Kubernetes events, warnings first" section under the
// roll feed.
func (m Model) kubeSummary(r state.Roll, w, n int) Block {
	evs := m.visible(m.rollEvents(r), func(e state.Event) bool { return e.Source == state.SourceKube })
	slices.SortStableFunc(evs, func(a, b state.Event) int {
		aw, bw := a.Level == state.LevelWarn, b.Level == state.LevelWarn
		switch {
		case aw && !bw:
			return -1
		case bw && !aw:
			return 1
		}
		return 0
	})
	out := Block{append(Line{sp(1)}, hrule(w-2)...), append(Line{sp(1)}, section("Kube events", dimS("warnings first"))...)}
	for i := 0; i < len(evs) && i < n; i++ {
		out = append(out, kubeLine(evs[i]))
	}
	return out
}

// eventLine draws one feed event by its source.
func eventLine(e state.Event) Line {
	switch e.Source {
	case state.SourceKube:
		return kubeLine(e)
	case state.SourceAWS:
		return Line{sp(1), dimS(clock(e.At)), sp(1), fg(colBlue, padRight(e.Subject, 26)), sub(e.Text)}
	}
	l := Line{sp(1), dimS(clock(e.At)), sp(1), levelGlyph(e.Level), sp(1)}
	c := levelColor(e.Level)
	if e.Level == state.LevelInfo {
		c = colSubtext
	}
	switch e.Text {
	case "joining", "online", "draining", "terminated":
		l = append(l, fg(c, padRight(e.Text, 11)), tx(e.Subject))
	default:
		if e.Subject != "" {
			l = append(l, tx(e.Subject), sp(1))
		}
		l = append(l, fg(c, e.Text))
	}
	if e.Detail != "" {
		l = append(l, sp(2), dimS(e.Detail))
	}
	return l
}

func kubeLine(e state.Event) Line {
	kind := fg(colBlue, "Normal ")
	if e.Level == state.LevelWarn {
		kind = fg(colYellow, "Warning")
	}
	return Line{sp(1), dimS(clock(e.At)), sp(1), kind, sp(1), sub(padRight(e.Text, 19)), tx(padRight(e.Subject, 32)), dimS(e.Detail)}
}
