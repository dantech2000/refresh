package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/view"
)

// A terminal far below the least size, down to one cell and none, in
// both themes: the frame still fills it exactly, nothing panics, an ASCII
// terminal gets no byte above 127, and the screen comes back when the
// terminal grows.
func TestTinyTerminal(t *testing.T) {
	for _, th := range []view.Theme{colorTheme, asciiTheme} {
		h := newHarness(t, 120, 40, th)
		h.state(fleetState(3))
		h.keys("down", "enter")
		for _, size := range [][2]int{{99, 23}, {40, 5}, {10, 3}, {2, 1}, {1, 1}, {0, 0}, {300, 1}, {1, 200}} {
			name := fmt.Sprintf("%dx%d, unicode %v", size[0], size[1], th.Unicode)
			h.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			h.keys("down", "?", "esc")
			if size[0] > 0 && size[1] > 0 {
				checkFrame(t, name, h.m)
			}
			if !th.Unicode {
				for _, r := range h.m.render() {
					if r > 127 {
						t.Errorf("%s: the ASCII frame has %q", name, r)
						break
					}
				}
			}
		}
		h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
		checkFrame(t, "grown back", h.m)
		if h.m.screen != screenCluster {
			t.Errorf("unicode %v: the screen did not survive the tiny sizes", th.Unicode)
		}
	}
}
