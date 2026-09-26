package render

import (
	"strings"
	"testing"
)

func TestErrorLines(t *testing.T) {
	msg := "insufficient AWS permissions while listing clusters\n" +
		"AWS: AccessDeniedException: not authorized to perform: eks:ListClusters on resource: x\n" +
		"\n" +
		"Permissions refresh uses (grant the ones for the commands you run):\n" +
		"  eks:ListClusters     status, cluster list\n" +
		"  eks:DescribeCluster  Every cluster command\n" +
		"\n" +
		"See https://example.com/iam"
	got := New(ColorNone, true).ErrorLines(msg, 0)
	want := []string{
		"✗ Error: insufficient AWS permissions while listing clusters",
		"  AWS AccessDeniedException: not authorized to perform: eks:ListClusters on resource: x",
		"",
		"  Permissions refresh uses (grant the ones for the commands you run):",
		"  ▲ eks:ListClusters     status, cluster list",
		"    eks:DescribeCluster  Every cluster command",
		"",
		"  See https://example.com/iam",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Color is additive: the ASCII fallback keeps the same words.
	if a := New(ColorNone, false).ErrorLines("boom", 0); len(a) != 1 || !strings.HasSuffix(a[0], "Error: boom") {
		t.Errorf("ascii = %q", a)
	}
}

func TestErrorLinesWrapWithAHangingIndent(t *testing.T) {
	msg := "headline\nAWS: one two three four five six\n  eks:Describe  alpha beta gamma delta"
	got := New(ColorNone, true).ErrorLines(msg, 24)
	want := []string{
		"✗ Error: headline",
		"  AWS one two three four",
		"      five six",
		"    eks:Describe  alpha",
		"                  beta",
		"                  gamma",
		"                  delta",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
