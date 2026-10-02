package runner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/ui"
)

func TestReportFailures(t *testing.T) {
	throttled := diag.FromError(diag.KindNodegroup, "web", diag.OpDescribeNodegroup, mocks.Throttling())
	throttled.Cluster, throttled.Region = "prod", "us-east-1"
	region := diag.FromError(diag.KindRegion, "ap-east-1", diag.OpListClusters, mocks.APIError("OptInRequired", "not subscribed"))
	region.Region = "ap-east-1"
	notFound := diag.FromError(diag.KindCluster, "gone", diag.OpDescribeCluster, mocks.NotFound())
	update := diag.New(diag.KindUpdate, "api", diag.ReasonNotAttempted, "")
	update.Cluster = "prod"

	fs := []diag.Failure{throttled, update, region, notFound}
	var buf bytes.Buffer
	ReportFailures(&buf, fs)
	want := "" +
		"warning: cluster gone: NotFound: ResourceNotFoundException: The requested resource was not found.\n" +
		"warning: nodegroup prod/web (us-east-1): Throttled: ThrottlingException: Rate exceeded\n" +
		"warning: region ap-east-1: RegionUnavailable: OptInRequired: not subscribed\n" +
		"warning: update prod/api: NotAttempted\n"
	if got := ui.StripANSI(buf.String()); got != want {
		t.Errorf("ReportFailures wrote\n%s\nwant\n%s", got, want)
	}
	if fs[0] != throttled || fs[1] != update {
		t.Error("ReportFailures reordered the caller's slice")
	}

	buf.Reset()
	ReportFailures(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no failures wrote %q", buf.String())
	}
}

func TestIncompleteExit(t *testing.T) {
	if err := IncompleteExit(nil); err != nil {
		t.Errorf("IncompleteExit(nil) = %v, want nil", err)
	}
	fs := []diag.Failure{
		diag.New(diag.KindNodegroup, "a", diag.ReasonThrottled, "x"),
		diag.New(diag.KindRegion, "ap-east-1", diag.ReasonRegionUnavailable, "x"),
		diag.New(diag.KindNodegroup, "b", diag.ReasonThrottled, "x"),
		diag.New(diag.KindCluster, "c", diag.ReasonNotFound, "x"),
	}
	err := IncompleteExit(fs)
	if code := ExitCodeOf(err); code != ExitIncomplete {
		t.Errorf("exit code = %d, want %d", code, ExitIncomplete)
	}
	if want := "incomplete data: 4 failure(s) (1 cluster, 2 nodegroups, 1 region)"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// WriteFailures names each failure once: in the table view's INCOMPLETE DATA
// section on stdout, and for json, yaml, and plain on stderr.
func TestWriteFailures(t *testing.T) {
	f := diag.New(diag.KindNodegroup, "web", diag.ReasonThrottled, "Rate exceeded")
	f.Cluster = "prod"
	for _, tc := range []struct {
		format        string
		wantTableView bool
	}{{"", true}, {"table", true}, {"json", false}, {"yaml", false}, {"plain", false}} {
		var out, errOut bytes.Buffer
		WriteFailures(tc.format, &out, &errOut, []diag.Failure{f})
		table := strings.Contains(ui.StripANSI(out.String()), "INCOMPLETE DATA")
		warned := strings.Contains(errOut.String(), "warning: nodegroup prod/web: Throttled: Rate exceeded")
		if table != tc.wantTableView || warned == tc.wantTableView {
			t.Errorf("format %q: stdout section %v, stderr line %v; want the failure named once in the right place", tc.format, table, warned)
		}
	}
	var out, errOut bytes.Buffer
	WriteFailures("table", &out, &errOut, nil)
	if out.Len()+errOut.Len() != 0 {
		t.Errorf("no failures wrote %q / %q", out.String(), errOut.String())
	}
}

// Only changes that did not start: the message does not call it missing
// data. Mixed with a failed read, it names both.
func TestIncompleteExitNamesChangesThatDidNotStart(t *testing.T) {
	change := diag.Failure{Kind: diag.KindAddon, Name: "a", Operation: diag.OpUpdateAddon, Reason: diag.ReasonAccessDenied}
	read := diag.Failure{Kind: diag.KindNodegroup, Name: "n", Operation: diag.OpDescribeNodegroup}
	if got := IncompleteExit([]diag.Failure{change}).Error(); got != "1 update(s) could not start (1 addon)" {
		t.Errorf("changes only: %q", got)
	}
	if got := IncompleteExit([]diag.Failure{change, read}).Error(); got != "1 update(s) could not start, incomplete data: 2 failure(s) (1 addon, 1 nodegroup)" {
		t.Errorf("mixed: %q", got)
	}
	// A change whose call got no clear answer may have started.
	unclear := diag.Failure{Kind: diag.KindAddon, Name: "a", Operation: diag.OpUpdateAddon, Reason: diag.ReasonNetworkError}
	if got := IncompleteExit([]diag.Failure{unclear}).Error(); got != "incomplete data: 1 failure(s) (1 addon)" {
		t.Errorf("unclear: %q", got)
	}
}
