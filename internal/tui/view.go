package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.BackgroundColor = colBase
	v.WindowTitle = "refresh"
	return v
}

// render draws the whole screen as a string: exactly m.h rows of m.w cells.
func (m Model) render() string {
	b := m.frame()
	rows := make([]string, len(b))
	for i, l := range b {
		rows[i] = l.Render(colBase)
	}
	return strings.Join(rows, "\n")
}

// frame draws the whole screen as a Block.
func (m Model) frame() Block {
	w, h := m.w, m.h
	if w == 0 || h == 0 {
		return nil
	}
	if w < minWidth || h < minHeight {
		msg := Line{sub(fmt.Sprintf("refresh ui needs a %d×%d terminal (now %d×%d).", minWidth, minHeight, w, h))}
		return Block{msg}.fit(w, h)
	}
	body := h - 3
	var main Block
	switch {
	case !m.loaded:
		main = Block{{}, {sp(1), tok(state.LevelProgress, "loading the fleet…")}}
	case m.screen == screenCluster:
		main = m.clusterScreen(w, body)
	case m.screen == screenRolls:
		main = m.rollScreen(w, body)
	case m.screen == screenUpgrade:
		main = m.upgradeScreen(w, body)
	default:
		main = m.fleetScreen(w, body)
	}
	out := Block{m.topBar(w).WithBG(colMantle)}
	out = append(out, main.fit(w, body)...)
	out = append(out, m.logStrip(w).WithBG(colMantle), m.keyBar(w).WithBG(colCrust))
	if d, ok := m.openDialog(); ok {
		out = m.withDialog(out, m.drawDialog(d))
	}
	return out
}

func (m Model) withDialog(base, dlg Block) Block {
	x := (m.w - dlg[0].Width()) / 2
	y := max(1, (m.h-len(dlg))/2)
	return overlay(base.dimmed(), dlg.withBG(colBase), x, y)
}

func (m Model) topBar(w int) Line {
	left := Line{sp(1), badge(colMauve, "refresh"), sp(1)}
	for i, name := range screenNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if screen(i) == m.screen {
			left = append(left, sp(1), Seg{Text: " " + label + " ", FG: colCrust, BG: colBlue, Bold: true})
		} else {
			left = append(left, sp(1), dimS(" "+label+" "))
		}
	}
	// The right side drops its least useful parts until it fits.
	parts := []Line{
		{sub("ctx "), fg(colMauve, m.st.Context)},
		{sub("aws "), fg(colSky, m.st.Profile)},
		{sub("regions "), fg(colGreen, fmt.Sprintf("%d/%d", m.st.RegionsAnswered, m.st.RegionsTotal))},
		{tok(state.LevelProgress, "live · synced "+ago(m.st.Now.Sub(m.st.SyncedAt)))},
	}
	var badgeL Line
	if m.st.Badge != "" {
		c := colPeach
		if m.st.Badge == "CHANGES ON" {
			c = colRed // this TUI can change real clusters
		}
		badgeL = Line{badge(c, m.st.Badge)}
	}
	for _, drop := range [][]int{nil, {1}, {1, 2}, {0, 1, 2}, {0, 1, 2, 3}} {
		var right Line
		for i, p := range parts {
			if !slices.Contains(drop, i) {
				right = append(append(right, p...), sp(2))
			}
		}
		right = append(append(right, badgeL...), sp(1))
		if left.Width()+right.Width()+1 <= w {
			return joinRight(left, right, w)
		}
	}
	return left.Fit(w)
}

