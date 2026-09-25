// Package tui is the full-screen terminal UI for refresh: the fleet, the
// readiness checks, live nodegroup rolls, and cluster upgrades, with live
// event and log streams. It draws a state.State from a state.Backend and never
// calls AWS or Kubernetes itself.
//
// Every backend call runs in a Bubble Tea command, off the UI loop, and comes
// back as a message, so a slow backend never freezes the screen or the keys.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Minimum terminal size. Below it the TUI shows a resize message.
const (
	minWidth  = 100
	minHeight = 24
)

type screen int

const (
	screenFleet screen = iota
	screenCluster
	screenRolls
	screenUpgrade
)

var screenNames = []string{"Fleet", "Cluster", "Rolls", "Upgrade"}

// logSource is the filter of a live log pane.
type logSource int

const (
	logRoll logSource = iota // the change's own feed (roll or upgrade timeline)
	logKube
	logAWS
	logAll
)

// Model is the Bubble Tea model.
type Model struct {
	ctx      context.Context
	b        state.Backend
	st       state.State
	loaded   bool
	interval time.Duration
	// fetchID numbers state fetches; a reply older than the last one
	// applied is dropped, so the screen never steps back in time.
	fetchID, appliedID int
	// planID numbers dry runs; only the reply to the newest one opens.
	// planning names the dry run on its way, for the log strip.
	planID   int
	planning string
	// pick is the open nodegroup picker, if any.
	pick *picker

	w, h   int
	screen screen

	sel      int // fleet cursor
	checkSel int
	rollIdx  int
	upIdx    int
	src      logSource
	// pausedSeq freezes the live panes at an event sequence number; zero
	// follows. frozen holds the state at the freeze, so the panes keep
	// showing events that the backend's capped lists have since dropped.
	pausedSeq uint64
	frozen    state.State
	pausedAt  time.Time
	warnOnly  bool
	// feedAll shows every cluster in the fleet feed; otherwise only the
	// selected one.
	feedAll bool

	confirm    *state.Plan
	confirmErr string
	starting   bool
	help       bool
	// scroll is the first body row shown in the open dialog.
	scroll int
	// focusAfter selects the change a successful Start created, once a
	// state fetched after the Start (id focusFrom or later) arrives.
	focusAfter *state.Action
	focusFrom  int

	notice     string
	noticeLvl  state.Level
	noticeTill time.Time
}

// New returns a model that reads b every interval. Calls to b use ctx.
func New(ctx context.Context, b state.Backend, interval time.Duration) Model {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	return Model{ctx: ctx, b: b, interval: interval, feedAll: true}
}

// Messages from backend calls.
type (
	tickMsg  struct{}
	stateMsg struct {
		id int
		// poll marks the fetch of the polling loop: its reply schedules
		// the next tick. Fetches after an action only refresh the screen.
		poll bool
		st   state.State
		err  error
	}
	planMsg struct {
		id   int
		plan state.Plan
		err  error
	}
	startMsg struct {
		a   state.Action
		err error
	}
	// doneMsg reports a call with no result: readiness, stop, pause.
	doneMsg struct {
		ok  string
		err error
	}
)

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
}

// fetch reads the state in a command, outside the polling loop.
func (m *Model) fetch() tea.Cmd { return m.fetchState(false) }

// poll reads the state for the polling loop.
func (m *Model) poll() tea.Cmd { return m.fetchState(true) }

func (m *Model) fetchState(poll bool) tea.Cmd {
	m.fetchID++
	return fetchCmd(m.ctx, m.b, m.fetchID, poll)
}

func fetchCmd(ctx context.Context, b state.Backend, id int, poll bool) tea.Cmd {
	return func() tea.Msg {
		st, err := b.State(ctx)
		return stateMsg{id: id, poll: poll, st: st, err: err}
	}
}

// call runs a backend call with no result in a command.
func (m Model) call(ok string, fn func(context.Context) error) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg { return doneMsg{ok: ok, err: fn(ctx)} }
}

// Init implements tea.Model. It starts the polling loop with fetch id 0;
// fetches made later count up from 1, so no two share an id.
func (m Model) Init() tea.Cmd { return fetchCmd(m.ctx, m.b, 0, true) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.scroll = m.clampScroll(m.scroll)
		if m.pick != nil {
			m.scrollTo(m.pick.sel)
		}
	case tickMsg:
		cmd := m.poll() // poll bumps m.fetchID; take it before m is copied out
		return m, cmd
	case stateMsg:
		return m.applyState(msg)
	case planMsg:
		if msg.id != m.planID || m.pick != nil {
			return m, nil // cancelled, superseded, or the picker is open
		}
		m.planning = ""
		m.focusAfter = nil // the dialog takes over; don't move the screen behind it
		if msg.err != nil {
			m.say(state.LevelError, "%v", msg.err)
			return m, nil
		}
		p := msg.plan
		m.confirm, m.confirmErr, m.scroll, m.starting, m.help = &p, "", 0, false, false
	case startMsg:
		m.starting = false
		if msg.err != nil {
			m.confirmErr = msg.err.Error()
			return m, nil
		}
		m.confirm = nil
		a := msg.a
		if a.Kind == state.ActionAddons {
			m.say(state.LevelProgress, "add-on update started on %s", a.Cluster)
		}
		cmd := m.fetch() // fetch bumps m.fetchID; take it before m is copied out
		m.focusAfter, m.focusFrom = &a, m.fetchID
		return m, cmd
	case doneMsg:
		if msg.err != nil {
			m.say(state.LevelError, "%v", msg.err)
		} else if msg.ok != "" {
			m.say(state.LevelOK, "%s", msg.ok)
		}
		cmd := m.fetch() // fetch bumps m.fetchID; take it before m is copied out
		return m, cmd
	case tea.KeyPressMsg:
		return m.key(msg.String())
	}
	return m, nil
}

