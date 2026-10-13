// Package view draws the output for people: the table of a list command,
// its plain form, and a block of lines that is redrawn in place.
//
// Color is always an addition. Every status has a glyph and a label, so
// the output reads the same without color and without Unicode. The package
// keeps no state: a Theme says how to draw on one stream, and the command
// gets it from its IO streams.
package view

import (
	"strconv"
	"strings"
)

// ColorDepth is how many colors a stream can show.
type ColorDepth int

// The color depths.
const (
	NoColor   ColorDepth = iota // no escape codes at all
	Color256                    // the 256-color palette
	TrueColor                   // 24-bit color
)

// Theme says how to draw on one stream. It is a value with no state.
type Theme struct {
	Depth ColorDepth
	// Unicode allows glyphs outside ASCII.
	Unicode bool
}

// Status is the state of a thing, with one glyph and one color. The glyph
// carries the meaning; the color only repeats it.
type Status int

// The statuses.
const (
	Neutral  Status = iota // no judgement
	Healthy                // active, current, passed
	Warn                   // stale, needs attention
	Fail                   // failed, blocked
	Progress               // changing
	Unknown                // not known, could not be read
)

// rgb is a 24-bit color.
type rgb struct{ r, g, b uint8 }

// The palette is Catppuccin Mocha, which reads well on a dark terminal and
// maps cleanly to 256 colors.
var (
	colorDim    = rgb{108, 112, 134}
	colorWhite  = rgb{235, 239, 252}
	colorGreen  = rgb{166, 227, 161}
	colorYellow = rgb{249, 226, 175}
	colorRed    = rgb{243, 139, 168}
	colorTeal   = rgb{148, 226, 213}
	colorMauve  = rgb{203, 166, 247}
	colorPeach  = rgb{250, 179, 135}
)

// parts returns the glyph of s, its ASCII form, and its color.
func (s Status) parts() (glyph, ascii string, c rgb) {
	switch s {
	case Healthy:
		return "●", "[OK]", colorGreen
	case Warn:
		return "▲", "[!]", colorYellow
	case Fail:
		return "✗", "[X]", colorRed
	case Progress:
		return "◷", "[~]", colorTeal
	case Unknown:
		return "○", "[?]", colorDim
	}
	return "•", "-", colorDim
}

// Glyph returns the glyph of s in its color.
func (t Theme) Glyph(s Status) string {
	glyph, ascii, c := s.parts()
	if !t.Unicode {
		glyph = ascii
	}
	return t.paint(c, glyph, false)
}

// Token returns the glyph of s, a space, and label. The label keeps the
// default color of the terminal.
func (t Theme) Token(s Status, label string) string {
	if label == "" {
		return t.Glyph(s)
	}
	return t.Glyph(s) + " " + label
}

// Heading returns title as the heading of a view or of a section.
func (t Theme) Heading(title string) string { return t.paint(colorMauve, title, true) }

// Strong returns s as text that stands out.
func (t Theme) Strong(s string) string { return t.paint(colorWhite, s, false) }

// Dim returns s as text of less weight: a column title, an empty cell.
func (t Theme) Dim(s string) string { return t.paint(colorDim, s, false) }

// Caution returns s as a notice that is not an error.
func (t Theme) Caution(s string) string { return t.paint(colorYellow, s, false) }

// paint wraps s in the escape codes for c at the depth of the theme.
func (t Theme) paint(c rgb, s string, bold bool) string {
	var code string
	switch t.Depth {
	case TrueColor:
		code = "38;2;" + strconv.Itoa(int(c.r)) + ";" + strconv.Itoa(int(c.g)) + ";" + strconv.Itoa(int(c.b))
	case Color256:
		code = "38;5;" + strconv.Itoa(to256(c))
	default:
		return s
	}
	if bold {
		code = "1;" + code
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// to256 returns the index in the xterm 256-color palette nearest to c: the
// 6x6x6 cube, or the gray ramp for a gray.
func to256(c rgb) int {
	r, g, b := int(c.r), int(c.g), int(c.b)
	if r == g && g == b {
		switch {
		case r < 8:
			return 16
		case r > 248:
			return 231
		}
		return 232 + (r-8)*24/247
	}
	cube := func(v int) int {
		switch {
		case v < 48:
			return 0
		case v < 115:
			return 1
		}
		return (v - 35) / 40
	}
	return 16 + 36*cube(r) + 6*cube(g) + cube(b)
}

// StatusOf maps an AWS status string, such as ACTIVE or UPDATE_FAILED, to a
// Status. AWS can add a status at any time, so one that is not known is
// Neutral. The values of refresh's own enums are mapped with a switch on
// their type, not through this function.
func StatusOf(awsStatus string) Status {
	switch strings.ToUpper(strings.TrimSpace(awsStatus)) {
	case "ACTIVE", "RUNNING", "SUCCESSFUL":
		return Healthy
	case "PENDING":
		return Warn
	case "FAILED", "DEGRADED", "CANCELLED", "CREATE_FAILED", "UPDATE_FAILED", "DELETE_FAILED":
		return Fail
	case "UPDATING", "CREATING", "DELETING", "INPROGRESS":
		return Progress
	case "", "UNKNOWN":
		return Unknown
	}
	return Neutral
}
