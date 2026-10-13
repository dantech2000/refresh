package view

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// Width returns the number of terminal cells that s takes. A wide rune
// counts two cells and a combining mark none. s must hold no escape codes.
func Width(s string) int {
	return runewidth.StringWidth(s)
}

// truncate cuts s to at most width cells. A cut string ends in an ellipsis,
// which is three dots without Unicode.
func truncate(s string, width int, unicode bool) string {
	if Width(s) <= width {
		return s
	}
	tail := "…"
	if !unicode {
		tail = "..."
	}
	if width <= Width(tail) {
		return runewidth.Truncate(s, width, "")
	}
	return runewidth.Truncate(s, width, tail)
}

// pad fills s with spaces to width cells, on the left when right is set.
func pad(s string, width int, right bool) string {
	n := width - Width(s)
	if n <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", n) + s
	}
	return s + strings.Repeat(" ", n)
}

// visibleWidth returns the cells of a line that may hold color codes.
func visibleWidth(line string) int {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			// Skip to the final byte of the sequence, a letter.
			i += 2
			for i < len(line) && (line[i] < '@' || line[i] > '~') {
				i++
			}
			continue
		}
		b.WriteByte(line[i])
	}
	return Width(b.String())
}
