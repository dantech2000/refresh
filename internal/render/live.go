package render

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
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
	prev     int  // lines painted last frame (TTY only)
	appended bool // whether we've appended at least one snapshot (non-TTY)
}

// NewLiveRegion returns a LiveRegion for w. It repaints in place only when w is
// a terminal and the theme has color enabled.
func (t *Theme) NewLiveRegion(w io.Writer) *LiveRegion {
	return &LiveRegion{w: w, tty: isTerminal(w) && t.Level != ColorNone}
}

// Draw paints one frame. On a TTY it overwrites the previous frame in place; off
// a TTY it appends the frame (callers throttle the cadence so logs stay sane).
func (lr *LiveRegion) Draw(frame []string) {
	body := strings.Join(frame, "\n")
	if lr.tty {
		if lr.prev > 0 {
			_, _ = fmt.Fprintf(lr.w, "\x1b[%dA\x1b[0J", lr.prev) // up prev lines, clear to end
		}
		_, _ = fmt.Fprint(lr.w, body+"\n")
		lr.prev = len(frame)
		return
	}
	if lr.appended {
		_, _ = fmt.Fprintln(lr.w)
	}
	_, _ = fmt.Fprint(lr.w, body+"\n")
	lr.appended = true
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
