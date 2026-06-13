// Package render is the human-facing visual system for refresh: a small design
// language (palette, status tokens, primitives) plus an in-place live-region
// printer. It is line-oriented CLI output — NOT a TUI (no alternate screen, no
// input loop). Color is always additive: every status also carries a glyph and
// a label, so output stays legible with --no-color, when piped, or on a
// non-UTF-8 terminal.
//
// Machine formats (-o json/yaml/plain) do not go through this package; they
// stay in runner.EncodeStdout. render only styles the human `table`/detail
// surfaces, reusing the ANSI-width math in internal/ui.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fatih/color"
)

// ColorLevel is the terminal's color capability.
type ColorLevel int

const (
	ColorNone ColorLevel = iota // no ANSI color (NO_COLOR, piped, dumb term)
	Color256                    // 256-color palette
	ColorTrue                   // 24-bit truecolor
)

// Color is a 24-bit RGB color.
type Color struct{ R, G, B uint8 }

// Palette is the named color set the design system draws from.
type Palette struct {
	Text, Subtext, Dim, White, Rule                   Color
	Green, Yellow, Red, Teal, Blue, Mauve, Peach, Sky Color
}

// Mocha is the default palette (Catppuccin Mocha) — a real, widely-used
// terminal theme that downgrades cleanly to 256-color.
var Mocha = Palette{
	Text:    Color{205, 214, 244},
	Subtext: Color{166, 173, 200},
	Dim:     Color{108, 112, 134},
	White:   Color{235, 239, 252},
	Rule:    Color{69, 71, 90},
	Green:   Color{166, 227, 161},
	Yellow:  Color{249, 226, 175},
	Red:     Color{243, 139, 168},
	Teal:    Color{148, 226, 213},
	Blue:    Color{137, 180, 250},
	Mauve:   Color{203, 166, 247},
	Peach:   Color{250, 179, 135},
	Sky:     Color{137, 220, 235},
}

// Theme renders strings at a given color level and Unicode capability. It is
// safe to copy; it holds no state.
type Theme struct {
	Level   ColorLevel
	Unicode bool
	Pal     Palette
}

// New builds a Theme with an explicit level/unicode (used in tests for
// determinism).
func New(level ColorLevel, unicode bool) *Theme {
	return &Theme{Level: level, Unicode: unicode, Pal: Mocha}
}

// Default detects the color level and Unicode support from w and the
// environment.
func Default(w io.Writer) *Theme {
	return &Theme{Level: DetectLevel(w), Unicode: detectUnicode(), Pal: Mocha}
}

// DetectLevel reports the color capability for w. Honors NO_COLOR, fatih/color's
// global (set by --no-color), TTY-ness, COLORTERM and TERM.
func DetectLevel(w io.Writer) ColorLevel {
	if color.NoColor || os.Getenv("NO_COLOR") != "" {
		return ColorNone
	}
	if !isTerminal(w) {
		return ColorNone
	}
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return ColorTrue
	}
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return ColorNone
	}
	if strings.Contains(term, "256") {
		return Color256
	}
	// Modern terminals that don't advertise COLORTERM still do at least 256.
	return Color256
}

func detectUnicode() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToLower(os.Getenv(k)); v != "" {
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return true // assume a modern UTF-8 terminal
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// Paint wraps s in the foreground color for the active level (no-op at
// ColorNone). Bold is the same with the bold attribute.
func (t *Theme) Paint(c Color, s string) string { return t.paint(c, s, false) }
func (t *Theme) Bold(c Color, s string) string  { return t.paint(c, s, true) }

func (t *Theme) paint(c Color, s string, bold bool) string {
	if t.Level == ColorNone {
		return s
	}
	var code string
	switch t.Level {
	case ColorTrue:
		code = fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B)
	case Color256:
		code = fmt.Sprintf("38;5;%d", rgbTo256(c))
	default:
		return s
	}
	if bold {
		code = "1;" + code
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// glyph returns the Unicode glyph, or its ASCII fallback when the terminal
// isn't UTF-8 capable.
func (t *Theme) glyph(unicode, ascii string) string {
	if t.Unicode {
		return unicode
	}
	return ascii
}

// rgbTo256 maps a 24-bit color to the nearest xterm-256 index (6x6x6 cube +
// grayscale ramp) for terminals without truecolor.
func rgbTo256(c Color) int {
	r, g, b := int(c.R), int(c.G), int(c.B)
	if r == g && g == b {
		if r < 8 {
			return 16
		}
		if r > 248 {
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
		default:
			return (v - 35) / 40
		}
	}
	return 16 + 36*cube(r) + 6*cube(g) + cube(b)
}
