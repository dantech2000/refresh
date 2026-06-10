package ui

import (
	"github.com/fatih/color"
)

// DynamicTable provides automatic width calculation for key-value displays
// ensuring perfect alignment regardless of content length or ANSI color codes.
type DynamicTable struct {
	rows []DynamicRow
}

// DynamicRow represents a key-value pair for display
type DynamicRow struct {
	Key   string
	Value string
}

// NewDynamicTable creates a new dynamic table instance
func NewDynamicTable() *DynamicTable {
	return &DynamicTable{
		rows: make([]DynamicRow, 0),
	}
}

// Add appends a key-value row to the table
func (dt *DynamicTable) Add(key, value string) *DynamicTable {
	dt.rows = append(dt.rows, DynamicRow{
		Key:   key,
		Value: value,
	})
	return dt
}

// AddIf conditionally adds a row only if the condition is true
func (dt *DynamicTable) AddIf(condition bool, key, value string) *DynamicTable {
	if condition {
		dt.Add(key, value)
	}
	return dt
}

// AddColored adds a row with colored value for status indication
func (dt *DynamicTable) AddColored(key string, value string, colorFunc func(string) string) *DynamicTable {
	coloredValue := colorFunc(value)
	return dt.Add(key, coloredValue)
}

// AddStatus adds a row with automatic color coding for common status values
func (dt *DynamicTable) AddStatus(key, status string) *DynamicTable {
	return dt.Add(key, StatusColorString(status))
}

// AddBool adds a boolean value with automatic ENABLED/DISABLED coloring
func (dt *DynamicTable) AddBool(key string, enabled bool) *DynamicTable {
	if enabled {
		return dt.AddStatus(key, "ENABLED")
	}
	return dt.AddStatus(key, "DISABLED")
}

// Render prints the table with perfect alignment. Under `-o plain` it emits
// uncolored "key\tvalue" lines instead.
func (dt *DynamicTable) Render() {
	if len(dt.rows) == 0 {
		return
	}

	if plainOutput {
		for _, row := range dt.rows {
			Outf("%s\t%s\n", StripANSI(row.Key), StripANSI(row.Value))
		}
		return
	}

	// Calculate the maximum visible width needed for keys
	maxKeyWidth := 0
	for _, row := range dt.rows {
		visibleWidth := VisibleWidth(row.Key)
		if visibleWidth > maxKeyWidth {
			maxKeyWidth = visibleWidth
		}
	}

	// Print each row with consistent alignment
	for _, row := range dt.rows {
		coloredKey := color.YellowString(row.Key)
		paddedKey := PadANSI(coloredKey, maxKeyWidth, AlignLeft)
		Outf("%s │ %s\n", paddedKey, row.Value)
	}
}

// RenderSection renders the table with a section header
func (dt *DynamicTable) RenderSection(sectionTitle string) {
	if sectionTitle != "" {
		Outf("\n%s:\n", color.CyanString(sectionTitle))
	}
	dt.Render()
}

// Clear removes all rows from the table
func (dt *DynamicTable) Clear() *DynamicTable {
	dt.rows = dt.rows[:0]
	return dt
}

// Count returns the number of rows in the table
func (dt *DynamicTable) Count() int {
	return len(dt.rows)
}

// IsEmpty returns true if the table has no rows
func (dt *DynamicTable) IsEmpty() bool {
	return len(dt.rows) == 0
}
