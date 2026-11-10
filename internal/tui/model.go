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
	"runtime/debug"
	"strings"
	"sync"
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
	// its log (space), nil follows; warnOnly filters the log to warnings
	// and failures.
	chg      int
	frozen   *freeze
	warnOnly bool
	// focusAfter is a change the model started: the next state that
	// carries it opens the Change screen on it.
	focusAfter state.ChangeID
	// asked is when the model first had each open question. y and n
	// answer one only after answerDelay, so that the y that started a
	// change, pressed again or held, cannot accept its health gate.
	asked map[question]time.Time
	// quitWarning is the notice that a q said while a change of this UI
	// runs. While that notice shows, a second q quits, and any other key
	// only takes it back.
	quitWarning string
	// tick is the command of the clock; the tests hold it.
	tick func() tea.Cmd
	// crash keeps the first panic of the model, shared by every copy.
	crash *crash

	// The dialogs. pick chooses what to dry-run; planning is a dry run on
	// its way; confirm is a dry run on screen, and starting is set from y
	// until the backend answers.
	pick       *picker
	planning   *planning
	planSeq    int
	confirm    *state.Proposal
	confirmErr string
	starting   bool

	// notice shows in the log strip until noticeTill. A zero noticeTill
	// is a notice that no tick or state has seen yet: the first one sets
	// it, so that a notice shows for noticeFor however long the model was
	// idle before it.
	notice       string
	noticeStatus view.Status
	noticeTill   time.Time
}

// question names an open question of a change.
type question struct {
	change state.ChangeID
	id     change.QuestionID
}

// noticeFor is how long a notice shows.
const noticeFor = 20 * time.Second

// answerDelay is how long a question shows before y and n answer it.
const answerDelay = time.Second

// New returns a model over b that draws with th. Calls to b use ctx.
func New(ctx context.Context, b Backend, th view.Theme) Model {
	m := Model{ctx: ctx, b: b, th: th, bd: th.Border(), p: newPainter(th), feedAll: true, tick: tick, crash: &crash{}}
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

// crash is the first panic of the model and its stack. Bubble Tea
// recovers a panic and prints it to os.Stderr, which is the null device
// while the UI runs; the command reads the trace from here once the
// terminal is restored.
type crash struct {
	mu    sync.Mutex
	trace string
}

// record keeps the first panic and its stack. It runs in the deferred
// function that recovered r, so the stack is the one of the panic.
func (c *crash) record(r any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trace == "" {
		c.trace = fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
	}
}

// catch records a panic in Update or View and panics again: Bubble Tea
// recovers it on the goroutine of Run, before Run returns, and restores
// the terminal. It must be deferred.
func (c *crash) catch() {
	if r := recover(); r != nil {
		c.record(r)
		panic(r)
	}
}

// crashMsg reports a panic in a command, which guard recovered.
type crashMsg struct{}

// Panic returns the first panic of the model, in Update, in View, or in a
// command of the model, with its stack, or "".
func (m Model) Panic() string {
	m.crash.mu.Lock()
	defer m.crash.mu.Unlock()
	return m.crash.trace
}

// guard runs cmd so that a panic in it is recorded and ends the session
// as a message. Bubble Tea would recover it on a goroutine that it does
// not wait for: Run returns first, and the trace that goroutine prints
// lands after the terminal is restored, a second time.
func (m Model) guard(cmd tea.Cmd) tea.Cmd {
	c := m.crash
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				c.record(r)
				msg = crashMsg{}
			}
		}()
		return cmd()
	}
}

// next waits for the state after version after, in a command.
func (m Model) next(after uint64) tea.Cmd {
	ctx, b := m.ctx, m.b
	return m.guard(func() tea.Msg {
		st, err := b.Next(ctx, after)
		return stateMsg{st: st, err: err}
	})
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg{at: t} })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return m.next(0) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.crash.catch()
	switch msg := msg.(type) {
	case crashMsg:
		return m, tea.Quit
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
		m.seeNotice()
		cmd := m.scheduleTick()
		return m, cmd
	case doneMsg:
		if msg.err != nil {
			m.say(view.Fail, "%v", msg.err)
		}
		cmd := m.scheduleTick()
		return m, cmd
	case proposalMsg:
		m.applyProposal(msg)
		cmd := m.scheduleTick()
		return m, cmd
	case startedMsg:
		m.applyStarted(msg)
		cmd := m.scheduleTick()
		return m, cmd
	case tea.KeyPressMsg:
		next, cmd := m.key(msg.String())
		// A notice that the key said needs the clock to go away.
		clock := next.scheduleTick()
		return next, tea.Batch(cmd, clock)
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
	m.seeNotice()
	m.seeQuestions()
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
			m.frozen = nil
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
		m.say(view.Fail, "dry run of %s: %v", a.Describe(true), msg.err)
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
		m.chg, m.screen, m.focusAfter, m.frozen = i, screenChange, 0, nil
	}
}

// seeQuestions records when the model first had each open question, and
// forgets the ones that closed.
func (m *Model) seeQuestions() {
	asked := map[question]time.Time{}
	for _, c := range m.st.Changes {
		if c.Question == nil {
			continue
		}
		q := question{change: c.ID, id: c.Question.ID}
		at, ok := m.asked[q]
		if !ok {
			at = m.now
		}
		asked[q] = at
	}
	m.asked = asked
}

// answerable reports whether y and n answer the question of c now: the
// handle offers the answer, and the question has shown for answerDelay.
func (m Model) answerable(c state.Change) bool {
	if !c.Controls.Answer || c.Question == nil {
		return false
	}
	at, ok := m.asked[question{change: c.ID, id: c.Question.ID}]
	return ok && m.now.Sub(at) >= answerDelay
}

// needsTick reports whether something on screen shows an elapsed time, or
// a notice waits to go away. A question waits for its keys on a change
// that runs, which ticks.
func (m Model) needsTick() bool {
	if m.noticeShows() {
		return true
	}
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
	return m.tick()
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

// tooSmall reports whether the terminal is below the least size, where
// the screen shows only the resize message.
func (m Model) tooSmall() bool { return m.w < minWidth || m.h < minHeight }

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

// say shows a notice. Its time starts at the next tick or state: m.now is
// as old as the last one, which is long ago when the UI is idle.
func (m *Model) say(s view.Status, format string, args ...any) {
	m.notice = fmt.Sprintf(format, args...)
	m.noticeStatus = s
	m.noticeTill = time.Time{}
}

// seeNotice starts the time of a notice that no tick or state has seen.
func (m *Model) seeNotice() {
	if m.notice != "" && m.noticeTill.IsZero() {
		m.noticeTill = m.now.Add(noticeFor)
	}
}

// noticeShows reports whether the notice is on screen.
func (m Model) noticeShows() bool {
	return m.notice != "" && (m.noticeTill.IsZero() || m.now.Before(m.noticeTill))
}

// progressStatus is the status of a notice about work in progress.
const progressStatus = view.Progress
