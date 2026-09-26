package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/sim"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// harness drives a Model against the simulated world. It runs every command
// the model returns, synchronously, and feeds the result back, the way the
// Bubble Tea runtime would, except the tick after each state fetch: tests
// fetch with refresh when they want a new frame.
type harness struct {
	t    *testing.T
	w    *sim.World
	m    Model
	quit bool
}

func newHarness(t *testing.T, w, h int, warmup time.Duration) *harness {
	t.Helper()
	world := sim.New(sim.Options{Seed: 7, Warmup: warmup})
	hs := &harness{t: t, w: world, m: New(t.Context(), world, time.Millisecond)}
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.refresh()
	return hs
}

// send delivers msg and runs the commands it produces.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if _, ok := m.(tea.QuitMsg); ok {
			h.quit = true
			continue
		}
		next, cmd := h.m.Update(m)
		h.m = next.(Model)
		if _, isState := m.(stateMsg); isState || cmd == nil {
			continue // after a state comes the next tick
		}
		queue = append(queue, cmd())
	}
}

// keys presses each key in turn.
func (h *harness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		next, cmd := h.m.key(k)
		h.m = next.(Model)
		if cmd != nil {
			h.send(cmd())
		}
	}
}

// refresh fetches a new state, as a tick would.
func (h *harness) refresh() { h.send(tickMsg{}) }

// advance runs the simulated world and fetches the new state.
func (h *harness) advance(d time.Duration) {
	h.w.Advance(d)
	h.refresh()
}

// text is the frame without styles.
func (h *harness) text() string {
	var b strings.Builder
	for _, l := range h.m.frame() {
		b.WriteString(l.Plain())
		b.WriteByte('\n')
	}
	return b.String()
}

func (h *harness) contains(want ...string) {
	h.t.Helper()
	txt := h.text()
	for _, s := range want {
		if !strings.Contains(txt, s) {
			h.t.Fatalf("frame lacks %q:\n%s", s, txt)
		}
	}
}

func (h *harness) lacks(bad ...string) {
	h.t.Helper()
	txt := h.text()
	for _, s := range bad {
		if strings.Contains(txt, s) {
			h.t.Fatalf("frame has %q:\n%s", s, txt)
		}
	}
}

// checkFrame asserts the frame is exactly w×h cells.
func checkFrame(t *testing.T, name string, m Model) {
	t.Helper()
	f := m.frame()
	if len(f) != m.h {
		t.Fatalf("%s: %d rows, want %d", name, len(f), m.h)
	}
	for i, l := range f {
		if got := l.Width(); got != m.w {
			t.Fatalf("%s: row %d is %d cells, want %d: %q", name, i, got, m.w, l.Plain())
		}
	}
	if rows := strings.Count(m.render(), "\n") + 1; rows != m.h {
		t.Fatalf("%s: rendered %d rows, want %d", name, rows, m.h)
	}
}

func TestEveryScreenFillsTheTerminalExactly(t *testing.T) {
	for _, size := range [][2]int{{minWidth, minHeight}, {160, 42}, {231, 64}} {
		h := newHarness(t, size[0], size[1], 19*time.Minute)
		h.keys("p")
		checkFrame(t, "fleet+confirm", h.m)
		h.keys("G")
		checkFrame(t, "confirm scrolled", h.m)
		h.keys("y")
		for _, k := range []string{"1", "2", "3", "4"} {
			h.keys(k)
			h.advance(3 * time.Second)
			checkFrame(t, "screen "+k, h.m)
			for range 4 {
				h.keys("tab")
				checkFrame(t, "screen "+k+" tab", h.m)
			}
		}
		h.keys("?")
		checkFrame(t, "help", h.m)
		h.keys("esc", "1", "U")
		checkFrame(t, "blocked upgrade dialog", h.m)
	}
}

func TestTooSmallTerminal(t *testing.T) {
	h := newHarness(t, 80, 20, 0)
	checkFrame(t, "small", h.m)
	h.contains("needs a 100×24 terminal (now 80×20)")
}

func TestLoadingFrameBeforeTheFirstState(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(Model)
	checkFrame(t, "loading", m)
	if !strings.Contains(m.frame()[2].Plain(), "loading the fleet") {
		t.Fatalf("no loading line: %q", m.frame()[2].Plain())
	}
}

