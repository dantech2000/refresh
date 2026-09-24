package render

import (
	"slices"
	"strings"

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

// FailureSection returns the INCOMPLETE DATA section of a human table view:
// a blank line, the title, and one line per failure in diag.Sort order. It
// returns nil when fs is empty. The table views list their failures here
// instead of on stderr; fs is not changed.
func (t *Theme) FailureSection(fs []diag.Failure) []string {
	if len(fs) == 0 {
		return nil
	}
	sorted := slices.Clone(fs)
	diag.Sort(sorted)
	out := make([]string, 0, len(sorted)+2)
	out = append(out, "", t.Bold(t.Pal.Peach, "INCOMPLETE DATA"))
	for _, f := range sorted {
		out = append(out, t.Glyph(Unknown)+" "+t.Paint(t.Pal.Peach, FailureText(f)))
	}
	return out
}
