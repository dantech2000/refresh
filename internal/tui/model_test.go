package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

func TestEveryScreenFillsTheTerminalExactly(t *testing.T) {
	for _, th := range []view.Theme{colorTheme, asciiTheme} {
		for _, size := range [][2]int{{minWidth, minHeight}, {120, 40}, {160, 42}, {231, 64}} {
			h := newHarness(t, size[0], size[1], th)
			name := func(step string) string {
				return fmt.Sprintf("%s at %dx%d, unicode %v", step, size[0], size[1], th.Unicode)
			}
			checkFrame(t, name("loading"), h.m)
			h.state(fleetState(12))
			checkFrame(t, name("fleet"), h.m)
			for _, k := range []string{"down", "down", "down", "G", "g", "f", "enter", "down", "down", "2", "?", "G", "esc", "1", "esc", "down", "down", "down", "down", "down", "enter"} {
				h.keys(k)
				checkFrame(t, name("after "+k), h.m)
			}
			h.tick(now.Add(time.Second))
			checkFrame(t, name("tick"), h.m)
			h.update(tea.WindowSizeMsg{Width: 80, Height: 20})
			checkFrame(t, name("small"), h.m)
			h.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			checkFrame(t, name("resized back"), h.m)
			h.state(state.State{Version: 2, Now: now, Sweep: state.Sweep{Regions: 1, StartedAt: now}})
			checkFrame(t, name("empty, sweeping"), h.m)
			h.state(state.State{Version: 3, Now: now, Sweep: state.Sweep{Regions: 1, StartedAt: now, EndedAt: now, Count: 1, Error: "no region answered: the credentials are not valid\nCheck AWS_PROFILE"}})
			checkFrame(t, name("empty, failed"), h.m)
			h.keys("2", "?")
			checkFrame(t, name("empty cluster screen with help"), h.m)
		}
	}
}

func TestTooSmallTerminal(t *testing.T) {
	h := newHarness(t, 80, 20, colorTheme)
	h.state(fleetState(3))
	h.contains("needs a 100×24 terminal (now 80×20)")
	checkFrame(t, "small", h.m)
}

func TestLoadingFrameBeforeTheFirstState(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.contains("loading the fleet")
	h.keys("down", "enter", "2", "r")
	if len(h.b.readiness) != 0 {
		t.Fatal("a key before the first state asked the backend for something")
	}
}

// Update never calls the backend itself: every call runs in a command.
func TestUpdateNeverCallsTheBackend(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(3))
	for _, k := range []string{"down", "enter", "esc", "ctrl+r", "?", "esc"} {
		next, _ := h.m.key(k)
		h.m = next
	}
	if h.b.nextCalls != 0 || len(h.b.readiness) != 0 {
		t.Fatalf("Update called the backend: %d Next, %d Readiness", h.b.nextCalls, len(h.b.readiness))
	}
}

// Idle, the model schedules no tick; while a sweep or a readiness run
// shows an elapsed time, it ticks once a second.
func TestTickOnlyWhileSomethingElapses(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(3)) // sweep ended, no readiness running
	if h.m.ticking {
		t.Fatal("an idle fleet scheduled a tick")
	}
	st := fleetState(3)
	st.Sweep.EndedAt = time.Time{}
	h.state(st)
	if !h.m.ticking {
		t.Fatal("a sweep in progress scheduled no tick")
	}
	h.tick(now.Add(time.Second))
	if !h.m.ticking {
		t.Fatal("the tick did not reschedule while the sweep goes on")
	}
	h.state(fleetState(3))
	h.tick(now.Add(2 * time.Second))
	if h.m.ticking {
		t.Fatal("the tick rescheduled after the sweep ended")
	}
	st = fleetState(7) // c-006 has a readiness run in progress
	h.state(st)
	if !h.m.ticking {
		t.Fatal("a readiness run scheduled no tick")
	}
}