func TestPatchFlowRunsARollToTheEnd(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.contains("▶ prod-api", "readiness not run")
	h.keys("p")
	if h.m.confirm == nil || h.m.confirm.Action.Kind != state.ActionRoll || h.m.confirm.Action.Nodegroup != "ng-general" {
		t.Fatalf("confirm = %+v", h.m.confirm)
	}
	h.contains("Patch nodegroup · prod-api / ng-general", "dry run · nothing changed yet", "refresh nodegroup update -c prod-api -n ng-general", "Start roll and watch")
	h.keys("y")
	if h.m.confirm != nil || h.m.screen != screenRolls {
		t.Fatalf("after y: confirm %v screen %d", h.m.confirm, h.m.screen)
	}
	h.advance(2 * time.Minute)
	h.contains("▸ prod-api / ng-general", "replaced", "Kube events", "health gates")
	sawPods := false
	for range 60 {
		h.advance(10 * time.Second)
		if strings.Contains(h.text(), "still evicting") {
			sawPods = true
			break
		}
	}
	if !sawPods {
		t.Fatalf("never showed the draining node's pods:\n%s", h.text())
	}
	for range 200 {
		h.advance(10 * time.Second)
		if strings.Contains(h.text(), "done in") {
			break
		}
	}
	h.contains("6/6 replaced", "done in")
	h.keys("1")
	h.contains("ng-general roll complete")
}

func TestBlockedUpgradeDoesNotStart(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("U")
	h.contains("blocked: 1 readiness blocker: deprecated APIs")
	h.lacks("Start upgrade")
	h.keys("y", "enter")
	if h.m.confirm == nil {
		t.Fatal("y or enter closed a blocked dialog")
	}
	if n := len(h.w.Snapshot().Upgrades); n != 0 {
		t.Fatalf("%d upgrades started", n)
	}
	h.keys("esc")
	if h.m.confirm != nil {
		t.Fatal("esc did not close the dialog")
	}
}

func TestEnterNeverConfirmsAChange(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("p", "enter")
	if h.m.confirm == nil || len(h.w.Snapshot().Rolls) != 0 {
		t.Fatal("enter started a change; only y may")
	}
}

func TestDialogScrollsOnAShortTerminal(t *testing.T) {
	h := newHarness(t, minWidth, minHeight, 0)
	h.keys("U") // the longest plan: every add-on and nodegroup
	// The verdict and the keys stay on screen without scrolling.
	h.contains("blocked: 1 readiness blocker", "esc", "Cancel", "more below")
	h.lacks("CLI equivalent")
	h.keys("G")
	h.contains("CLI equivalent  refresh cluster upgrade -c prod-api --to 1.32", "more above", "Cancel")
	h.lacks("more below")
	h.keys("g")
	h.contains("more below")
	h.lacks("more above")
	h.keys("pgdown", "pgdown", "pgdown", "pgdown")
	h.contains("CLI equivalent")
	for range 3 {
		checkFrame(t, "scrolled dialog", h.m)
		h.keys("k")
	}
}

func TestClusterScreenRunsReadiness(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("2")
	if _, ok := h.w.Snapshot().Readiness["prod-api"]; !ok {
		t.Fatal("opening the cluster screen did not start the checks")
	}
	h.advance(time.Second)
	h.contains("Readiness · prod-api 1.31 → 1.32", "checking")
	h.advance(time.Minute)
	h.contains("✗ blocked", "1 blocker", "CONTROL PLANE", "Check log")
	h.keys("down", "down", "down")
	h.contains("✗ deprecated APIs", "USER AGENT", "helm/v3.14.2", "Fix")
	h.keys("G")
	h.contains("▶ ● EC2 quota headroom")
	h.keys("g")
	h.contains("▶ ● version skew")
	// r on this screen runs the checks again.
	h.keys("r")
	if !h.w.Snapshot().Readiness["prod-api"].Running {
		t.Fatal("r did not run the checks again")
	}
}

