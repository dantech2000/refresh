package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// binding is one key action. The same table drives the key handler, the
// key bar, and the help dialog, so what the screen offers is what the
// keys do.
type binding struct {
	keys  []string // key names as Bubble Tea reports them
	label string   // how the key is written in the key bar and the help
	desc  string   // what it does, for the help dialog
	short string   // the key bar's text; desc when empty
	// bar shows the binding in the key bar; the rest are in the help only.
	bar bool
	// primary keeps its words in the key bar when the bar is too narrow
	// for all of them: a bare letter does not explain itself.
	primary bool
	// when reports whether the binding applies now; nil means always.
	when func(Model) bool
	do   func(*Model) tea.Cmd
}

func (b binding) active(m Model) bool { return b.when == nil || b.when(m) }

func (b binding) barText() string {
	if b.short != "" {
		return b.short
	}
	return b.desc
}

func (b binding) matches(k string) bool { return slices.Contains(b.keys, k) }

// Movement keys shared by every list and dialog.
var (
	keysUp     = []string{"up", "k"}
	keysDown   = []string{"down", "j"}
	keysTop    = []string{"home", "g"}
	keysBottom = []string{"end", "G"}
	keysPgUp   = []string{"pgup"}
	keysPgDown = []string{"pgdown"}
)

func onScreen(ss ...screen) func(Model) bool {
	return func(m Model) bool { return slices.Contains(ss, m.screen) }
}

func (m Model) hasCluster() bool { return m.cluster().Name != "" }

// globalBindings apply on every screen.
func globalBindings() []binding {
	return []binding{
		{keys: []string{"1"}, label: "1-2", desc: "switch screen: fleet, cluster", do: func(m *Model) tea.Cmd { return m.goTo(screenFleet) }},
		{keys: []string{"2"}, do: func(m *Model) tea.Cmd { return m.goTo(screenCluster) }},
		{keys: []string{"esc", "backspace"}, label: "esc", desc: "back to the fleet", short: "back", bar: true,
			when: func(m Model) bool { return m.screen != screenFleet },
			do:   func(m *Model) tea.Cmd { m.screen = screenFleet; return nil }},
		{keys: []string{"ctrl+r"}, label: "ctrl+r", desc: "read the fleet from AWS now", do: func(m *Model) tea.Cmd {
			m.b.Refresh()
			m.say(progressStatus, "reading the fleet")
			return nil
		}},
		{keys: []string{"ctrl+l"}, label: "ctrl+l", desc: "redraw the whole screen (if another program wrote over it)", do: func(*Model) tea.Cmd { return tea.ClearScreen }},
		{keys: []string{"?"}, label: "?", desc: "keys", do: func(m *Model) tea.Cmd { m.help, m.scroll = true, 0; return nil }},
		{keys: []string{"q"}, label: "q", desc: "quit; in a dialog, close it", do: func(*Model) tea.Cmd { return tea.Quit }},
	}
}

// listBindings move a cursor over n items.
func listBindings(when func(Model) bool, cur func(*Model) *int, n func(Model) int, what string) []binding {
	move := func(d int) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			p := cur(m)
			*p = min(max(0, *p+d), max(0, n(*m)-1))
			return nil
		}
	}
	to := func(end bool) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			p := cur(m)
			*p = 0
			if end {
				*p = max(0, n(*m)-1)
			}
			return nil
		}
	}
	return []binding{
		{keys: keysUp, label: "↑↓", desc: what, bar: true, when: when, do: move(-1)},
		{keys: keysDown, when: when, do: move(1)},
		{keys: keysTop, label: "g G", desc: "first, last", when: when, do: to(false)},
		{keys: keysBottom, when: when, do: to(true)},
	}
}

// screenBindings apply on one screen.
func screenBindings() []binding {
	var bs []binding
	onFleet := onScreen(screenFleet)
	bs = append(bs, listBindings(onFleet, func(m *Model) *int { return &m.sel }, func(m Model) int { return len(m.st.Clusters) }, "move")...)
	bs = append(bs,
		binding{keys: []string{"enter"}, label: "enter", desc: "open the cluster", short: "open", bar: true,
			when: func(m Model) bool { return onFleet(m) && m.hasCluster() },
			do:   func(m *Model) tea.Cmd { return m.goTo(screenCluster) }},
		binding{keys: []string{"f"}, label: "f", desc: "feed: all clusters or this one", short: "feed scope", bar: true, when: onFleet,
			do: func(m *Model) tea.Cmd { m.feedAll = !m.feedAll; return nil }},
	)
	onCluster := onScreen(screenCluster)
	bs = append(bs, listBindings(onCluster, func(m *Model) *int { return &m.checkSel }, func(m Model) int {
		return len(m.checks())
	}, "checks")...)
	here := func(m Model) bool { return (onFleet(m) || onCluster(m)) && m.hasCluster() }
	bs = append(bs,
		binding{keys: []string{"r", "R"}, label: "r", desc: "run the readiness checks", short: "readiness", bar: true, primary: true,
			when: here, do: func(m *Model) tea.Cmd { return m.runReadiness() }},
	)
	return bs
}

// scrollBindings scroll the open dialog.
func scrollBindings() []binding {
	by := func(d func(Model) int) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd { m.scroll = m.clampScroll(m.scroll + d(*m)); return nil }
	}
	page := func(m Model) int { return max(1, m.h/2) }
	return []binding{
		{keys: keysUp, label: "↑↓", desc: "scroll", do: by(func(Model) int { return -1 })},
		{keys: keysDown, do: by(func(Model) int { return 1 })},
		{keys: keysPgUp, label: "pgup pgdn", desc: "scroll a page", do: by(func(m Model) int { return -page(m) })},
		{keys: keysPgDown, do: by(page)},
		{keys: keysTop, do: func(m *Model) tea.Cmd { m.scroll = 0; return nil }},
		{keys: keysBottom, do: func(m *Model) tea.Cmd { m.scroll = m.clampScroll(1 << 20); return nil }},
	}
}

// key handles one key press.
func (m Model) key(k string) (Model, tea.Cmd) {
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	var table []binding
	if m.help {
		table = append([]binding{{keys: []string{"esc", "?", "q"}, do: func(m *Model) tea.Cmd { m.help = false; return nil }}}, scrollBindings()...)
	} else {
		table = append(screenBindings(), globalBindings()...)
	}
	for _, b := range table {
		if b.matches(k) && b.active(m) {
			cmd := b.do(&m)
			m.clamp()
			return m, cmd
		}
	}
	return m, nil
}

// barBindings are the bindings the key bar shows now, in table order.
func (m Model) barBindings() []binding {
	var out []binding
	for _, b := range screenBindings() {
		if b.bar && b.active(m) {
			out = append(out, b)
		}
	}
	for _, b := range globalBindings() {
		if b.bar && b.active(m) && b.label != "?" && b.label != "q" {
			out = append(out, b)
		}
	}
	return out
}

func (m *Model) goTo(s screen) tea.Cmd {
	m.screen = s
	if s == screenCluster && m.hasCluster() && m.cluster().Readiness == nil {
		return m.runReadiness()
	}
	return nil
}

// runReadiness asks the backend for the checks of the selected cluster.
func (m *Model) runReadiness() tea.Cmd {
	key, b, ctx := m.cluster().Key, m.b, m.ctx
	m.checkSel = 0
	return func() tea.Msg { return doneMsg{err: b.Readiness(ctx, key)} }
}
