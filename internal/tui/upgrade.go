package tui

import (
	"fmt"
	"strings"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// upgradeScreen: the orchestrator's phases as a timeline on the left, the
// live timeline and logs on the right.
func (m Model) upgradeScreen(w, h int) Block {
	u, ok := m.upgrade()
	if !ok {
		return Block{{}, append(Line{sp(1)}, section("Upgrade")...), {},
			{sp(1), sub("No cluster upgrade yet.")},
			{sp(1), dimS("On the fleet screen, select a cluster and press U to plan one.")}}
	}
	top := m.upgradeHeader(u, w)
	bodyH := h - len(top)
	lw := max(44, w*33/100)
	rw := w - lw - 1
	return append(top, hjoin(bodyH, col{m.phaseList(u, lw, bodyH), lw}, vrule(bodyH), col{m.upgradeFeed(u, rw, bodyH), rw})...)
}

func (m Model) upgradeHeader(u state.Upgrade, w int) Block {
	title := Line{sp(1), bold(colMauve, heading(fmt.Sprintf("Upgrade %s %s → %s", u.Cluster, u.From, u.To))), sp(2)}
	cur := u.Current()
	switch {
	case u.Failed != "":
		title = append(title, tok(state.LevelError, "failed · "+u.Failed))
	case u.Stopped:
		title = append(title, tok(state.LevelWarn, "stopped"))
	case !u.Running():
		title = append(title, tok(state.LevelOK, "done in "+dur(u.EndedAt.Sub(u.StartedAt))))
	case u.Paused && cur < 0:
		title = append(title, fg(colYellow, "❚❚ paused"))
	case cur >= 0:
		title = append(title, tok(state.LevelProgress, fmt.Sprintf("phase %d/%d · %s", cur+1, len(u.Phases), strings.ToLower(u.Phases[cur].Name))))
	}
	elapsed := m.st.Now.Sub(u.StartedAt)
	if !u.Running() {
		elapsed = u.EndedAt.Sub(u.StartedAt)
	}
	right := Line{sub("elapsed "), tx(dur(elapsed)), sp(2), dimS("safe to quit · a rerun resumes from live cluster state"), sp(1)}
	if title.Width()+right.Width() > w {
		right = Line{sub("elapsed "), tx(dur(elapsed)), sp(1)}
	}
	if len(m.st.Upgrades) > 1 {
		right = append(Line{dimS(fmt.Sprintf("upgrade %d/%d · [ ]", m.upIdx+1, len(m.st.Upgrades))), sp(2)}, right...)
	}
	return Block{{}, joinRight(title, right, w), append(Line{sp(1)}, hrule(w-2)...)}
}

func (m Model) phaseList(u state.Upgrade, w, h int) Block {
	out := Block{{}}
	pipe := fg(colSurface1, "│")
	for i, p := range u.Phases {
		took := ""
		switch {
		case p.Status == state.PhaseRunning:
			took = dur(m.st.Now.Sub(p.StartedAt))
		case !p.EndedAt.IsZero() && !p.StartedAt.IsZero():
			took = dur(p.EndedAt.Sub(p.StartedAt))
		default:
			took = "—"
		}
		nameC := colText
		if p.Status == state.PhasePending {
			nameC = colDim
		}
		row := joinRight(Line{sp(1), phaseGlyph(p.Status), sp(1), bold(nameC, p.Name)}, Line{fg(levelColor(phaseLevel(p.Status)), took), sp(1)}, w)
		if p.Status == state.PhaseRunning {
			row = row.WithBG(colSurface0)
		}
		out = append(out, row)
		last := i == len(u.Phases)-1
		conn := pipe
		if last {
			conn = sp(1)
		}
		if p.Summary != "" {
			out = append(out, Line{sp(2), conn, sp(1), dimS(p.Summary)})
		} else if p.Status == state.PhasePending {
			out = append(out, Line{sp(2), conn, sp(1), dimS(pendingHint(i))})
		}
		if p.Status == state.PhaseRunning && len(p.Items) == 0 {
			out = append(out, append(Line{sp(2), conn, sp(1)}, bar(w-12, p.Progress, 0)...))
		}
		for _, it := range p.Items {
			l := Line{sp(2), conn, sp(1), phaseGlyph(it.Status), sp(1), tx(padRight(it.Name, 12))}
			if it.Status == state.PhaseRunning && it.Progress > 0 && it.Progress < 1 {
				l = append(l, fg(colTeal, padRight(it.Text, 6)), sp(1))
				l = append(l, bar(max(4, w-l.Width()-2), it.Progress, 0)...)
			} else {
				l = append(l, sub(it.Text))
			}
			out = append(out, l)
		}
	}
	stopOn, pauseOn := dimS("off"), dimS("off")
	if u.StopAfter {
		stopOn = fg(colYellow, "on")
	}
	if u.Paused {
		pauseOn = fg(colYellow, "on")
	}
	var opts Block
	if u.Running() {
		opts = box(Line{bold(colMauve, "Stop options")}, Block{
			{chip("S"), sp(1), sub("stop after the current step "), stopOn},
			{chip("P"), sp(1), sub("pause before next phase "), pauseOn},
			{dimS("An in-flight EKS update is never cancelled.")},
		}, w-2, colSurface1)
	}
	out = out.fit(w, h-len(opts))
	return append(out, opts.indent(1)...)
}

func pendingHint(i int) string {
	return []string{"readiness checks", "UpdateClusterVersion", "update stale add-ons", "roll each nodegroup", "nodes, pods, add-on health"}[min(i, 4)]
}

// upgradeFeed shows the upgrade timeline, or the Kubernetes events and AWS
// calls of its rolls.
func (m Model) upgradeFeed(u state.Upgrade, w, h int) Block {
	out := Block{{}, joinRight(m.logTabs("Timeline"), append(m.followToken(), sp(1)), w)}
	now := m.nowCard(u, w-2)
	feedH := h - len(out) - len(now)
	var evs []state.Event
	switch m.src {
	case logRoll:
		evs = m.visible(m.upgradeEvents(u), nil)
	case logAWS:
		evs = m.visible(m.feed().Log, func(e state.Event) bool { return e.Cluster == u.Cluster && !e.At.Before(u.StartedAt) })
	default:
		var all []state.Event
		for _, r := range m.feed().Rolls {
			if r.UpgradeOf == u.Cluster {
				all = append(all, r.Events...)
			}
		}
		if m.src == logAll {
			all = append(all, m.upgradeEvents(u)...)
		}
		sortByTime(all)
		evs = m.visible(all, func(e state.Event) bool { return m.src == logAll || e.Source == state.SourceKube })
	}
	for i := 0; i < len(evs) && i < feedH; i++ {
		e := evs[i]
		if e.Source == state.SourceUpgrade {
			l := Line{sp(1), dimS(clock(e.At)), sp(1)}
			l = append(l, Line{fg(colBlue, e.Subject)}.Fit(13)...)
			l = append(l, sp(1), levelGlyph(e.Level), sp(1), tx(e.Text))
			if e.Detail != "" {
				l = append(l, sp(2), dimS(e.Detail))
			}
			out = append(out, l)
			continue
		}
		out = append(out, eventLine(e))
	}
	switch {
	case m.pausedSeq != 0 && m.upgradeEvents(u) == nil:
		out = append(out, Line{sp(1), dimS("this upgrade started after the freeze · space to follow")})
	case len(evs) == 0:
		out = append(out, Line{sp(1), dimS("nothing yet")})
	}
	out = out.fit(w, h-len(now))
	return append(out, now.indent(1)...)
}

func sortByTime(evs []state.Event) {
	// Stable insertion sort: the lists are short and mostly sorted.
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].At.Before(evs[j-1].At); j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
}