func TestUpgradeScreenStopAndPause(t *testing.T) {
	h := newHarness(t, 160, 42, 19*time.Minute)
	h.keys("down", "down", "enter")
	if h.m.screen != screenUpgrade {
		t.Fatalf("enter on a busy cluster opened screen %d, want upgrade", h.m.screen)
	}
	h.contains("▸ Upgrade prod-eu 1.32 → 1.33", "Nodegroups", "Stop options", "Now")
	h.keys("P")
	h.contains("pause before next phase on")
	h.keys("S")
	h.contains("stop after the current step on")
	for range 200 {
		h.advance(10 * time.Second)
		if strings.Contains(h.text(), "▲ stopped") {
			break
		}
	}
	h.contains("▲ stopped", "a rerun resumes from live cluster state")
	// A finished upgrade offers no stop or pause keys.
	h.lacks(" S  stop after ")
	next, cmd := h.m.key("S")
	if cmd != nil || next.(Model).screen != screenUpgrade {
		t.Fatal("S did something on a finished upgrade")
	}
}

func TestPausedFeedHoldsStill(t *testing.T) {
	h := newHarness(t, 160, 42, 19*time.Minute)
	h.keys("space")
	before := h.text()
	if !strings.Contains(before, "frozen at") {
		t.Fatalf("no frozen marker:\n%s", before)
	}
	feed := func(s string) string {
		var rows []string
		for _, l := range strings.Split(s, "\n") {
			if i := strings.Index(l, "│"); i >= 0 {
				rows = append(rows, l[i:])
			}
		}
		return strings.Join(rows[:20], "\n")
	}
	// A second's worth of events lands in the same second as the pause.
	h.advance(time.Second)
	h.advance(3 * time.Minute)
	if feed(h.text()) != feed(before) {
		t.Fatal("the feed moved while paused")
	}
	h.keys("space")
	h.contains("● following")
}

func TestPauseIgnoresEventsWithTheSameTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	m := Model{st: state.State{Now: now, Seq: 2}}
	m.pausedSeq = 2
	evs := []state.Event{{Seq: 1, At: now}, {Seq: 2, At: now}, {Seq: 3, At: now}}
	if got := m.visible(evs, nil); len(got) != 2 || got[0].Seq != 2 {
		t.Fatalf("visible = %+v, want the two events up to seq 2", got)
	}
}

func TestAddonUpdateFromTheFleet(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("a")
	h.contains("Update add-ons · prod-api", "vpc-cni", "Start add-on update")
	h.keys("y")
	h.contains("add-on update started on prod-api")
	h.advance(5 * time.Second)
	h.contains("◷ updating")
}

func TestKeyBarShowsOnlyKeysThatWork(t *testing.T) {
	h := newHarness(t, 200, 42, 0)
	h.contains(" p  patch", " enter  open", " f  feed")
	h.lacks(" esc  back ")
	h.keys("3")
	// No roll yet: no action keys, no roll paging.
	h.contains("No nodegroup roll yet", " esc  back ")
	h.lacks(" p  patch", "[ ]")
	next, cmd := h.m.key("p")
	if cmd != nil || next.(Model).confirm != nil {
		t.Fatal("p did something on the rolls screen")
	}
	// Paging appears once there is more than one roll.
	h.keys("1", "p", "y")
	h.keys("1", "down", "down", "down", "down", "p", "enter", "y") // stage-data: pick the first stale nodegroup
	h.keys("3")
	h.contains(" [ ]  rolls")
	h.keys("[")
	if h.m.rollIdx != 0 {
		t.Fatalf("[ selected roll %d, want 0", h.m.rollIdx)
	}
}

func TestLogSourceKeysCycleBothWays(t *testing.T) {
	h := newHarness(t, 160, 42, 19*time.Minute)
	h.keys("4")
	for i, k := range []string{"tab", "right", "l", "tab"} {
		h.keys(k)
		if want := logSource((i + 1) % 4); h.m.src != want {
			t.Fatalf("after %s src = %d, want %d", k, h.m.src, want)
		}
	}
	h.keys("shift+tab")
	if h.m.src != logAll {
		t.Fatalf("shift+tab src = %d, want %d", h.m.src, logAll)
	}
	h.keys("left", "h")
	if h.m.src != logKube {
		t.Fatalf("left, h src = %d, want %d", h.m.src, logKube)
	}
}

func TestHelpListsThisScreensKeys(t *testing.T) {
	h := newHarness(t, 160, 42, 19*time.Minute)
	h.keys("4", "?")
	h.contains("on the Upgrade screen", "THIS SCREEN", "stop after this step", "log source", "ON EVERY SCREEN", "IN THE CONFIRM DIALOG", "IN ANY DIALOG")
	h.lacks("feed: all clusters")
	h.keys("q")
	if h.m.help || h.quit {
		t.Fatal("q in help should close help, not quit")
	}
}

