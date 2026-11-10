package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

// HumanView is the output of a command for a person: a table, and the same
// data as tab-separated values. view.List implements it.
type HumanView interface {
	// Render returns the lines of the table. A width of 0 means that the
	// output is not a terminal.
	Render(th view.Theme, width int) []string
	// Plain writes the tab-separated form.
	Plain(w io.Writer) error
}

// Print writes the result of a command to the output stream in format: doc
// as JSON or YAML, or v as a table or as plain text.
func Print(ios *IOStreams, format Format, doc apidoc.Document, v HumanView) error {
	switch format {
	case FormatJSON, FormatYAML:
		return PrintDocument(ios, format, doc)
	case FormatPlain:
		return v.Plain(ios.Out)
	case FormatTable:
		width, _ := ios.Size()
		return WriteLines(ios.Out, v.Render(ios.Theme(), width))
	}
	return fmt.Errorf("cannot print as %q", format)
}

// PrintResult prints the result of a read and returns the error that ends
// the run. It is the output contract of a result that may be partial:
//
//  1. doc in format, or v for a person (Print).
//  2. The failures on stderr, for the formats whose stdout cannot list
//     them (ReportFailures).
//  3. ErrInterrupted when the user interrupted the run.
//  4. Else the IncompleteError of failures: exit 4, or nil.
func PrintResult(ctx context.Context, ios *IOStreams, format Format, doc apidoc.Document, v HumanView, failures []apidoc.Failure) error {
	if err := PrintReport(ctx, ios, format, doc, v, failures); err != nil {
		return err
	}
	return IncompleteError(failures)
}

// PrintReport does the first three steps of PrintResult and returns nil
// where PrintResult would go on: for a command that decides its own exit
// code from the result, failures included.
func PrintReport(ctx context.Context, ios *IOStreams, format Format, doc apidoc.Document, v HumanView, failures []apidoc.Failure) error {
	if err := Print(ios, format, doc, v); err != nil {
		return err
	}
	if format != FormatTable {
		ReportFailures(ios, failures)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrInterrupted
	}
	return nil
}

// WriteLines writes each line and a line break to w, in one write.
func WriteLines(w io.Writer, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}

// ReportFailures names the failures of a run on stderr, one warning line
// each, in the order of fs. A command calls it for the formats whose
// stdout cannot hold the failures: plain, where stdout is only rows, and
// JSON and YAML, where a person reads stderr. The table lists its failures
// itself.
func ReportFailures(ios *IOStreams, fs []apidoc.Failure) {
	th := ios.ErrTheme()
	for _, f := range fs {
		// Nothing can report a failed write to stderr.
		_, _ = fmt.Fprintln(ios.ErrOut, th.Caution("warning: "+view.FailureLine(f)))
	}
}

// IncompleteError returns the error of a run whose result has failures:
// exit code 4 and a message that counts them by kind, such as
//
//	incomplete data: 3 failure(s) (1 cluster, 2 regions)
//
// It returns nil when fs is empty. The command prints its result first.
func IncompleteError(fs []apidoc.Failure) error {
	if len(fs) == 0 {
		return nil
	}
	counts := map[apidoc.FailureKind]int{}
	for _, f := range fs {
		counts[f.Kind]++
	}
	kinds := make([]apidoc.FailureKind, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		noun := k.Noun()
		if counts[k] != 1 {
			noun += "s"
		}
		parts[i] = fmt.Sprintf("%d %s", counts[k], noun)
	}
	return &ExitError{
		Code: ExitIncomplete,
		Err:  fmt.Errorf("incomplete data: %d failure(s) (%s)", len(fs), strings.Join(parts, ", ")),
	}
}
