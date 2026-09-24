package runner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
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

// NoRegionAnswered asks STS only when a region looked closed, and returns
// the credential error only when STS rejects the credentials.
func TestNoRegionAnswered(t *testing.T) {
	// Build the failures the way the sweeps do, from typed API errors.
	region := func(name, code string) diag.Failure {
		return diag.FromError(diag.KindRegion, name, diag.OpListClusters, &smithy.GenericAPIError{Code: code, Message: "fake " + code})
	}
	unavailable := []diag.Failure{region("us-east-1", "UnrecognizedClientException")}
	throttled := []diag.Failure{region("us-east-1", "ThrottlingException")}
	for _, tc := range []struct {
		name     string
		badCreds bool
		skipped  []string
		failed   []diag.Failure
		wantErr  bool
		stsCalls int
	}{
		{name: "expired token needs no STS call", failed: []diag.Failure{region("us-east-1", "ExpiredTokenException")}, wantErr: true},
		{name: "expired token after another failure", failed: []diag.Failure{
			region("us-east-1", "ThrottlingException"), region("us-west-2", "ExpiredTokenException"),
		}, wantErr: true},
		{name: "region-closed code, valid credentials", failed: unavailable, stsCalls: 1},
		{name: "skipped, bad credentials", badCreds: true, skipped: []string{"us-east-1"}, wantErr: true, stsCalls: 1},
		{name: "unavailable, bad credentials", badCreds: true, failed: unavailable, wantErr: true, stsCalls: 1},
		{name: "skipped, valid credentials", skipped: []string{"us-east-1"}, stsCalls: 1},
		{name: "throttled only", badCreds: true, failed: throttled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeaws.New(t)
			if tc.badCreds {
				srv.FailCredentials("InvalidClientTokenId")
			}
			cfg, err := config.LoadDefaultConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = NoRegionAnswered(t.Context(), cfg, tc.skipped, tc.failed)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "AWS credentials not configured or invalid") {
				t.Errorf("err lacks the credential help:\n%v", err)
			}
			if n := len(srv.Calls()); n != tc.stsCalls {
				t.Errorf("AWS calls = %d, want %d: %v", n, tc.stsCalls, srv.Calls())
			}
		})
	}
}