func TestQuitKeys(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	if _, cmd := h.m.key("q"); cmd == nil {
		t.Fatal("q returned no command")
	}
	h.keys("?")
	if _, cmd := h.m.key("q"); cmd != nil {
		t.Fatal("q in the help dialog quit instead of closing it")
	}
	h.keys("esc", "p")
	if _, cmd := h.m.key("q"); cmd != nil {
		t.Fatal("q in the confirm dialog quit instead of cancelling")
	}
	if _, cmd := h.m.key("ctrl+c"); cmd == nil {
		t.Fatal("ctrl+c returned no command")
	}
}

// blockingBackend blocks every call until release is closed.
type blockingBackend struct {
	state.Backend
	release chan struct{}
}

func (b blockingBackend) State(ctx context.Context) (state.State, error) {
	select {
	case <-b.release:
		return b.Backend.State(ctx)
	case <-ctx.Done():
		return state.State{}, ctx.Err()
	}
}

func TestUpdateNeverCallsTheBackend(t *testing.T) {
	b := blockingBackend{Backend: sim.New(sim.Options{}), release: make(chan struct{})}
	m := New(t.Context(), b, time.Millisecond)
	// Each of these would hang if Update called State itself.
	next, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("a tick returned no fetch command")
	}
	next, _ = next.(Model).Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if _, quit := next.(Model).key("q"); quit == nil {
		t.Fatal("q did not quit while a fetch was pending")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	close(b.release)
	if msg, ok := (<-done).(stateMsg); !ok || msg.err != nil || len(msg.st.Clusters) == 0 {
		t.Fatalf("fetch = %+v", msg)
	}
}

func TestOlderStateIsDropped(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	newer := state.State{Now: time.Unix(200, 0)}
	older := state.State{Now: time.Unix(100, 0)}
	next, _ := m.Update(stateMsg{id: 2, st: newer})
	next, _ = next.(Model).Update(stateMsg{id: 1, st: older})
	if got := next.(Model).st.Now; !got.Equal(newer.Now) {
		t.Fatalf("state went back to %v", got)
	}
}

func TestBackendErrorsBecomeNotices(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	next, _ := m.Update(stateMsg{id: 1, err: errors.New("throttled")})
	next, _ = next.(Model).Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	got := next.(Model)
	if !strings.Contains(got.notice, "throttled") {
		t.Fatalf("notice = %q", got.notice)
	}
	next, _ = got.Update(startMsg{a: state.Action{Cluster: "x"}, err: errors.New("blocked: busy")})
	if next.(Model).confirmErr != "blocked: busy" {
		t.Fatalf("confirmErr = %q", next.(Model).confirmErr)
	}
}

func TestStartIsSentOnce(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("p")
	next, cmd := h.m.key("y")
	if cmd == nil || !next.(Model).starting {
		t.Fatal("y did not start")
	}
	if _, again := next.(Model).key("y"); again != nil {
		t.Fatal("a second y sent a second start")
	}
}

func TestFetchIDsAreNeverReused(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	first := m.Init()().(stateMsg).id
	next, cmd := m.Update(tickMsg{})
	second := cmd().(stateMsg).id
	_, cmd = next.(Model).Update(tickMsg{})
	third := cmd().(stateMsg).id
	if first >= second || second >= third {
		t.Fatalf("fetch ids %d, %d, %d; they must increase", first, second, third)
	}
}

func TestOnlyPollingRepliesScheduleATick(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	if _, cmd := m.Update(stateMsg{id: 1, poll: false}); cmd != nil {
		t.Fatal("a refresh after an action scheduled a tick: the polling loop would fork")
	}
	next, cmd := m.Update(stateMsg{id: 3, poll: true})
	if cmd == nil {
		t.Fatal("a polling reply scheduled no tick: the loop would stop")
	}
	// A polling reply dropped as stale still keeps the loop alive.
	if _, cmd := next.(Model).Update(stateMsg{id: 2, poll: true}); cmd == nil {
		t.Fatal("a stale polling reply stopped the loop")
	}
}

