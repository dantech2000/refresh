package runner

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/ui"
)

// RegionScopeHint tells the user how to narrow a region sweep.
const RegionScopeHint = "scope with -r or REFRESH_EKS_REGIONS"

// ReportSkippedRegions writes the one notice line of a default region sweep
// (no -r, no REFRESH_EKS_REGIONS) that skipped regions closed to these
// credentials, such as an SCP denial or a region that is not enabled:
//
//	Skipped 2 region(s) not accessible to these credentials: ap-east-1, me-south-1 (scope with -r or REFRESH_EKS_REGIONS)
//
// Skipped regions are not failures: they do not go in the document's
// "failures" and do not change the exit code. It writes nothing when skipped
// is empty.
func ReportSkippedRegions(w io.Writer, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, ui.ColorFor(w, color.FgYellow).Sprintf("Skipped %d region(s) not accessible to these credentials: %s (%s)",
		len(skipped), strings.Join(skipped, ", "), RegionScopeHint))
}

// TableListsFailures reports whether the output format is a human view
// (table, or cluster list's tree) that lists the run's failures itself, in
// an INCOMPLETE DATA section (render.FailureSection). For every other format
// (json, yaml, plain) the command writes them to stderr with ReportFailures,
// so a table run does not print each failure twice.
func TableListsFailures(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "table", "tree":
		return true
	default:
		return false
	}
}
