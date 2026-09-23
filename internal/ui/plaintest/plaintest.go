// Package plaintest checks `-o plain` output against the plain contract in
// tests: a header row, then one tab-separated row per item, nothing else.
package plaintest

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[")

// Check fails t unless out is pure TSV whose first line is exactly headers:
// no ANSI escapes, no blank lines, and the same tab count on every line. It
// returns the data rows split into cells.
func Check(t testing.TB, out string, headers ...string) [][]string {
	t.Helper()
	if ansiRe.MatchString(out) {
		t.Errorf("plain output contains ANSI escapes:\n%q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("plain output must end with a newline:\n%q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if got, want := lines[0], strings.Join(headers, "\t"); got != want {
		t.Fatalf("plain header = %q, want %q\nfull output:\n%s", got, want, out)
	}
	tabs := strings.Count(lines[0], "\t")
	var rows [][]string
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			t.Errorf("plain output line %d is blank:\n%s", i+1, out)
			continue
		}
		if n := strings.Count(l, "\t"); n != tabs {
			t.Errorf("plain output line %d has %d tabs, header has %d: %q", i+1, n, tabs, l)
		}
		if i > 0 {
			rows = append(rows, strings.Split(l, "\t"))
		}
	}
	return rows
}

// Field returns the VALUE of the first FIELD/VALUE row named field, and
// whether it was found.
func Field(rows [][]string, field string) (string, bool) {
	for _, r := range rows {
		if len(r) == 2 && r[0] == field {
			return r[1], true
		}
	}
	return "", false
}