func TestOlderStateDoesNotConsumeTheFocusAfterStart(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	old := state.State{Clusters: []state.Cluster{{Name: "a"}}, Rolls: []state.Roll{{Cluster: "a", Nodegroup: "old", EndedAt: time.Unix(1, 0)}}}
	next, _ := m.Update(stateMsg{id: 1, st: old})
	m = next.(Model)
	// Start returns while an older fetch (id 2) is still in flight.
	m.fetchID = 2
	next, cmd := m.Update(startMsg{a: state.Action{Kind: state.ActionRoll, Cluster: "a"}})
	m = next.(Model)
	after := cmd().(stateMsg).id
	next, _ = m.Update(stateMsg{id: 2, st: old})
	m = next.(Model)
	if m.focusAfter == nil {
		t.Fatal("a state fetched before the start consumed the focus")
	}
	newer := old
	newer.Rolls = append(newer.Rolls, state.Roll{Cluster: "a", Nodegroup: "new"})
	next, _ = m.Update(stateMsg{id: after, st: newer})
	m = next.(Model)
	if r, _ := m.roll(); r.Nodegroup != "new" || m.screen != screenRolls {
		t.Fatalf("focused %q on screen %d, want the new roll on the rolls screen", r.Nodegroup, m.screen)
	}
}

func TestOnlyTheNewestPlanOpens(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	// Ask for two dry runs; the older reply arrives last.
	next, older := h.m.key("p")
	h.m = next.(Model)
	next, newer := h.m.key("a")
	h.m = next.(Model)
	h.send(newer())
	h.send(older())
	if h.m.confirm == nil || h.m.confirm.Action.Kind != state.ActionAddons {
		t.Fatalf("confirm = %+v, want the add-on plan asked for last", h.m.confirm)
	}
}

func TestLongErrorKeepsTheDialogKeysOnScreen(t *testing.T) {
	h := newHarness(t, minWidth, minHeight, 0)
	h.keys("p")
	h.m.confirmErr = strings.Repeat("backend failure ", 120)
	h.contains("Cancel", "Start roll and watch", "full text below")
	checkFrame(t, "long error", h.m)
	h.keys("G")
	h.contains("more above", "backend failure backend failure", "Cancel")
	h.lacks("more below")
}

func TestKeyBarKeepsEveryKeyAtMinimumWidth(t *testing.T) {
	h := newHarness(t, minWidth, minHeight, 0)
	bar := h.m.frame()[minHeight-1].Plain()
	for _, b := range h.m.barBindings() {
		if !strings.Contains(bar, " "+b.label+" ") {
			t.Errorf("key bar lacks %q at %d columns: %q", b.label, minWidth, bar)
		}
	}
	for _, k := range []string{" ? ", " q "} {
		if !strings.Contains(bar, k) {
			t.Errorf("key bar lacks %q: %q", k, bar)
		}
	}
}

func TestCancelIsOffWhileAChangeStarts(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("p")
	next, cmd := h.m.key("y")
	h.m = next.(Model)
	h.contains("starting… the result shows here")
	h.lacks("Cancel")
	for _, k := range []string{"esc", "n", "q"} {
		next, _ := h.m.key(k)
		if next.(Model).confirm == nil {
			t.Fatalf("%s closed the dialog while the change was starting", k)
		}
	}
	h.send(cmd())
	if h.m.confirm != nil || h.m.screen != screenRolls {
		t.Fatal("the start result did not close the dialog and open the roll")
	}
}

func TestFrozenFeedSurvivesEviction(t *testing.T) {
	ev := func(seq uint64, text string) state.Event {
		return state.Event{Seq: seq, At: time.Unix(int64(seq), 0), Cluster: "a", Text: text}
	}
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	m.w, m.h = 160, 42
	next, _ := m.Update(stateMsg{id: 1, st: state.State{Seq: 2, Clusters: []state.Cluster{{Name: "a"}}, Feed: []state.Event{ev(1, "first"), ev(2, "second")}}})
	next, _ = next.(Model).key("space")
	// The backend's capped feed has since dropped both events.
	next, _ = next.(Model).Update(stateMsg{id: 2, st: state.State{Seq: 4, Clusters: []state.Cluster{{Name: "a"}}, Feed: []state.Event{ev(3, "third"), ev(4, "fourth")}}})
	got := next.(Model).visible(next.(Model).feed().Feed, nil)
	if len(got) != 2 || got[0].Text != "second" || got[1].Text != "first" {
		t.Fatalf("frozen feed = %+v, want second and first", got)
	}
}

