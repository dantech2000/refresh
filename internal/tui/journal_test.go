package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// The journal keeps each event once, fills in the cluster of a roll's or an
// upgrade's events, and drops the oldest past its limit without taking
// them again.
func TestJournalKeepsEachEventOnce(t *testing.T) {
	ev := func(seq uint64, src state.Source) state.Event {
		return state.Event{Seq: seq, Source: src, Text: "e"}
	}
	j := newJournal()
	st := state.State{
		Feed:     []state.Event{ev(1, state.SourceCheck)},
		Log:      []state.Event{ev(2, state.SourceAWS)},
		Rolls:    []state.Roll{{Cluster: "prod", Events: []state.Event{ev(3, state.SourceKube)}}},
		Upgrades: []state.Upgrade{{Cluster: "stage", Events: []state.Event{ev(4, state.SourceUpgrade)}}},
	}
	j.add(st)
	j.add(st) // the same state again: nothing new
	all := j.of(exportAll)
	if len(all) != 4 || all[2].Cluster != "prod" || all[3].Cluster != "stage" {
		t.Fatalf("events = %+v", all)
	}
	if n := len(j.of(exportTimeline)); n != 2 {
		t.Errorf("timeline = %d events, want 2 (no Kube events, no AWS calls)", n)
	}
	if len(j.of(exportKube)) != 1 || len(j.of(exportAWS)) != 1 {
		t.Errorf("kube %d, aws %d", len(j.of(exportKube)), len(j.of(exportAWS)))
	}

	j.limit = 3
	j.add(state.State{Feed: []state.Event{ev(5, state.SourceCheck)}})
	j.add(st) // events 1-4 are still in the feeds: the dropped one stays out
	if got := j.of(exportAll); len(got) != 3 || got[0].Seq != 3 || j.dropped != 2 {
		t.Fatalf("after the limit: %+v, dropped %d", got, j.dropped)
	}
}

// e exports a log of the session to a file only the user can read: a
// header, then one tab-separated line per event.
func TestExportWritesTheSessionLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	dir := filepath.Join(home, ".local", "state")
	h := newHarness(t, 140, 40, 3*time.Minute)
	h.advance(2 * time.Minute)
	h.keys("e")
	h.contains("Export which log of this session?", "Timeline", "Kube events", "AWS API", "All")
	h.keys("4") // All
	files, _ := filepath.Glob(filepath.Join(dir, "refresh", "sessions", "refresh-ui-*-all.tsv"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
	data, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "# refresh ui session log: all") || lines[2] != "TIME\tCLUSTER\tSOURCE\tLEVEL\tSUBJECT\tTEXT\tDETAIL" {
		t.Fatalf("file:\n%s", data)
	}
	for _, l := range lines[3:] {
		if n := strings.Count(l, "\t"); n != 6 {
			t.Fatalf("line has %d tabs, want 6: %q", n, l)
		}
	}
	h.contains("exported", "~/.local/state/refresh/sessions/"+filepath.Base(files[0]))
	if got := tildePath(filepath.Join(os.Getenv("HOME"), "x", "y.tsv")); got != filepath.Join("~", "x", "y.tsv") {
		t.Errorf("tildePath = %q", got)
	}
}
