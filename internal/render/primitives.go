package render

import (
	"strings"

	"github.com/dantech2000/refresh/internal/ui"
)

// colGap separates table columns.
const colGap = "  "

// Section returns a section header line: "▸ TITLE" in the accent color.
func (t *Theme) Section(title string) string {
	return t.Bold(t.Pal.Mauve, t.glyph("▸", ">")+" "+title)
}

// KV renders aligned key/value detail rows: the keys are dim and padded to a
// common width, the values are passed through (may already be colored).
func (t *Theme) KV(pairs [][2]string) []string {
	keyW := 0
	for _, p := range pairs {
		if w := ui.VisibleWidth(p[0]); w > keyW {
			keyW = w
		}
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		key := ui.PadANSI(t.Paint(t.Pal.Dim, p[0]), keyW, ui.AlignLeft)
		out = append(out, key+colGap+p[1])
	}
	return out
}

// Table builds an aligned, optionally-colored table that returns its lines
// (header + rows). Column sizing/alignment reuse the ANSI-width helpers in
// internal/ui, so colored and glyph cells line up exactly.
type Table struct {
	theme *Theme
	cols  []ui.Column
	rows  [][]string
}

// NewTable starts a table with the given columns.
func (t *Theme) NewTable(cols ...ui.Column) *Table {
	return &Table{theme: t, cols: cols}
}

// Row appends a row. Extra cells are ignored; missing cells render empty.
func (tb *Table) Row(cells ...string) *Table {
	tb.rows = append(tb.rows, cells)
	return tb
}

// Render returns the header line followed by one line per row.
func (tb *Table) Render() []string {
	n := len(tb.cols)
	widths := make([]int, n)
	for i, c := range tb.cols {
		widths[i] = max(c.Min, ui.VisibleWidth(c.Title))
	}
	for _, row := range tb.rows {
		for i := 0; i < n && i < len(row); i++ {
			if w := ui.VisibleWidth(row[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i, c := range tb.cols {
		if c.Max > 0 && widths[i] > c.Max {
			widths[i] = c.Max
		}
	}

	out := make([]string, 0, len(tb.rows)+1)
	header := make([]string, n)
	for i, c := range tb.cols {
		header[i] = ui.PadANSI(tb.theme.Paint(tb.theme.Pal.Dim, c.Title), widths[i], c.Align)
	}
	out = append(out, strings.TrimRight(strings.Join(header, colGap), " "))

	for _, row := range tb.rows {
		cells := make([]string, n)
		for i := 0; i < n; i++ {
			v := ""
			if i < len(row) {
				v = row[i]
			}
			v = ui.TruncateANSI(v, widths[i])
			cells[i] = ui.PadANSI(v, widths[i], tb.cols[i].Align)
		}
		out = append(out, strings.TrimRight(strings.Join(cells, colGap), " "))
	}
	return out
}

// Callout renders a titled block bounded by top/bottom rules (no side borders,
// so it is trivially alignment-safe). width is the rule length.
func (t *Theme) Callout(title string, body []string, width int) []string {
	if width < 8 {
		width = 8
	}
	hr := t.glyph("━", "=")
	titleSeg := t.Bold(t.Pal.Mauve, title)
	used := 3 + ui.VisibleWidth(title) + 1 // "━━ " + title + " "
	fill := width - used
	if fill < 0 {
		fill = 0
	}
	top := t.Paint(t.Pal.Rule, strings.Repeat(hr, 2)+" ") + titleSeg + " " + t.Paint(t.Pal.Rule, strings.Repeat(hr, fill))
	out := make([]string, 0, len(body)+2)
	out = append(out, top)
	out = append(out, body...)
	out = append(out, t.Paint(t.Pal.Rule, strings.Repeat(hr, width)))
	return out
}

// Bar renders a block-glyph progress bar of the given width filled to
// done/total in color c.
func (t *Theme) Bar(done, total, width int, c Color) string {
	if total <= 0 {
		total = 1
	}
	f := done * width / total
	if f > width {
		f = width
	}
	if f < 0 {
		f = 0
	}
	fillCh, emptyCh := t.glyph("█", "#"), t.glyph("░", "-")
	edgeL, edgeR := t.glyph("▕", "["), t.glyph("▏", "]")
	return edgeL + t.Paint(c, strings.Repeat(fillCh, f)) + t.Paint(t.Pal.Rule, strings.Repeat(emptyCh, width-f)) + edgeR
}
