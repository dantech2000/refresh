//go:build livecheck

package status

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// TestLiveSupportCalendar compares the compiled-in support calendar with
// what DescribeClusterVersions returns today.
func TestLiveSupportCalendar(t *testing.T) {
	ctx := context.Background()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections) // keep-alive connections are not a leak
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	api := eks.NewFromConfig(cfg)
	out, err := api.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{IncludeAll: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	cal := map[string]calendarEntry{}
	for _, e := range fallbackCalendar {
		cal[e.version] = e
	}
	day := func(p *time.Time) string {
		if p == nil {
			return "-"
		}
		return p.UTC().Format("2006-01-02")
	}
	for _, cv := range out.ClusterVersions {
		v := aws.ToString(cv.ClusterVersion)
		e, ok := cal[v]
		note := "OK"
		switch {
		case !ok:
			note = "NOT IN fallbackCalendar"
		case day(cv.EndOfStandardSupportDate) != e.standardEnd.Format("2006-01-02") || day(cv.EndOfExtendedSupportDate) != e.extendedEnd.Format("2006-01-02"):
			note = "MISMATCH: calendar " + e.standardEnd.Format("2006-01-02") + " / " + e.extendedEnd.Format("2006-01-02")
		}
		t.Logf("%-5s status=%-18s default=%-5v std=%s ext=%s  %s", v, cv.VersionStatus, cv.DefaultVersion, day(cv.EndOfStandardSupportDate), day(cv.EndOfExtendedSupportDate), note)
		// The calendar only has to cover versions that are still supported.
		if note != "OK" && cv.VersionStatus != ekstypes.VersionStatusUnsupported {
			t.Errorf("%s: fallbackCalendar is stale: %s", v, note)
		}
		// The per-version path the fleet uses must agree.
		if std, ext, ok := supportDatesFromAPI(ctx, api, v); !ok || std.Format("2006-01-02") != day(cv.EndOfStandardSupportDate) || ext.Format("2006-01-02") != day(cv.EndOfExtendedSupportDate) {
			t.Errorf("%s: supportDatesFromAPI = %v %v %v", v, std, ext, ok)
		}
		t.Logf("      resolved: %+v", NewSupportResolver(api).Resolve(ctx, v))
	}
}
