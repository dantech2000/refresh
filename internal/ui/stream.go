package ui

import (
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
	"github.com/pterm/pterm"
)

// Color is decided per stream: stdout is colored only when stdout is a color
// terminal, and stderr only when stderr is one. NO_COLOR, --no-color and
// TERM=dumb turn color off on both. fatih/color's global NoColor follows
// stdout alone, so anything written to stderr goes through Stderr (which
// strips ANSI when stderr is not a color terminal) and, for fatih colors,
// ColorFor / StderrColor.

// isTerminalFd reports whether fd is a terminal. A var so tests can fake
// which streams are TTYs.
var isTerminalFd = func(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// colorOff is set by --no-color.
var colorOff atomic.Bool

// SetColorDisabled records an explicit --no-color. It turns color off on
// every stream.
func SetColorDisabled(disabled bool) { colorOff.Store(disabled) }

// ColorDisabled reports whether color is off on every stream: --no-color, a
// non-empty NO_COLOR (no-color.org), or TERM=dumb.
func ColorDisabled() bool {
	return colorOff.Load() || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
}

// fder is a writer backed by a file descriptor (*os.File, Stderr).
type fder interface{ Fd() uintptr }

// IsTerminal reports whether w writes to a terminal. Writers without a file
// descriptor (buffers, pipes wrapped in other writers) are not terminals.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(fder)
	if !ok {
		return false
	}
	if file, ok := w.(*os.File); ok && file == nil {
		return false
	}
	return isTerminalFd(f.Fd())
}

// IsStderr reports whether w writes to the process's stderr.
func IsStderr(w io.Writer) bool {
	if w == Stderr {
		return true
	}
	f, ok := w.(*os.File)
	return ok && f != nil && f == os.Stderr
}

// StreamColor reports whether ANSI color may be written to w.
func StreamColor(w io.Writer) bool {
	return !ColorDisabled() && IsTerminal(w)
}

// InitColor sets the stdout color decision of both output libraries from
// stdout alone: fatih/color's global NoColor and pterm's color switch. pterm
// otherwise keeps color on when stdout is piped or TERM=dumb, so tree and
// pterm tables would write escape codes into a file or pipe.
func InitColor() {
	on := StreamColor(os.Stdout)
	color.NoColor = !on
	if on {
		pterm.EnableColor()
	} else {
		pterm.DisableColor()
	}
}

// ResetOutputState clears the process-wide output switches that commands
// only ever turn on (--no-color, -o plain) and re-derives the color decision.
// Tests that run several commands in one process call it between runs, so
// one run's -o plain or --no-color cannot leak into the next.
func ResetOutputState() {
	colorOff.Store(false)
	SetPlainOutput(false)
	InitColor()
}

// ColorFor returns a fatih color for writing to w. For stderr, color is on
// iff stderr is a color terminal, whatever stdout is. For any other writer
// it follows fatih's global (the stdout decision).
func ColorFor(w io.Writer, attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if IsStderr(w) {
		if StreamColor(os.Stderr) {
			c.EnableColor()
		} else {
			c.DisableColor()
		}
	}
	return c
}

// StderrColor is ColorFor(Stderr, attrs...).
func StderrColor(attrs ...color.Attribute) *color.Color {
	return ColorFor(Stderr, attrs...)
}

// stderrWriter writes to os.Stderr (read at write time, so tests that swap
// os.Stderr capture it). It passes bytes through when stderr is a color
// terminal. On a terminal with color disabled (NO_COLOR, --no-color,
// TERM=dumb) it drops only SGR color sequences (CSI ... m), so cursor control
// such as the spinner's erase-line still works. When stderr is not a terminal
// it drops every escape sequence. The escape state carries across writes, so
// a sequence split over two writes is still handled.
type stderrWriter struct {
	mu      sync.Mutex
	pending []byte // escape sequence read so far, starting with ESC
}

// Stderr is the writer for every human line on stderr: warnings, notices,
// prompts, spinner lines, and reports moved off stdout in machine modes.
var Stderr io.Writer = &stderrWriter{}

// Fd returns stderr's descriptor, so terminal checks on Stderr see the real
// stream.
func (s *stderrWriter) Fd() uintptr { return os.Stderr.Fd() }

func (s *stderrWriter) Write(p []byte) (int, error) {
	dst := os.Stderr
	tty := IsTerminal(dst)
	if tty && !ColorDisabled() {
		return dst.Write(p)
	}
	s.mu.Lock()
	out := s.filter(p, tty)
	s.mu.Unlock()
	if len(out) > 0 {
		if _, err := dst.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// filter removes escape sequences from p: only SGR color sequences when
// keepControl is true, all of them otherwise.
func (s *stderrWriter) filter(p []byte, keepControl bool) []byte {
	const maxEscapeLen = 32
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if b == 0x1b {
			// A new ESC ends any unfinished sequence.
			if keepControl {
				out = append(out, s.pending...)
			}
			s.pending = append(s.pending[:0], b)
			continue
		}
		if len(s.pending) == 0 {
			out = append(out, b)
			continue
		}
		s.pending = append(s.pending, b)
		final := (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
		if !final && len(s.pending) <= maxEscapeLen {
			continue
		}
		sgr := final && b == 'm' && len(s.pending) >= 2 && s.pending[1] == '['
		if keepControl && !sgr {
			out = append(out, s.pending...)
		}
		s.pending = s.pending[:0]
	}
	return out
}

// SetTerminalCheck replaces the TTY check for file descriptors and returns a
// func that restores the previous one. Tests in other packages use it to fake
// which streams are terminals.
func SetTerminalCheck(isTTY func(fd uintptr) bool) (restore func()) {
	prev := isTerminalFd
	isTerminalFd = isTTY
	return func() { isTerminalFd = prev }
}
