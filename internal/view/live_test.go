package view_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/view"
)

func TestLive(t *testing.T) {
	var buf bytes.Buffer
	height := 5
	live := view.NewLive(&buf, func() (int, int) { return 10, height })

	if !live.Draw([]string{"one", "two"}) {
		t.Fatal("Draw refused a block that fits")
	}
	if !live.Draw([]string{"1", "2", "3"}) {
		t.Fatal("Draw refused a block that fits")
	}
	// The second block starts where the first one did.
	want := "one\ntwo\n" + "\x1b[2A\x1b[J" + "1\n2\n3\n"
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}

	// A line longer than the terminal takes two rows, so this block of
	// four lines needs five rows and does not fit: the old block goes.
	buf.Reset()
	if live.Draw([]string{"a", "b", "c", "a line of 14.."}) {
		t.Error("Draw drew a block taller than the terminal")
	}
	if diff := cmp.Diff("\x1b[3A\x1b[J", buf.String()); diff != "" {
		t.Errorf("output after a block that does not fit (-want +got):\n%s", diff)
	}

	buf.Reset()
	live.Clear()
	if buf.Len() != 0 {
		t.Errorf("Clear with nothing on the screen wrote %q", buf.String())
	}
	live.Draw([]string{"x"})
	buf.Reset()
	live.Clear()
	if diff := cmp.Diff("\x1b[1A\x1b[J", buf.String()); diff != "" {
		t.Errorf("Clear (-want +got):\n%s", diff)
	}
}

// The redraw moves the cursor up by the rows that the last block took on
// the screen, and not by its lines: a line longer than the terminal wraps.
// Escape codes take no cells, and a wide rune cannot use the last column
// of a row, so three of them take three rows of a terminal that is three
// columns wide, not two. A count that is too low leaves the top of the
// old block on the screen.
func TestLive_RedrawCountsWrappedRows(t *testing.T) {
	green := func(s string) string { return "\x1b[32m" + s + "\x1b[0m" }
	tests := []struct {
		name  string
		width int
		lines []string
		rows  int
	}{
		{"short lines", 20, []string{"one", "two"}, 2},
		{"a line as wide as the terminal", 20, []string{strings.Repeat("x", 20)}, 1},
		{"a line one cell wider", 20, []string{strings.Repeat("x", 21)}, 2},
		{"a line of two and a half widths", 20, []string{"head", strings.Repeat("x", 50), "tail"}, 5},
		{"a colored line that fits", 20, []string{green(strings.Repeat("x", 18))}, 1},
		{"a colored line that wraps", 20, []string{green(strings.Repeat("x", 22)), "tail"}, 3},
		{"an empty line", 20, []string{"", "x"}, 2},
		{"wide runes at an odd width", 3, []string{"日本語"}, 3},
		{"wide runes at an even width", 4, []string{"日本語"}, 2},
		{"one wide rune a row", 1, []string{"日本語"}, 3},
		{"colored wide runes", 4, []string{"\x1b[31m日本\x1b[0m"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			l := view.NewLive(&out, func() (int, int) { return tt.width, 40 })
			if !l.Draw(tt.lines) {
				t.Fatal("the block was not drawn")
			}
			if strings.Contains(out.String(), "\x1b[J") {
				t.Errorf("the first draw erased something: %q", out.String())
			}
			out.Reset()
			if !l.Draw([]string{"x"}) {
				t.Fatal("the second block was not drawn")
			}
			want := "\x1b[" + strconv.Itoa(tt.rows) + "A\x1b[Jx\n"
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
