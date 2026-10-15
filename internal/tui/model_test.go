package tui

import (
	"errors"
	"fmt"
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
	// The notice goes away after a while; the log strip shows the feed.
	h.tick(now.Add(time.Minute))
	h.lacks("checks of this cluster are running")
	h.contains("readiness: review")
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
