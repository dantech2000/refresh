package render

import (
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
)

// FailureText is the one-line human text of a failure, without color:
//
//	nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded
//
// The cluster, the region, and the error are left out when they are empty,
// and the region is left out when it is the item's name (a region failure).
// The stderr warning lines (runner.ReportFailures) and the INCOMPLETE DATA
// section of the table views both use it, so they read the same.
func FailureText(f diag.Failure) string {
	var b strings.Builder
	b.WriteString(f.Kind.Noun())
	b.WriteByte(' ')
	if f.Cluster != "" {
		b.WriteString(f.Cluster)
		b.WriteByte('/')
	}
	b.WriteString(f.Name)
	if f.Region != "" && f.Region != f.Name {
		b.WriteString(" (")
		b.WriteString(f.Region)
		b.WriteByte(')')
	}
	b.WriteString(": ")
	b.WriteString(string(f.Reason))
	if f.Error != "" {
		b.WriteString(": ")
		b.WriteString(f.Error)
	}
	return b.String()
}

// FailureSection returns the failure sections of a human table view, each a
// blank line, a title, and one line per failure in diag.Sort order: NOT
// STARTED for a change AWS rejected (diag.Failure.NotStarted), with the IAM
// action to grant when it was denied, INTERRUPTED for what Ctrl+C or
// SIGTERM stopped, then INCOMPLETE DATA for the rest. It
// returns nil when fs is empty. The table views list their failures here
// instead of on stderr; fs is not changed.
func (t *Theme) FailureSection(fs []diag.Failure) []string {
	if len(fs) == 0 {
		return nil
	}
	sorted := slices.Clone(fs)
	diag.Sort(sorted)
	var changes, stopped, reads []diag.Failure
	for _, f := range sorted {
		switch {
		case f.NotStarted():
			changes = append(changes, f)
		case f.Reason == diag.ReasonInterrupted:
			// The user stopped it: no data is missing.
			stopped = append(stopped, f)
		default:
			reads = append(reads, f)
		}
	}
	var out []string
	if len(changes) > 0 {
		out = append(out, "", t.Bold(t.Pal.Red, "NOT STARTED"))
		var denied []string
		for _, f := range changes {
			out = append(out, t.Glyph(Fail)+" "+t.Paint(t.Pal.Red, FailureText(f)))
			if f.Reason == diag.ReasonAccessDenied && !slices.Contains(denied, f.Operation) {
				denied = append(denied, f.Operation)
			}
		}
		for _, op := range denied {
			out = append(out, "  "+t.Paint(t.Pal.Yellow, "Grant "+op+" to this identity; see "+awserr.PermissionsDocURL))
		}
	}
	if len(stopped) > 0 {
		out = append(out, "", t.Bold(t.Pal.Yellow, "INTERRUPTED"))
		for _, f := range stopped {
			out = append(out, t.Glyph(Warn)+" "+t.Paint(t.Pal.Yellow, FailureText(f)))
		}
	}
	if len(reads) > 0 {
		out = append(out, "", t.Bold(t.Pal.Peach, "INCOMPLETE DATA"))
		for _, f := range reads {
			out = append(out, t.Glyph(Unknown)+" "+t.Paint(t.Pal.Peach, FailureText(f)))
		}
	}
	return out
}
