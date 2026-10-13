package view_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

type row struct {
	name, status, note string
	count              int
}

func newList(rows ...row) view.List[row] {
	return view.List[row]{
		Title:    "THINGS",
		Subtitle: "2",
		Empty:    "No things found",
		Rows:     rows,
		Columns: []view.Column[row]{
			{Title: "NAME", MinWidth: 6, Value: func(r row) string { return r.name }},
			{
				Title: "STATUS", Truncate: view.TruncateNever,
				Value:  func(r row) string { return r.status },
				Status: func(r row) view.Status { return view.StatusOf(r.status) },
			},
			{Title: "NOTE", MaxWidth: 12, Value: func(r row) string { return r.note }},
			{Title: "N", Align: view.AlignRight, Truncate: view.TruncateNever, Value: func(r row) string { return strings.Repeat("1", r.count) }},
		},
	}
}

var (
	ascii   = view.Theme{}
	unicode = view.Theme{Unicode: true}
)

func TestList_Render(t *testing.T) {
	l := newList(
		row{name: "alpha", status: "ACTIVE", note: "first", count: 2},
		row{name: "a-longer-name", status: "UPDATING", count: 1},
	)
	want := []string{
		"THINGS  2",
		"",
		"NAME           STATUS        NOTE    N",
		"alpha          [OK] ACTIVE   first  11",
		"a-longer-name  [~] UPDATING  -       1",
	}
	if diff := cmp.Diff(want, l.Render(ascii, 0)); diff != "" {
		t.Errorf("Render (-want +got):\n%s", diff)
	}
}

func TestList_RenderEmptyNotesAndFailures(t *testing.T) {
	l := newList()
	l.Notes = []string{"a note"}
	l.Failures = []apidoc.Failure{
		{Kind: apidoc.FailureNodegroup, Name: "web", Cluster: "prod", Region: "us-east-1", Reason: apidoc.ReasonThrottled, Error: "Rate exceeded"},
		{Kind: apidoc.FailureRegion, Name: "eu-west-1", Region: "eu-west-1", Reason: apidoc.ReasonInterrupted},
	}
	want := []string{
		"THINGS  2",
		"",
		"No things found",
		"",
		"a note",
		"",
		"INTERRUPTED",
		"[!] region eu-west-1: Interrupted",
		"",
		"INCOMPLETE DATA",
		"[?] nodegroup prod/web (us-east-1): Throttled: Rate exceeded",
	}
	if diff := cmp.Diff(want, l.Render(ascii, 0)); diff != "" {
		t.Errorf("Render (-want +got):\n%s", diff)
	}
}

func TestList_Plain(t *testing.T) {
	l := newList(
		row{name: "alpha", status: "ACTIVE", note: "a\tb\nc", count: 2},
		row{name: "beta", status: "UPDATING"},
	)
	l.Notes = []string{"a note"}
	l.Failures = []apidoc.Failure{{Kind: apidoc.FailureRegion, Name: "eu-west-1"}}
	var buf bytes.Buffer
	if err := l.Plain(&buf); err != nil {
		t.Fatal(err)
	}
	// A header row, tabs, "-" for an empty cell, one line for each row, and
	// nothing else: no title, no note, no failure.
	want := "NAME\tSTATUS\tNOTE\tN\nalpha\tACTIVE\ta b c\t11\nbeta\tUPDATING\t-\t-\n"
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Plain (-want +got):\n%s", diff)
	}
}

// The table and plain output cannot differ in a word: once the glyph and
// the padding are gone, a table cell is the plain cell. This holds for
// every theme, and it is why a column has one Value function.
func TestList_TableAndPlainHaveTheSameText(t *testing.T) {
	l := newList(
		row{name: "alpha", status: "ACTIVE", note: "first", count: 2},
		row{name: "beta", status: "DEGRADED"},
		row{name: "日本語", status: "", note: "x"},
	)
	var buf bytes.Buffer
	if err := l.Plain(&buf); err != nil {
		t.Fatal(err)
	}
	plain := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")

	for name, th := range map[string]view.Theme{"ascii": ascii, "unicode": unicode} {
		table := l.Render(th, 0)[2:] // without the title and the blank line
		if len(table) != len(plain) {
			t.Fatalf("%s: the table has %d lines and plain output %d", name, len(table), len(plain))
		}
		for i := range plain {
			words := strings.Fields(table[i])
			// Drop the glyph of the status column.
			glyphs := []string{"●", "▲", "✗", "◷", "○", "•", "[OK]", "[!]", "[X]", "[~]", "[?]"}
			words = slices.DeleteFunc(words, func(w string) bool { return slices.Contains(glyphs, w) })
			want := strings.Split(plain[i], "\t")
			if diff := cmp.Diff(want, words); diff != "" {
				t.Errorf("%s: line %d of the table and of plain output differ (-plain +table):\n%s", name, i, diff)
			}
		}
	}
}

