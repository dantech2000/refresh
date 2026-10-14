// Package tui is the full-screen terminal UI for refresh: the fleet, the
// readiness checks, and the changes. It draws a state.State from a Backend
// and never calls AWS or Kubernetes itself.
//
// Every backend call runs in a Bubble Tea command, off the UI loop, and
// comes back as a message, so a slow backend never freezes the screen or
// the keys. The model does not poll: it waits for the next version of the
// state in a command, and it ticks only while something on screen shows
// an elapsed time.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// Minimum terminal size. Below it the TUI shows a resize message.
const (
	minWidth  = 100
	minHeight = 24
)

// tickInterval is how often the clock ticks while an elapsed time shows.
const tickInterval = time.Second

// Backend is what the model needs. backend.Live implements it, and the
// tests of the model use a scripted one.
type Backend interface {
	// Next returns the first state whose version is above after. It
	// blocks until there is one, or ctx ends.
	Next(ctx context.Context, after uint64) (state.State, error)
	// Refresh asks for a sweep now.
	Refresh()
	// Readiness starts the readiness checks of a cluster. The result
	// lands in a later state.
	Readiness(ctx context.Context, c state.Key) error
	// Plan dry-runs an action and returns the proposal to show. It works
	// read-only too: the dialog then names the command.
	Plan(ctx context.Context, a state.Action) (state.Proposal, error)
	// Start runs the proposal, which must be the newest. It is the one
	// call that changes a cluster, and the one that --allow-changes gates.
	Start(ctx context.Context, id state.ProposalID) (state.ChangeID, error)
	// Answer, Pause, Resume, and Stop steer a running change of this UI.
	Answer(ctx context.Context, id state.ChangeID, q change.QuestionID, yes bool) error
	Pause(ctx context.Context, id state.ChangeID) error
	Resume(ctx context.Context, id state.ChangeID) error
	Stop(ctx context.Context, id state.ChangeID) error
}

type screen int

const (
	screenFleet screen = iota
	screenCluster
	screenChange
)

var screenNames = []string{"Fleet", "Cluster", "Change"}

// Model is the Bubble Tea model.
type Model struct {
	ctx context.Context //nolint:containedctx // the context of the session, for the commands the model starts
	b   Backend
	th  view.Theme
	bd  view.Border
	p   *painter
	// ascii replaces the glyphs of the labels and the texts that a
	// terminal without Unicode cannot show, each with one ASCII cell, so
	// a frame keeps its width. Nil with Unicode.
	ascii *strings.Replacer

	st     state.State
	loaded bool
	// now is the clock of the elapsed-time displays: the time of the
	// last tick, or of the last state.
	now time.Time
	// ticking is set while a tick is scheduled, so that no second clock
	// starts.
	ticking bool
	// redrawnAt is when the screen was last redrawn in full after a sweep.
	redrawnAt time.Time

	w, h   int
	screen screen

	sel      int // the fleet cursor
	checkSel int // the cursor of the readiness checks
	feedAll  bool
	help     bool
	scroll   int // the first body row shown in the open dialog

	// chg is the index of the change on the Change screen. frozen holds
	// its log at a sequence number (space), 0 follows; warnOnly filters
	// the log to warnings and failures.
	chg      int
	frozen   uint64
	warnOnly bool
	// focusAfter is a change the model started: the next state that
	// carries it opens the Change screen on it.
	focusAfter state.ChangeID

	// The dialogs. pick chooses what to dry-run; planning is a dry run on
	// its way; confirm is a dry run on screen, and starting is set from y
	// until the backend answers.
	pick       *picker
	planning   *planning
	planSeq    int
	confirm    *state.Proposal
	confirmErr string
	starting   bool

	notice       string
	noticeStatus view.Status
	noticeTill   time.Time
}

// New returns a model over b that draws with th. Calls to b use ctx.
func New(ctx context.Context, b Backend, th view.Theme) Model {
	m := Model{ctx: ctx, b: b, th: th, bd: th.Border(), p: newPainter(th), feedAll: true}
	if !th.Unicode {
		m.ascii = strings.NewReplacer("…", m.bd.Ellipsis, "·", "-", "→", ">", "←", "<", "↑", "^", "↓", "v", "×", "x")
	}
	return m
}

// Messages from backend calls and the clock.
type (
	stateMsg struct {
		st  state.State
		err error
	}
	tickMsg struct{ at time.Time }
	// doneMsg reports a call with no result.
	doneMsg struct{ err error }
	// proposalMsg is the reply of a dry run; seq says which.
	proposalMsg struct {
		seq int
		p   state.Proposal
		err error
	}
	// startedMsg is the reply of Start.
	startedMsg struct {
		id  state.ChangeID
		err error
	}
)