func TestSelectionFollowsTheClusterNotTheRow(t *testing.T) {
	m := New(t.Context(), sim.New(sim.Options{}), time.Millisecond)
	cs := func(names ...string) []state.Cluster {
		var out []state.Cluster
		for _, n := range names {
			out = append(out, state.Cluster{Name: n})
		}
		return out
	}
	next, _ := m.Update(stateMsg{id: 1, st: state.State{Clusters: cs("a", "b", "c")}})
	next, _ = next.(Model).key("down")
	if got := next.(Model).cluster().Name; got != "b" {
		t.Fatalf("selected %q, want b", got)
	}
	// A cluster appears above the selection.
	next, _ = next.(Model).Update(stateMsg{id: 2, st: state.State{Clusters: cs("0", "a", "b", "c")}})
	if got := next.(Model).cluster().Name; got != "b" {
		t.Fatalf("selection moved to %q when a cluster was added above it", got)
	}
}

func TestEscCancelsAPendingPlan(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	next, pending := h.m.key("p")
	h.m = next.(Model)
	h.keys("esc")
	h.send(pending())
	if h.m.confirm != nil {
		t.Fatal("a dry run answered after esc opened its dialog")
	}
}

func TestNavigationAfterStartIsNotOverridden(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("p")
	next, start := h.m.key("y")
	h.m = next.(Model)
	h.send(start()) // the start reply fetches a state and focuses the roll
	h.keys("2")
	h.refresh()
	if h.m.screen != screenCluster {
		t.Fatalf("screen %d after the user moved to the cluster screen", h.m.screen)
	}
	// The same, with the state landing after the user moved.
	h.keys("1", "down", "down", "down", "down", "p", "enter")
	next, start = h.m.key("y")
	h.m = next.(Model)
	next, fetch := h.m.Update(start())
	h.m = next.(Model)
	h.keys("2")
	h.send(fetch())
	if h.m.screen != screenCluster {
		t.Fatalf("a late state pulled the user from the cluster screen to %d", h.m.screen)
	}
}

func TestFrozenFeedExplainsAChangeStartedLater(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("space", "p", "y")
	h.keys("3")
	h.contains("this roll started after the freeze · space to follow")
}

func TestHelpOmitsKeysThatDoNothingHere(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("?")
	h.lacks("back to the fleet")
	h.keys("esc", "2", "?")
	h.contains("back to the fleet")
}

func TestPickerChoosesAmongStaleNodegroups(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("down", "down", "down", "down", "p") // stage-data: ng-stream and ng-spot are stale
	if h.m.pick == nil || len(h.m.pick.items) != 2 {
		t.Fatalf("picker = %+v", h.m.pick)
	}
	h.contains("Patch which nodegroup?", "ng-stream", "ng-spot")
	checkFrame(t, "picker", h.m)
	h.keys("down", "enter")
	if h.m.confirm == nil || h.m.confirm.Action.Nodegroup != "ng-spot" {
		t.Fatalf("confirm = %+v, want a plan for ng-spot", h.m.confirm)
	}
	h.keys("esc", "p", "1")
	if h.m.confirm == nil || h.m.confirm.Action.Nodegroup != "ng-stream" {
		t.Fatalf("1 in the picker planned %+v, want ng-stream", h.m.confirm)
	}
	h.keys("esc", "p", "esc")
	if h.m.pick != nil || h.m.confirm != nil {
		t.Fatal("esc did not close the picker")
	}
}

func TestPendingPlanSurvivesKeysThatKeepTheTarget(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	next, pending := h.m.key("p")
	h.m = next.(Model)
	h.contains("planning a patch of prod-api/ng-general…")
	h.keys("space", "w", "x") // freeze, warnings only, an unbound key
	h.send(pending())
	if h.m.confirm == nil {
		t.Fatal("keys that keep the target cancelled the dry run")
	}
}

func TestMovingTheSelectionCancelsThePlanAndTheFocus(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	next, pending := h.m.key("p")
	h.m = next.(Model)
	h.keys("down")
	h.send(pending())
	if h.m.confirm != nil {
		t.Fatal("a dry run for the old selection opened after the user moved")
	}
	h.contains("dry run for a patch of prod-api/ng-general cancelled")
	// Start a change, move the selection before its state lands.
	h.keys("up", "p")
	next, start := h.m.key("y")
	h.m = next.(Model)
	next, fetch := h.m.Update(start())
	h.m = next.(Model)
	h.keys("down")
	h.send(fetch())
	if h.m.screen != screenFleet {
		t.Fatalf("a late state pulled the user to screen %d after they moved the selection", h.m.screen)
	}
}

