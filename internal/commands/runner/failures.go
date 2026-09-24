package runner

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// ReportFailures writes one warning line per failure to w, in diag.Sort
// order:
//
//	warning: nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded
//
// The text after "warning: " is render.FailureText. The line is yellow when
// w is a color terminal. fs is not changed.
func ReportFailures(w io.Writer, fs []diag.Failure) {
	if len(fs) == 0 {
		return
	}
	sorted := slices.Clone(fs)
	diag.Sort(sorted)
	yellow := ui.ColorFor(w, color.FgYellow)
	for _, f := range sorted {
		_, _ = fmt.Fprintln(w, yellow.Sprint("warning: "+render.FailureText(f)))
	}
}

// WriteFailures names each failure of a run once, where format puts it: in
// the table view (TableListsFailures), an INCOMPLETE DATA section on out;
// for json, yaml, and plain, one warning line each on errOut
// (ReportFailures). It is for views that end with a summary rather than a
// table of their own, such as the mutating commands. It writes nothing when
// fs is empty.
func WriteFailures(format string, out, errOut io.Writer, fs []diag.Failure) {
	if !TableListsFailures(format) {
		ReportFailures(errOut, fs)
		return
	}
	for _, line := range render.Default(out).FailureSection(fs) {
		_, _ = fmt.Fprintln(out, line)
	}
}

// IncompleteExit returns the ExitIncomplete error for a run with failures,
// or nil when fs is empty:
//
//	incomplete data: 3 failure(s) (1 cluster, 2 nodegroup)
//
// Print the document and call ReportFailures first. Wrap the result with
// UnlessInterrupted, so a run cut short by Ctrl+C exits 1.
func IncompleteExit(fs []diag.Failure) error {
	if len(fs) == 0 {
		return nil
	}
	counts := map[diag.Kind]int{}
	for _, f := range fs {
		counts[f.Kind]++
	}
	kinds := make([]diag.Kind, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k.Noun()))
	}
	return cli.Exit(fmt.Sprintf("incomplete data: %d failure(s) (%s)", len(fs), strings.Join(parts, ", ")), ExitIncomplete)
}
