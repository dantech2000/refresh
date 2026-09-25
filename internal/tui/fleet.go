package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// fleetScreen: the cluster table and the selected cluster's card on the
// left, the live fleet feed and the changes in flight on the right.
func (m Model) fleetScreen(w, h int) Block {
	lw := w * 62 / 100
	rw := w - lw - 1
	return hjoin(h, col{m.fleetTable(lw, h), lw}, vrule(h), col{m.fleetFeed(rw, h), rw})
}

type fleetCol struct {
	name string
	w    int
}

func (m Model) fleetCols(w int) []fleetCol {
	cols := []fleetCol{{"", 2}, {"CLUSTER", 14}, {"REGION", 11}, {"VERSION", 13}, {"NODEGROUPS", 12}, {"ADD-ONS", 11}, {"STATUS", 0}}
	if w < 92 {
		cols = slices.Delete(cols, 2, 3)
	}
	used := 0
	for _, c := range cols {
		used += c.w
	}
	cols[len(cols)-1].w = max(10, w-2-used)
	return cols
}

func (m Model) fleetTable(w, h int) Block {
	need, busy := 0, 0
	for _, c := range m.st.Clusters {
		if lvl, _ := c.Health(); lvl == state.LevelWarn || lvl == state.LevelError {
			need++
		}
		if c.Busy != "" {
			busy++
		}
	}
	out := Block{
		{},
		{sp(1), bold(colMauve, heading("Fleet")), sp(2), dimS(fmt.Sprintf("%d clusters · %d need attention · %d changing", len(m.st.Clusters), need, busy))},
	}
	cols := m.fleetCols(w)
	head := Line{sp(1)}
	for _, c := range cols {
		head = append(head, Seg{Text: padRight(c.name, c.w), FG: colDim, Bold: true})
	}
	out = append(out, head, append(Line{sp(1)}, hrule(w-2)...))
	for i, c := range m.st.Clusters {
		row := Line{sp(1)}
		for _, fc := range cols {
			row = append(row, fleetCell(fc.name, c).Fit(fc.w)...)
		}
		if i == m.sel {
			row[1] = fg(colMauve, "▶ ")
			row = row.Fit(w).WithBG(colSurface0)
		}
		out = append(out, row)
	}
	out = append(out, Line{})
	card := m.clusterCard(w - 2)
	out = append(out, card.indent(1)...)
	return out.fit(w, h)
}

func fleetCell(col string, c state.Cluster) Line {
	switch col {
	case "":
		return Line{sp(2)}
	case "CLUSTER":
		return Line{tx(c.Name)}
	case "REGION":
		return Line{sub(c.Region)}
	case "VERSION":
		l := Line{tx(c.Version)}
		if c.Busy == "upgrading" || strings.HasPrefix(c.Busy, "upgrading") {
			return append(l, sp(1), levelGlyph(state.LevelProgress))
		}
		if c.Behind() > 0 {
			l = append(l, dimS(" → "+c.Latest))
		}
		return l
	case "NODEGROUPS":
		l := Line{tx(strconv.Itoa(len(c.Nodegroups))), sp(1)}
		if strings.HasPrefix(c.Busy, "rolling") || strings.HasSuffix(c.Busy, "nodegroups") {
			return append(l, levelGlyph(state.LevelProgress))
		}
		if n := len(c.StaleNodegroups()); n > 0 {
			return append(l, tok(state.LevelWarn, fmt.Sprintf("%d stale", n)))
		}
		return append(l, levelGlyph(state.LevelOK))
	case "ADD-ONS":
		if c.Busy == "updating add-ons" || strings.HasSuffix(c.Busy, "add-ons") {
			return Line{tok(state.LevelProgress, "updating")}
		}
		if n := len(c.StaleAddons()); n > 0 {
			return Line{tok(state.LevelWarn, fmt.Sprintf("%d stale", n))}
		}
		return Line{tok(state.LevelOK, "current")}
	default:
		lvl, text := c.Health()
		return Line{Seg{Text: levelGlyph(lvl).Text + " " + text, FG: levelColor(lvl)}}
	}
}

