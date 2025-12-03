package ui

import (
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

// AddRow appends a row. The number of cells must match the number of columns.
func (t *PTable) AddRow(cells ...string) {
	if len(cells) != len(t.columns) {
		// Maintain same debugging behavior as original
		if len(cells) != len(t.columns) {
			return
		}
	}
	t.rows = append(t.rows, cells)
}

// Render prints the table using pterm while maintaining the current design standards.
func (t *PTable) Render() {
	// Build table data for pterm
	var ptermData pterm.TableData

	// Add header row
	headerRow := make([]string, len(t.columns))
	for i, col := range t.columns {
		header := col.Title
		if t.headerColor != nil {
			header = t.headerColor(header)
		}
		headerRow[i] = header
	}
	ptermData = append(ptermData, headerRow)

	// Add data rows with proper alignment and truncation
	for _, row := range t.rows {
		ptermRow := make([]string, len(row))
		for i, cell := range row {
			// Apply truncation based on column max width
			processedCell := cell
			if t.columns[i].Max > 0 && len(cell) > t.columns[i].Max {
				processedCell = truncateString(cell, t.columns[i].Max)
			}

			// Apply alignment - pterm will handle this, but we can format the content
			ptermRow[i] = processedCell
		}
		ptermData = append(ptermData, ptermRow)
	}

	// Create and configure pterm table
	table := pterm.DefaultTable.
		WithHasHeader(true).
		WithBoxed(true).
		WithData(ptermData)

	// Apply column-specific styling if needed
	// For now, we'll use pterm's default styling which is similar to our box-drawing style

	// Render the table
	_ = table.Render()
}

// Helper function to maintain compatibility with existing truncation logic
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
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

	return NewPTable(columns, WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
}
