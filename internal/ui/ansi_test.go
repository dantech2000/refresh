package ui

import (
	"strings"
	"testing"
)

func TestVisibleWidthAndPadANSI(t *testing.T) {
	green := "\x1b[32mPASS\x1b[0m"
	if VisibleWidth(green) != 4 {
		t.Fatalf("VisibleWidth failed: got %d want %d", VisibleWidth(green), 4)
	}
	padded := PadANSI(green, 8, AlignLeft)
	if VisibleWidth(padded) != 8 {
		t.Fatalf("PadANSI failed: got visible %d want %d", VisibleWidth(padded), 8)
	}
}

func TestVisibleWidthMalformedEscape(t *testing.T) {
	// A runaway escape sequence must not swallow the rest of the string.
	malformed := "\x1b[" + strings.Repeat("9", 40) + "hello"
	if got := VisibleWidth(malformed); got == 0 {
		t.Fatalf("VisibleWidth(malformed) = %d, want > 0", got)
	}
}

func TestPadANSIRightAlign(t *testing.T) {
	out := PadANSI("123", 6, AlignRight)
	if VisibleWidth(out) != 6 {
		t.Fatalf("right align visible length: got %d want %d", VisibleWidth(out), 6)
	}
	if out[len(out)-3:] != "123" {
		t.Fatalf("right align content: expected suffix '123', got %q", out)
	}
	if got := PadANSI("already-long", 3, AlignLeft); got != "already-long" {
		t.Fatalf("PadANSI long = %q", got)
	}
}

func TestTruncateANSI(t *testing.T) {
	yellow := "\x1b[33mWARNING\x1b[0m"
	out := TruncateANSI(yellow, 4)
	if VisibleWidth(out) != 4 {
		t.Fatalf("TruncateANSI visible length: got %d want %d", VisibleWidth(out), 4)
	}
}

func TestTruncateANSIEdges(t *testing.T) {
	if got := TruncateANSI("abcdef", 0); got != "" {
		t.Fatalf("TruncateANSI width 0 = %q", got)
	}
	if got := TruncateANSI("abcdef", 2); got != "ab" {
		t.Fatalf("TruncateANSI width 2 = %q", got)
	}
	if got := TruncateANSI("hello", 10); got != "hello" {
		t.Fatalf("TruncateANSI below limit = %q", got)
	}
	if got := TruncateANSI("hello world", 8); got != "hello..." {
		t.Fatalf("TruncateANSI ellipsis = %q", got)
	}
	colored := "\x1b[31mabcdef\x1b[0m"
	if got := TruncateANSI(colored, 6); !strings.Contains(got, "abcdef") {
		t.Fatalf("TruncateANSI colored = %q", got)
	}
	if got := TruncateANSI(colored, 4); VisibleWidth(got) != 4 {
		t.Fatalf("TruncateANSI colored width = %q", got)
	}
}

func TestTruncateANSIAppendsResetForColoredCut(t *testing.T) {
	// Cutting a colored string discards its trailing reset; the truncation
	// must append one so the color cannot bleed into the rest of the line.
	colored := "\x1b[32mABCDEFGH\x1b[0m"
	got := TruncateANSI(colored, 6)
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("TruncateANSI colored cut lost its reset code: %q", got)
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[string]StatusCategory{
		"ACTIVE":      StatusGood,
		"active":      StatusGood,
		"SUCCESSFUL":  StatusGood,
		"UPDATING":    StatusWarning,
		"PENDING":     StatusWarning,
		"FAILED":      StatusBad,
		"DEGRADED":    StatusBad,
		"CANCELLED":   StatusBad,
		"IN PROGRESS": StatusInProgress,
		"IN_PROGRESS": StatusInProgress,
		"UNKNOWN":     StatusUnknown,
		"whatever":    StatusNeutral,
	}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Errorf("ClassifyStatus(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestStatusColorStringNeutralUnchanged(t *testing.T) {
	if got := StatusColorString("whatever"); got != "whatever" {
		t.Fatalf("neutral status should be uncolored, got %q", got)
	}
}