func TestAddonUpdateHintDoesNotPromiseAWatchScreen(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("a", "y")
	h.advance(2 * time.Second)
	h.contains("updating add-ons  progress in the live feed")
	h.lacks("enter to watch")
}

func TestNoPlanOpensBehindThePicker(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("down", "down", "down", "down") // stage-data
	next, pending := h.m.key("a")
	h.m = next.(Model)
	h.keys("p") // opens the picker, which cancels the add-on dry run
	h.send(pending())
	h.keys("esc")
	if h.m.confirm != nil {
		t.Fatal("a dry run answered behind the picker opened after it closed")
	}
}

func TestPickerKeepsItsSelectionOnScreen(t *testing.T) {
	h := newHarness(t, minWidth, minHeight, 0)
	var items []state.Nodegroup
	for i := range 20 {
		items = append(items, state.Nodegroup{Name: fmt.Sprintf("ng-%02d", i), Version: "1.31", AMI: "old", LatestAMI: "new"})
	}
	h.m.pick = &picker{cluster: "prod-api", items: items}
	for range 15 {
		h.keys("down")
	}
	h.contains("▶ 16 ng-15")
	checkFrame(t, "scrolled picker", h.m)
	for range 15 {
		h.keys("up")
	}
	h.contains("▶ 1  ng-00")
}

func TestPlanningNoticeLastsWhileThePlanIsPending(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	next, _ := h.m.key("p")
	h.m = next.(Model)
	h.advance(time.Minute)
	h.contains("planning a patch of prod-api/ng-general… · esc cancels")
}

func TestAPlanRequestDropsThePendingFocus(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	h.keys("p")
	next, start := h.m.key("y")
	h.m = next.(Model)
	next, fetch := h.m.Update(start())
	h.m = next.(Model)
	next, _ = h.m.key("a") // ask for another dry run before the state lands
	h.m = next.(Model)
	h.send(fetch())
	if h.m.screen != screenFleet {
		t.Fatalf("the post-start state moved the screen to %d under a pending dry run", h.m.screen)
	}
}

func TestResizeKeepsThePickerSelectionVisible(t *testing.T) {
	h := newHarness(t, minWidth, 42, 0)
	var items []state.Nodegroup
	for i := range 20 {
		items = append(items, state.Nodegroup{Name: fmt.Sprintf("ng-%02d", i), Version: "1.31", AMI: "old", LatestAMI: "new"})
	}
	h.m.pick = &picker{cluster: "prod-api", items: items}
	for range 15 {
		h.keys("down")
	}
	h.send(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
	h.contains("▶ 16 ng-15")
}

type refreshingBackend struct {
	state.Backend
	refreshed int
}

func (r *refreshingBackend) Refresh() { r.refreshed++ }

func TestCtrlRRefreshesABackendThatCan(t *testing.T) {
	h := newHarness(t, 160, 42, 0)
	next, cmd := h.m.key("ctrl+r")
	if cmd != nil || next.(Model).notice != "" {
		t.Fatal("ctrl+r did something on a backend with no Refresh")
	}
	rb := &refreshingBackend{Backend: h.w}
	h.m.b = rb
	h.keys("ctrl+r", "?")
	if rb.refreshed != 1 {
		t.Fatalf("Refresh called %d times, want 1", rb.refreshed)
	}
	h.contains("read the fleet from AWS now")
}

// askingBackend puts a question on the running upgrade and records answers.
type askingBackend struct {
	*sim.World
	answers []bool
}

func (a *askingBackend) State(ctx context.Context) (state.State, error) {
	st, err := a.World.State(ctx)
	for i := range st.Upgrades {
		if st.Upgrades[i].Running() && len(a.answers) == 0 {
			st.Upgrades[i].Question = "health warnings before rolling ng-general: PodDisruptionBudgets (Warn)"
		}
	}
	return st, err
}

func (a *askingBackend) Answer(_ context.Context, _ string, yes bool) error {
	a.answers = append(a.answers, yes)
	return nil
}

func TestUpgradeQuestionIsAnsweredWithYOrN(t *testing.T) {
	world := sim.New(sim.Options{Seed: 7, Warmup: 19 * time.Minute})
	ab := &askingBackend{World: world}
	h := &harness{t: t, w: world, m: New(t.Context(), ab, time.Millisecond)}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 42})
	h.refresh()
	h.keys("down", "down", "enter")
	h.contains("health warnings before rolling ng-general", " y  go on", " n  stop")
	checkFrame(t, "question", h.m)
	h.keys("n")
	if len(ab.answers) != 1 || ab.answers[0] {
		t.Fatalf("answers = %v, want one no", ab.answers)
	}
	h.refresh()
	h.lacks("health warnings before rolling")
	// With no question pending, y and n do nothing on the upgrade screen.
	if _, cmd := h.m.key("y"); cmd != nil {
		t.Fatal("y did something with no question")
	}
}

