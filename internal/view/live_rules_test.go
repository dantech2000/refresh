package view_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/view"
)

// The redraw moves the cursor up by the rows that the last block took on
// the screen, and not by its lines: a line longer than the terminal wraps.
// Escape codes take no cells. A count that is too low leaves the top of
// the old block on the screen.
func TestLive_RedrawCountsWrappedRows(t *testing.T) {
	const width = 20
	green := func(s string) string { return "\x1b[32m" + s + "\x1b[0m" }
	tests := []struct {
		name  string
		lines []string
		rows  int
	}{
		{"short lines", []string{"one", "two"}, 2},
		{"a line as wide as the terminal", []string{strings.Repeat("x", width)}, 1},
		{"a line one cell wider", []string{strings.Repeat("x", width+1)}, 2},
		{"a line of two and a half widths", []string{"head", strings.Repeat("x", 2*width+10), "tail"}, 5},
		{"a colored line that fits", []string{green(strings.Repeat("x", width-2))}, 1},
		{"a colored line that wraps", []string{green(strings.Repeat("x", width+2)), "tail"}, 3},
		{"an empty line", []string{"", "x"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			l := view.NewLive(&out, func() (int, int) { return width, 40 })
			if !l.Draw(tt.lines) {
				t.Fatal("the block was not drawn")
			}
			if strings.Contains(out.String(), "\x1b[J") {
				t.Errorf("the first draw erased something: %q", out.String())
			}
			out.Reset()
			if !l.Draw([]string{"next"}) {
				t.Fatal("the second block was not drawn")
			}
			want := "\x1b[" + strconv.Itoa(tt.rows) + "A\x1b[Jnext\n"
			if out.String() != want {
				t.Errorf("the redraw wrote %q, want %q: up over the %d rows of the block", out.String(), want, tt.rows)
			}
			out.Reset()
			l.Clear()
			if out.String() != "\x1b[1A\x1b[J" {
				t.Errorf("the clear wrote %q, want up over the one row of the new block", out.String())
			}
		})
	}
}
