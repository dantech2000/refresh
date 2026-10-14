package tui

import (
	"strings"
	"testing"
)

// A cluster with a nodegroup whose AMI could not be read does not show
// as current in the fleet table: its nodegroups cell says how many are
// unknown, in place of the mark of a healthy row.
func TestFleetNodegroupsSayUnknown(t *testing.T) {
	h := newHarness(t, 140, 40, colorTheme)
	st := fleetState(1)
	current := h.m.fleetCell("NODEGROUPS", st.Clusters[0]).Plain()
	unread := h.m.fleetCell("NODEGROUPS", incomplete(st.Clusters[0])).Plain()
	if !strings.Contains(unread, "? 1 unknown") || unread == current {
		t.Errorf("the nodegroups cell of a cluster with an unread nodegroup = %q, and of a current one %q; want it to say 1 unknown", unread, current)
	}
	st.Clusters[0] = incomplete(st.Clusters[0])
	h.state(st)
	h.contains("? 1 unknown")
}

// keyWord reports whether the bar has the key with the start of its word
// beside it.
func keyWord(bar string, b binding) bool {
	word := firstWord(b)
	return strings.Contains(bar, " "+b.label+"  "+word[:min(3, len(word))])
}

// When the key bar has to shorten, the keys that explain themselves lose
// their words first: at 100 columns every letter key still has its word.
// At no width does a letter key lose its word while an arrow, enter, or
// esc keeps one.
func TestKeyBarKeepsWordsOnLetterKeys(t *testing.T) {
	h := newHarness(t, 100, 40, colorTheme)
	h.state(fleetState(3))
	bar := h.m.keyBar(100).Plain()
	letters := 0
	for _, b := range h.m.barBindings() {
		if selfExplaining(b.label) {
			continue
		}
		letters++
		if !keyWord(bar, b) {
			t.Errorf("the key %q lost its word %q at 100 columns: %q", b.label, firstWord(b), bar)
		}
	}
	if letters == 0 || !strings.Contains(bar, " f  feed ") {
		t.Fatalf("the bar has %d letter keys, and f has no word: %q", letters, bar)
	}
	// The bar is drawn below the least width of the screen too: a test
	// asks for the forms that a wide screen never needs.
	sawShort := false
	for w := 40; w <= 200; w++ {
		bar := h.m.keyBar(w).Plain()
		kept, lost := "", ""
		for _, b := range h.m.barBindings() {
			switch {
			case selfExplaining(b.label) && keyWord(bar, b):
				kept = b.label
			case !selfExplaining(b.label) && !keyWord(bar, b):
				lost = b.label
			}
		}
		if kept != "" && lost != "" {
			t.Errorf("at %d columns the key %q lost its word while %q keeps one: %q", w, lost, kept, bar)
		}
		sawShort = sawShort || (kept == "" && lost == "")
	}
	if !sawShort {
		t.Error("no width has the letters with their words and the other keys without: the test did not see the bar shorten")
	}
}

// No width cuts a word of the bar: a form that does not fit gives way to
// a shorter one.
func TestKeyBarNeverCutsAWord(t *testing.T) {
	h := newHarness(t, 100, 40, colorTheme)
	h.state(fleetState(3))
	for w := minWidth; w <= 200; w++ {
		if bar := h.m.keyBar(w).Plain(); strings.Contains(bar, "…") {
			t.Errorf("at %d columns the bar cuts a word: %q", w, bar)
		}
	}
}
