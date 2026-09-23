package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// The `-o plain` contract: stdout carries one header row, then one
// tab-separated row per item, and nothing else — no title, no timing line, no
// blank lines, no footer, no glyphs, no color. Cells go through PlainCell so an
// embedded tab or newline can't split a row. Anything informational goes to
// stderr. Describe commands use the two-column FIELD/VALUE shape
// (PlainKVHeaders).

// PlainKVHeaders is the header row of every describe command's `-o plain`
// output: one FIELD/VALUE row per attribute.
var PlainKVHeaders = []string{"FIELD", "VALUE"}

// PlainEmpty is the cell written for an empty value. An empty cell would give
// two adjacent tabs, which `IFS=$'\t' read` collapses into one separator.
const PlainEmpty = "-"

// PlainTable collects rows for `-o plain` output. It never truncates.
type PlainTable struct {
	headers []string
	rows    [][]string
}

// NewPlainTable returns a table with the given header row.
func NewPlainTable(headers ...string) *PlainTable {
	return &PlainTable{headers: append([]string(nil), headers...)}
}

// NewPlainKV returns a FIELD/VALUE table for a describe command.
func NewPlainKV() *PlainTable { return NewPlainTable(PlainKVHeaders...) }

// Row appends one row. A row whose cell count doesn't match the header is
// dropped with a stderr warning, so a bug can't shift columns.
func (t *PlainTable) Row(cells ...string) *PlainTable {
	if len(cells) != len(t.headers) {
		_, _ = fmt.Fprintf(Stderr, "plain table: dropped row with %d cells (expected %d)\n", len(cells), len(t.headers))
		return t
	}
	t.rows = append(t.rows, append([]string(nil), cells...))
	return t
}

// Add appends a FIELD/VALUE row; it is Row for two-column tables.
func (t *PlainTable) Add(field, value string) *PlainTable { return t.Row(field, value) }

// Lines returns the header line followed by one line per row.
func (t *PlainTable) Lines() []string {
	out := make([]string, 0, len(t.rows)+1)
	out = append(out, plainLine(t.headers))
	for _, r := range t.rows {
		out = append(out, plainLine(r))
	}
	return out
}

// Write writes the table to w.
func (t *PlainTable) Write(w io.Writer) {
	for _, l := range t.Lines() {
		_, _ = fmt.Fprintln(w, l)
	}
}

// Render writes the table to stdout.
func (t *PlainTable) Render() { t.Write(os.Stdout) }

// PlainPairs joins alternating keys and values as "k=v k=v", the VALUE format
// of a repeated item's row in a FIELD/VALUE table. An empty value becomes "-"
// so the pair list keeps its shape.
func PlainPairs(kv ...string) string {
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		v := strings.TrimSpace(kv[i+1])
		if v == "" {
			v = PlainEmpty
		}
		parts = append(parts, kv[i]+"="+v)
	}
	return strings.Join(parts, " ")
}

func plainLine(cells []string) string {
	out := make([]string, len(cells))
	for i, c := range cells {
		c = strings.TrimSpace(PlainCell(c))
		if c == "" {
			c = PlainEmpty
		}
		out[i] = c
	}
	return strings.Join(out, "\t")
}
