package runner

import (
	"bytes"
	"testing"

	"github.com/dantech2000/refresh/internal/ui"
)

func TestReportSkippedRegions(t *testing.T) {
	var buf bytes.Buffer
	ReportSkippedRegions(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no skipped regions wrote %q", buf.String())
	}
	ReportSkippedRegions(&buf, []string{"ap-east-1", "me-south-1"})
	want := "Skipped 2 region(s) not accessible to these credentials: ap-east-1, me-south-1 (scope with -r or REFRESH_EKS_REGIONS)\n"
	if got := ui.StripANSI(buf.String()); got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestTableListsFailures(t *testing.T) {
	for format, want := range map[string]bool{
		"": true, "table": true, "Table": true, "tree": true,
		"json": false, "yaml": false, "plain": false,
	} {
		if got := TableListsFailures(format); got != want {
			t.Errorf("TableListsFailures(%q) = %v, want %v", format, got, want)
		}
	}
}
