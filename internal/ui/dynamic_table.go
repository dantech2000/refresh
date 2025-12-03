package ui

import (
	"strings"

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
	var coloredStatus string
	switch strings.ToUpper(status) {
	case "ACTIVE", "ENABLED", "PASS", "SUCCESS", "HEALTHY":
		coloredStatus = color.GreenString(status)
	case "DISABLED", "FAIL", "FAILED", "ERROR", "CRITICAL":
		coloredStatus = color.RedString(status)
	case "WARN", "WARNING", "UPDATING", "PENDING", "IN PROGRESS":
		coloredStatus = color.YellowString(status)
	case "UNKNOWN", "N/A":
		coloredStatus = color.WhiteString(status)
	default:
		coloredStatus = status
	}
	return dt.Add(key, coloredStatus)
}

// AddBool adds a boolean value with automatic ENABLED/DISABLED coloring
func (dt *DynamicTable) AddBool(key string, enabled bool) *DynamicTable {
	if enabled {
		return dt.AddStatus(key, "ENABLED")
	}
	return dt.AddStatus(key, "DISABLED")
}

// Render prints the table with perfect alignment
func (dt *DynamicTable) Render() {
	if len(dt.rows) == 0 {
		return
	}

	// Calculate the maximum visible width needed for keys
	maxKeyWidth := 0
	for _, row := range dt.rows {
		visibleWidth := calculateVisibleWidth(row.Key)
		if visibleWidth > maxKeyWidth {
			maxKeyWidth = visibleWidth
		}
	}

	// Print each row with consistent alignment
	for _, row := range dt.rows {
		coloredKey := color.YellowString(row.Key)
		paddedKey := padANSIString(coloredKey, maxKeyWidth)
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

// calculateVisibleWidth returns the printable width of a string, excluding ANSI codes
func calculateVisibleWidth(s string) int {
	visibleLen := 0
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
		visibleLen++
	}
	return visibleLen
}

// padANSIString pads an ANSI-colored string to the specified width
func padANSIString(s string, width int) string {
	visibleLen := calculateVisibleWidth(s)
	if visibleLen >= width {
		return s
	}

	// Add padding spaces to reach desired width
	padding := strings.Repeat(" ", width-visibleLen)
	return s + padding
}

// Helper functions for common patterns

// CreateInfoTable creates a pre-configured table for informational displays
func CreateInfoTable() *DynamicTable {
	return NewDynamicTable()
}

// CreateStatusTable creates a pre-configured table for status displays
func CreateStatusTable() *DynamicTable {
	return NewDynamicTable()
}

// CreateSecurityTable creates a pre-configured table for security information
func CreateSecurityTable() *DynamicTable {
	return NewDynamicTable()
}
