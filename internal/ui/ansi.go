package ui

import (
	"strings"

	"github.com/fatih/color"
	"github.com/mattn/go-runewidth"
)

// This file is the single home for ANSI-aware string measurement, padding,
// truncation, and status coloring. Table renderers and command formatters
// must use these helpers instead of growing their own copies (the codebase
// previously had three diverging truncation implementations, one of which
// sliced colored strings mid-escape-sequence).

// Alignment represents horizontal text alignment within a column.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
)

// Column defines a table column with sizing and alignment rules.
type Column struct {
	Title string
	Min   int
	Max   int
	Align Alignment
}

// VisibleWidth returns the printable terminal width of a string, excluding ANSI
// escape codes. Width is measured in display cells, not runes, so CJK
// ideographs and emoji (2 columns) and zero-width/combining marks are counted
// correctly — otherwise PadANSI/TruncateANSI and the table column math drift on
// non-ASCII cells. Hardened against malformed sequences with a bounded escape
// length.
func VisibleWidth(s string) int {
	count := 0
	inEscape := false
	escapeLen := 0
	const maxEscapeLen = 32 // longest reasonable ANSI escape sequence

	for _, r := range s {
		if r == '\x1b' { // ESC
			inEscape = true
			escapeLen = 0
			continue
		}
		if inEscape {
			escapeLen++
			// Bail out of malformed sequences that never terminate.
			if escapeLen > maxEscapeLen {
				inEscape = false
				escapeLen = 0
				continue
			}
			// CSI sequences end with a letter.
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
				escapeLen = 0
			}
			continue
		}
		count += runewidth.RuneWidth(r)
	}
	return count
}

// PadANSI left- or right-aligns a possibly ANSI-colored string to width.
func PadANSI(s string, width int, align Alignment) string {
	vis := VisibleWidth(s)
	if vis >= width {
		return s
	}
	pad := strings.Repeat(" ", width-vis)
	if align == AlignRight {
		return pad + s
	}
	return s + pad
}

// ansiReset terminates any active styling.
const ansiReset = "\x1b[0m"

// TruncateANSI truncates a possibly ANSI-colored string to width, adding an
// ellipsis when there is room. Escape sequences are preserved, and when the
// cut discards a trailing reset code, a reset is appended so the truncated
// cell's color cannot bleed into the rest of the line.
func TruncateANSI(s string, width int) string {
	vis := VisibleWidth(s)
	if vis <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	target := width
	ellipsis := ""
	if width >= 3 {
		target = width - 3
		ellipsis = "..."
	}

	var out strings.Builder
	reserve := target*3 + len(ellipsis) + len(ansiReset)
	if esc := strings.Count(s, "\x1b"); esc > 0 {
		reserve += esc * 8
	}
	if reserve > len(s)+len(ansiReset) {
		reserve = len(s) + len(ansiReset)
	}
	out.Grow(reserve)

	sawEscape := false
	visibleCount := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' { // ESC
			inEscape = true
			sawEscape = true
			out.WriteRune(r)
			continue
		}
		if inEscape {
			out.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		w := runewidth.RuneWidth(r)
		if visibleCount+w > target {
			break
		}
		out.WriteRune(r)
		visibleCount += w
	}
	if sawEscape {
		// The cut may have dropped the original trailing reset.
		out.WriteString(ansiReset)
	}
	out.WriteString(ellipsis)
	return out.String()
}

// StripANSI removes ANSI escape sequences from a string.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	inEscape := false
	escapeLen := 0
	const maxEscapeLen = 32
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			escapeLen = 0
			continue
		}
		if inEscape {
			escapeLen++
			if escapeLen > maxEscapeLen {
				inEscape = false
				escapeLen = 0
				continue
			}
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
				escapeLen = 0
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// PlainCell prepares a value for tab-separated `-o plain` output: it strips
// ANSI codes and replaces any embedded tab/newline/carriage-return with a
// single space. Without this, a cell containing a literal tab or newline (AWS
// tags, multi-line status/error strings) would split one logical row across
// columns or physical lines, breaking `awk -F'\t'` / `cut`. (REF-65)
func PlainCell(s string) string {
	s = StripANSI(s)
	if strings.ContainsAny(s, "\t\n\r") {
		s = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
	}
	return s
}

// plainOutput, when enabled, makes table renderers emit uncolored,
// tab-separated rows without box drawing — grep/awk/cut-friendly output for
// the `-o plain` format.
var plainOutput = false

// SetPlainOutput toggles plain rendering for PTable and DynamicTable.
func SetPlainOutput(enabled bool) { plainOutput = enabled }

// PlainOutput reports whether plain rendering is enabled.
func PlainOutput() bool { return plainOutput }

// StatusCategory classifies a free-form status string for rendering.
type StatusCategory int

const (
	StatusNeutral StatusCategory = iota
	StatusGood
	StatusWarning
	StatusBad
	StatusInProgress
	StatusUnknown
)

// ClassifyStatus maps common AWS/Kubernetes status vocabulary to a render
// category. This is THE status->category table; per-renderer copies drift.
func ClassifyStatus(status string) StatusCategory {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ACTIVE", "RUNNING", "HEALTHY", "PASS", "SUCCESS", "SUCCESSFUL", "ENABLED", "READY", "COMPLETED":
		return StatusGood
	case "WARN", "WARNING", "UPDATING", "PENDING", "SCALING", "CREATING", "DELETING":
		return StatusWarning
	case "FAIL", "FAILED", "ERROR", "CRITICAL", "DISABLED", "DEGRADED", "CANCELLED",
		"CREATE_FAILED", "UPDATE_FAILED", "DELETE_FAILED":
		return StatusBad
	case "IN PROGRESS", "IN_PROGRESS":
		return StatusInProgress
	case "UNKNOWN", "N/A":
		return StatusUnknown
	default:
		return StatusNeutral
	}
}

// StatusColorString colors a status string according to its category.
func StatusColorString(status string) string {
	switch ClassifyStatus(status) {
	case StatusGood:
		return color.GreenString(status)
	case StatusWarning:
		return color.YellowString(status)
	case StatusBad:
		return color.RedString(status)
	case StatusInProgress:
		return color.CyanString(status)
	case StatusUnknown:
		return color.WhiteString(status)
	default:
		return status
	}
}
