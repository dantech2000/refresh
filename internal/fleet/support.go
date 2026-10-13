package fleet

import (
	"context"
	"math"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// Support is the support window of the Kubernetes version of a cluster.
type Support struct {
	Tier apidoc.SupportTier
	// StandardEnd and ExtendedEnd are zero when the date is not known.
	StandardEnd, ExtendedEnd time.Time
	// DaysRemaining is the days until the tier ends, with a part of a day
	// as a whole day. It is 0 when no window is open.
	DaysRemaining int
	// AutoUpgrade is true when the upgrade policy of the cluster is
	// STANDARD: EKS upgrades the cluster at the end of standard support,
	// and the cluster never enters extended support.
	AutoUpgrade bool
	// BuiltIn is true when the dates are the ones that refresh ships
	// with, because EKS did not answer.
	BuiltIn bool
}

// window is the two end dates of a version.
type window struct {
	standardEnd, extendedEnd time.Time
}

// calendar returns the support window of each version that EKS reports in
// the region of c. One unfiltered call serves every cluster of the region
// for the run.
func (f *Fetcher) calendar(ctx context.Context, c *awsx.Clients) (map[Version]window, error) {
	return f.calendars.Get(ctx, c.Region, func(ctx context.Context) (map[Version]window, error) {
		versions, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*eks.DescribeClusterVersionsOutput, error) {
				return c.EKS.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{NextToken: token})
			},
			func(out *eks.DescribeClusterVersionsOutput) ([]ekstypes.ClusterVersionInformation, *string) {
				return out.ClusterVersions, out.NextToken
			},
		)
		if err != nil {
			return nil, err
		}
		out := map[Version]window{}
		for _, v := range versions {
			version, err := ParseVersion(aws.ToString(v.ClusterVersion))
			if err != nil || v.EndOfStandardSupportDate == nil {
				continue
			}
			out[version] = window{standardEnd: aws.ToTime(v.EndOfStandardSupportDate), extendedEnd: aws.ToTime(v.EndOfExtendedSupportDate)}
		}
		return out, nil
	})
}

// supportFor returns the support of version at the time now. live is the
// calendar from EKS, or nil when EKS did not answer. A version that live
// does not have comes from the built-in calendar. ok is false when neither
// has it: a version newer than the built-in calendar.
func supportFor(version Version, live map[Version]window, upgradePolicy string, now time.Time) (s Support, ok bool) {
	w, found := live[version]
	if !found {
		if w, found = builtInWindow(version); !found {
			return Support{Tier: apidoc.SupportUnknown}, false
		}
		s.BuiltIn = true
	}
	s.StandardEnd, s.ExtendedEnd = w.standardEnd, w.extendedEnd
	s.AutoUpgrade = upgradePolicy == string(ekstypes.SupportTypeStandard)
	switch {
	case !w.standardEnd.IsZero() && now.Before(w.standardEnd):
		s.Tier, s.DaysRemaining = apidoc.SupportStandard, daysLeft(now, w.standardEnd)
	case !w.extendedEnd.IsZero() && now.Before(w.extendedEnd):
		s.Tier, s.DaysRemaining = apidoc.SupportExtended, daysLeft(now, w.extendedEnd)
	default:
		s.Tier = apidoc.SupportUnsupported
	}
	return s, true
}

// builtInWindow returns the window of version from the calendar that
// refresh ships with. A version older than its first row is past the end of
// extended support: it gets a window with no dates, which is unsupported.
func builtInWindow(version Version) (window, bool) {
	rows := fleetdata.Calendar()
	for _, row := range rows {
		if v, err := ParseVersion(row.Version); err == nil && v == version {
			return window{standardEnd: row.StandardEnd, extendedEnd: row.ExtendedEnd}, true
		}
	}
	if oldest, err := ParseVersion(rows[0].Version); err == nil && !version.IsZero() && version.Compare(oldest) < 0 {
		return window{}, true
	}
	return window{}, false
}

// daysLeft returns the days from now to end, with a part of a day as a
// whole day: a window with 23 hours left has 1 day, not 0.
func daysLeft(now, end time.Time) int {
	return int(math.Ceil(end.Sub(now).Hours() / 24))
}
