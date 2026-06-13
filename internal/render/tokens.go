package render

import "github.com/dantech2000/refresh/internal/ui"

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
