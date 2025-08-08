package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Alignment represents horizontal text alignment within a column.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
)

// Column defines a table column with sizing and alignment rules.
type Column struct {
	Title string
	Min   int
	Max   int
	Align Alignment
}

// Option configures optional rendering behavior for a Table.
type Option func(*Table)

// Table is a reusable, ANSI-aware renderer for CLI tables.
type Table struct {
	columns     []Column
	rows        [][]string
	headerColor func(string) string
}

// WithHeaderColor sets a coloring function used for header titles.
func WithHeaderColor(fn func(string) string) Option {
	return func(t *Table) { t.headerColor = fn }
}

// NewTable creates a new table with the given columns and options.
func NewTable(columns []Column, opts ...Option) *Table {
	t := &Table{columns: make([]Column, len(columns))}
	copy(t.columns, columns)
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// AddRow appends a row. The number of cells must match the number of columns.
func (t *Table) AddRow(cells ...string) {
	if len(cells) != len(t.columns) {
		// Optionally log when row shape mismatches to aid debugging
		if os.Getenv("REFRESH_DEBUG_TABLE") == "1" {
			_, _ = fmt.Fprintf(os.Stderr, "table: dropped row with %d cells (expected %d)\n", len(cells), len(t.columns))
		}
		return
	}
	t.rows = append(t.rows, cells)
}

// Render prints the table to stdout.
func (t *Table) Render() {
	widths := t.computeColumnWidths()
	w := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer w.Flush()

	// Top border
	fmt.Fprintln(w, buildBorder("┌", "┬", "┐", widths))

	// Header row
	var b strings.Builder
	b.WriteString("│")
	for i, col := range t.columns {
		header := col.Title
		if t.headerColor != nil {
			header = t.headerColor(header)
		}
		b.WriteString(" ")
		b.WriteString(padANSI(header, widths[i], t.columns[i].Align))
		b.WriteString(" ")
		b.WriteString("│")
	}
	fmt.Fprintln(w, b.String())

	// Separator
	fmt.Fprintln(w, buildBorder("├", "┼", "┤", widths))

	// Rows
	for _, row := range t.rows {
		var rb strings.Builder
		rb.WriteString("│")
		for i, cell := range row {
			rb.WriteString(" ")
			rb.WriteString(padANSI(truncateANSI(cell, widths[i]), widths[i], t.columns[i].Align))
			rb.WriteString(" ")
			rb.WriteString("│")
		}
		fmt.Fprintln(w, rb.String())
	}

	// Bottom border
	fmt.Fprintln(w, buildBorder("└", "┴", "┘", widths))
}

// computeColumnWidths determines the width of each column based on headers and rows,
// clamped to each column's Min/Max.
func (t *Table) computeColumnWidths() []int {
	widths := make([]int, len(t.columns))
	// Start with header titles
	for i, col := range t.columns {
		w := visibleLength(col.Title)
		if col.Min > 0 && w < col.Min {
			w = col.Min
		}
		if col.Max > 0 && w > col.Max {
			w = col.Max
		}
		widths[i] = w
	}
	// Grow based on rows
	for _, row := range t.rows {
		for i, cell := range row {
			w := visibleLength(cell)
			if t.columns[i].Max > 0 && w > t.columns[i].Max {
				w = t.columns[i].Max
			}
			if w > widths[i] {
				widths[i] = w
			}
			if t.columns[i].Min > 0 && widths[i] < t.columns[i].Min {
				widths[i] = t.columns[i].Min
			}
		}
	}
	return widths
}

// buildBorder renders a horizontal border based on widths.
func buildBorder(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range widths {
		b.WriteString(strings.Repeat("─", w+2))
		if i < len(widths)-1 {
			b.WriteString(mid)
		} else {
			b.WriteString(right)
		}
	}
	return b.String()
}

// padANSI left- or right-aligns a possibly ANSI-colored string to width.
func padANSI(s string, width int, align Alignment) string {
	vis := visibleLength(s)
	if vis >= width {
		return s
	}
	pad := strings.Repeat(" ", width-vis)
	if align == AlignRight {
		return pad + s
	}
	return s + pad
}

// truncateANSI truncates a possibly ANSI-colored string to width, adding ellipsis when helpful.
func truncateANSI(s string, width int) string {
	vis := visibleLength(s)
	if vis <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	// If there's room, add ellipsis (requires >= 3)
	target := width
	ellipsis := ""
	if width >= 3 {
		target = width - 3
		ellipsis = "..."
	}

	// Walk runes of the visible portion while preserving escape sequences
	var out strings.Builder
	// Improved capacity estimate:
	// - Assume up to ~3 bytes per visible rune in prefix (UTF-8 average)
	// - Add ~8 bytes per ANSI escape sequence encountered in the source
	// - Cap to original string length to avoid gross over-allocation
	reserve := target*3 + len(ellipsis)
	if esc := strings.Count(s, "\u001b"); esc > 0 {
		reserve += esc * 8
	}
	if reserve > len(s) {
		reserve = len(s)
	}
	if reserve < width { // ensure at least width capacity
		reserve = width
	}
	out.Grow(reserve)
	var visibleCount int
	inEscape := false
	for _, r := range s {
		if r == '\u001b' { // ESC
			inEscape = true
			out.WriteRune(r)
			continue
		}
		if inEscape {
			out.WriteRune(r)
			// CSI sequences typically end with a letter
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		if visibleCount < target {
			out.WriteRune(r)
			visibleCount++
		} else {
			break
		}
	}
	out.WriteString(ellipsis)
	return out.String()
}

// visibleLength returns the printable length of a string, excluding ANSI codes.
func visibleLength(s string) int {
	count := 0
	inEscape := false
	for _, r := range s {
		if r == '\u001b' { // ESC
			inEscape = true
			continue
		}
		if inEscape {
			// End on letter terminator
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		count++
	}
	return count
}