func TestEnterOpensTheClusterAndRunsReadiness(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	h.keys("down", "enter")
	if h.m.screen != screenCluster {
		t.Fatal("enter did not open the cluster screen")
	}
	if len(h.b.readiness) != 1 || h.b.readiness[0] != (state.Key{Region: "eu-west-1", Name: "c-001"}) {
		t.Fatalf("readiness asked for %v, want c-001 in eu-west-1", h.b.readiness)
	}
	h.contains("Readiness", "c-001", "Nodegroups", "api", "web", "Add-ons", "coredns")
	h.keys("esc")
	if h.m.screen != screenFleet {
		t.Fatal("esc did not go back to the fleet")
	}
}

// A cluster with a readiness run is opened without a new run; r runs one.
func TestReadinessKeyRunsTheChecks(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(7))
	h.keys("G", "up", "enter") // c-005, with a report
	if len(h.b.readiness) != 0 {
		t.Fatal("enter ran the checks of a cluster that has a report")
	}
	h.contains("review", "Insights", "VersionSkew", "ControlPlane", "DEPRECATED_APIS", "Deprecated APIs removed")
	h.keys("r")
	if len(h.b.readiness) != 1 {
		t.Fatal("r did not run the checks")
	}
	h.keys("1", "r")
	if len(h.b.readiness) != 2 {
		t.Fatal("r on the fleet screen did not run the checks")
	}
}

func TestReadinessKeyNeedsACluster(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(state.State{Version: 1, Now: now, Sweep: state.Sweep{Regions: 1, EndedAt: now, Count: 1}})
	h.keys("r", "enter", "2", "r", "down", "G", "f", "esc", "?", "esc", "1")
	if len(h.b.readiness) != 0 {
		t.Fatal("a key without a cluster asked for readiness")
	}
	h.contains("No EKS clusters in the regions swept")
	checkFrame(t, "empty", h.m)
}

func TestBackendErrorsBecomeNotices(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.b.readinessErr = errors.New("the readiness checks of this cluster are running")
	h.state(fleetState(3))
	h.keys("r")
	h.contains("the readiness checks of this cluster are running")
	// The notice goes away a while after the first tick that saw it; the
	// log strip shows the feed.
	h.tick(now.Add(time.Minute))
	h.contains("the readiness checks of this cluster are running")
	h.tick(now.Add(time.Minute + noticeFor))
	h.lacks("checks of this cluster are running")
	h.contains("readiness: review")
}

// Found in review: a notice expired against the clock of the last tick or
// state, which is old when the UI is idle, so the next state hid an error
// at once, and nothing ticked to hide a notice when no state came. The
// time of a notice starts at the first tick or state after it, and the
// key that says it starts the clock.
func TestNoticeOutlivesAStaleClock(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"an error of a key", tea.KeyPressMsg{Code: 'r', Text: "r"}, "the readiness checks of this cluster are running"},
		{"ctrl+r, whose sweep publishes a state at once", tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}, "reading the fleet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 120, 40, colorTheme)
			h.b.readinessErr = errors.New("the readiness checks of this cluster are running")
			h.state(fleetState(3)) // idle: nothing ticks, the clock stays at now
			if h.m.ticking {
				t.Fatal("an idle fleet scheduled a tick")
			}
			h.update(tc.key)
			h.contains(tc.want)
			if !h.m.ticking {
				t.Fatal("a notice scheduled no tick to end it")
			}
			// A state 45 seconds on, the first since the notice.
			st := fleetState(3)
			st.Version, st.Now = 2, now.Add(45*time.Second)
			h.state(st)
			h.contains(tc.want)
			h.tick(now.Add(45*time.Second + noticeFor - time.Second))
			h.contains(tc.want)
			h.tick(now.Add(45*time.Second + noticeFor))
			h.lacks(tc.want)
			if h.m.ticking {
				t.Fatal("the clock went on after the notice ended")
			}
		})
	}
}

