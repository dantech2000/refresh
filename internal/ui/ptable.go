package ui

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
)

// PTable is a pterm-based table that maintains compatibility with the existing Table interface
// while solving ANSI alignment issues through pterm's robust table implementation.
type PTable struct {
	columns     []Column
	rows        [][]string
	headerColor func(string) string
}

// PTableOption configures optional rendering behavior for a PTable.
type PTableOption func(*PTable)

// WithPTableHeaderColor sets a coloring function used for header titles.
func WithPTableHeaderColor(fn func(string) string) PTableOption {
	return func(t *PTable) { t.headerColor = fn }
}

// NewPTable creates a new pterm-based table with the given columns and options.
// This maintains the same interface as the original table but uses pterm internally.
func NewPTable(columns []Column, opts ...PTableOption) *PTable {
	t := &PTable{columns: make([]Column, len(columns))}
	copy(t.columns, columns)
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// AddRow appends a row. The number of cells must match the number of columns;
// mismatched rows are dropped with a stderr warning (silent data loss in a
// table is much harder to debug than a noisy skip).
func (t *PTable) AddRow(cells ...string) {
	if len(cells) != len(t.columns) {
		_, _ = fmt.Fprintf(os.Stderr, "table: dropped row with %d cells (expected %d)\n", len(cells), len(t.columns))
		return
	}
	t.rows = append(t.rows, cells)
}

// Render prints the table using pterm. Column Max is enforced with ANSI-aware
// truncation, and Min/Align are honored by pre-padding cells to the computed
// column width (pterm itself only left-aligns).
func (t *PTable) Render() {
	// Truncate cells and compute final visible column widths.
	truncated := make([][]string, len(t.rows))
	widths := make([]int, len(t.columns))
	for i, col := range t.columns {
		widths[i] = VisibleWidth(col.Title)
		if col.Min > 0 && widths[i] < col.Min {
			widths[i] = col.Min
		}
	}
	for r, row := range t.rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			if t.columns[i].Max > 0 {
				cell = TruncateANSI(cell, t.columns[i].Max)
			}
			cells[i] = cell
			if w := VisibleWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
		truncated[r] = cells
	}

	var ptermData pterm.TableData

	headerRow := make([]string, len(t.columns))
	for i, col := range t.columns {
		header := col.Title
		if t.headerColor != nil {
			header = t.headerColor(header)
		}
		headerRow[i] = PadANSI(header, widths[i], AlignLeft)
	}
	ptermData = append(ptermData, headerRow)

	for _, row := range truncated {
		ptermRow := make([]string, len(row))
		for i, cell := range row {
			ptermRow[i] = PadANSI(cell, widths[i], t.columns[i].Align)
		}
		ptermData = append(ptermData, ptermRow)
	}

	table := pterm.DefaultTable.
		WithHasHeader(true).
		WithBoxed(true).
		WithData(ptermData)

	_ = table.Render()
}

// CyanHeaders returns the standard cyan header-color option used by all
// command tables.
func CyanHeaders() PTableOption {
	return WithPTableHeaderColor(func(s string) string { return color.CyanString(s) })
}

// CreateCompatibleTable is a convenience function that creates a PTable with the same
// interface as the original ui.NewTable function, making migration easier.
func CreateCompatibleTable(columns []Column, headerColor func(string) string) *PTable {
	return NewPTable(columns, WithPTableHeaderColor(headerColor))
}

// Alternative simple constructor for common case
func NewPTableWithHeaders(headers []string) *PTable {
	columns := make([]Column, len(headers))
	for i, header := range headers {
		columns[i] = Column{
			Title: header,
			Min:   0,
			Max:   0,
			Align: AlignLeft,
		}
	}

	return NewPTable(columns, CyanHeaders())
}
