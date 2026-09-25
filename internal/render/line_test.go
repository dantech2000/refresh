package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	if got := New(ColorNone, true).Line(Warn, "%d stale", 2); got != "▲ 2 stale" {
		t.Errorf("unicode line = %q", got)
	}
	if got := New(ColorNone, false).Line(Neutral, "skipped"); got != "- skipped" {
		t.Errorf("ASCII neutral line = %q", got)
	}
	// With color the label takes the status color too.
	if got := New(ColorTrue, true).Line(Healthy, "ok"); strings.Count(got, "\x1b[38;2;166;227;161m") != 2 {
		t.Errorf("truecolor line = %q, want glyph and label green", got)
	}
}

// Notef themes for the writer it gets: a buffer is not a terminal, so no
// escape codes reach it.
func TestNotefPlainForNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	Notef(&buf, Fail, "update %s failed", "u-1")
	if got := buf.String(); strings.Contains(got, "\x1b") || !strings.HasSuffix(got, " update u-1 failed\n") {
		t.Errorf("Notef = %q", got)
	}
}
