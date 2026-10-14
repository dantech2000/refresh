package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/view"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	if c, ok := m.th.Color(view.RoleBase); ok {
		v.BackgroundColor = c // with color off, the terminal keeps its own
	}
	v.WindowTitle = "refresh"
	return v
}

// render draws the whole screen as a string: exactly m.h rows of m.w cells.
func (m Model) render() string {
	b := m.frame()
	rows := make([]string, len(b))
	for i, l := range b {
		rows[i] = l.Render(m.p, view.RoleBase)
	}
	out := strings.Join(rows, "\n")
	if m.ascii != nil {
		out = m.ascii.Replace(out)
	}
	return out
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
		main = Block{{}, {sp(1), m.tok(view.Progress, "loading the fleet")}}
	case m.screen == screenCluster:
		main = m.clusterScreen(w, body)
	case m.screen == screenChange:
		main = m.changeScreen(w, body)
	default:
		main = m.fleetScreen(w, body)
	}
	out := Block{m.topBar(w).WithBG(view.RoleMantle)}
	out = append(out, main.fit(w, body)...)
	out = append(out, m.logStrip(w).WithBG(view.RoleMantle), m.keyBar(w).WithBG(view.RoleCrust))
	if d, ok := m.openDialog(); ok {
		out = m.withDialog(out, m.drawDialog(d))
	}
	return out
}

// tok is a status token: the mark and the label, both in the color of the
// status.
func (m Model) tok(s view.Status, label string) Seg {
	return statusSeg(s, m.th.Mark(s)+" "+label)
}

// mark is the mark of a status in its color.
func (m Model) mark(s view.Status) Seg { return statusSeg(s, m.th.Mark(s)) }

// heading is a section title.
func (m Model) heading(title string) Seg { return bold(view.RoleAccent, title) }

func (m Model) withDialog(base, dlg Block) Block {
	x := (m.w - dlg[0].Width()) / 2
	y := max(1, (m.h-len(dlg))/2)
	return overlay(base.dimmed(), dlg.withBG(view.RoleBase), x, y)
}

func (m Model) topBar(w int) Line {
	left := Line{sp(1), badge(view.RoleAccent, "refresh"), sp(1)}
	for i, name := range screenNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if screen(i) == m.screen {
			left = append(left, sp(1), Seg{Text: " " + label + " ", Role: view.RoleCrust, BG: view.RoleInfo, Bold: true})
		} else {
			left = append(left, sp(1), dim(" "+label+" "))
		}
	}
	// The right side drops its least useful parts until it fits. Before
	// the first sweep ends there is no sync time.
	sw := m.st.Sweep
	regions := fmt.Sprintf("%d/%d", len(sw.Answered), sw.Regions)
	if n := len(sw.Skipped); n > 0 {
		regions += fmt.Sprintf(", %d skipped", n)
	}
	synced := Line{m.tok(view.Healthy, "synced "+clock(sw.EndedAt))}
	if sw.EndedAt.IsZero() {
		synced = Line{m.tok(view.Progress, "sweeping "+dur(m.now.Sub(sw.StartedAt)))}
	}
	parts := []Line{
		{sub("ctx "), role(view.RoleAccent, orDash(m.st.Context))},
		{sub("aws "), role(view.RoleInfo, orDash(m.st.Profile))},
		{sub("regions "), role(view.RoleHealthy, regions)},
		synced,
	}
	badgeL := Line{badge(view.RoleWarn, "READ-ONLY")}
	if !m.st.ReadOnly {
		badgeL = Line{badge(view.RoleFail, "CHANGES ON")} // this TUI can change real clusters
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// logStrip shows the notice of the moment, or the newest line of the feed
// about the cluster on screen.
func (m Model) logStrip(w int) Line {
	if p := m.planning; p != nil {
		return Line{sp(1), m.tok(view.Progress, "dry run of "+describeAction(p.action)), sp(2), chip("esc"), sp(1), dim("cancel")}.Fit(w)
	}
	if m.notice != "" && m.now.Before(m.noticeTill) {
		return Line{sp(1), m.mark(m.noticeStatus), sp(1), tx(m.notice)}.Fit(w)
	}
	l := Line{sp(1), dim("log"), sp(2)}
	var cluster string
	if m.screen != screenFleet {
		cluster = m.cluster().Name
	}
	for i := len(m.st.Feed) - 1; i >= 0; i-- {
		e := m.st.Feed[i]
		if cluster != "" && e.Cluster.Name != cluster {
			continue
		}
		l = append(l, dim(clock(e.At)), sp(1))
		if e.Cluster.Name != "" && cluster == "" {
			l = append(l, role(view.RoleInfo, e.Cluster.Name), sp(1))
		}
		l = append(l, m.mark(e.Status), sp(1), sub(e.Text))
		break
	}
	return l.Fit(w)
}

func firstWord(b binding) string {
	first, _, _ := strings.Cut(b.barText(), " ")
	return first
}

// keyBar shows the keys that work on this screen now. On a narrow screen
// it shortens the labels, then drops them, before it lets a key fall off
// the end: every key stays visible.
func (m Model) keyBar(w int) Line {
	full := Line{chip("?"), sp(1), dim("keys"), sp(2), chip("q"), sp(1), dim("quit"), sp(1)}
	compact := Line{chip("?"), sp(1), chip("q"), sp(1)} // everyone knows these two
	bs := m.barBindings()
	var l Line
	right := full
	for _, text := range []func(binding) string{
		binding.barText,
		firstWord,
		// Drop the words of the keys that explain themselves (arrows,
		// enter, esc); a letter keeps its first word.
		func(b binding) string {
			if selfExplaining(b.label) {
				return ""
			}
			return firstWord(b)
		},
		// Then keep words only on the action keys.
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
				l = append(l, sp(1), dim(t))
			}
			l = append(l, sp(gap))
		}
		if l.Width()+full.Width() <= w {
			right = full
			break
		}
		if l.Width()+compact.Width() <= w {
			right = compact
			break
		}
	}
	return joinRight(l, right, w)
}

