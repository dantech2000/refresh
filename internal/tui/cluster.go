package tui

import (
	"fmt"
	"strconv"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// clusterScreen: the readiness checks of the selected cluster, streaming in
// as they finish, with the selected check's detail and the check log.
func (m Model) clusterScreen(w, h int) Block {
	c := m.cluster()
	r, ok := m.st.Readiness[c.Name]
	if !ok {
		return Block{{}, append(Line{sp(1)}, section("Readiness · "+c.Name)...), {}, {sp(1), dimS("No readiness run yet. Press R to run the checks.")}}
	}
	head := Line{sp(1), bold(colMauve, heading(fmt.Sprintf("Readiness · %s %s → %s", c.Name, r.From, r.To))), sp(2)}
	switch {
	case r.Running:
		head = append(head, tok(state.LevelProgress, "checking"))
	case r.Blockers() > 0:
		head = append(head, tok(state.LevelError, "blocked"))
	case r.Warnings() > 0:
		head = append(head, tok(state.LevelWarn, "ready with warnings"))
	default:
		head = append(head, tok(state.LevelOK, "ready"))
	}
	head = append(head, sp(2), sub(plural(r.Blockers(), "blocker")+" · "+plural(r.Warnings(), "warning")+" · "+strconv.Itoa(r.Passed())+" passed"))
	right := Line{dimS("started " + clock(r.StartedAt)), sp(1)}
	if n := r.Pending(); n > 0 {
		right = Line{tok(state.LevelProgress, fmt.Sprintf("%d checks to go", n)), sp(1)}
	}
	top := Block{{}, joinRight(head, right, w), append(Line{sp(1)}, hrule(w-2)...)}

	bodyH := h - len(top)
	lw := w * 44 / 100
	rw := w - lw - 1
	return append(top, hjoin(bodyH, col{m.checkList(r, lw, bodyH), lw}, vrule(bodyH), col{m.checkDetail(r, rw, bodyH), rw})...)
}

func (m Model) checkList(r state.Readiness, w, h int) Block {
	var out Block
	group := ""
	selRow := 0
	nameW := 0
	for _, ch := range r.Checks {
		nameW = max(nameW, width(ch.Name))
	}
	nameW = min(nameW+2, 24)
	for i, ch := range r.Checks {
		if ch.Group != group {
			group = ch.Group
			if len(out) > 0 {
				out = append(out, Line{})
			}
			out = append(out, Line{sp(1), Seg{Text: group, FG: colDim, Bold: true}})
		}
		mark := sp(2)
		if i == m.checkSel {
			mark = fg(colMauve, "▶ ")
			selRow = len(out)
		}
		l := Line{sp(1), mark, levelGlyph(checkLevel(ch.Status)), sp(1)}
		if ch.Status == state.CheckPending || ch.Status == state.CheckRunning {
			l = append(l, sub(ch.Name))
		} else {
			l = append(l, Line{tx(ch.Name)}.Fit(nameW)...)
			l = append(l, dimS(ch.Summary))
		}
		if i == m.checkSel {
			l = l.Fit(w).WithBG(colSurface0)
		}
		out = append(out, l)
	}
	// Scroll so the selected check stays visible.
	if selRow >= h-1 {
		out = out[selRow-h+2:]
	}
	return append(Block{{}}, out...)
}

func (m Model) checkDetail(r state.Readiness, w, h int) Block {
	out := Block{{}}
	if m.checkSel < len(r.Checks) {
		out = append(out, checkBody(r.Checks[m.checkSel], w)...)
	}
	// The check log fills the bottom.
	logH := min(8, max(3, h-len(out)-2))
	out = out.fit(w, h-logH-1)
	out = append(out, append(Line{sp(2)}, section("Check log", m.liveToken(r.Running)...)...))
	evs := m.visible(m.feed().Readiness[r.Cluster].Log, nil)
	for i := 0; i < len(evs) && i < logH; i++ {
		e := evs[i]
		l := Line{sp(2), dimS(clock(e.At)), sp(1), levelGlyph(e.Level), sp(1)}
		l = append(l, Line{fg(colBlue, e.Subject)}.Fit(25)...)
		out = append(out, append(l, sp(1), sub(e.Text)))
	}
	return out
}

// liveToken marks a pane that is still receiving events.
func (m Model) liveToken(live bool) []Seg {
	if m.pausedSeq != 0 || m.warnOnly {
		return m.followToken()
	}
	if live {
		return []Seg{tok(state.LevelOK, "live")}
	}
	return []Seg{dimS("done")}
}

// checkBody is the detail of one check: verdict, explanation, table, fix.
func checkBody(ch state.Check, w int) Block {
	inner := w - 4
	lvl := checkLevel(ch.Status)
	title := Line{sp(2), Seg{Text: levelGlyph(lvl).Text + " " + ch.Name, FG: levelColor(lvl), Bold: true}}
	if ch.Source != "" {
		title = append(title, sp(2), dimS("source: "+ch.Source))
	}
	out := Block{title}
	switch ch.Status {
	case state.CheckPending:
		out = append(out, Line{sp(2), dimS("waiting to run")})
	case state.CheckRunning:
		out = append(out, Line{sp(2), tok(state.LevelProgress, "running…")})
	default:
		out = append(out, Line{sp(2), tx(ch.Summary)})
	}
	for _, d := range ch.Detail {
		for _, s := range wrap(d, inner) {
			out = append(out, Line{sp(2), sub(s)})
		}
	}
	if ch.Table != nil {
		out = append(out, Line{})
		out = append(out, tableBlock(ch.Table, inner).indent(2)...)
	}
	if len(ch.Fix) > 0 {
		var fix Block
		for i, f := range ch.Fix {
			for j, s := range wrap(f, inner-7) {
				prefix := "   "
				if j == 0 {
					prefix = strconv.Itoa(i+1) + ". "
				}
				fix = append(fix, Line{sub(prefix + s)})
			}
		}
		out = append(out, Line{})
		out = append(out, box(Line{bold(colMauve, "Fix")}, fix, w-4, colSurface1).indent(2)...)
	}
	return out
}

// tableBlock draws a small table with a header and a rule.
func tableBlock(t *state.Table, w int) Block {
	widths := make([]int, len(t.Header))
	for i, hd := range t.Header {
		widths[i] = width(hd) + 3
		for _, row := range t.Rows {
			if i < len(row) {
				widths[i] = max(widths[i], width(row[i])+3)
			}
		}
	}
	hl := Line{}
	for i, hd := range t.Header {
		hl = append(hl, Seg{Text: padRight(hd, widths[i]), FG: colDim, Bold: true})
	}
	out := Block{hl, hrule(w)}
	for _, row := range t.Rows {
		l := Line{}
		for i, cell := range row {
			if i < len(widths) {
				l = append(l, tx(padRight(cell, widths[i])))
			}
		}
		out = append(out, l)
	}
	return out
}
