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

// Errln writes a line to stderr, ignoring write errors intentionally.
func Errln(a ...any) {
    _, _ = fmt.Fprintln(os.Stderr, a...)
}

// Errf writes formatted output to stderr, ignoring write errors intentionally.
func Errf(format string, a ...any) {
    _, _ = fmt.Fprintf(os.Stderr, format, a...)
}