// emptyFleet is a world whose sweep found no clusters, as in an account
// with no EKS clusters.
type emptyFleet struct{ *sim.World }

func (e emptyFleet) State(ctx context.Context) (state.State, error) {
	st, err := e.World.State(ctx)
	st.Clusters, st.Rolls, st.Upgrades, st.Feed = nil, nil, nil, nil
	return st, err
}

func TestAnEmptyFleetSaysSoAndSurvivesEveryKey(t *testing.T) {
	world := sim.New(sim.Options{Seed: 7})
	h := &harness{t: t, w: world, m: New(t.Context(), emptyFleet{world}, time.Millisecond)}
	h.send(tea.WindowSizeMsg{Width: 120, Height: 36})
	h.refresh()
	h.contains("0 clusters", "No EKS clusters in the regions swept.", "all clusters")
	// f scopes the feed to the selected cluster; with none it stays on all.
	h.keys("f")
	h.contains("all clusters")
	for _, k := range []string{"2", "3", "4", "1", "enter", "j", "k", "r", "u", "a", "U", "p", "y", "n", "S", "P", "space", "?", "esc"} {
		h.keys(k)
		h.refresh()
	}
	h.keys("1")
	h.contains("No EKS clusters in the regions swept.")
}

// closedFleet is a world in which no region answered.
type closedFleet struct{ emptyFleet }

func (c closedFleet) State(ctx context.Context) (state.State, error) {
	st, err := c.emptyFleet.State(ctx)
	st.RegionsAnswered, st.RegionsTotal = 0, 1
	st.FleetProblem = "no region answered, but STS in us-east-1 accepts these credentials\nAWS: UnrecognizedClientException"
	return st, err
}

func TestNoRegionAnsweredSaysWhyNotEmpty(t *testing.T) {
	world := sim.New(sim.Options{Seed: 7})
	h := &harness{t: t, w: world, m: New(t.Context(), closedFleet{emptyFleet{world}}, time.Millisecond)}
	h.send(tea.WindowSizeMsg{Width: 120, Height: 36})
	h.refresh()
	h.contains("no region answered, but STS in us-east-1 accepts these credentials", "AWS: UnrecognizedClientException")
	h.lacks("No EKS clusters in the regions swept.")
}

// busyFleet is a world whose only cluster is being upgraded from elsewhere.
type busyFleet struct{ *sim.World }

func (b busyFleet) State(ctx context.Context) (state.State, error) {
	st, err := b.World.State(ctx)
	st.Clusters = []state.Cluster{{Name: "prod-api", Region: "us-east-1", Version: "1.35", Latest: "1.36", Busy: "upgrading"}}
	st.Rolls, st.Upgrades = nil, nil
	return st, err
}

// A cluster that is changing (here, an upgrade the CLI started) says so on
// p, a, and U instead of opening a dry run against a moving cluster.
func TestChangeKeysOnABusyClusterSayWhy(t *testing.T) {
	world := sim.New(sim.Options{Seed: 7})
	h := &harness{t: t, w: world, m: New(t.Context(), busyFleet{world}, time.Millisecond)}
	h.send(tea.WindowSizeMsg{Width: 120, Height: 36})
	h.refresh()
	h.contains("p a U wait until the change finishes")
	for _, k := range []string{"p", "a", "U"} {
		h.m.notice = ""
		h.keys(k)
		if h.m.confirm != nil || h.m.pick != nil {
			t.Fatalf("%s opened a dialog on a busy cluster", k)
		}
		if !strings.Contains(h.m.notice, "prod-api is busy: upgrading · changes wait until it finishes") {
			t.Fatalf("%s: notice = %q", k, h.m.notice)
		}
	}
}
