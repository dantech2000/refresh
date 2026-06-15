package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
)

// Outln writes a line to stdout, ignoring write errors intentionally.
func Outln(a ...any) {
	_, _ = fmt.Fprintln(os.Stdout, a...)
}

// Outf writes formatted output to stdout, ignoring write errors intentionally.
func Outf(format string, a ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format, a...)
}

// ElapsedString renders an elapsed duration as green seconds, e.g. "1.2s".
func ElapsedString(elapsed time.Duration) string {
	return color.GreenString("%.1fs", elapsed.Seconds())
}

// PrintElapsed prints the standard "Retrieved in X.Xs" line followed by a
// blank line — the shared trailer between a command's heading and its table.
func PrintElapsed(elapsed time.Duration) {
	Outf("Retrieved in %s\n\n", ElapsedString(elapsed))
}