// clusterCard summarizes the selected cluster: next hop, readiness,
// support, and what is stale.
func (m Model) clusterCard(w int) Block {
	c := m.cluster()
	if c.Name == "" {
		return nil
	}
	var body Block
	if c.ARN != "" {
		body = append(body, Line{dimS(c.ARN)})
	} else {
		body = append(body, Line{dimS(c.Region)})
	}
	if c.Incomplete {
		body = append(body, Line{tok(state.LevelWarn, "part of this cluster could not be read · counts may be low")})
	}
	next := Line{sub("next hop "), tx(c.Version + " → " + state.NextMinor(c.Version))}
	switch {
	case c.Latest == "":
		next = Line{sub("version "), tx(c.Version), dimS(" · newest EKS version unknown")}
	case c.Behind() == 0:
		next = Line{sub("version "), tx(c.Version), dimS(" · newest")}
	}
	next = append(next, sp(3), sub("readiness "))
	if r, ok := m.st.Readiness[c.Name]; ok {
		next = append(next, readinessVerdict(r)...)
	} else {
		next = append(next, dimS("not run · press r"))
	}
	next = append(next, sp(3), sub("support "))
	if c.ExtendedSupport {
		next = append(next, fg(colRed, "extended since "+c.SupportEnds.Format("2006-01-02")))
	} else {
		next = append(next, tx("until "+c.SupportEnds.Format("2006-01-02")))
	}
	body = append(body, next)
	var stale Line
	for _, ng := range c.StaleNodegroups() {
		what := "AMI " + ng.LatestAMI
		if ng.LatestAMI == "" {
			what = "AMI outdated"
		}
		if ng.Version != c.Version {
			what = "on " + ng.Version
		}
		stale = append(stale, sub(ng.Name+" "), tok(state.LevelWarn, ""+what), sp(3))
	}
	for _, a := range c.StaleAddons() {
		stale = append(stale, sub(a.Name+" "), tok(state.LevelWarn, ""+a.Version+" → "+a.Latest), sp(3))
	}
	if len(stale) == 0 {
		stale = Line{tok(state.LevelOK, "every nodegroup and add-on is current")}
	}
	for _, l := range wrapLine(stale, w-4) {
		body = append(body, l)
	}
	if c.Busy != "" {
		hint := "progress in the live feed"
		if m.watchable(c.Name) {
			hint = "enter to watch"
		}
		body = append(body, Line{tok(state.LevelProgress, c.Busy), sp(2), dimS(hint)})
	}
	body = append(body, Line{chip("r"), sp(1), sub("readiness"), sp(2), chip("p"), sp(1), sub("patch nodegroup"), sp(2), chip("a"), sp(1), sub("update add-ons"), sp(2), chip("U"), sp(1), sub("upgrade cluster")})
	return box(Line{bold(colMauve, c.Name)}, body, w, colSurface1)
}

