package tui

import (
	"context"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Run shows the TUI on in/out until the user quits or ctx ends.
func Run(ctx context.Context, b state.Backend, in io.Reader, out io.Writer) error {
	p := tea.NewProgram(New(ctx, b, 100*time.Millisecond),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	_, err := p.Run()
	return err
}
