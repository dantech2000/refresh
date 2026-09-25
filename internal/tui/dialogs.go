package tui

import (
	"fmt"
	"image/color"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// dialogParts is an open dialog: a header and a footer that always show,
// and a body that scrolls when the terminal is too short for all of it.
type dialogParts struct {
	head, body, foot Block
	border           color.Color
	w                int
}

// dialogWidth is the outer width of a dialog on a w-wide screen.
func dialogWidth(w, want int) int { return min(w-4, want) }

// openDialog returns the dialog on screen, if any.
func (m Model) openDialog() (dialogParts, bool) {
	switch {
	case m.pick != nil:
		return m.pickerParts(dialogWidth(m.w, 72)), true
	case m.confirm != nil:
		return m.confirmParts(dialogWidth(m.w, 92)), true
	case m.help:
		return m.helpParts(dialogWidth(m.w, 76)), true
	}
	return dialogParts{}, false
}

// bodyRows is how many body rows fit: the screen minus a one-row margin
// above and below, the borders, the header, the footer, and the two scroll
// hint rows.
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
			up = Line{dimS(fmt.Sprintf("↑ %d more above · ↑↓ pgup pgdn to scroll", scroll))}
		}
		if end < len(d.body) {
			down = Line{dimS(fmt.Sprintf("↓ %d more below · ↑↓ pgup pgdn to scroll", len(d.body)-end))}
		}
		content = append(content, up)
		content = append(content, d.body[scroll:end]...)
		content = append(content, down)
	}
	content = append(content, d.foot...)
	return box(nil, content.fit(inner, len(content)), d.w, d.border)
}

// confirmParts is the dry run of an action and the start key.
func (m Model) confirmParts(w int) dialogParts {
	p := m.confirm
	inner := w - 4
	head := Block{joinRight(Line{bold(colMauve, p.Title)}, Line{dimS("dry run · nothing changed yet")}, inner), {}}
	body := Block{{Seg{Text: "PLAN", FG: colDim, Bold: true}}}
	keyW := 16
	for _, c := range p.Changes {
		keyW = max(keyW, width(c.Field)+2)
	}
	for _, f := range p.Facts {
		keyW = max(keyW, width(f.Key)+2)
	}
	for _, c := range p.Changes {
		body = append(body,
			Line{sub(padRight(c.Field, keyW)), fg(colRed, "- "+c.From)},
			Line{sp(keyW), fg(colGreen, "+ "+c.To)})
	}
	for _, f := range p.Facts {
		l := Line{sub(padRight(f.Key, keyW)), tx(f.Value)}
		if f.Note != "" {
			l = append(l, sp(1), dimS("("+f.Note+")"))
		}
		body = append(body, l)
	}
	body = append(body, Line{}, Line{Seg{Text: "PRE-FLIGHT GATES", FG: colDim, Bold: true}})
	for _, g := range p.Gates {
		l := Line{levelGlyph(checkLevel(g.Status)), sp(1), tx(g.Text)}
		if g.Note != "" {
			l = append(l, dimS(" · "+g.Note))
		}
		body = append(body, l)
	}
	body = append(body, Line{}, Line{dimS("CLI equivalent  "), sub(p.Command)})

	// The verdict and the keys stay on screen however the body scrolls.
	// Each message gets at most two footer lines; a longer one is cut there
	// and shown in full at the end of the body.
	foot := Block{{}}
	for _, msg := range []string{blockedText(p.Blocked), m.confirmErr} {
		lines := wrap(msg, inner-2)
		if msg == "" {
			continue
		}
		if len(lines) > 2 {
			body = append(body, Line{}, Line{Seg{Text: "FULL MESSAGE", FG: colDim, Bold: true}})
			for _, s := range lines {
				body = append(body, Line{sub(s)})
			}
			const more = " … full text below"
			second := Line{sub(lines[1])}.Cut(0, max(1, inner-4-width(more))).Plain()
			lines = []string{lines[0], second + more}
		}
		for _, s := range lines {
			foot = append(foot, Line{tok(state.LevelError, s)})
		}
	}
	buttons := Line{chip("esc"), sp(1), sub("Cancel"), sp(3), chip("c"), sp(1), sub("Copy command")}
	switch {
	case m.starting:
		// Cancel would only hide the dialog; the change may still start.
		buttons = Line{chip("c"), sp(1), sub("Copy command"), sp(3), tok(state.LevelProgress, "starting… the result shows here")}
	case p.Blocked == "":
		buttons = append(buttons, sp(3), Seg{Text: " y ", FG: colCrust, BG: colMauve, Bold: true}, sp(1), bold(colMauve, startLabel(p.Action.Kind)))
	}
	foot = append(foot, joinRight(nil, buttons, inner))
	border := colMauve
	if p.Blocked != "" {
		border = colRed
	}
	return dialogParts{head: head, body: body, foot: foot, border: border, w: w}
}

