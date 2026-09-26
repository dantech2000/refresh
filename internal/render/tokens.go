package render

import (
	"fmt"
	"io"

	"github.com/dantech2000/refresh/internal/ui"
)

// Status is a semantic state with a fixed glyph + color, so meaning is carried
// by the glyph (not color alone).
type Status int

const (
	Neutral  Status = iota // •  dim
	Healthy                // ●  green  — active / current / pass
	Warn                   // ▲  yellow — stale / warning
	Fail                   // ✗  red    — failed / blocked
	Progress               // ◷  teal   — updating / in-progress
	Unknown                // ○  dim    — unknown / n-a
)

func (t *Theme) tokenParts(s Status) (glyph, ascii string, col Color) {
	switch s {
	case Healthy:
		return "●", "[OK]", t.Pal.Green
	case Warn:
		return "▲", "[!]", t.Pal.Yellow
	case Fail:
		return "✗", "[X]", t.Pal.Red
	case Progress:
		return "◷", "[~]", t.Pal.Teal
	case Unknown:
		return "○", "[?]", t.Pal.Dim
	default:
		return "•", "-", t.Pal.Dim
	}
}

// Glyph renders just the status glyph, colored.
func (t *Theme) Glyph(s Status) string {
	g, a, col := t.tokenParts(s)
	return t.Paint(col, t.glyph(g, a))
}

// Token renders "glyph label" — the glyph colored, the label in the caller's
// default text color. Pass an empty label for the glyph alone.
func (t *Theme) Token(s Status, label string) string {
	if label == "" {
		return t.Glyph(s)
	}
	return t.Glyph(s) + " " + label
}

// Tokenf is Token with the label colored to match the status (used where the
// whole token should read as one unit, e.g. a verdict).
func (t *Theme) Tokenf(s Status, label string) string {
	_, _, col := t.tokenParts(s)
	if label == "" {
		return t.Glyph(s)
	}
	return t.Glyph(s) + " " + t.Paint(col, label)
}

// StatusFromString maps a free-form AWS/Kubernetes status to a Status using the
// single classification table in internal/ui (so the vocabulary never drifts).
func StatusFromString(s string) Status {
	switch ui.ClassifyStatus(s) {
	case ui.StatusGood:
		return Healthy
	case ui.StatusWarning:
		return Warn
	case ui.StatusBad:
		return Fail
	case ui.StatusInProgress:
		return Progress
	case ui.StatusUnknown:
		return Unknown
	default:
		return Neutral
	}
}

// Line is one status line: the token with the label colored to match (see
// Tokenf), for notices and banners such as "Update started" or "Nothing to
// do". Without color or Unicode it still reads, e.g. "[!] 2 stale".
func (t *Theme) Line(s Status, format string, args ...any) string {
	return t.Tokenf(s, fmt.Sprintf(format, args...))
}

// Notef writes one status line (see Line) to w, themed for w itself: stderr
// can be colored when stdout is piped, and the reverse.
func Notef(w io.Writer, s Status, format string, args ...any) {
	_, _ = fmt.Fprintln(w, Default(w).Line(s, format, args...))
}

// SpinnerDone stops s and writes msg as a Healthy status line on the
// spinner's stream (stderr, and only when it is a terminal). Spinner copy is
// sentence case with no closing punctuation: "Upgrade plan computed".
func SpinnerDone(s *ui.FunSpinner, msg string) {
	s.Done(Default(ui.Stderr).Line(Healthy, "%s", msg))
}

// Plural is n and noun, with an "s" unless n is 1: "1 cluster", "2 clusters".
func Plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