// selfExplaining reports whether a key's label says what it does without
// words: arrows, enter, esc.
func selfExplaining(label string) bool {
	switch label {
	case "enter", "esc":
		return true
	}
	return strings.ContainsAny(label, "↑↓←→")
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("15:04:05")
}

// dur formats a duration compactly: 42s, 4m02s, 1h03m.
func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// plural formats a count and a noun: "1 warning", "3 warnings".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// dialogParts is an open dialog: a header and a footer that always show,
// and a body that scrolls when the terminal is too short for all of it.
type dialogParts struct {
	head, body, foot Block
	w                int
}

// dialogWidth is the outer width of a dialog on a w-wide screen.
func dialogWidth(w, want int) int { return min(w-4, want) }

// bodyRows is how many body rows fit: the screen minus a one-row margin
// above and below, the borders, the header, the footer, and the two
// scroll hint rows.
func (m Model) bodyRows(d dialogParts) int {
	return max(1, m.h-2-2-len(d.head)-len(d.foot)-2)
}

// clampScroll limits a scroll offset to the open dialog's body.
func (m Model) clampScroll(v int) int {
	d, ok := m.openDialog()
	if !ok {
		return 0
	}
	return min(max(0, v), max(0, len(d.body)-m.bodyRows(d)))
}

// drawDialog lays out a dialog with its body scrolled to m.scroll.
func (m Model) drawDialog(d dialogParts) Block {
	inner := d.w - 4
	rows := m.bodyRows(d)
	content := append(Block(nil), d.head...)
	if len(d.body) <= rows {
		content = append(content, d.body...)
	} else {
		scroll := m.clampScroll(m.scroll)
		end := min(len(d.body), scroll+rows)
		up, down := Line{}, Line{}
		if scroll > 0 {
			up = Line{dim(fmt.Sprintf("↑ %d more above · ↑↓ pgup pgdn to scroll", scroll))}
		}
		if end < len(d.body) {
			down = Line{dim(fmt.Sprintf("↓ %d more below · ↑↓ pgup pgdn to scroll", len(d.body)-end))}
		}
		content = append(content, up)
		content = append(content, d.body[scroll:end]...)
		content = append(content, down)
	}
	content = append(content, d.foot...)
	return box(nil, content.fit(inner, len(content)), d.w, m.bd)
}

// helpParts lists the keys of the screen and the keys of every screen.
func (m Model) helpParts(w int) dialogParts {
	head := Block{{bold(view.RoleAccent, "Keys"), sp(2), dim("on the " + screenNames[m.screen] + " screen · screen keys do nothing while a dialog is open")}, {}}
	var body Block
	group := func(title string, bs []binding) {
		var rows Block
		for _, b := range bs {
			if b.label != "" {
				rows = append(rows, Line{Seg{Text: padRight(b.label, 12), Role: view.RoleText, Bold: true}, sub(b.desc)})
			}
		}
		if len(rows) == 0 {
			return
		}
		body = append(body, Line{Seg{Text: title, Role: view.RoleDim, Bold: true}})
		body = append(body, rows...)
		body = append(body, Line{})
	}
	var here, everywhere []binding
	for _, b := range screenBindings() {
		if b.active(m) {
			here = append(here, b)
		}
	}
	for _, b := range globalBindings() {
		if b.active(m) {
			everywhere = append(everywhere, b)
		}
	}
	group("THIS SCREEN", here)
	group("ON EVERY SCREEN", everywhere)
	group("IN A DIALOG", append([]binding{{label: "esc", desc: "close"}}, scrollBindings()...))
	group("IN A DRY RUN", dialogBindings())
	if m.st.ReadOnly {
		body = append(body, Line{role(view.RoleWarn, "Read-only: p, a, U, and B dry-run a change and name the command; nothing starts from this UI.")})
	} else {
		body = append(body, Line{role(view.RoleFail, "Changes on: y in a dry run starts that plan on a real cluster. A change started elsewhere is watched, never steered.")})
	}
	foot := Block{{}, {chip("esc"), sp(1), dim("close")}}
	return dialogParts{head: head, body: body, foot: foot, w: w}
}
