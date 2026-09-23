package ui

import (
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
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

// InitColor sets fatih/color's global (which governs stdout) from stdout
// alone. fatih does the same at package init; calling it again after flags
// and seams are in place keeps both decisions on one code path.
func InitColor() {
	color.NoColor = !StreamColor(os.Stdout)
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
// os.Stderr capture it) and strips ANSI escape sequences unless stderr is a
// color terminal. The escape state carries across writes, so a sequence split
// over two writes is still removed.
type stderrWriter struct {
	mu       sync.Mutex
	inEscape bool
	escLen   int
}

// Stderr is the writer for every human line on stderr: warnings, notices,
// prompts, spinner lines, and reports moved off stdout in machine modes.
var Stderr io.Writer = &stderrWriter{}

// Fd returns stderr's descriptor, so terminal checks on Stderr see the real
// stream.
func (s *stderrWriter) Fd() uintptr { return os.Stderr.Fd() }

func (s *stderrWriter) Write(p []byte) (int, error) {
	dst := os.Stderr
	if StreamColor(dst) {
		return dst.Write(p)
	}
	s.mu.Lock()
	out := s.strip(p)
	s.mu.Unlock()
	if _, err := dst.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// strip removes ANSI escape sequences from p. It mirrors StripANSI but keeps
// its state between calls.
func (s *stderrWriter) strip(p []byte) []byte {
	const maxEscapeLen = 32
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if b == 0x1b {
			s.inEscape = true
			s.escLen = 0
			continue
		}
		if s.inEscape {
			s.escLen++
			if s.escLen > maxEscapeLen {
				s.inEscape = false
				s.escLen = 0
				continue
			}
			if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
				s.inEscape = false
				s.escLen = 0
			}
			continue
		}
		out = append(out, b)
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
