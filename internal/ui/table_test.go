package ui

import (
	"testing"
)

func TestVisibleLengthAndPadANSI(t *testing.T) {
	green := "\x1b[32mPASS\x1b[0m"
	if visibleLength(green) != 4 {
		t.Fatalf("visibleLength failed: got %d want %d", visibleLength(green), 4)
	}
	padded := padANSI(green, 8, AlignLeft)
	if visibleLength(padded) != 8 {
		t.Fatalf("padANSI failed: got visible %d want %d", visibleLength(padded), 8)
	}
}

func TestTruncateANSI(t *testing.T) {
	yellow := "\x1b[33mWARNING\x1b[0m"
	out := truncateANSI(yellow, 4)
	if visibleLength(out) != 4 {
		t.Fatalf("truncateANSI visible length: got %d want %d", visibleLength(out), 4)
	}
}

func TestTableRenderWidths(t *testing.T) {
	cols := []Column{{Title: "NAME", Min: 4, Max: 10, Align: AlignLeft}, {Title: "STATUS", Min: 5, Max: 0, Align: AlignLeft}}
	tbl := NewTable(cols)
	tbl.AddRow("VeryLongNameThatWillBeTruncated", "\x1b[32mOK\x1b[0m")
	widths := tbl.computeColumnWidths()
	if widths[0] != 10 {
		t.Fatalf("expected first column width to respect Max (10), got %d", widths[0])
	}
}

func TestPadANSIRightAlign(t *testing.T) {
	s := "123"
	out := padANSI(s, 6, AlignRight)
	if visibleLength(out) != 6 {
		t.Fatalf("right align visible length: got %d want %d", visibleLength(out), 6)
	}
	// Expect 3 leading spaces then "123"
	if out[len(out)-3:] != "123" {
		t.Fatalf("right align content: expected suffix '123', got %q", out)
	}
}
