package ui

import (
	"bytes"
	"io"
	"os"
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
// NewPTableWithHeaders
// ──────────────────────────────────────────────────────────────────────────────

func TestNewPTableWithHeaders_ColumnCount(t *testing.T) {
	pt := NewPTableWithHeaders([]string{"Col1", "Col2", "Col3"})
	if len(pt.columns) != 3 {
		t.Errorf("expected 3 columns, got %d", len(pt.columns))
	}
}

func TestNewPTableWithHeaders_HeaderColorSet(t *testing.T) {
	pt := NewPTableWithHeaders([]string{"H"})
	if pt.headerColor == nil {
		t.Error("headerColor should be set by NewPTableWithHeaders")
	}
	if got := pt.headerColor("H"); got == "" {
		t.Fatal("headerColor returned empty string")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CreateCompatibleTable
// ──────────────────────────────────────────────────────────────────────────────

func TestCreateCompatibleTable_SetsHeaderColor(t *testing.T) {
	colorFn := func(s string) string { return ">" + s }
	cols := []Column{{Title: "X"}}
	pt := CreateCompatibleTable(cols, colorFn)
	if pt.headerColor == nil {
		t.Error("CreateCompatibleTable should set headerColor")
	}
	if pt.headerColor("test") != ">test" {
		t.Error("headerColor function not applied correctly")
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

func TestNewPTableWithHeadersEmpty(t *testing.T) {
	pt := NewPTableWithHeaders(nil)
	if len(pt.columns) != 0 {
		t.Fatalf("columns = %d, want 0", len(pt.columns))
	}
}
