package tui

import (
	"context"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Run shows the TUI on in/out until the user quits or ctx ends. noColor is
// refresh's own color decision (NO_COLOR, --no-color, TERM=dumb): the TUI
// then sends no color and leaves the terminal's background alone; Bubble
// Tea's own detection would not see --no-color.
func Run(ctx context.Context, b state.Backend, in io.Reader, out io.Writer, noColor bool) error {
	m := New(ctx, b, 100*time.Millisecond)
	m.noColor = noColor
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)}
	if noColor {
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	p := tea.NewProgram(m, opts...)
	_, err := p.Run()
	return err
}
