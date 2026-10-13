package view

import (
	"fmt"
	"io"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// Align is the side of a column that its cells stand on.
type Align int

// The alignments.
const (
	AlignLeft Align = iota
	AlignRight
)

// Truncate says how a column gives up width on a narrow terminal.
type Truncate int

// The truncation modes.
const (
	// TruncateEnd cuts the end of a cell and adds an ellipsis.
	TruncateEnd Truncate = iota
	// TruncateNever keeps every cell whole.
	TruncateNever
)

// Column is one column of a list. Value is the only source of the text of
// a cell, for the table and for plain output, so the two cannot differ in a
// word. Status can add a glyph and a color in the table and nothing else.
type Column[T any] struct {
	Title string
	// Value returns the text of the cell. "" means an empty cell.
	Value func(row T) string
	// Status, when set, returns the status whose glyph stands before the
	// text in the table. NoStatus draws no glyph for that row.
	Status   func(row T) Status
	MinWidth int
	// MaxWidth is the widest the column gets on a terminal. 0 means no
	// limit.
	MaxWidth int
	Align    Align
	Truncate Truncate
}

// List is the view of a list command: a table for a person, and tab-separated
// values for a script.
type List[T any] struct {
	// Title and Subtitle make the first line of the table: "CLUSTERS" and
	// "12 · 3 regions".
	Title    string
	Subtitle string
	Columns  []Column[T]
	Rows     []T
	// Empty is the line that the table shows in place of the rows when
	// there are none.
	Empty string
	// Notes are lines after the table. Plain output does not have them.
	Notes []string
	// Failures are listed in a section after the table. Plain output does
	// not have them: the command writes them to stderr.
	Failures []apidoc.Failure
}

// emptyCell stands for an empty value. In plain output an empty cell would
// put two tabs side by side, which a shell "read" takes as one separator.
const emptyCell = "-"

// columnGap separates the columns of a table.
const columnGap = "  "

// Render returns the lines of the table. width is the width of the
// terminal: cells are cut to fit it. A width of 0 means that the output is
// not a terminal, and nothing is cut.
func (l List[T]) Render(th Theme, width int) []string {
	out := make([]string, 0, len(l.Rows)+len(l.Notes)+len(l.Failures)+6)
	if l.Title != "" {
		head := th.Heading(l.Title)
		if l.Subtitle != "" {
			head += "  " + th.Strong(l.Subtitle)
		}
		out = append(out, head, "")
	}
	if len(l.Rows) == 0 && l.Empty != "" {
		out = append(out, l.Empty)
	} else {
		out = append(out, l.table(th, width)...)
	}
	if len(l.Notes) > 0 {
		out = append(out, "")
		out = append(out, l.Notes...)
	}
	return append(out, FailureSection(th, l.Failures)...)
}

// table returns the header line and one line for each row.
func (l List[T]) table(th Theme, width int) []string {
	if len(l.Columns) == 0 {
		return nil
	}
	// The text of every cell, and the cells that a status glyph takes
	// before it.
	text := make([][]string, len(l.Rows))
	glyph := make([][]string, len(l.Rows))
	for i, row := range l.Rows {
		text[i] = make([]string, len(l.Columns))
		glyph[i] = make([]string, len(l.Columns))
		for j, col := range l.Columns {
			text[i][j] = plainCell(col.Value(row))
			if col.Status != nil {
				glyph[i][j] = th.Glyph(col.Status(row))
			}
		}
	}

	widths := make([]int, len(l.Columns))
	floors := make([]int, len(l.Columns))
	for j, col := range l.Columns {
		prefix := 0
		for i := range l.Rows {
			prefix = max(prefix, glyphWidth(glyph[i][j]))
			widths[j] = max(widths[j], glyphWidth(glyph[i][j])+Width(text[i][j]))
		}
		floors[j] = max(col.MinWidth, Width(col.Title), prefix+1)
		widths[j] = max(widths[j], floors[j])
		if width > 0 && col.MaxWidth > 0 {
			widths[j] = max(min(widths[j], col.MaxWidth), floors[j])
		}
		if col.Truncate == TruncateNever {
			floors[j] = widths[j]
		}
	}
	if width > 0 {
		shrink(widths, floors, width-len(columnGap)*(len(l.Columns)-1))
	}

	lines := make([]string, 0, len(l.Rows)+1)
	header := make([]string, len(l.Columns))
	for j, col := range l.Columns {
		header[j] = th.Dim(pad(col.Title, widths[j], col.Align == AlignRight))
	}
	lines = append(lines, trimLine(strings.Join(header, columnGap)))
	for i := range l.Rows {
		cells := make([]string, len(l.Columns))
		for j, col := range l.Columns {
			room := widths[j] - glyphWidth(glyph[i][j])
			cell := pad(truncate(text[i][j], room, th.Unicode), room, col.Align == AlignRight)
			if text[i][j] == emptyCell {
				cell = th.Dim(cell)
			}
			if glyph[i][j] != "" {
				cell = glyph[i][j] + " " + cell
			}
			cells[j] = cell
		}
		lines = append(lines, trimLine(strings.Join(cells, columnGap)))
	}
	return lines
}

// glyphWidth returns the cells that a painted glyph and the space after it
// take, or 0 when there is no glyph.
func glyphWidth(glyph string) int {
	if glyph == "" {
		return 0
	}
	return visibleWidth(glyph) + 1
}

// shrink lowers widths until their sum is at most total: it takes one cell
// at a time from the widest column that is still above its floor. When
// every column is at its floor it stops, and the lines stay too long.
func shrink(widths, floors []int, total int) {
	sum := 0
	for _, w := range widths {
		sum += w
	}
	for sum > total {
		widest := -1
		for j, w := range widths {
			if w > floors[j] && (widest < 0 || w > widths[widest]) {
				widest = j
			}
		}
		if widest < 0 {
			return
		}
		widths[widest]--
		sum--
	}
}

// trimLine removes the padding at the end of a line. A color reset may
// follow the padding of a dimmed cell.
func trimLine(line string) string {
	const reset = "\x1b[0m"
	if strings.HasSuffix(line, reset) {
		return strings.TrimRight(strings.TrimSuffix(line, reset), " ") + reset
	}
	return strings.TrimRight(line, " ")
}

// Plain writes the list as tab-separated values: the column titles, then
// one line for each row. Nothing is cut, and there is no title, no note,
// and no color.
func (l List[T]) Plain(w io.Writer) error {
	cells := make([]string, len(l.Columns))
	for j, col := range l.Columns {
		cells[j] = col.Title
	}
	var b strings.Builder
	b.WriteString(strings.Join(cells, "\t"))
	b.WriteByte('\n')
	for _, row := range l.Rows {
		for j, col := range l.Columns {
			cells[j] = plainCell(col.Value(row))
		}
		b.WriteString(strings.Join(cells, "\t"))
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// plainCell returns the text of a cell on one line with no tab, so that a
// value cannot split a row or a column. An empty value is "-".
func plainCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return emptyCell
	}
	return s
}

// FailureLine returns one line for a failure, for people:
//
//	nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded
//
// The cluster, the region, and the error are left out when they are empty,
// and the region is left out when it is the name of the item.
func FailureLine(f apidoc.Failure) string {
	var b strings.Builder
	b.WriteString(f.Kind.Noun())
	b.WriteByte(' ')
	if f.Cluster != "" {
		b.WriteString(f.Cluster)
		b.WriteByte('/')
	}
	b.WriteString(f.Name)
	if f.Region != "" && f.Region != f.Name {
		fmt.Fprintf(&b, " (%s)", f.Region)
	}
	b.WriteString(": ")
	b.WriteString(string(f.Reason))
	if f.Error != "" {
		b.WriteString(": ")
		b.WriteString(f.Error)
	}
	return b.String()
}

// FailureSection returns the lines that list the failures of a run after a
// table: a blank line, a heading, and one line for each failure, in the
// order of fs. What the user stopped is under INTERRUPTED, and everything
// else under INCOMPLETE DATA. It returns nil when fs is empty.
func FailureSection(th Theme, fs []apidoc.Failure) []string {
	var stopped, missing []string
	for _, f := range fs {
		if f.Reason == apidoc.ReasonInterrupted {
			stopped = append(stopped, th.Token(Warn, FailureLine(f)))
		} else {
			missing = append(missing, th.Token(Unknown, FailureLine(f)))
		}
	}
	var out []string
	if len(stopped) > 0 {
		out = append(out, "", th.paint(colorYellow, "INTERRUPTED", true))
		out = append(out, stopped...)
	}
	if len(missing) > 0 {
		out = append(out, "", th.paint(colorPeach, "INCOMPLETE DATA", true))
		out = append(out, missing...)
	}
	return out
}
