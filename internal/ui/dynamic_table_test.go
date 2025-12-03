package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestDynamicTable_BasicFunctionality(t *testing.T) {
	table := NewDynamicTable()

	if table.IsEmpty() != true {
		t.Error("New table should be empty")
	}

	table.Add("Status", "Active")
	table.Add("Version", "1.32")

	if table.Count() != 2 {
		t.Errorf("Expected 2 rows, got %d", table.Count())
	}

	if table.IsEmpty() != false {
		t.Error("Table with rows should not be empty")
	}
}

func TestDynamicTable_ChainedOperations(t *testing.T) {
	table := NewDynamicTable().
		Add("Status", "Active").
		Add("Version", "1.32").
		AddStatus("Health", "ENABLED").
		AddBool("Protection", true)

	if table.Count() != 4 {
		t.Errorf("Expected 4 rows, got %d", table.Count())
	}
}

func TestDynamicTable_ConditionalAdd(t *testing.T) {
	table := NewDynamicTable()

	table.AddIf(true, "Included", "yes")
	table.AddIf(false, "Excluded", "no")

	if table.Count() != 1 {
		t.Errorf("Expected 1 row, got %d", table.Count())
	}
}

func TestDynamicTable_StatusColoring(t *testing.T) {
	// Disable color output for testing
	color.NoColor = true
	defer func() { color.NoColor = false }()

	table := NewDynamicTable()
	table.AddStatus("Health", "ACTIVE")
	table.AddStatus("Status", "FAILED")
	table.AddStatus("Warning", "WARN")

	if table.Count() != 3 {
		t.Errorf("Expected 3 rows, got %d", table.Count())
	}
}

func TestDynamicTable_Alignment(t *testing.T) {
	// Capture output to test alignment
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	table := NewDynamicTable()
	table.Add("Short", "value1")
	table.Add("Very Long Key Name", "value2")
	table.Add("Medium Key", "value3")

	table.Render()

	// Restore stdout and read captured output
	_ = w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines of output, got %d", len(lines))
	}

	// Verify all lines have the separator at the same position
	separatorPositions := make([]int, len(lines))
	for i, line := range lines {
		separatorPos := strings.Index(line, "│")
		if separatorPos == -1 {
			t.Errorf("Line %d missing separator: %s", i, line)
		}
		separatorPositions[i] = separatorPos
	}

	// All separator positions should be the same (perfect alignment)
	firstPos := separatorPositions[0]
	for i, pos := range separatorPositions {
		if pos != firstPos {
			t.Errorf("Line %d separator at position %d, expected %d", i, pos, firstPos)
		}
	}
}

func TestDynamicTable_ANSIColorAlignment(t *testing.T) {
	// Test that ANSI color codes don't break alignment
	table := NewDynamicTable()
	table.Add("Normal", "regular text")
	table.AddColored("Colored", "colored text", func(s string) string { return color.RedString(s) })
	table.Add("Another Normal", "more regular text")

	// This test mainly ensures no panics occur and proper structure is maintained
	if table.Count() != 3 {
		t.Errorf("Expected 3 rows, got %d", table.Count())
	}
}

func TestCalculateVisibleWidth(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"simple text", 11},
		{"", 0},
		{"\u001b[31mred text\u001b[0m", 8},    // ANSI colored text
		{"mix\u001b[31mred\u001b[0mtext", 10}, // "mix" + "red" + "text" = 10 chars
	}

	for _, test := range tests {
		result := calculateVisibleWidth(test.input)
		if result != test.expected {
			t.Errorf("calculateVisibleWidth(%q) = %d, expected %d", test.input, result, test.expected)
		}
	}
}

func TestPadANSIString(t *testing.T) {
	tests := []struct {
		input    string
		width    int
		expected int // expected visible width after padding
	}{
		{"test", 10, 10},
		{"longer text", 5, 11},             // Should not truncate, just return original
		{"\u001b[31mred\u001b[0m", 10, 10}, // ANSI colored text
	}

	for _, test := range tests {
		result := padANSIString(test.input, test.width)
		actualWidth := calculateVisibleWidth(result)
		if actualWidth != test.expected {
			t.Errorf("padANSIString(%q, %d) resulted in width %d, expected %d", test.input, test.width, actualWidth, test.expected)
		}
	}
}

func TestDynamicTable_Clear(t *testing.T) {
	table := NewDynamicTable()
	table.Add("Key1", "Value1")
	table.Add("Key2", "Value2")

	if table.Count() != 2 {
		t.Errorf("Expected 2 rows before clear, got %d", table.Count())
	}

	table.Clear()

	if table.Count() != 0 {
		t.Errorf("Expected 0 rows after clear, got %d", table.Count())
	}

	if !table.IsEmpty() {
		t.Error("Table should be empty after clear")
	}
}

// Benchmark tests for performance validation
func BenchmarkDynamicTable_Add(b *testing.B) {
	table := NewDynamicTable()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		table.Add(fmt.Sprintf("Key%d", i), fmt.Sprintf("Value%d", i))
	}
}

func BenchmarkDynamicTable_Render(b *testing.B) {
	table := NewDynamicTable()
	for i := 0; i < 100; i++ {
		table.Add(fmt.Sprintf("Key%d", i), fmt.Sprintf("Value%d", i))
	}

	// Redirect output to discard
	originalStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = originalStdout }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		table.Render()
	}
}
