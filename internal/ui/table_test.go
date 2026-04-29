package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
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
	tbl.AddRow("x", "ok")
	widths := tbl.computeColumnWidths()
	if widths[0] != 10 {
		t.Fatalf("expected first column width to respect Max (10), got %d", widths[0])
	}
}

func TestComputeColumnWidthsHeaderMaxAndRowMin(t *testing.T) {
	tbl := NewTable([]Column{
		{Title: "VERY-LONG-HEADER", Min: 0, Max: 4, Align: AlignLeft},
		{Title: "B", Min: 6, Max: 0, Align: AlignLeft},
	})
	tbl.AddRow("x", "y")
	widths := tbl.computeColumnWidths()
	if widths[0] != 4 {
		t.Fatalf("header max width = %d, want 4", widths[0])
	}
	if widths[1] != 6 {
		t.Fatalf("row min width = %d, want 6", widths[1])
	}
}

func TestTableOptionsRowsAndRender(t *testing.T) {
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = originalStdout })

	tbl := NewTable(
		[]Column{
			{Title: "NAME", Min: 2, Max: 8, Align: AlignLeft},
			{Title: "COUNT", Min: 5, Max: 5, Align: AlignRight},
		},
		WithHeaderColor(func(s string) string { return "\x1b[32m" + s + "\x1b[0m" }),
	)
	t.Setenv("REFRESH_DEBUG_TABLE", "1")
	tbl.AddRow("bad-shape")
	tbl.AddRow("alpha", "12")
	tbl.Render()
	_ = w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()
	if !strings.Contains(output, "alpha") || !strings.Contains(output, "COUNT") {
		t.Fatalf("Render output = %q", output)
	}

	if got := buildBorder("<", "+", ">", []int{1, 2}); got != "<───+────>" {
		t.Fatalf("buildBorder() = %q", got)
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

func TestPadAndTruncateEdges(t *testing.T) {
	if got := padANSI("already-long", 3, AlignLeft); got != "already-long" {
		t.Fatalf("padANSI long = %q", got)
	}
	if got := truncateANSI("abcdef", 0); got != "" {
		t.Fatalf("truncateANSI width 0 = %q", got)
	}
	if got := truncateANSI("abcdef", 2); got != "ab" {
		t.Fatalf("truncateANSI width 2 = %q", got)
	}
	colored := "\x1b[31mabcdef\x1b[0m"
	if got := truncateANSI(colored, 6); !strings.Contains(got, "abcdef") {
		t.Fatalf("truncateANSI colored = %q", got)
	}
	if got := truncateANSI("\x1b[31mabcdef\x1b[0m", 4); visibleLength(got) != 4 {
		t.Fatalf("truncateANSI colored width = %q", got)
	}
	if got := truncateANSI("abcdefghijklmnopqrstuvwxyz", 4); got != "a..." {
		t.Fatalf("truncateANSI reserve cap path = %q", got)
	}
}