func (m Model) nowCard(u state.Upgrade, w int) Block {
	var what Line
	cur := u.Current()
	switch {
	case !u.Running() && u.Failed == "" && !u.Stopped:
		what = Line{tok(state.LevelOK, ""+u.Cluster+" runs "+u.To)}
	case !u.Running():
		what = Line{sub("ended · " + clock(u.EndedAt))}
	case cur < 0:
		what = Line{sub("waiting before the next phase")}
	default:
		p := u.Phases[cur]
		what = Line{sub(strings.ToLower(p.Name))}
		for _, it := range p.Items {
			if it.Status == state.PhaseRunning {
				what = append(what, sub(" · "+it.Name+" "+it.Text))
			}
		}
		if cur == 3 {
			for _, r := range m.st.Rolls {
				if r.UpgradeOf == u.Cluster && r.Running() {
					if pods := r.Pods; len(pods) > 0 {
						for _, ps := range pods {
							for _, p := range ps {
								if p.State == state.PodBlocked {
									what = append(what, fg(colYellow, " · a drain waits on a PDB"))
								}
							}
						}
					}
				}
			}
		}
	}
	body := Block{
		what,
		append(bar(w-18, u.Progress(), 0), sp(2), sub(fmt.Sprintf("overall %3.0f%%", u.Progress()*100))),
	}
	return box(Line{bold(colMauve, "Now")}, body, w, colSurface1)
}
