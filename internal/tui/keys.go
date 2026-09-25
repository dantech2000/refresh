package tui

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// binding is one key action. The same table drives the key handler, the key
// bar, and the help dialog, so what the screen offers is what the keys do.
type binding struct {
	keys  []string // key names as Bubble Tea reports them
	label string   // how the key is written in the key bar and help
	desc  string   // what it does, for the help dialog
	short string   // the key bar's text; desc when empty
	// bar shows the binding in the key bar; the rest are in help only.
	bar bool
	// primary keeps its words in the key bar when the bar is too narrow
	// for all of them: the change keys, which a bare letter does not explain.
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
	keysPgUp   = []string{"pgup", "ctrl+u"}
	keysPgDown = []string{"pgdown", "ctrl+d"}
)

func onScreen(ss ...screen) func(Model) bool {
	return func(m Model) bool { return slices.Contains(ss, m.screen) }
}

func (m Model) hasCluster() bool { return m.cluster().Name != "" }

// globalBindings apply on every screen.
func globalBindings() []binding {
	return []binding{
		{keys: []string{"1"}, label: "1–4", desc: "switch screen: fleet, cluster, rolls, upgrade", do: func(m *Model) tea.Cmd { return m.goTo(screenFleet) }},
		{keys: []string{"2"}, do: func(m *Model) tea.Cmd { return m.goTo(screenCluster) }},
		{keys: []string{"3"}, do: func(m *Model) tea.Cmd { return m.goTo(screenRolls) }},
		{keys: []string{"4"}, do: func(m *Model) tea.Cmd { return m.goTo(screenUpgrade) }},
		{keys: []string{"esc", "backspace"}, label: "esc", desc: "back to the fleet · changes keep running", short: "back", bar: true,
			when: func(m Model) bool { return m.screen != screenFleet },
			do:   func(m *Model) tea.Cmd { m.screen = screenFleet; return nil }},
		{keys: []string{"space"}, label: "space", desc: "freeze or follow the live panes", short: "freeze", bar: true, do: func(m *Model) tea.Cmd {
			if m.pausedSeq == 0 {
				m.pausedSeq, m.pausedAt, m.frozen = max(1, m.st.Seq), m.st.Now, m.st
			} else {
				m.pausedSeq, m.pausedAt, m.frozen = 0, m.st.Now, state.State{}
			}
			return nil
		}},
		{keys: []string{"w"}, label: "w", desc: "warnings and errors only", do: func(m *Model) tea.Cmd { m.warnOnly = !m.warnOnly; return nil }},
		{keys: []string{"ctrl+r"}, label: "ctrl+r", desc: "read the fleet from AWS now",
			when: func(m Model) bool { _, ok := m.b.(refresher); return ok },
			do: func(m *Model) tea.Cmd {
				if r, ok := m.b.(refresher); ok {
					r.Refresh()
					m.say(state.LevelProgress, "reading the fleet…")
				}
				return nil
			}},
		{keys: []string{"?"}, label: "?", desc: "keys", do: func(m *Model) tea.Cmd { m.help, m.scroll = true, 0; return nil }},
		{keys: []string{"q"}, label: "q", desc: "quit · in a dialog, close it · changes in flight keep running in EKS", do: func(*Model) tea.Cmd { return tea.Quit }},
	}
}

// actionBindings start the dry run of a change on the selected cluster.
// They apply where the selected cluster is the one on screen.
func actionBindings() []binding {
	here := func(m Model) bool {
		return (m.screen == screenFleet || m.screen == screenCluster) && m.hasCluster()
	}
	return []binding{
		{keys: []string{"r", "R"}, label: "r", desc: "readiness", bar: true, primary: true, when: here, do: func(m *Model) tea.Cmd {
			m.screen = screenCluster
			return m.runReadiness()
		}},
		{keys: []string{"p"}, label: "p", desc: "patch", bar: true, primary: true, when: here, do: func(m *Model) tea.Cmd { return m.planRoll() }},
		{keys: []string{"a"}, label: "a", desc: "add-ons", bar: true, primary: true, when: here, do: func(m *Model) tea.Cmd {
			return m.plan(state.Action{Kind: state.ActionAddons, Cluster: m.cluster().Name})
		}},
		{keys: []string{"U"}, label: "U", desc: "upgrade", bar: true, primary: true, when: here, do: func(m *Model) tea.Cmd {
			return m.plan(state.Action{Kind: state.ActionUpgrade, Cluster: m.cluster().Name})
		}},
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
		{keys: keysTop, label: "g G", desc: "first · last", when: when, do: to(false)},
		{keys: keysBottom, when: when, do: to(true)},
	}
}