func TestSelectionFollowsTheClusterNotTheRow(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(4))
	h.keys("down", "down") // c-002
	st := fleetState(4)
	st.Clusters = append([]state.Cluster{cluster("a-000", "eu-west-1", "1.33")}, st.Clusters...)
	h.state(st)
	if got := h.m.cluster().Name; got != "c-002" {
		t.Fatalf("selected %q after a row was inserted above, want c-002", got)
	}
}

func TestKeyBarShowsOnlyKeysThatWork(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	bar := h.m.keyBar(120).Plain()
	for _, want := range []string{" enter ", " f ", " r ", " ? ", " q "} {
		if !strings.Contains(bar, want) {
			t.Errorf("fleet key bar lacks %q: %q", want, bar)
		}
	}
	if strings.Contains(bar, " esc ") {
		t.Errorf("fleet key bar has esc: %q", bar)
	}
	h.keys("enter")
	bar = h.m.keyBar(120).Plain()
	if !strings.Contains(bar, " esc ") || strings.Contains(bar, " f ") || strings.Contains(bar, " enter ") {
		t.Errorf("cluster key bar: %q", bar)
	}
}

// At the minimum width every key stays on the bar; words go first.
func TestKeyBarKeepsEveryKeyAtMinimumWidth(t *testing.T) {
	h := newHarness(t, minWidth, minHeight, colorTheme)
	h.state(fleetState(3))
	bar := h.m.keyBar(minWidth).Plain()
	for _, b := range h.m.barBindings() {
		if !strings.Contains(bar, " "+b.label+" ") {
			t.Errorf("key %q fell off the bar: %q", b.label, bar)
		}
	}
	if !strings.Contains(bar, "readiness") {
		t.Errorf("the action key lost its word first: %q", bar)
	}
}

func TestHelpListsThisScreensKeys(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	h.keys("?")
	h.contains("THIS SCREEN", "ON EVERY SCREEN", "open the cluster", "run the readiness checks", "Read-only")
	h.lacks("back to the fleet")
	h.keys("esc")
	if h.m.help {
		t.Fatal("esc did not close the help")
	}
	h.keys("enter", "?")
	h.contains("back to the fleet")
	h.lacks("open the cluster")
	h.keys("q")
	if h.m.help || h.quit {
		t.Fatal("q in the help must close it, not quit")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		h := newHarness(t, 100, 24, colorTheme)
		h.state(fleetState(3))
		h.keys(k)
		if !h.quit {
			t.Errorf("%s did not quit", k)
		}
	}
}

// Found in review: q quit at once while a change of this UI ran, which
// stops it before its next step. The first q says so, the second quits,
// and any other key keeps the UI. ctrl+c quits at once. A change started
// elsewhere is not stopped by quitting, and does not ask.
func TestQuitAsksWhileAChangeOfThisUIRuns(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(writable(fleetState(3)), upgradeChange(4, false)))
	h.keys("q")
	if h.quit {
		t.Fatal("one q quit while an upgrade of this UI runs")
	}
	h.contains("upgrade of c-000 runs from this UI: q again quits")
	h.keys("down", "q")
	if h.quit {
		t.Fatal("a q after another key quit without asking again")
	}
	h.keys("q")
	if !h.quit {
		t.Fatal("the second q did not quit")
	}

	h = newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(writable(fleetState(3)), upgradeChange(4, false)))
	h.keys("ctrl+c")
	if !h.quit {
		t.Fatal("ctrl+c did not quit at once")
	}

	h = newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(writable(fleetState(3)), upgradeChange(4, true), finishedChange(5)))
	h.keys("q")
	if !h.quit {
		t.Fatal("q asked with only a change started elsewhere and a finished one")
	}
}

