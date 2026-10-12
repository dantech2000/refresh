// Package cmdutil holds what the command packages share: the IO streams, the
// factory, exit errors, and the output-format flag.
package cmdutil

import (
	"io"
	"os"

	"golang.org/x/term"
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
}

// System returns the streams of the process.
func System() *IOStreams {
	return &IOStreams{
		In:     os.Stdin,
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		InTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		OutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		ErrTTY: term.IsTerminal(int(os.Stderr.Fd())),
	}
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