// wrapLine breaks a line of segments into lines at most w wide, on segment
// boundaries.
func wrapLine(l Line, w int) []Line {
	var out []Line
	var cur Line
	for _, s := range l {
		if cur.Width()+width(s.Text) > w && len(cur) > 0 {
			out = append(out, cur)
			cur = nil
			if strings.TrimSpace(s.Text) == "" {
				continue
			}
		}
		cur = append(cur, s)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func readinessVerdict(r state.Readiness) Line {
	switch {
	case r.Running:
		return Line{tok(state.LevelProgress, fmt.Sprintf("checking %d/%d", len(r.Checks)-r.Pending(), len(r.Checks)))}
	case r.Blockers() > 0:
		l := Line{tok(state.LevelError, ""+plural(r.Blockers(), "blocker"))}
		if r.Warnings() > 0 {
			l = append(l, sp(1), tok(state.LevelWarn, ""+plural(r.Warnings(), "warning")))
		}
		return l
	case r.Warnings() > 0:
		return Line{tok(state.LevelWarn, ""+plural(r.Warnings(), "warning"))}
	default:
		return Line{tok(state.LevelOK, "ready")}
	}
}

// fleetFeed is the live fleet event feed, newest first, over cards for the
// changes in flight.
func (m Model) fleetFeed(w, h int) Block {
	scope := "all clusters"
	if !m.feedAll {
		scope = m.cluster().Name
	}
	out := Block{{}, joinRight(append(Line{sp(1), bold(colMauve, heading("Live")), sp(2)}, m.followToken()...), Line{dimS(scope), sp(1), chip("f"), sp(1)}, w)}
	cards := m.activeCards(w - 2)
	feedH := h - len(out) - len(cards) - 1
	nameW := 0
	for _, c := range m.st.Clusters {
		nameW = max(nameW, width(c.Name))
	}
	sel := m.cluster().Name
	evs := m.visible(m.feed().Feed, func(e state.Event) bool { return m.feedAll || e.Cluster == sel })
	for i := 0; i < len(evs) && i < feedH; i++ {
		e := evs[i]
		l := Line{sp(1), dimS(clock(e.At)), sp(1)}
		if m.feedAll {
			l = append(l, fg(colSky, padRight(e.Cluster, nameW)), sp(1))
		}
		l = append(l, levelGlyph(e.Level), sp(1))
		if e.Subject != "" {
			l = append(l, tx(e.Subject), sp(1))
		}
		l = append(l, sub(e.Text))
		if e.Detail != "" {
			l = append(l, sp(1), dimS(e.Detail))
		}
		out = append(out, l)
	}
	if len(evs) == 0 {
		out = append(out, Line{sp(1), dimS("no events yet")})
	}
	out = out.fit(w, h-len(cards))
	return append(out, cards.indent(1)...)
}

// activeCards shows a progress card for each change in flight.
func (m Model) activeCards(w int) Block {
	var out Block
	n := 0
	for i := len(m.st.Upgrades) - 1; i >= 0 && n < 2; i-- {
		u := m.st.Upgrades[i]
		if !u.Running() {
			continue
		}
		n++
		cur := u.Current()
		phase := "waiting"
		if cur >= 0 {
			phase = strings.ToLower(u.Phases[cur].Name)
		}
		title := Line{levelGlyph(state.LevelProgress), sp(1), bold(colText, u.Cluster), sp(1), sub(u.From + " → " + u.To)}
		body := Block{
			joinRight(Line{sub("upgrade · " + phase)}, Line{dimS("4 to watch")}, w-4),
			append(bar(w-12, u.Progress(), 0), sp(1), tx(fmt.Sprintf("%3.0f%%", u.Progress()*100))),
		}
		out = append(out, box(title, body, w, colSurface1)...)
	}
	for i := len(m.st.Rolls) - 1; i >= 0 && n < 2; i-- {
		r := m.st.Rolls[i]
		if !r.Running() || r.UpgradeOf != "" {
			continue
		}
		n++
		title := Line{levelGlyph(state.LevelProgress), sp(1), bold(colText, r.Cluster), sp(1), sub("/ " + r.Nodegroup)}
		frac := float64(r.Replaced()) / float64(max(1, r.Planned))
		body := Block{
			joinRight(Line{sub(fmt.Sprintf("roll · %d/%d nodes replaced", r.Replaced(), r.Planned))}, Line{dimS("3 to watch")}, w-4),
			append(bar(w-12, frac, 0.5/float64(max(1, r.Planned))), sp(1), tx(fmt.Sprintf("%3.0f%%", frac*100))),
		}
		out = append(out, box(title, body, w, colSurface1)...)
	}
	return out
}

// watchable reports whether this TUI runs a change on cluster that the rolls
// or upgrade screen can show.
func (m Model) watchable(cluster string) bool {
	for _, u := range m.st.Upgrades {
		if u.Cluster == cluster && u.Running() {
			return true
		}
	}
	for _, r := range m.st.Rolls {
		if r.Cluster == cluster && r.Running() {
			return true
		}
	}
	return false
}