func TestList_Widths(t *testing.T) {
	l := newList(row{name: "a-very-long-cluster-name", status: "ACTIVE", note: "a note that goes on and on", count: 3})
	tests := []struct {
		name  string
		theme view.Theme
		width int
		want  string
	}{
		{"not a terminal: nothing is cut", ascii, 0, "a-very-long-cluster-name  [OK] ACTIVE  a note that goes on and on  111"},
		{"a wide terminal: only MaxWidth cuts", unicode, 200, "a-very-long-cluster-name  ● ACTIVE  a note that…  111"},
		{"a narrow terminal: the widest column gives way first", unicode, 40, "a-very-lon…  ● ACTIVE  a note that…  111"},
		{"three dots without Unicode", ascii, 44, "a-very-lo...  [OK] ACTIVE  a note th...  111"},
		{"at the floors, the line stays too long", unicode, 10, "a-ver…  ● ACTIVE  a n…  111"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := l.Render(tt.theme, tt.width)
			if got := lines[len(lines)-1]; got != tt.want {
				t.Errorf("row = %q\n        want %q", got, tt.want)
			}
			if tt.width > 20 {
				if w := view.Width(lines[len(lines)-1]); w > tt.width {
					t.Errorf("the row is %d cells wide on a terminal of %d", w, tt.width)
				}
			}
		})
	}
}

// Width counts terminal cells: a wide rune is two, a combining mark none.
func TestList_WideRunesLineUp(t *testing.T) {
	l := newList(
		row{name: "日本語", status: "ACTIVE"},
		row{name: "café", status: "ACTIVE"},
		row{name: "plain", status: "ACTIVE"},
	)
	lines := l.Render(ascii, 0)[3:]
	col := -1
	for _, line := range lines {
		// The status column starts at the same cell in every row.
		before, _, ok := strings.Cut(line, "[OK]")
		if !ok {
			t.Fatalf("the row %q has no status", line)
		}
		at := view.Width(before)
		if col >= 0 && at != col {
			t.Errorf("the status column starts at cell %d in %q, and at %d in the row before", at, line, col)
		}
		col = at
	}
	if got := view.Width("日本語"); got != 6 {
		t.Errorf("Width(日本語) = %d, want 6", got)
	}
	if got := view.Width("café"); got != 4 {
		t.Errorf("Width(café with a combining accent) = %d, want 4", got)
	}
}

func TestTheme_ColorIsAnAddition(t *testing.T) {
	l := newList(row{name: "alpha", status: "ACTIVE", note: "", count: 1})
	plain := l.Render(view.Theme{Unicode: true}, 0)
	for _, depth := range []view.ColorDepth{view.Color256, view.TrueColor} {
		colored := l.Render(view.Theme{Unicode: true, Depth: depth}, 0)
		for i := range plain {
			if got := stripColor(colored[i]); got != plain[i] {
				t.Errorf("depth %d, line %d: without its color codes it is %q, want %q", depth, i, got, plain[i])
			}
		}
		if !strings.Contains(strings.Join(colored, "\n"), "\x1b[") {
			t.Errorf("depth %d: the table has no color", depth)
		}
	}
	if got := strings.Join(plain, "\n"); strings.Contains(got, "\x1b") {
		t.Errorf("a theme without color wrote an escape code:\n%q", got)
	}
}

func stripColor(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestStatusOf(t *testing.T) {
	tests := map[string]view.Status{
		"ACTIVE":        view.Healthy,
		"active":        view.Healthy,
		"UPDATING":      view.Progress,
		"CREATING":      view.Progress,
		"DELETING":      view.Progress,
		"FAILED":        view.Fail,
		"UPDATE_FAILED": view.Fail,
		"DEGRADED":      view.Fail,
		"PENDING":       view.Warn,
		"":              view.Unknown,
		"SOMETHING_NEW": view.Neutral,
	}
	for in, want := range tests {
		if got := view.StatusOf(in); got != want {
			t.Errorf("StatusOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLive(t *testing.T) {
	var buf bytes.Buffer
	height := 5
	live := view.NewLive(&buf, func() (int, int) { return 10, height })

	if !live.Draw([]string{"one", "two"}) {
		t.Fatal("Draw refused a block that fits")
	}
	if !live.Draw([]string{"1", "2", "3"}) {
		t.Fatal("Draw refused a block that fits")
	}
	// The second block starts where the first one did.
	want := "one\ntwo\n" + "\x1b[2A\x1b[J" + "1\n2\n3\n"
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("output (-want +got):\n%s", diff)
	}

	// A line longer than the terminal takes two rows, so this block of
	// four lines needs five rows and does not fit: the old block goes.
	buf.Reset()
	if live.Draw([]string{"a", "b", "c", "a line of 14.."}) {
		t.Error("Draw drew a block taller than the terminal")
	}
	if diff := cmp.Diff("\x1b[3A\x1b[J", buf.String()); diff != "" {
		t.Errorf("output after a block that does not fit (-want +got):\n%s", diff)
	}

	buf.Reset()
	live.Clear()
	if buf.Len() != 0 {
		t.Errorf("Clear with nothing on the screen wrote %q", buf.String())
	}
	live.Draw([]string{"x"})
	buf.Reset()
	live.Clear()
	if diff := cmp.Diff("\x1b[1A\x1b[J", buf.String()); diff != "" {
		t.Errorf("Clear (-want +got):\n%s", diff)
	}
}
