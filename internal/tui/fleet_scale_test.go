package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// bigFleet is a fleet longer than any screen, with names long enough to be
// cut in a narrow pane.
func bigFleet(n int) state.State {
	var st state.State
	for i := range n {
		st.Clusters = append(st.Clusters, state.Cluster{
			Name: fmt.Sprintf("staging-notify-%02d-long-name", i), Region: "ap-northeast-1", Version: "1.34", Latest: "1.36",
			Nodegroups: []state.Nodegroup{{Name: "ng-a", Version: "1.34"}},
		})
	}
	return st
}

func plain(l Line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Found at fleet scale: the list did not scroll, so the selection left the
// screen and Enter opened a cluster no one could see.
func TestFleetTableKeepsTheSelectionInView(t *testing.T) {
	st := bigFleet(60)
	for _, size := range [][2]int{{98, 44}, {60, 26}, {60, 12}} {
		w, h := size[0], size[1]
		for _, sel := range []int{0, 30, 59} {
			m := Model{st: st, sel: sel}
			out := m.fleetTable(w, h)
			if len(out) != h {
				t.Fatalf("%dx%d: %d lines, want %d", w, h, len(out), h)
			}
			text := ""
			found := false
			for _, l := range out {
				p := plain(l)
				text += p + "\n"
				if strings.Contains(p, "▶") && strings.Contains(p, fmt.Sprintf("staging-notify-%02d", sel)) {
					found = true
				}
			}
			if !found {
				t.Errorf("%dx%d sel %d: selected row not on screen:\n%s", w, h, sel, text)
			}
			if sel == 30 && !strings.Contains(text, "above") || sel == 30 && !strings.Contains(text, "below") {
				t.Errorf("%dx%d sel 30: no above/below line:\n%s", w, h, text)
			}
		}
	}
}

// Found at fleet scale: a cut name ran into the next column
// ("dev-catalog-381.34 → 1.36"). A cut cell ends in "…", which must be
// followed by a space (or end the row).
func TestFleetCellsKeepAGap(t *testing.T) {
	m := Model{st: bigFleet(3)}
	for _, w := range []int{98, 60, 44} {
		for _, l := range m.fleetTable(w, 30) {
			p := strings.TrimRight(plain(l), " ")
			for i, r := range []rune(p) {
				rs := []rune(p)
				if r == '…' && i+1 < len(rs) && rs[i+1] != ' ' {
					t.Errorf("width %d: a cut cell touches the next: %q", w, p)
					break
				}
			}
		}
	}
}

// Found at fleet scale: a nodegroup whose AMI could not be read showed as
// current (●) in the fleet table.
func TestFleetNodegroupsSayUnknown(t *testing.T) {
	c := state.Cluster{Name: "a", Nodegroups: []state.Nodegroup{{Name: "ng-a", AMIUnknown: true}, {Name: "ng-b"}}}
	if got := plain(fleetCell("NODEGROUPS", c)); !strings.Contains(got, "? 1 unknown") {
		t.Errorf("NODEGROUPS = %q, want the unknown count", got)
	}
}

// Found at fleet scale: until the first sweep ended (10 s on a big fleet),
// the top bar said "synced 2562047h47m ago" and the fleet said there were
// no clusters.
func TestBeforeTheFirstSweep(t *testing.T) {
	m := Model{st: state.State{Now: time.Now()}, w: 160, h: 40}
	var all strings.Builder
	all.WriteString(plain(m.topBar(160)) + "\n")
	for _, l := range m.fleetTable(98, 30) {
		all.WriteString(plain(l) + "\n")
	}
	text := all.String()
	if strings.Contains(text, "2562047h") || strings.Contains(text, "No EKS clusters") {
		t.Errorf("before the first sweep:\n%s", text)
	}
	if !strings.Contains(text, "sweeping") || !strings.Contains(text, "Sweeping the regions") {
		t.Errorf("no sweeping notice:\n%s", text)
	}
}

// Found at fleet scale: at 100 columns the key bar showed "f" and "space"
// with no words. Only keys that explain themselves (arrows, enter) lose
// their words first.
func TestKeyBarKeepsWordsOnLetterKeys(t *testing.T) {
	m := Model{st: bigFleet(3), w: 100, h: 30}
	got := plain(m.keyBar(100))
	for _, want := range []string{"f feed", "space freeze", "r readiness", "U upgrade"} {
		if !strings.Contains(strings.Join(strings.Fields(got), " "), want) {
			t.Errorf("key bar at 100 columns lacks %q: %q", want, got)
		}
	}
	if width(got) > 100 {
		t.Errorf("key bar is %d cells", width(got))
	}
}

// From review: with color off (NO_COLOR, --no-color, TERM=dumb) the UI still
// set the terminal's background color. It leaves it alone now.
func TestNoColorLeavesTheBackgroundAlone(t *testing.T) {
	m := Model{st: bigFleet(2), w: 120, h: 30}
	if v := m.View(); v.BackgroundColor == nil {
		t.Fatal("with color on, the view should set its background")
	}
	m.noColor = true
	if v := m.View(); v.BackgroundColor != nil {
		t.Errorf("with color off, BackgroundColor = %v, want none", v.BackgroundColor)
	}
}
