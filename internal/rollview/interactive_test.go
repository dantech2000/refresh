package rollview

import (
	"os"
	"testing"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/ui"
)

// The panel is on by default only for a color terminal on stdout: piped
// stdout and NO_COLOR both fall back to text progress.
func TestInteractive(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	prevNoColor := color.NoColor
	t.Cleanup(func() { color.NoColor = prevNoColor })

	for _, tc := range []struct {
		name    string
		tty     bool
		noColor string
		want    bool
	}{
		{"terminal", true, "", true},
		{"piped", false, "", false},
		{"NO_COLOR on a terminal", true, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			restore := ui.SetTerminalCheck(func(fd uintptr) bool { return tc.tty && fd == os.Stdout.Fd() })
			defer restore()
			ui.InitColor()
			if got := Interactive(os.Stdout); got != tc.want {
				t.Errorf("Interactive(stdout) = %v, want %v", got, tc.want)
			}
		})
	}
}
