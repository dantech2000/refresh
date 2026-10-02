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
	case m.unlocking:
		return m.unlockParts(dialogWidth(m.w, 80)), true
	case m.pick != nil:
		return m.pickerParts(dialogWidth(m.w, 96)), true
	case m.confirm != nil:
		return m.confirmParts(dialogWidth(m.w, 116)), true // fits the longest CLI command
	case m.help:
		return m.helpParts(dialogWidth(m.w, 100)), true
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
	// Long lines wrap under their own column instead of running off the
	// dialog's edge: a notice names a command to run later.
	for _, f := range p.Facts {
		text := f.Value
		if f.Note != "" {
			text += " (" + f.Note + ")"
		}
		for i, s := range wrapExact(text, max(10, inner-keyW)) {
			key := sp(keyW)
			if i == 0 {
				key = sub(padRight(f.Key, keyW))
			}
			body = append(body, Line{key, tx(s)})
		}
	}
	body = append(body, Line{}, Line{Seg{Text: "PRE-FLIGHT GATES", FG: colDim, Bold: true}})
	for _, g := range p.Gates {
		text := g.Text
		if g.Note != "" {
			text += " · " + g.Note
		}
		for i, s := range wrapExact(text, max(10, inner-2)) {
			l := Line{sp(2)}
			if i == 0 {
				l = Line{levelGlyph(checkLevel(g.Status)), sp(1)}
			}
			body = append(body, append(l, tx(s)))
		}
	}
	body = append(body, Line{})
	const cmdKey = "CLI equivalent  "
	for i, s := range wrapExact(p.Command, max(10, inner-width(cmdKey))) {
		key := sp(width(cmdKey))
		if i == 0 {
			key = dimS(cmdKey)
		}
		body = append(body, Line{key, sub(s)})
	}

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
		buttons = append(buttons, sp(3), Seg{Text: " y ", FG: colCrust, BG: colMauve, Bold: true}, sp(1), bold(colMauve, startLabel(p.Action)))
	case p.ReadOnly && m.canUnlock():
		buttons = append(buttons, sp(3), Seg{Text: " ctrl+u ", FG: colCrust, BG: colMauve, Bold: true}, sp(1), bold(colMauve, "Allow changes"))
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

func startLabel(a state.Action) string {
	switch a.Kind {
	case state.ActionRoll:
		return "Start roll and watch"
	case state.ActionUpgrade:
		if a.Nodegroup != "" {
			return "Start roll and watch" // one nodegroup to the control plane's version
		}
		return "Start upgrade and watch"
	case state.ActionRollback:
		return "Start rollback and watch"
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
	switch m.st.Badge {
	case "SIMULATED":
		body = append(body, Line{fg(colPeach, "Simulated fleet: no AWS calls are made.")})
	case "READ-ONLY":
		body = append(body, Line{fg(colPeach, "Read-only: changes are dry runs. ctrl+u allows changes for this session; c in a dry run copies the CLI command.")})
	case "CHANGES ON":
		body = append(body, Line{fg(colRed, "Changes on: y in a roll's or an add-on update's dry run changes the real cluster.")})
	}
	foot := Block{{}, joinRight(nil, Line{chip("esc"), sp(1), sub("close")}, w-4)}
	return dialogParts{head: head, body: body, foot: foot, border: colMauve, w: w}
}

// unlockParts asks before the session may change clusters.
func (m Model) unlockParts(w int) dialogParts {
	inner := w - 4
	head := Block{{bold(colPeach, "Allow changes for this session?")}, {}}
	var body Block
	for _, s := range []string{
		"The UI can then start nodegroup rolls, add-on updates, cluster upgrades, and rollbacks on the real clusters.",
		"Each change still runs its dry run and its gates first, and starts only when you press y in it.",
		"This lasts until you quit. refresh ui --allow-changes starts with changes allowed.",
	} {
		for _, l := range wrap(s, inner) {
			body = append(body, Line{sub(l)})
		}
		body = append(body, Line{})
	}
	if m.confirm != nil && m.confirm.ReadOnly {
		body = append(body, Line{dimS("The open dry run runs again with the live gates.")})
	}
	foot := Block{{}, joinRight(nil, Line{chip("esc"), sp(1), sub("Stay read-only"), sp(3), Seg{Text: " y ", FG: colCrust, BG: colPeach, Bold: true}, sp(1), bold(colPeach, "Allow changes")}, inner)}
	return dialogParts{head: head, body: body, foot: foot, border: colPeach, w: w}
}

// pickerParts lists the changes to choose from.
func (m Model) pickerParts(w int) dialogParts {
	p := m.pick
	inner := w - 4
	head := Block{{bold(colMauve, p.title), sp(2), dimS(p.cluster)}, {}}
	nameW := 14
	for _, it := range p.items {
		nameW = max(nameW, width(it.name)+2)
	}
	var body Block
	for i, it := range p.items {
		mark := sp(2)
		if i == p.sel {
			mark = fg(colMauve, "▶ ")
		}
		l := Line{mark, dimS(fmt.Sprintf("%-2d", i+1)), sp(1)}
		l = append(l, tx(padRight(it.name, nameW)), tok(it.level, it.why))
		if it.note != "" {
			l = append(l, sp(2), dimS(it.note))
		}
		if i == p.sel {
			l = l.Fit(inner).WithBG(colSurface0)
		}
		body = append(body, l)
	}
	verb := "dry run"
	if len(p.items) > 0 && p.items[p.sel].do != nil {
		verb = "export"
	}
	foot := Block{{}, joinRight(nil, Line{chip("↑↓"), sp(1), sub("choose"), sp(3), chip("enter"), sp(1), sub(verb), sp(3), chip("esc"), sp(1), sub("close")}, inner)}
	return dialogParts{head: head, body: body, foot: foot, border: colMauve, w: w}
}
