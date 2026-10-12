package tui

import (
	"strings"
	"testing"
)

func TestLineFitPadsAndTruncates(t *testing.T) {
	l := Line{tx("hello "), fg(colGreen, "● world")}
	if got := l.Fit(20); got.Width() != 20 || got.Plain() != "hello ● world       " {
		t.Fatalf("Fit(20) = %q", got.Plain())
	}
	if got := l.Fit(8); got.Width() != 8 || got.Plain() != "hello ●…" {
		t.Fatalf("Fit(8) = %q", got.Plain())
	}
	if got := l.Fit(0); got != nil {
		t.Fatalf("Fit(0) = %q", got.Plain())
	}
}

func TestLineCutKeepsCellsAndWideRunes(t *testing.T) {
	l := Line{tx("ab"), fg(colRed, "日本"), tx("cd")}
	cases := []struct {
		from, to int
		want     string
	}{
		{0, 2, "ab"},
		{2, 6, "日本"},
		{3, 7, " 本c"}, // half of 日 becomes a space
		{5, 8, " cd"},
		{0, 8, "ab日本cd"},
	}
	for _, c := range cases {
		got := l.Cut(c.from, c.to)
		if got.Plain() != c.want || got.Width() != c.to-c.from {
			t.Errorf("Cut(%d,%d) = %q (%d cells), want %q", c.from, c.to, got.Plain(), got.Width(), c.want)
		}
	}
}

func TestOverlayAndBox(t *testing.T) {
	base := Block{{tx("0123456789")}, {tx("abcdefghij")}}
	b := box(Line{tx("t")}, Block{{tx("x")}}, 6, colSurface1)
	if len(b) != 3 || b[0].Width() != 6 || b[1].Plain() != "│ x  │" {
		t.Fatalf("box = %q %q %q", b[0].Plain(), b[1].Plain(), b[2].Plain())
	}
	out := overlay(base, Block{{tx("XY")}}, 3, 1)
	if out[1].Plain() != "abcXYfghij" || out[0].Plain() != "0123456789" {
		t.Fatalf("overlay = %q / %q", out[0].Plain(), out[1].Plain())
	}
}

func TestWrapAndBar(t *testing.T) {
	got := wrap("one two three four", 9)
	if len(got) != 3 || got[0] != "one two" || got[1] != "three" || got[2] != "four" {
		t.Fatalf("wrap = %q", got)
	}
	if b := bar(10, 0.5, 0.2); b.Width() != 10 || b.Plain() != "███████░░░" {
		t.Fatalf("bar = %q", b.Plain())
	}
	if b := bar(10, 2, 2); b.Width() != 10 {
		t.Fatalf("clamped bar = %q", b.Plain())
	}
}

func TestJoinRight(t *testing.T) {
	if got := joinRight(Line{tx("ab")}, Line{tx("cd")}, 8); got.Plain() != "ab    cd" {
		t.Fatalf("joinRight = %q", got.Plain())
	}
	if got := joinRight(Line{tx("abcdef")}, Line{tx("cd")}, 6); got.Width() != 6 {
		t.Fatalf("crowded joinRight = %q", got.Plain())
	}
}

func TestLineCutKeepsGraphemesWhole(t *testing.T) {
	coder := "\U0001F469\u200d\U0001F4BB" // woman technologist: one grapheme, two cells
	l := Line{tx("A" + coder + "B")}
	if w := l.Width(); w != 4 {
		t.Fatalf("width = %d, want 4", w)
	}
	cases := []struct {
		from, to int
		want     string
	}{
		{1, 3, coder},
		{0, 2, "A "}, // the grapheme straddles the right edge
		{2, 4, " B"}, // and the left edge
		{0, 4, "A" + coder + "B"},
	}
	for _, c := range cases {
		got := l.Cut(c.from, c.to)
		if got.Plain() != c.want || got.Width() != c.to-c.from {
			t.Errorf("Cut(%d,%d) = %q (%d cells), want %q", c.from, c.to, got.Plain(), got.Width(), c.want)
		}
	}
}

func TestWrapSplitsWordsWiderThanTheLine(t *testing.T) {
	long := strings.Repeat("x", 25)
	got := wrap("a "+long+" b", 10)
	joined := strings.Join(got, "")
	if strings.Count(joined, "x") != 25 {
		t.Fatalf("wrap lost characters: %q", got)
	}
	for _, l := range got {
		if width(l) > 10 {
			t.Fatalf("line %q is wider than 10", l)
		}
	}
}

func TestSplitCellsKeepsWideCharactersWhole(t *testing.T) {
	head, rest := splitCells("ab日本語", 3)
	if head != "ab" || rest != "日本語" {
		t.Fatalf("splitCells = %q, %q", head, rest)
	}
	got := wrap(strings.Repeat("日", 7), 4)
	for _, l := range got {
		if width(l) > 4 || strings.ContainsRune(l, ' ') {
			t.Fatalf("wrap of wide text = %q", got)
		}
	}
	if head, _ := splitCells("日", 1); head != "日" {
		t.Fatal("splitCells must take one grapheme even when it is wider than w")
	}
}