// logBindings switch the log source of a live pane and page through rolls
// or upgrades.
func logBindings(ss screen, idx func(*Model) *int, n func(Model) int, what string) []binding {
	on := onScreen(ss)
	src := func(d logSource) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd { m.src = (m.src + d + 4) % 4; return nil }
	}
	page := func(d int) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			p := idx(m)
			*p = min(max(0, *p+d), max(0, n(*m)-1))
			return nil
		}
	}
	many := func(m Model) bool { return on(m) && n(m) > 1 }
	return []binding{
		{keys: []string{"tab", "right", "l"}, label: "tab ←→", desc: "log source", bar: true, when: on, do: src(1)},
		{keys: []string{"shift+tab", "left", "h"}, when: on, do: src(-1)},
		{keys: []string{"]"}, label: "[ ]", desc: what, bar: true, when: many, do: page(1)},
		{keys: []string{"["}, when: many, do: page(-1)},
	}
}

// screenBindings apply on one screen.
func screenBindings() []binding {
	var bs []binding
	onFleet := onScreen(screenFleet)
	bs = append(bs, listBindings(onFleet, func(m *Model) *int { return &m.sel }, func(m Model) int { return len(m.st.Clusters) }, "move")...)
	bs = append(bs,
		binding{keys: []string{"enter"}, label: "enter", desc: "open", bar: true, when: func(m Model) bool { return onFleet(m) && m.hasCluster() }, do: func(m *Model) tea.Cmd { return m.open() }},
		binding{keys: []string{"f"}, label: "f", desc: "feed: all clusters or this one", short: "feed scope", bar: true, when: onFleet, do: func(m *Model) tea.Cmd { m.feedAll = !m.feedAll; return nil }},
	)
	onCluster := onScreen(screenCluster)
	bs = append(bs, listBindings(onCluster, func(m *Model) *int { return &m.checkSel }, func(m Model) int {
		return len(m.st.Readiness[m.cluster().Name].Checks)
	}, "checks")...)
	bs = append(bs, actionBindings()...)
	bs = append(bs, logBindings(screenRolls, func(m *Model) *int { return &m.rollIdx }, func(m Model) int { return len(m.st.Rolls) }, "rolls")...)
	bs = append(bs, logBindings(screenUpgrade, func(m *Model) *int { return &m.upIdx }, func(m Model) int { return len(m.st.Upgrades) }, "upgrades")...)
	running := func(m Model) bool {
		u, ok := m.upgrade()
		return m.screen == screenUpgrade && ok && u.Running()
	}
	bs = append(bs,
		binding{keys: []string{"S"}, label: "S", desc: "stop after this step (an EKS update in flight finishes)", short: "stop after", bar: true, primary: true, when: running, do: func(m *Model) tea.Cmd {
			u, _ := m.upgrade()
			b := m.b
			return m.call("", func(ctx context.Context) error { return b.StopAfterCurrent(ctx, u.Cluster) })
		}},
		binding{keys: []string{"P"}, label: "P", desc: "pause before the next phase", short: "pause phase", bar: true, primary: true, when: running, do: func(m *Model) tea.Cmd {
			u, _ := m.upgrade()
			b := m.b
			return m.call("", func(ctx context.Context) error { return b.TogglePause(ctx, u.Cluster) })
		}},
	)
	return bs
}

