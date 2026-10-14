package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
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
		{keys: []string{"1"}, label: "1-3", desc: "switch screen: fleet, cluster, change", do: func(m *Model) tea.Cmd { return m.goTo(screenFleet) }},
		{keys: []string{"2"}, do: func(m *Model) tea.Cmd { return m.goTo(screenCluster) }},
		{keys: []string{"3"}, do: func(m *Model) tea.Cmd { return m.goTo(screenChange) }},
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
	bs = append(bs, actionBindings(here)...)
	bs = append(bs, changeBindings()...)
	return bs
}

// actionBindings propose a change on the selected cluster: each opens a
// dry run, and nothing starts before y in its dialog. In a read-only UI
// the dialog shows the plan and the command; the keys stay, so that the
// dry run is the same with and without --allow-changes.
func actionBindings(here func(Model) bool) []binding {
	propose := func(do func(*Model) tea.Cmd) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			if m.saidBusy() {
				return nil
			}
			return do(m)
		}
	}
	return []binding{
		{keys: []string{"p"}, label: "p", desc: "dry-run a roll of a stale nodegroup (patch)", short: "patch", bar: true, primary: true,
			when: here, do: propose(func(m *Model) tea.Cmd { return m.planRoll() })},
		{keys: []string{"a"}, label: "a", desc: "dry-run the update of the stale add-ons", short: "add-ons", bar: true, primary: true,
			when: here, do: propose(func(m *Model) tea.Cmd {
				return m.plan(state.Action{Kind: state.ActionAddons, Cluster: m.cluster().Key})
			})},
		{keys: []string{"U"}, label: "U", desc: "dry-run an upgrade: choose the parts", short: "upgrade", bar: true, primary: true,
			when: here, do: propose(func(m *Model) tea.Cmd { return m.planUpgrade() })},
		{keys: []string{"B"}, label: "B", desc: "dry-run the rollback of the control plane (in its window)", short: "rollback", bar: true, primary: true,
			when: func(m Model) bool { return here(m) && m.cluster().Rollback != nil },
			do: propose(func(m *Model) tea.Cmd {
				return m.plan(state.Action{Kind: state.ActionRollback, Cluster: m.cluster().Key})
			})},
	}
}

// changeBindings steer the change on the Change screen. The steering keys
// show only while the handle offers them: a change started elsewhere, or
// one past its last checkpoint, has none.
func changeBindings() []binding {
	on := onScreen(screenChange)
	controls := func(m Model) change.Controls {
		if c, ok := m.change(); ok && on(m) {
			return c.Controls
		}
		return change.Controls{}
	}
	steer := func(m Model) bool { return controls(m).Pause || controls(m).Stop }
	asking := func(m Model) bool { return controls(m).Answer }
	paused := func(m Model) bool {
		c, ok := m.change()
		return ok && on(m) && c.State == change.Paused && c.Controls.Pause
	}
	several := func(m Model) bool { return on(m) && len(m.st.Changes) > 1 }
	call := func(name string, f func(m *Model, c state.Change) error) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			c, ok := m.change()
			if !ok {
				return nil
			}
			if err := f(m, c); err != nil {
				m.say(view.Fail, "%s: %v", name, err)
			}
			return nil
		}
	}
	return []binding{
		{keys: []string{"y"}, label: "y", desc: "answer the question: go on", short: "go on", bar: true, primary: true, when: asking,
			do: call("answer", func(m *Model, c state.Change) error { return m.b.Answer(m.ctx, c.ID, c.Question.ID, true) })},
		{keys: []string{"n"}, label: "n", desc: "answer the question: stop", short: "stop here", bar: true, primary: true, when: asking,
			do: call("answer", func(m *Model, c state.Change) error { return m.b.Answer(m.ctx, c.ID, c.Question.ID, false) })},
		{keys: []string{"P"}, label: "P", desc: "pause before the next phase; again to resume", short: "pause", bar: true, primary: true,
			when: func(m Model) bool { return steer(m) && !asking(m) && !paused(m) },
			do: call("pause", func(m *Model, c state.Change) error {
				m.say(progressStatus, "pause requested: the change pauses before its next phase")
				return m.b.Pause(m.ctx, c.ID)
			})},
		{keys: []string{"P"}, label: "P", desc: "resume", short: "resume", bar: true, primary: true, when: paused,
			do: call("resume", func(m *Model, c state.Change) error { return m.b.Resume(m.ctx, c.ID) })},
		{keys: []string{"S"}, label: "S", desc: "stop before the next phase; what started finishes", short: "stop", bar: true, primary: true,
			when: func(m Model) bool { return steer(m) && !asking(m) },
			do: call("stop", func(m *Model, c state.Change) error {
				m.say(view.Warn, "stop requested: the change stops before its next phase")
				return m.b.Stop(m.ctx, c.ID)
			})},
		{keys: []string{"space"}, label: "space", desc: "freeze the log; again to follow", short: "freeze", bar: true,
			when: func(m Model) bool { _, ok := m.change(); return ok && on(m) },
			do: func(m *Model) tea.Cmd {
				c, _ := m.change()
				if m.frozen != 0 {
					m.frozen = 0
				} else {
					m.frozen = c.LastSeq
				}
				return nil
			}},
		{keys: []string{"w"}, label: "w", desc: "log: warnings and failures only", short: "warnings", bar: true, when: on,
			do: func(m *Model) tea.Cmd { m.warnOnly = !m.warnOnly; return nil }},
		{keys: []string{"["}, label: "[ ]", desc: "the change before, the change after", short: "other change", bar: true, when: several,
			do: func(m *Model) tea.Cmd { m.chg, m.frozen = max(0, m.chg-1), 0; return nil }},
		{keys: []string{"]"}, when: several,
			do: func(m *Model) tea.Cmd { m.chg, m.frozen = min(len(m.st.Changes)-1, m.chg+1), 0; return nil }},
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

// key handles one key press. A dialog takes every key; the screen keys
// do nothing behind it.
func (m Model) key(k string) (Model, tea.Cmd) {
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	var table []binding
	switch {
	case m.help:
		table = append([]binding{{keys: []string{"esc", "?", "q"}, do: func(m *Model) tea.Cmd { m.help = false; return nil }}}, scrollBindings()...)
	case m.pick != nil:
		table = pickerBindings()
	case m.confirm != nil:
		table = append(dialogBindings(), scrollBindings()...)
	case m.planning != nil:
		table = []binding{{keys: []string{"esc", "q"}, do: func(m *Model) tea.Cmd { m.cancelPlan(); return nil }}}
	default:
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