func blockedText(reason string) string {
	if reason == "" {
		return ""
	}
	return "blocked: " + reason
}

func startLabel(k state.ActionKind) string {
	switch k {
	case state.ActionRoll:
		return "Start roll and watch"
	case state.ActionUpgrade:
		return "Start upgrade and watch"
	default:
		return "Start add-on update"
	}
}

// helpParts lists the keys for the current screen and the keys that work
// everywhere, from the same tables the key handler uses.
func (m Model) helpParts(w int) dialogParts {
	head := Block{{bold(colMauve, "Keys"), sp(2), dimS("on the " + screenNames[m.screen] + " screen · screen keys do nothing while a dialog is open")}, {}}
	var body Block
	group := func(title string, bs []binding) {
		var rows Block
		for _, b := range bs {
			if b.label != "" {
				rows = append(rows, Line{Seg{Text: padRight(b.label, 12), FG: colText, Bold: true}, sub(b.desc)})
			}
		}
		if len(rows) == 0 {
			return
		}
		body = append(body, Line{Seg{Text: title, FG: colDim, Bold: true}})
		body = append(body, rows...)
		body = append(body, Line{})
	}
	var here []binding
	for _, b := range screenBindings() {
		if b.active(m) {
			here = append(here, b)
		}
	}
	group("THIS SCREEN", here)
	var everywhere []binding
	for _, b := range globalBindings() {
		if b.active(m) {
			everywhere = append(everywhere, b)
		}
	}
	group("ON EVERY SCREEN", everywhere)
	group("IN THE CONFIRM DIALOG", dialogBindings())
	group("IN ANY DIALOG", append([]binding{{label: "esc", desc: "close (a confirm dialog stays open while its change starts)"}}, scrollBindings()...))
	if m.st.Backend == "simulated" {
		body = append(body, Line{fg(colPeach, "Simulated fleet: no AWS calls are made.")})
	}
	foot := Block{{}, joinRight(nil, Line{chip("esc"), sp(1), sub("close")}, w-4)}
	return dialogParts{head: head, body: body, foot: foot, border: colMauve, w: w}
}

// pickerParts lists the stale nodegroups to choose from.
func (m Model) pickerParts(w int) dialogParts {
	p := m.pick
	inner := w - 4
	head := Block{{bold(colMauve, "Patch which nodegroup?"), sp(2), dimS(p.cluster)}, {}}
	var body Block
	for i, ng := range p.items {
		why := "AMI " + ng.AMI + " → " + ng.LatestAMI
		if ng.Version != m.clusterVersion(p.cluster) {
			why = "on " + ng.Version
		}
		mark := sp(2)
		if i == p.sel {
			mark = fg(colMauve, "▶ ")
		}
		l := Line{mark, dimS(fmt.Sprintf("%-2d", i+1)), sp(1)}
		l = append(l, tx(padRight(ng.Name, 14)), tok(state.LevelWarn, why), sp(2), dimS(fmt.Sprintf("%d nodes", ng.Nodes)))
		if i == p.sel {
			l = l.Fit(inner).WithBG(colSurface0)
		}
		body = append(body, l)
	}
	foot := Block{{}, joinRight(nil, Line{chip("↑↓"), sp(1), sub("choose"), sp(3), chip("enter"), sp(1), sub("dry run"), sp(3), chip("esc"), sp(1), sub("close")}, inner)}
	return dialogParts{head: head, body: body, foot: foot, border: colMauve, w: w}
}

func (m Model) clusterVersion(name string) string {
	for _, c := range m.st.Clusters {
		if c.Name == name {
			return c.Version
		}
	}
	return ""
}