// Found in review: keys acted while the screen showed only the resize
// message, so y could start a change or accept a health-gate warning
// that the user could not see. Below the least size only q and ctrl+c
// work.
func TestKeysDoNothingOnTheResizeScreen(t *testing.T) {
	small := tea.WindowSizeMsg{Width: 80, Height: 20}
	big := tea.WindowSizeMsg{Width: 160, Height: 42}

	// A dry run on screen: y starts nothing, esc does not close it.
	h := newHarness(t, 160, 42, colorTheme)
	h.state(writable(fleetState(3)))
	h.b.proposal = proposalOf(5, state.Action{Kind: state.ActionAddons, Cluster: h.m.cluster().Key}, finishedChange(0).Plan)
	h.keys("a")
	if h.m.confirm == nil {
		t.Fatal("a did not open the dry run")
	}
	h.update(small)
	h.keys("y", "esc", "n")
	if len(h.b.starts) != 0 || h.m.confirm == nil {
		t.Fatalf("keys on the resize screen: starts %v, dialog open %v", h.b.starts, h.m.confirm != nil)
	}

	// A health-gate question: y and n answer nothing; S and P steer nothing.
	h = newHarness(t, 160, 42, colorTheme)
	h.state(withChanges(writable(fleetState(3)), rollChange(5, true)))
	h.keys("3")
	h.tick(now.Add(answerDelay))
	h.update(small)
	h.keys("y", "n", "S", "P", "1", "p", "a", "U", "ctrl+r")
	if len(h.b.calls) != 0 || len(h.b.plans) != 0 || h.b.refreshed != 0 || h.m.screen != screenChange {
		t.Fatalf("keys on the resize screen: calls %v, plans %v, refreshed %d, screen %d", h.b.calls, h.b.plans, h.b.refreshed, h.m.screen)
	}
	h.update(big)
	h.keys("y")
	if want := []string{"answer 5 3 true"}; !slices.Equal(h.b.calls, want) {
		t.Fatalf("y at full size: calls %v, want %v", h.b.calls, want)
	}

	// q works, and its question shows on the resize screen.
	h = newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(writable(fleetState(3)), upgradeChange(4, false)))
	h.update(small)
	h.keys("q")
	h.contains("needs a 100×24 terminal", "q again quits")
	h.keys("q")
	if !h.quit {
		t.Fatal("q on the resize screen did not quit")
	}
}

func TestCtrlRRefreshes(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(3))
	h.keys("ctrl+r")
	if h.b.refreshed != 1 {
		t.Fatalf("Refresh called %d times, want 1", h.b.refreshed)
	}
	h.contains("reading the fleet")
}

// Found at fleet scale in the old TUI: the list did not scroll, so the
// selection left the screen.
func TestFleetTableKeepsTheSelectionInView(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(200))
	h.keys("G")
	h.contains("▶ c-199", "above")
	h.keys("g")
	h.contains("▶ c-000", "below")
	h.lacks("c-199")
}

// Found at fleet scale in the old TUI: a cut name ran into the next
// column. A cut cell ends in the ellipsis, which must be followed by a
// space or end the row.
func TestFleetCellsKeepAGap(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	st := fleetState(2)
	st.Clusters[0].Name = "a-very-long-cluster-name-that-does-not-fit-in-its-column"
	h.state(st)
	txt := h.text()
	if !strings.Contains(txt, "…") {
		t.Fatalf("the long name was not cut:\n%s", txt)
	}
	for _, line := range strings.Split(txt, "\n") {
		if !strings.Contains(line, "us-east-1") && !strings.Contains(line, "eu-west-1") {
			continue // a table row; the card cuts its title against its border
		}
		if i := strings.Index(line, "…"); i >= 0 && i+len("…") < len(line) && line[i+len("…")] != ' ' {
			t.Fatalf("a cut cell runs into the next: %q", line)
		}
	}
}