// dialogBindings apply while the confirm dialog is open.
func dialogBindings() []binding {
	canStart := func(m Model) bool { return m.confirm != nil && m.confirm.Blocked == "" && !m.starting }
	return []binding{
		{keys: []string{"y"}, label: "y", desc: "start the change", when: canStart, do: func(m *Model) tea.Cmd { return m.start() }},
		{keys: []string{"esc", "n", "q"}, label: "esc n q", desc: "cancel (not while the change is starting)",
			when: func(m Model) bool { return !m.starting },
			do:   func(m *Model) tea.Cmd { m.confirm = nil; return nil }},
		{keys: []string{"c"}, label: "c", desc: "copy the CLI command", do: func(m *Model) tea.Cmd {
			m.say(state.LevelOK, "copied: %s", m.confirm.Command)
			return tea.SetClipboard(m.confirm.Command)
		}},
	}
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
func (m Model) key(k string) (tea.Model, tea.Cmd) {
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	var table []binding
	switch {
	case m.pick != nil:
		table = pickerBindings()
	case m.confirm != nil:
		table = append(dialogBindings(), scrollBindings()...)
	case m.help:
		table = append([]binding{{keys: []string{"esc", "?", "q"}, do: func(m *Model) tea.Cmd { m.help = false; return nil }}}, scrollBindings()...)
	default:
		if k == "esc" && m.planning != "" {
			m.cancelPlan()
			return m, nil
		}
		table = append(screenBindings(), globalBindings()...)
	}
	screenBefore, clusterBefore, planBefore := m.screen, m.cluster().Name, m.planID
	for _, b := range table {
		if b.matches(k) && b.active(m) {
			cmd := b.do(&m)
			if m.screen != screenBefore || m.cluster().Name != clusterBefore {
				// The user moved on: a dry run for the old target must not
				// open over the new one, and a started change must not pull
				// them back.
				m.focusAfter = nil
				if m.planning != "" && m.planID == planBefore {
					m.cancelPlan()
				}
			}
			m.clamp()
			return m, cmd
		}
	}
	return m, nil
}

// cancelPlan drops the dry run on its way and says so.
func (m *Model) cancelPlan() {
	m.planID++
	m.say(state.LevelInfo, "dry run for %s cancelled", m.planning)
	m.planning = ""
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
	switch s {
	case screenCluster:
		if _, ok := m.st.Readiness[m.cluster().Name]; !ok && m.hasCluster() {
			return m.runReadiness()
		}
	case screenUpgrade:
		m.focusUpgrade(m.cluster().Name)
	case screenRolls:
		m.focusRoll(m.cluster().Name)
	}
	return nil
}

// open goes to the selected cluster's change in flight, or its readiness.
func (m *Model) open() tea.Cmd {
	c := m.cluster()
	if c.Busy != "" {
		if m.focusUpgrade(c.Name) && m.st.Upgrades[m.upIdx].Running() {
			m.screen = screenUpgrade
			return nil
		}
		if m.focusRoll(c.Name) {
			m.screen = screenRolls
			return nil
		}
	}
	return m.goTo(screenCluster)
}

func (m *Model) runReadiness() tea.Cmd {
	name, b := m.cluster().Name, m.b
	m.checkSel = 0
	return m.call("", func(ctx context.Context) error { return b.RunReadiness(ctx, name) })
}

// planRoll dry-runs a roll of the selected cluster's stale nodegroup, or
// opens a picker when more than one is stale.
func (m *Model) planRoll() tea.Cmd {
	c := m.cluster()
	stale := c.StaleNodegroups()
	switch len(stale) {
	case 0:
		m.say(state.LevelOK, "every nodegroup in %s is current", c.Name)
		return nil
	case 1:
		return m.plan(state.Action{Kind: state.ActionRoll, Cluster: c.Name, Nodegroup: stale[0].Name})
	default:
		// The picker takes over: nothing may open or move behind it.
		if m.planning != "" {
			m.cancelPlan()
		}
		m.focusAfter = nil
		m.pick, m.scroll = &picker{cluster: c.Name, items: stale}, 0
		return nil
	}
}

// pickerBindings apply while the nodegroup picker is open.
func pickerBindings() []binding {
	move := func(d int) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			m.pick.sel = min(max(0, m.pick.sel+d), len(m.pick.items)-1)
			m.scrollTo(m.pick.sel)
			return nil
		}
	}
	choose := func(m *Model) tea.Cmd {
		p := m.pick
		m.pick = nil
		return m.plan(state.Action{Kind: state.ActionRoll, Cluster: p.cluster, Nodegroup: p.items[p.sel].Name})
	}
	bs := []binding{
		{keys: keysUp, label: "↑↓", desc: "choose a nodegroup", do: move(-1)},
		{keys: keysDown, do: move(1)},
		{keys: []string{"enter"}, label: "enter", desc: "dry-run a patch of it", do: choose},
		{keys: []string{"esc", "q"}, label: "esc", desc: "close", do: func(m *Model) tea.Cmd { m.pick = nil; return nil }},
	}
	for i := range 9 {
		n := i
		bs = append(bs, binding{keys: []string{string(rune('1' + i))}, when: func(m Model) bool { return n < len(m.pick.items) },
			do: func(m *Model) tea.Cmd { m.pick.sel = n; return choose(m) }})
	}
	return bs
}

func (m *Model) plan(a state.Action) tea.Cmd {
	m.planID++
	m.planning = planName(a)
	m.focusAfter = nil // a dry run is on its way; don't move the screen under it
	ctx, b, id := m.ctx, m.b, m.planID
	return func() tea.Msg {
		p, err := b.Plan(ctx, a)
		return planMsg{id: id, plan: p, err: err}
	}
}

func (m *Model) start() tea.Cmd {
	m.starting = true
	m.confirmErr = ""
	ctx, b, a := m.ctx, m.b, m.confirm.Action
	return func() tea.Msg { return startMsg{a: a, err: b.Start(ctx, a)} }
}

func planName(a state.Action) string {
	switch a.Kind {
	case state.ActionRoll:
		return "a patch of " + a.Cluster + "/" + a.Nodegroup
	case state.ActionUpgrade:
		return "an upgrade of " + a.Cluster
	default:
		return "add-on updates on " + a.Cluster
	}
}

// scrollTo scrolls the open dialog so body row i is visible.
func (m *Model) scrollTo(i int) {
	d, ok := m.openDialog()
	if !ok {
		return
	}
	rows := m.bodyRows(d)
	switch {
	case i < m.scroll:
		m.scroll = i
	case i >= m.scroll+rows:
		m.scroll = i - rows + 1
	}
	m.scroll = m.clampScroll(m.scroll)
}

// refresher is a backend that can read its data again on request.
type refresher interface{ Refresh() }
