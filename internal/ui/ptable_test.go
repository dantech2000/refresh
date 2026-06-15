package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// NewPTable / AddRow
// ──────────────────────────────────────────────────────────────────────────────

func TestNewPTable_ColumnsCopied(t *testing.T) {
	cols := []Column{{Title: "A"}, {Title: "B"}}
	pt := NewPTable(cols)
	if len(pt.columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(pt.columns))
	}
}

func TestPTable_AddRow_CorrectColumnCount(t *testing.T) {
	pt := NewPTable([]Column{{Title: "X"}, {Title: "Y"}})
	pt.AddRow("a", "b")
	if len(pt.rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(pt.rows))
	}
}

func TestPTable_AddRow_MismatchedColumnsDropped(t *testing.T) {
	pt := NewPTable([]Column{{Title: "X"}, {Title: "Y"}})
	pt.AddRow("only-one")
	if len(pt.rows) != 0 {
		t.Error("mismatched row should be silently dropped")
	}
}

func TestPTable_AddRow_TruncatesLongCell(t *testing.T) {
	pt := NewPTable([]Column{{Title: "X", Max: 5}})
	pt.AddRow("verylongvalue")
	if len(pt.rows) != 1 {
		t.Fatal("row should have been added")
	}
	// The AddRow just stores the original; truncation happens on Render.
	// Here we simply verify the row was stored.
	if pt.rows[0][0] != "verylongvalue" {
		t.Errorf("row cell = %q, want original value before render", pt.rows[0][0])
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// WithPTableHeaderColor
// ──────────────────────────────────────────────────────────────────────────────

func TestWithPTableHeaderColor_Option(t *testing.T) {
	fn := func(s string) string { return s + "!" }
	pt := NewPTable([]Column{{Title: "Z"}}, WithPTableHeaderColor(fn))
	if pt.headerColor == nil {
		t.Error("option should have set headerColor")
	}
	if pt.headerColor("hi") != "hi!" {
		t.Error("headerColor not applied correctly via option")
	}
}

func TestPTableRender(t *testing.T) {
	originalStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = originalStdout })

	pt := NewPTable([]Column{{Title: "Name", Max: 5}, {Title: "Status"}}, WithPTableHeaderColor(func(s string) string { return s + "!" }))
	pt.AddRow("very-long", "ok")
	pt.Render()
	_ = w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
}

func TestPTableRenderPlain(t *testing.T) {
	SetPlainOutput(true)
	t.Cleanup(func() { SetPlainOutput(false) })

	originalStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = originalStdout })

	pt := NewPTable([]Column{{Title: "NAME", Max: 4}, {Title: "STATUS"}}, CyanHeaders())
	pt.AddRow("very-long-name", "\x1b[32mACTIVE\x1b[0m")
	pt.Render()
	_ = w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	// Plain mode: tab-separated, no truncation, no box drawing, ANSI stripped.
	if !strings.Contains(out, "NAME\tSTATUS") {
		t.Fatalf("plain render missing tab-separated header: %q", out)
	}
	if !strings.Contains(out, "very-long-name\tACTIVE") {
		t.Fatalf("plain render should not truncate or color cells: %q", out)
	}
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "│") {
		t.Fatalf("plain render must not contain ANSI codes or box drawing: %q", out)
	}
}
