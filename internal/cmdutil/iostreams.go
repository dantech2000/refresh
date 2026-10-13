// Package cmdutil holds what the command packages share: the IO streams, the
// factory, exit errors, and the output-format flag.
package cmdutil

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/dantech2000/refresh/internal/view"
)

// IOStreams is the terminal of one run. A command reads and writes through
// it and never through os.Stdin, os.Stdout, or os.Stderr.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	// Whether each stream is a terminal.
	InTTY  bool
	OutTTY bool
	ErrTTY bool

	// NoColor turns color off on both output streams. The root command sets
	// it from --no-color, NO_COLOR, and TERM=dumb.
	NoColor bool

	// ColorDepth is how many colors the terminal shows, and Unicode
	// whether it shows glyphs outside ASCII. System reads both from the
	// environment. They apply to a stream only when it is a terminal and
	// color is on.
	ColorDepth view.ColorDepth
	Unicode    bool
	// TermSize returns the columns and rows of the terminal behind Out.
	// Nil, or zeros, mean that the size is not known.
	TermSize func() (width, height int)
}

// System returns the streams of the process.
func System() *IOStreams {
	return &IOStreams{
		In:         os.Stdin,
		Out:        os.Stdout,
		ErrOut:     os.Stderr,
		InTTY:      term.IsTerminal(int(os.Stdin.Fd())),
		OutTTY:     term.IsTerminal(int(os.Stdout.Fd())),
		ErrTTY:     term.IsTerminal(int(os.Stderr.Fd())),
		ColorDepth: colorDepth(os.Getenv("COLORTERM"), os.Getenv("TERM")),
		Unicode:    unicodeLocale(os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"), os.Getenv("LANG")),
		TermSize: func() (int, int) {
			width, height, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				return 0, 0
			}
			return width, height
		},
	}
}

// colorDepth returns the color depth that the values of COLORTERM and TERM
// promise. A terminal that says nothing about its colors still shows 256.
func colorDepth(colorterm, termName string) view.ColorDepth {
	switch {
	case colorterm == "truecolor" || colorterm == "24bit":
		return view.TrueColor
	case termName == "" || termName == "dumb":
		return view.NoColor
	}
	return view.Color256
}

// unicodeLocale reports whether the locale is UTF-8. The first variable
// that is set decides, in the order that the C library reads them. With
// none set, the terminal is taken to be a modern one.
func unicodeLocale(vars ...string) bool {
	for _, v := range vars {
		if v = strings.ToLower(v); v != "" {
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return true
}

// Theme returns how to draw on Out: with color only when Out may contain
// color.
func (s *IOStreams) Theme() view.Theme {
	return s.theme(s.ColorEnabled())
}

// ErrTheme returns how to draw on ErrOut.
func (s *IOStreams) ErrTheme() view.Theme {
	return s.theme(s.ErrColorEnabled())
}

func (s *IOStreams) theme(color bool) view.Theme {
	th := view.Theme{Unicode: s.Unicode}
	if color {
		th.Depth = s.ColorDepth
	}
	return th
}

// Size returns the columns and rows of the terminal behind Out, or zeros
// when Out is not a terminal or its size is not known.
func (s *IOStreams) Size() (width, height int) {
	if !s.OutTTY || s.TermSize == nil {
		return 0, 0
	}
	return s.TermSize()
}

// ColorEnabled reports whether output written to Out may contain color.
func (s *IOStreams) ColorEnabled() bool {
	return s.OutTTY && !s.NoColor
}

// ErrColorEnabled reports whether output written to ErrOut may contain
// color. Each stream decides alone, so "refresh status 2>log" keeps color on
// the terminal and leaves it out of the file.
func (s *IOStreams) ErrColorEnabled() bool {
	return s.ErrTTY && !s.NoColor
}