// Found at fleet scale in the old TUI: before the first sweep ended the
// bar said "synced 2562047h ago".
func TestBeforeTheFirstSweep(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(state.State{Version: 1, Now: now, Sweep: state.Sweep{Regions: 3, StartedAt: now.Add(-4 * time.Second)}})
	h.contains("sweeping 4s", "Sweeping the regions for EKS clusters", "regions 0/3")
	h.lacks("synced")
}

func TestNoRegionAnsweredSaysWhyNotEmpty(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(state.State{Version: 1, Now: now, Sweep: state.Sweep{Regions: 1, EndedAt: now, Count: 1, Error: "no region answered: the credentials are not valid"}})
	h.contains("the credentials are not valid")
	h.lacks("No EKS clusters")
}

// From review of the old TUI: with color off the UI still set the
// terminal's background.
func TestNoColorLeavesTheBackgroundAlone(t *testing.T) {
	h := newHarness(t, 100, 24, asciiTheme)
	h.state(fleetState(3))
	if h.m.View().BackgroundColor != nil {
		t.Fatal("a theme without color set the background")
	}
	if strings.Contains(h.m.render(), "\x1b[") {
		t.Fatal("a theme without color rendered escape codes")
	}
	h = newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(3))
	if h.m.View().BackgroundColor == nil {
		t.Fatal("a theme with color left the background alone")
	}
}

// Without Unicode the frame draws ASCII borders and marks, and the
// ellipsis is one ASCII cell.
func TestASCIIFrameHasNoUnicode(t *testing.T) {
	h := newHarness(t, 100, 24, asciiTheme)
	st := fleetState(4)
	st.Clusters[0].Name = "a-very-long-cluster-name-that-does-not-fit-in-its-column"
	h.state(st)
	h.keys("?")
	for _, r := range h.m.render() {
		if r > 127 && r != '×' {
			t.Fatalf("the ASCII frame has %q", r)
		}
	}
}

// A finished sweep redraws the whole screen, not for the first state and
// not more than once per minRedraw.
func TestSweepRedrawsTheScreen(t *testing.T) {
	h := newHarness(t, 100, 24, colorTheme)
	h.state(fleetState(3))
	if !h.m.redrawnAt.IsZero() {
		t.Fatal("the first state redrew the screen")
	}
	st := fleetState(3)
	st.Sweep.Count = 2
	st.Now = now.Add(time.Minute)
	h.tick(now.Add(time.Minute))
	h.state(st)
	if h.m.redrawnAt.IsZero() {
		t.Fatal("the second sweep did not redraw the screen")
	}
	first := h.m.redrawnAt
	st.Sweep.Count = 3
	h.state(st)
	if h.m.redrawnAt != first {
		t.Fatal("a sweep within minRedraw redrew the screen again")
	}
}

func TestSmallTerminalKeepsTheState(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	h.keys("down", "enter")
	h.update(tea.WindowSizeMsg{Width: 40, Height: 10})
	h.contains("needs a 100×24 terminal")
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if h.m.screen != screenCluster || h.m.cluster().Name != "c-001" {
		t.Fatal("a resize lost the screen or the selection")
	}
}

// panickyRefresh is a backend whose Refresh, which Update calls, panics.
type panickyRefresh struct{ *scripted }

func (panickyRefresh) Refresh() { panic("refresh broke") }

// A panic in Update is recorded with its stack before Bubble Tea
// recovers it, and the model still panics: Bubble Tea ends the program.
func TestUpdateRecordsAPanic(t *testing.T) {
	m := New(t.Context(), panickyRefresh{&scripted{}}, colorTheme)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	next, _ = next.Update(stateMsg{st: fleetState(3)})
	func() {
		defer func() {
			if r := recover(); r != "refresh broke" {
				t.Errorf("recovered %v, want the panic of Refresh", r)
			}
		}()
		next.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	}()
	trace := m.Panic()
	for _, want := range []string{"panic: refresh broke", "model_test.go", "keys.go"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("the trace lacks %q:\n%s", want, trace)
		}
	}
}
