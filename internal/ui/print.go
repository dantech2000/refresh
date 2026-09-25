// Package ui holds the low-level terminal helpers: ANSI-width math, trees,
// spinners, prompts, per-stream color, and the plain (TSV) output mode. The
// human status vocabulary (tokens, sections, tables) is internal/render,
// which builds on this package.
package ui

import (
	"fmt"
	"os"
)

// Outln writes a line to stdout, ignoring write errors intentionally.
func Outln(a ...any) {
	_, _ = fmt.Fprintln(os.Stdout, a...)
}

// Outf writes formatted output to stdout, ignoring write errors intentionally.
func Outf(format string, a ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format, a...)
}
