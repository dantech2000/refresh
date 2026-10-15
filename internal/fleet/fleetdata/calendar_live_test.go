//go:build livecheck

package fleetdata_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// TestLiveSupportCalendar compares the built-in support calendar with the
// dates of DescribeClusterVersions today. Every version that EKS still
// supports must be in the calendar with the same two dates. A version
// past the end of extended support is logged only.
//
// It also checks what it can of the extended-support premium without the
// Pricing API: EKS still has a version in extended support, so the premium
// still applies, and the premium was checked within premiumMaxAge. The
// price itself is checked by hand against the EKS pricing page,
// https://aws.amazon.com/eks/pricing/, and PremiumAsOf moved to that day.
//
// It runs in us-east-1 with read-only calls that cost nothing:
//
//   - eks:DescribeClusterVersions with includeAll, paged
func TestLiveSupportCalendar(t *testing.T) {
	cfg := awsxtest.LiveConfig(t, "us-east-1")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	built := map[string]fleetdata.SupportWindow{}
	for _, w := range fleetdata.Calendar() {
		built[w.Version] = w
	}
	day := func(p *time.Time) string {
		if p == nil {
			return "-"
		}
		return p.UTC().Format(time.DateOnly)
	}
	extended := 0
	pages := eks.NewDescribeClusterVersionsPaginator(eks.NewFromConfig(cfg), &eks.DescribeClusterVersionsInput{IncludeAll: aws.Bool(true)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatalf("eks:DescribeClusterVersions: %v", err)
		}
		for _, cv := range page.ClusterVersions {
			v := aws.ToString(cv.ClusterVersion)
			std, ext := day(cv.EndOfStandardSupportDate), day(cv.EndOfExtendedSupportDate)
			note := "ok"
			if w, ok := built[v]; !ok {
				note = "not in the calendar"
			} else if bstd, bext := w.StandardEnd.Format(time.DateOnly), w.ExtendedEnd.Format(time.DateOnly); std != bstd || ext != bext {
				note = "the calendar has " + bstd + " / " + bext
			}
			if cv.VersionStatus == ekstypes.VersionStatusExtendedSupport {
				extended++
			}
			t.Logf("%-5s %-18s standard %s, extended %s: %s", v, cv.VersionStatus, std, ext, note)
			if note != "ok" && cv.VersionStatus != ekstypes.VersionStatusUnsupported {
				t.Errorf("%s: the built-in calendar (as of %s) is stale: %s; EKS has %s / %s", v, fleetdata.CalendarAsOf, note, std, ext)
			}
		}
	}
	if extended == 0 {
		t.Errorf("no version is in extended support: check whether the premium of $%.2f/hr still applies", fleetdata.ExtendedSupportPremiumUSDPerHour)
	}
	checked, err := time.Parse(time.DateOnly, fleetdata.PremiumAsOf)
	if err != nil {
		t.Fatalf("PremiumAsOf = %q: %v", fleetdata.PremiumAsOf, err)
	}
	if age := time.Since(checked); age > premiumMaxAge {
		t.Errorf("the extended-support premium of $%.2f/hr was last checked on %s; check it by hand against https://aws.amazon.com/eks/pricing/ and move PremiumAsOf", fleetdata.ExtendedSupportPremiumUSDPerHour, fleetdata.PremiumAsOf)
	}
}

// premiumMaxAge is how long the extended-support premium goes without a
// check by hand before the live check fails.
const premiumMaxAge = 180 * 24 * time.Hour
