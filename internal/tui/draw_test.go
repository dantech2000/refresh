package tui

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/view"
)

var unicode = view.Theme{Unicode: true}.Border()

func TestLineFitPadsAndTruncates(t *testing.T) {
	l := Line{tx("hello"), sub(" world")}
	if got := l.Fit(14).Plain(); got != "hello world   " {
		t.Errorf("pad: %q", got)
	}
	if got := l.Fit(7).Plain(); got != "hello …" {
		t.Errorf("cut: %q", got)
	}
	if got := l.Fit(0); got != nil {
		t.Errorf("zero width: %v", got)
	}
}

// Cut splits only between graphemes: half of a wide rune becomes a space,
// and a cluster that straddles an edge is never broken.
func TestLineCutKeepsCellsAndGraphemes(t *testing.T) {
	for _, tc := range []struct {
		line     Line
		from, to int
		want     string
	}{
		{Line{tx("abc"), sub("def")}, 1, 5, "bcde"},
		{Line{tx("a日本b")}, 0, 2, "a "},
		{Line{tx("a日本b")}, 1, 3, "日"},
		{Line{tx("a日本b")}, 2, 4, "  "},
		{Line{tx("👩‍💻x")}, 1, 3, " x"},
		{Line{tx("x👩‍💻")}, 0, 2, "x "},
	} {
		if got := tc.line.Cut(tc.from, tc.to).Plain(); got != tc.want {
			t.Errorf("Cut(%d, %d) of %q = %q, want %q", tc.from, tc.to, tc.line.Plain(), got, tc.want)
		}
	}
}

func TestOverlayAndBox(t *testing.T) {
	base := Block{{tx("0123456789")}, {tx("abcdefghij")}, {tx("ABCDEFGHIJ")}}
	dlg := box(Line{tx("t")}, Block{{tx("x")}}, 8, unicode)
	out := overlay(base, dlg, 1, 0)
	want := []string{"0╭─ t ──╮9", "a│ x    │j", "A╰──────╯J"}
	for i, l := range out {
		if l.Plain() != want[i] || l.Width() != 10 {
			t.Errorf("row %d = %q (%d cells), want %q", i, l.Plain(), l.Width(), want[i])
		}
	}
}

func TestWrapAndBar(t *testing.T) {
	if got := wrap("one two three four", 9); strings.Join(got, "|") != "one two|three|four" {
		t.Errorf("wrap = %q", got)
	}
	if got := wrap("abcdefghij", 4); strings.Join(got, "|") != "abcd|efgh|ij" {
		t.Errorf("a word wider than the line: %q", got)
	}
	if got := wrap("a\n\nb", 9); strings.Join(got, "|") != "a||b" {
		t.Errorf("paragraphs: %q", got)
	}
	if got := bar(10, 0.5, unicode).Plain(); got != "█████░░░░░" {
		t.Errorf("bar = %q", got)
	}
	if got := bar(4, 2, unicode).Plain(); got != "████" {
		t.Errorf("bar past 1 = %q", got)
	}
}

func TestJoinRight(t *testing.T) {
	if got := joinRight(Line{tx("left")}, Line{tx("right")}, 12).Plain(); got != "left   right" {
		t.Errorf("fits: %q", got)
	}
	if got := joinRight(Line{tx("a long left side")}, Line{tx("right")}, 12).Plain(); got != "a lon… right" || width(got) != 12 {
		t.Errorf("too wide: %q", got)
	}
}

func TestSplitCellsKeepsWideCharactersWhole(t *testing.T) {
	head, rest := splitCells("日本語", 3)
	if head != "日" || rest != "本語" {
		t.Errorf("splitCells = %q, %q", head, rest)
	}
	head, rest = splitCells("日", 1)
	if head != "日" || rest != "" {
		t.Errorf("one grapheme always fits: %q, %q", head, rest)
	}
}

func TestBlockFitAndHjoin(t *testing.T) {
	b := Block{{tx("a")}}.fit(3, 2)
	if len(b) != 2 || b[0].Plain() != "a  " || b[1].Plain() != "   " {
		t.Errorf("fit = %v", b)
	}
	j := hjoin(2, col{Block{{tx("l")}}, 2}, vrule(2, unicode), col{Block{{tx("r")}, {tx("rr")}}, 2})
	if j[0].Plain() != "l │r " || j[1].Plain() != "  │rr" {
		t.Errorf("hjoin = %q, %q", j[0].Plain(), j[1].Plain())
	}
}

// A frame without color is its plain text, and one with color paints only
// what has a role.
func TestRenderWithoutColorIsPlain(t *testing.T) {
	l := Line{bold(view.RoleAccent, "head"), sp(1), chip("q")}
	if got := l.Render(newPainter(view.Theme{}), view.RoleBase); got != l.Plain() {
		t.Errorf("no color: %q", got)
	}
	got := l.Render(newPainter(view.Theme{Depth: view.TrueColor}), view.RoleBase)
	if !strings.Contains(got, "\x1b[38;2;203;166;247m\x1b[48;2;30;30;46m\x1b[1mhead\x1b[0m") || !strings.Contains(got, "\x1b[48;2;49;50;68m q \x1b[0m") {
		t.Errorf("true color: %q", got)
	}
	got = l.Render(newPainter(view.Theme{Depth: view.Color256}), view.RoleBase)
	if !strings.Contains(got, "\x1b[38;5;") || strings.Contains(got, "38;2;") {
		t.Errorf("256 colors: %q", got)
	}
}
