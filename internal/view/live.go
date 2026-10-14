package view

import (
	"fmt"
	"io"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Live redraws a block of lines in place on a terminal. It moves the cursor
// up over the block it drew last and writes the new one. It can only do
// that for a block that fits on the screen, so Draw refuses a taller one.
type Live struct {
	w io.Writer
	// size returns the columns and rows of the terminal.
	size func() (width, height int)
	// rows is the number of screen rows of the block on the screen.
	rows int
}

// NewLive returns a Live that writes to w, a terminal whose size the
// function returns.
func NewLive(w io.Writer, size func() (width, height int)) *Live {
	return &Live{w: w, size: size}
}

// Draw replaces the block on the screen with lines and reports whether it
// did. It draws nothing, and removes the block that is there, when the
// lines do not fit the height of the terminal: the top of a taller block
// would scroll away, where the cursor cannot reach it again.
func (l *Live) Draw(lines []string) bool {
	width, height := l.size()
	rows := 0
	for _, line := range lines {
		rows += screenRows(line, width)
	}
	if width <= 0 || rows >= height {
		l.Clear()
		return false
	}
	var b strings.Builder
	b.WriteString(l.erase())
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	// A failed write to a terminal has no one to report to.
	_, _ = io.WriteString(l.w, b.String())
	l.rows = rows
	return true
}

// Clear removes the block from the screen.
func (l *Live) Clear() {
	if l.rows == 0 {
		return
	}
	_, _ = io.WriteString(l.w, l.erase())
	l.rows = 0
}

// erase returns the codes that move the cursor to the start of the block
// and clear from there to the end of the screen.
func (l *Live) erase() string {
	if l.rows == 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dA\x1b[J", l.rows)
}

// screenRows returns the rows that line takes on a terminal of width
// columns: a line longer than the terminal wraps. A wide rune cannot start
// in the last column, so it wraps one cell early.
func screenRows(line string, width int) int {
	if width <= 0 {
		return 1
	}
	rows, col := 1, 0
	for _, r := range stripCodes(line) {
		w := runewidth.RuneWidth(r)
		if col > 0 && col+w > width {
			rows++
			col = 0
		}
		col += w
	}
	return rows
}