// applyState takes a fetched state. A polling reply schedules the next
// tick even when it is dropped, so the loop never stops; any other reply
// schedules nothing, so the loop never forks.
func (m Model) applyState(msg stateMsg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if msg.poll {
		next = m.tick()
	}
	if msg.id < m.appliedID {
		return m, next
	}
	m.appliedID = msg.id
	if msg.err != nil {
		m.say(state.LevelError, "reading state: %v", msg.err)
		return m, next
	}
	// Keep the selections on the same cluster, roll, and upgrade, wherever
	// they moved to in the new lists.
	selName := m.cluster().Name
	rollK, hasRoll := m.rollKey()
	upK, hasUp := m.upgradeKey()
	m.st = msg.st
	m.loaded = true
	for i, c := range m.st.Clusters {
		if c.Name == selName {
			m.sel = i
		}
	}
	for i, r := range m.st.Rolls {
		if hasRoll && keyOfRoll(r) == rollK {
			m.rollIdx = i
		}
	}
	for i, u := range m.st.Upgrades {
		if hasUp && keyOfUpgrade(u) == upK {
			m.upIdx = i
		}
	}
	if a := m.focusAfter; a != nil && msg.id >= m.focusFrom {
		switch a.Kind {
		case state.ActionRoll:
			if m.focusRoll(a.Cluster) {
				m.screen, m.src, m.focusAfter = screenRolls, logRoll, nil
			}
		case state.ActionUpgrade:
			if m.focusUpgrade(a.Cluster) {
				m.screen, m.src, m.focusAfter = screenUpgrade, logRoll, nil
			}
		default:
			m.focusAfter = nil
		}
	}
	m.clamp()
	return m, next
}

func (m *Model) clamp() {
	m.sel = min(max(0, m.sel), max(0, len(m.st.Clusters)-1))
	m.rollIdx = min(max(0, m.rollIdx), max(0, len(m.st.Rolls)-1))
	m.upIdx = min(max(0, m.upIdx), max(0, len(m.st.Upgrades)-1))
	n := len(m.st.Readiness[m.cluster().Name].Checks)
	m.checkSel = min(max(0, m.checkSel), max(0, n-1))
}

func (m Model) cluster() state.Cluster {
	if m.sel >= 0 && m.sel < len(m.st.Clusters) {
		return m.st.Clusters[m.sel]
	}
	return state.Cluster{}
}

func (m *Model) say(lvl state.Level, format string, args ...any) {
	m.notice = fmt.Sprintf(format, args...)
	m.noticeLvl = lvl
	m.noticeTill = m.st.Now.Add(20 * time.Second)
}

// focusRoll selects the newest roll of cluster, else the newest running
// roll. It reports whether cluster has one.
func (m *Model) focusRoll(cluster string) bool {
	for i := len(m.st.Rolls) - 1; i >= 0; i-- {
		if m.st.Rolls[i].Cluster == cluster {
			m.rollIdx = i
			return true
		}
	}
	for i := len(m.st.Rolls) - 1; i >= 0; i-- {
		if m.st.Rolls[i].Running() {
			m.rollIdx = i
			break
		}
	}
	return false
}

// focusUpgrade selects the newest upgrade of cluster, else the newest
// running one. It reports whether cluster has one.
func (m *Model) focusUpgrade(cluster string) bool {
	for i := len(m.st.Upgrades) - 1; i >= 0; i-- {
		if m.st.Upgrades[i].Cluster == cluster {
			m.upIdx = i
			return true
		}
	}
	for i := len(m.st.Upgrades) - 1; i >= 0; i-- {
		if m.st.Upgrades[i].Running() {
			m.upIdx = i
			break
		}
	}
	return false
}

func (m Model) roll() (state.Roll, bool) {
	if m.rollIdx >= 0 && m.rollIdx < len(m.st.Rolls) {
		return m.st.Rolls[m.rollIdx], true
	}
	return state.Roll{}, false
}

func (m Model) upgrade() (state.Upgrade, bool) {
	if m.upIdx >= 0 && m.upIdx < len(m.st.Upgrades) {
		return m.st.Upgrades[m.upIdx], true
	}
	return state.Upgrade{}, false
}

// Identities of changes, stable across state fetches.
func keyOfRoll(r state.Roll) string {
	return r.Cluster + "/" + r.Nodegroup + "@" + r.StartedAt.String()
}

func keyOfUpgrade(u state.Upgrade) string { return u.Cluster + "@" + u.StartedAt.String() }

func (m Model) rollKey() (string, bool) {
	r, ok := m.roll()
	return keyOfRoll(r), ok
}

func (m Model) upgradeKey() (string, bool) {
	u, ok := m.upgrade()
	return keyOfUpgrade(u), ok
}

// feed is the state the live panes read: the frozen copy while frozen.
func (m Model) feed() state.State {
	if m.pausedSeq != 0 {
		return m.frozen
	}
	return m.st
}

// rollEvents is r's feed as the live panes show it.
func (m Model) rollEvents(r state.Roll) []state.Event {
	k := keyOfRoll(r)
	for _, fr := range m.feed().Rolls {
		if keyOfRoll(fr) == k {
			return fr.Events
		}
	}
	return nil
}

// upgradeEvents is u's timeline as the live panes show it.
func (m Model) upgradeEvents(u state.Upgrade) []state.Event {
	k := keyOfUpgrade(u)
	for _, fu := range m.feed().Upgrades {
		if keyOfUpgrade(fu) == k {
			return fu.Events
		}
	}
	return nil
}

// picker chooses which stale nodegroup to patch.
type picker struct {
	cluster string
	items   []state.Nodegroup
	sel     int
}