// next waits for the state after version after, in a command.
func (m Model) next(after uint64) tea.Cmd {
	ctx, b := m.ctx, m.b
	return func() tea.Msg {
		st, err := b.Next(ctx, after)
		return stateMsg{st: st, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg{at: t} })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return m.next(0) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.scroll = m.clampScroll(m.scroll)
		return m, nil
	case stateMsg:
		return m.applyState(msg)
	case tickMsg:
		m.ticking = false
		if msg.at.After(m.now) {
			m.now = msg.at
		}
		cmd := m.scheduleTick()
		return m, cmd
	case doneMsg:
		if msg.err != nil {
			m.say(view.Fail, "%v", msg.err)
		}
		return m, nil
	case proposalMsg:
		m.applyProposal(msg)
		return m, nil
	case startedMsg:
		m.applyStarted(msg)
		return m, nil
	case tea.KeyPressMsg:
		next, cmd := m.key(msg.String())
		return next, cmd
	}
	return m, nil
}

// applyState takes a published state and asks for the next one. A
// backend that cannot answer any more, because the session ended, gets
// no further call.
func (m Model) applyState(msg stateMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, nil
	}
	var cmds []tea.Cmd
	if m.redrawAfter(msg.st) {
		cmds = append(cmds, tea.ClearScreen)
	}
	selected := m.cluster().Key
	shown, hadChange := m.change()
	m.st = msg.st
	m.loaded = true
	if msg.st.Now.After(m.now) {
		m.now = msg.st.Now
	}
	// The selection stays on its cluster, wherever the row moved to; the
	// Change screen stays on its change.
	for i, c := range m.st.Clusters {
		if c.Key == selected {
			m.sel = i
		}
	}
	if hadChange {
		m.chg = m.changeIndex(shown.ID)
	}
	if m.focusAfter != 0 {
		if i := m.changeIndex(m.focusAfter); i >= 0 {
			m.chg, m.screen, m.focusAfter = i, screenChange, 0
			m.frozen = 0
		}
	}
	m.clamp()
	cmds = append(cmds, m.next(m.st.Version), m.scheduleTick())
	return m, tea.Batch(cmds...)
}

// changeIndex is the index of a change in the state, or -1.
func (m Model) changeIndex(id state.ChangeID) int {
	for i, c := range m.st.Changes {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// applyProposal takes the reply of a dry run. An older dry run, or one
// that esc cancelled, is dropped.
func (m *Model) applyProposal(msg proposalMsg) {
	if m.planning == nil || msg.seq != m.planning.seq {
		return
	}
	a := m.planning.action
	m.planning = nil
	if msg.err != nil {
		m.say(view.Fail, "dry run of %s: %v", describeAction(a), msg.err)
		return
	}
	p := msg.p
	m.confirm, m.confirmErr, m.starting, m.scroll = &p, "", false, 0
}

// applyStarted takes the reply of Start. On success the dialog closes and
// the next state that carries the change opens it.
func (m *Model) applyStarted(msg startedMsg) {
	m.starting = false
	if msg.err != nil {
		m.confirmErr = msg.err.Error()
		return
	}
	m.confirm, m.confirmErr = nil, ""
	m.focusAfter = msg.id
	if i := m.changeIndex(msg.id); i >= 0 {
		m.chg, m.screen, m.focusAfter, m.frozen = i, screenChange, 0, 0
	}
}

// needsTick reports whether something on screen shows an elapsed time.
func (m Model) needsTick() bool {
	if !m.loaded {
		return false
	}
	if m.st.Sweep.EndedAt.IsZero() {
		return true
	}
	for _, c := range m.st.Clusters {
		if c.Readiness != nil && c.Readiness.Running() {
			return true
		}
	}
	for _, c := range m.st.Changes {
		if c.Running() {
			return true
		}
	}
	return false
}

// scheduleTick starts the clock when something needs it and no tick is
// on its way. Idle, the model schedules nothing.
func (m *Model) scheduleTick() tea.Cmd {
	if m.ticking || !m.needsTick() {
		return nil
	}
	m.ticking = true
	return tick()
}

// redrawAfter reports whether st ends a sweep that should redraw the
// whole screen, and records the redraw. Another program can write to the
// terminal, and the renderer only redraws the cells that change: such
// text would stay. A finished sweep redraws the screen, at most once per
// minRedraw, and not for the first state. ctrl+l redraws at once.
func (m *Model) redrawAfter(st state.State) bool {
	if !m.loaded || st.Sweep.Count <= m.st.Sweep.Count || m.now.Sub(m.redrawnAt) < minRedraw {
		return false
	}
	m.redrawnAt = m.now
	return true
}

// minRedraw is the least time between two full redraws after sweeps.
const minRedraw = 30 * time.Second

func (m *Model) clamp() {
	m.sel = min(max(0, m.sel), max(0, len(m.st.Clusters)-1))
	m.checkSel = min(max(0, m.checkSel), max(0, len(m.checks())-1))
	m.chg = min(max(0, m.chg), max(0, len(m.st.Changes)-1))
}

// cluster is the selected cluster, or the zero value with no fleet.
func (m Model) cluster() state.Cluster {
	if m.sel >= 0 && m.sel < len(m.st.Clusters) {
		return m.st.Clusters[m.sel]
	}
	return state.Cluster{}
}

func (m *Model) say(s view.Status, format string, args ...any) {
	m.notice = fmt.Sprintf(format, args...)
	m.noticeStatus = s
	m.noticeTill = m.now.Add(20 * time.Second)
}

// progressStatus is the status of a notice about work in progress.
const progressStatus = view.Progress
