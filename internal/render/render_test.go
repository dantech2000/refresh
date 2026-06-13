package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/ui"
)

func TestPaintLevels(t *testing.T) {
	// ColorNone is a pass-through.
	if got := New(ColorNone, true).Paint(Mocha.Green, "ok"); got != "ok" {
		t.Fatalf("ColorNone Paint = %q, want %q", got, "ok")
	}
	// Truecolor emits a 24-bit SGR.
	if got := New(ColorTrue, true).Paint(Color{1, 2, 3}, "x"); got != "\x1b[38;2;1;2;3mx\x1b[0m" {
		t.Fatalf("ColorTrue Paint = %q", got)
	}
	// 256 emits an indexed SGR; bold prefixes the attribute.
	if got := New(Color256, true).Bold(Mocha.Red, "x"); !strings.HasPrefix(got, "\x1b[1;38;5;") {
		t.Fatalf("Color256 Bold = %q, want 1;38;5; prefix", got)
	}
}

// Locks the production theming to the exact truecolor (and 256-color) SGR codes
// the design proof used — so the real CLI emits the same Catppuccin palette,
// not an approximation.
func TestThemingMatchesProofPalette(t *testing.T) {
	tc := New(ColorTrue, true)
	// Healthy ● is Catppuccin green {166,227,161}; the label stays uncolored.
	if got, want := tc.Token(Healthy, "current"), "\x1b[38;2;166;227;161m●\x1b[0m current"; got != want {
		t.Errorf("truecolor Healthy token = %q, want %q", got, want)
	}
	// Fail ✗ is red {243,139,168}.
	if got, want := tc.Glyph(Fail), "\x1b[38;2;243;139;168m✗\x1b[0m"; got != want {
		t.Errorf("truecolor Fail glyph = %q, want %q", got, want)
	}
	// 256-color terminals get an indexed code from the same palette.
	if got := New(Color256, true).Glyph(Progress); !strings.HasPrefix(got, "\x1b[38;5;") {
		t.Errorf("256-color Progress glyph = %q, want 38;5; prefix", got)
	}
}

func TestRGBTo256(t *testing.T) {
	if got := rgbTo256(Color{0, 0, 0}); got != 16 {
		t.Errorf("black = %d, want 16", got)
	}
	if got := rgbTo256(Color{255, 255, 255}); got != 231 {
		t.Errorf("white = %d, want 231", got)
	}
	if got := rgbTo256(Mocha.Green); got < 16 || got > 255 {
		t.Errorf("green index %d out of range", got)
	}
}

func TestDetectLevelNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if lvl := DetectLevel(&bytes.Buffer{}); lvl != ColorNone {
		t.Fatalf("NO_COLOR set: level = %d, want ColorNone", lvl)
	}
}

func TestTokenUnicodeAndASCII(t *testing.T) {
	if got := New(ColorNone, true).Token(Healthy, "current"); got != "● current" {
		t.Fatalf("unicode token = %q, want %q", got, "● current")
	}
	if got := New(ColorNone, false).Token(Fail, "blocked"); got != "[X] blocked" {
		t.Fatalf("ascii token = %q, want %q", got, "[X] blocked")
	}
	// Color is additive: ColorNone output never contains an escape.
	if got := New(ColorNone, true).Tokenf(Warn, "2 stale"); strings.Contains(got, "\x1b") {
		t.Fatalf("ColorNone token contains ANSI: %q", got)
	}
}

func TestStatusFromString(t *testing.T) {
	cases := map[string]Status{
		"ACTIVE":      Healthy,
		"UPDATING":    Warn,
		"FAILED":      Fail,
		"IN_PROGRESS": Progress,
		"UNKNOWN":     Unknown,
	}
	for in, want := range cases {
		if got := StatusFromString(in); got != want {
			t.Errorf("StatusFromString(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTableAlignment(t *testing.T) {
	th := New(ColorNone, true)
	got := th.NewTable(
		ui.Column{Title: "NAME"},
		ui.Column{Title: "AGE"},
	).Row("prod", "2").Row("x", "10").Render()

	want := []string{
		"NAME  AGE",
		"prod  2",
		"x     10",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestKVAlignment(t *testing.T) {
	th := New(ColorNone, true)
	got := th.KV([][2]string{{"version", "1.32"}, {"status", "ACTIVE"}})
	want := []string{"version  1.32", "status   ACTIVE"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBar(t *testing.T) {
	if got := New(ColorNone, true).Bar(2, 3, 9, Mocha.Teal); got != "▕██████░░░▏" {
		t.Fatalf("unicode bar = %q", got)
	}
	if got := New(ColorNone, false).Bar(1, 2, 6, Mocha.Teal); got != "[###---]" {
		t.Fatalf("ascii bar = %q", got)
	}
}

func TestCallout(t *testing.T) {
	got := New(ColorNone, true).Callout("VERDICT", []string{"  body"}, 20)
	if !strings.HasPrefix(got[0], "━━ VERDICT ") {
		t.Errorf("callout top = %q", got[0])
	}
	if last := got[len(got)-1]; last != strings.Repeat("━", 20) {
		t.Errorf("callout bottom = %q", last)
	}
}

func TestLiveAppendNonTTY(t *testing.T) {
	var buf bytes.Buffer
	lr := New(ColorNone, true).NewLiveRegion(&buf) // buf is not a *os.File -> not a TTY
	lr.Draw([]string{"a", "b"})
	lr.Draw([]string{"c"})
	want := "a\nb\n\nc\n"
	if buf.String() != want {
		t.Fatalf("non-TTY append = %q, want %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("non-TTY output contains ANSI cursor codes: %q", buf.String())
	}
}

func TestLiveRedrawTTY(t *testing.T) {
	var buf bytes.Buffer
	lr := &LiveRegion{w: &buf, tty: true} // force the in-place path
	lr.Draw([]string{"x", "y"})
	lr.Draw([]string{"z"})
	// Second frame must rewind over the 2 lines of the first and clear down.
	if !strings.Contains(buf.String(), "\x1b[2A\x1b[0J") {
		t.Fatalf("redraw missing cursor-up/clear: %q", buf.String())
	}
}