// logStrip shows the newest AWS call, or a notice.
func (m Model) logStrip(w int) Line {
	if m.planning != "" {
		return Line{sp(1), tok(state.LevelProgress, "planning "+m.planning+"… · esc cancels")}.Fit(w)
	}
	if m.notice != "" && m.st.Now.Before(m.noticeTill) {
		return Line{sp(1), levelGlyph(m.noticeLvl), sp(1), tx(m.notice)}.Fit(w)
	}
	l := Line{sp(1), dimS("log"), sp(2)}
	cluster := ""
	if m.screen != screenFleet {
		cluster = m.focusCluster()
	}
	log := m.feed().Log
	for i := len(log) - 1; i >= 0; i-- {
		e := log[i]
		if m.pausedSeq != 0 && e.Seq > m.pausedSeq {
			continue
		}
		if cluster != "" && e.Cluster != cluster {
			continue
		}
		l = append(l, dimS(clock(e.At)), sp(1))
		if e.Cluster != "" && cluster == "" {
			l = append(l, fg(colSky, e.Cluster), sp(1))
		}
		l = append(l, fg(colBlue, e.Subject), sp(1), sub(e.Text))
		break
	}
	return l.Fit(w)
}

// focusCluster is the cluster the current screen is about.
func (m Model) focusCluster() string {
	switch m.screen {
	case screenRolls:
		if r, ok := m.roll(); ok {
			return r.Cluster
		}
	case screenUpgrade:
		if u, ok := m.upgrade(); ok {
			return u.Cluster
		}
	}
	return m.cluster().Name
}

func firstWord(b binding) string {
	first, _, _ := strings.Cut(b.barText(), " ")
	return first
}

// keyBar shows the keys that work on this screen now. On a narrow screen
// it shortens the labels, then drops them, before it lets a key fall off the
// end: every key stays visible.
func (m Model) keyBar(w int) Line {
	right := Line{chip("?"), sp(1), dimS("keys"), sp(2), chip("q"), sp(1), dimS("quit"), sp(1)}
	bs := m.barBindings()
	var l Line
	for _, text := range []func(binding) string{
		binding.barText,
		firstWord,
		// Keep words on the one-letter action keys; arrows and enter
		// explain themselves.
		func(b binding) string {
			if b.primary {
				return firstWord(b)
			}
			return ""
		},
		func(binding) string { return "" },
	} {
		l = Line{sp(1)}
		gap := 2
		if text(binding{label: "x", desc: "a b"}) != "a b" {
			gap = 1 // the shortened forms sit closer together
		}
		for _, b := range bs {
			l = append(l, chip(b.label))
			if t := text(b); t != "" {
				l = append(l, sp(1), dimS(t))
			}
			l = append(l, sp(gap))
		}
		if l.Width()+right.Width() <= w {
			break
		}
	}
	return joinRight(l, right, w)
}

// visible filters a feed for the live panes: the pause point, the source,
// and warnings only. It returns the newest first.
func (m Model) visible(evs []state.Event, keep func(state.Event) bool) []state.Event {
	var out []state.Event
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if m.pausedSeq != 0 && e.Seq > m.pausedSeq {
			continue
		}
		if m.warnOnly && e.Level != state.LevelWarn && e.Level != state.LevelError {
			continue
		}
		if keep != nil && !keep(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// followToken is the "● following" / "❚❚ paused" marker of a live pane.
func (m Model) followToken() Line {
	var l Line
	if m.pausedSeq == 0 {
		l = Line{tok(state.LevelOK, "following")}
	} else {
		l = Line{fg(colYellow, "❚❚ frozen at "+clock(m.pausedAt))}
	}
	if m.warnOnly {
		l = append(l, sp(2), tok(state.LevelWarn, "warnings only"))
	}
	return l
}

func clock(t time.Time) string { return t.Format("15:04:05") }

func ago(d time.Duration) string {
	if d < time.Second {
		return "now"
	}
	return dur(d) + " ago"
}

// dur formats a duration compactly: 42s, 4m02s, 1h03m.
func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// section is a pane heading (marker and title) plus extra segments.
func section(title string, extra ...Seg) Line {
	l := Line{bold(colMauve, heading(title))}
	for _, e := range extra {
		l = append(l, sp(2), e)
	}
	return l
}

// plural formats a count and a noun: "1 warning", "3 warnings".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
