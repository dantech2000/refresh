package render

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/dantech2000/refresh/internal/ui"
)

// LiveRegion repaints a multi-line block in place on a TTY (cursor-up +
// clear-to-end), and degrades to appended snapshots when output isn't a
// terminal (or color is off) — so logs and pipes never get escape codes or a
// flicker of half-frames. It is line-oriented: it never uses the alternate
// screen buffer, so scrollback is preserved and it stays a CLI, not a TUI.
//
// The frame model is a pure func() []string; this type only paints it, which
// keeps the rendering testable without a pseudo-terminal.
type LiveRegion struct {
	w        io.Writer
	tty      bool // repaint in place vs. append
	prev     int  // terminal rows painted last frame, after wrapping (TTY only)
	appended bool // whether we've appended at least one snapshot (non-TTY)
	last     string
	// width reports the terminal's column count, or <= 0 when unknown (rows
	// are then counted as one per line). nil means unknown.
	width func() int
}

// NewLiveRegion returns a LiveRegion for w. It repaints in place only when w is
// a terminal and the theme has color enabled.
func (t *Theme) NewLiveRegion(w io.Writer) *LiveRegion {
	lr := &LiveRegion{w: w, tty: isTerminal(w) && t.Level != ColorNone}
	if f, ok := w.(*os.File); ok && lr.tty {
		lr.width = func() int {
			cols, _, err := term.GetSize(int(f.Fd()))
			if err != nil {
				return 0
			}
			return cols
		}
	}
	return lr
}

// rows returns how many terminal rows frame occupies at the given width. A
// line wider than the terminal wraps onto extra rows; the cursor-up on the
// next repaint must cover those too, or stale rows stay on screen. Width is
// measured ANSI-aware, in display cells. width <= 0 (unknown) counts one row
// per line.
func rows(frame []string, width int) int {
	if width <= 0 {
		return len(frame)
	}
	n := 0
	for _, line := range frame {
		w := ui.VisibleWidth(line)
		if w <= width {
			n++ // includes empty lines; exactly-full lines don't wrap early
			continue
		}
		n += (w + width - 1) / width
	}
	return n
}

// InPlace reports whether frames repaint in place (true) or are appended as
// snapshots (false). Callers throttle the cadence when appending.
func (lr *LiveRegion) InPlace() bool { return lr.tty }

// Draw paints one frame. On a TTY it overwrites the previous frame in place; off
// a TTY it appends the frame, skipping a frame identical to the last one so a
// quiet stretch doesn't repeat the same snapshot in logs (callers also
// throttle the cadence).
func (lr *LiveRegion) Draw(frame []string) {
	body := strings.Join(frame, "\n")
	if lr.tty {
		if lr.prev > 0 {
			_, _ = fmt.Fprintf(lr.w, "\x1b[%dA\x1b[0J", lr.prev) // up prev rows, clear to end
		}
		_, _ = fmt.Fprint(lr.w, body+"\n")
		width := 0
		if lr.width != nil {
			width = lr.width()
		}
		lr.prev = rows(frame, width)
		return
	}
	if lr.appended && body == lr.last {
		return
	}
	if lr.appended {
		_, _ = fmt.Fprintln(lr.w)
	}
	_, _ = fmt.Fprint(lr.w, body+"\n")
	lr.appended = true
	lr.last = body
}

// Run draws frames every interval until frame reports done==true or ctx is
// cancelled. The cursor is hidden during a TTY run and always restored.
func (lr *LiveRegion) Run(ctx context.Context, interval time.Duration, frame func() (lines []string, done bool)) error {
	if lr.tty {
		_, _ = fmt.Fprint(lr.w, "\x1b[?25l")                    // hide cursor
		defer func() { _, _ = fmt.Fprint(lr.w, "\x1b[?25h") }() // restore on return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		lines, done := frame()
		lr.Draw(lines)
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
