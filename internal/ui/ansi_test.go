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

// REF-67: width must be measured in display cells, not runes — CJK ideographs
// and emoji occupy 2 columns.
func TestVisibleWidthWideRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		want int
	}{
		{"ascii", "abc", 3},
		{"cjk", "世界", 4},
		{"emoji", "🚀", 2},
		{"mixed", "a世b", 4},
		{"colored cjk", "\x1b[32m世\x1b[0m", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := VisibleWidth(tc.s); got != tc.want {
				t.Errorf("VisibleWidth(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
	if got := VisibleWidth(PadANSI("世", 6, AlignLeft)); got != 6 {
		t.Errorf("PadANSI wide cell: visible width = %d, want 6", got)
	}
}

// REF-65: PlainCell must strip ANSI and neutralize embedded tab/newline/CR so a
// single logical cell stays a single TSV field.
func TestPlainCell(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[32mPASS\x1b[0m", "PASS"},
		{"a\tb", "a b"},
		{"line1\nline2", "line1 line2"},
		{"a\r\nb", "a  b"},
		{"k=v\tx=y", "k=v x=y"},
	} {
		if got := PlainCell(tc.in); got != tc.want {
			t.Errorf("PlainCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(PlainCell(tc.in), "\t\n\r") {
			t.Errorf("PlainCell(%q) still contains a TSV-breaking char", tc.in)
		}
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

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[32mPASS\x1b[0m"); got != "PASS" {
		t.Fatalf("StripANSI colored = %q, want PASS", got)
	}
	if got := StripANSI("plain"); got != "plain" {
		t.Fatalf("StripANSI plain = %q", got)
	}
	if got := StripANSI(""); got != "" {
		t.Fatalf("StripANSI empty = %q", got)
	}
}

func TestSetPlainOutput(t *testing.T) {
	SetPlainOutput(true)
	if !PlainOutput() {
		t.Fatal("PlainOutput should be true after SetPlainOutput(true)")
	}
	SetPlainOutput(false)
	if PlainOutput() {
		t.Fatal("PlainOutput should be false after SetPlainOutput(false)")
	}
}
