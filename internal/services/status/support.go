package status

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/services/common"
)

// Extended support roughly doubles the control-plane price: ~$0.60/hr vs the
// standard ~$0.10/hr, i.e. a ~$0.50/hr premium per cluster (~$4,380/yr). The
// premium is what we surface — it's the number that makes a lingering cluster
// worth upgrading.
const extendedSupportPremiumUSDPerHour = 0.50

// calendarEntry is one Kubernetes minor's published EKS support window.
type calendarEntry struct {
	version     string
	standardEnd time.Time
	extendedEnd time.Time
}

// fallbackCalendar is the published EKS support window per Kubernetes minor,
// oldest first, used when DescribeClusterVersions is unavailable (missing
// permission, older API). Rows derived from this table are flagged Fallback.
// Source: the release calendar at
// https://docs.aws.amazon.com/eks/latest/userguide/kubernetes-versions.html
// (1.28-1.30 have since left that page; their dates are from its earlier
// revisions). Keep it contiguous: a test checks no minor is skipped.
var fallbackCalendar = []calendarEntry{
	{"1.28", date(2024, 11, 26), date(2025, 11, 26)},
	{"1.29", date(2025, 3, 23), date(2026, 3, 23)},
	{"1.30", date(2025, 7, 23), date(2026, 7, 23)},
	{"1.31", date(2025, 11, 26), date(2026, 11, 26)},
	{"1.32", date(2026, 3, 23), date(2027, 3, 23)},
	{"1.33", date(2026, 7, 29), date(2027, 7, 29)},
	{"1.34", date(2026, 12, 2), date(2027, 12, 2)},
	{"1.35", date(2027, 3, 27), date(2028, 3, 27)},
	{"1.36", date(2027, 8, 2), date(2028, 8, 2)},
}

// fallbackPosture resolves a version against fallbackCalendar. A version in
// the table gets its dates; one older than the oldest row is past the end of
// extended support, so it is Unsupported; anything else (newer than the
// table, or unparseable) is Unknown.
func fallbackPosture(version string, now time.Time) SupportPosture {
	for _, e := range fallbackCalendar {
		if e.version == version {
			return classifySupport(e.standardEnd, e.extendedEnd, now, true)
		}
	}
	maj, minor, ok := parseMinor(version)
	oldMaj, oldMinor, _ := parseMinor(fallbackCalendar[0].version)
	if ok && (maj < oldMaj || (maj == oldMaj && minor < oldMinor)) {
		return SupportPosture{Tier: SupportUnsupported, Fallback: true}
	}
	return SupportPosture{Tier: SupportUnknown}
}

// parseMinor splits a "major.minor" Kubernetes version. Both parts must be
// plain digits: strconv.Atoi alone would read "1.-5" as minor -5.
func parseMinor(version string) (maj, minor int, ok bool) {
	a, b, found := strings.Cut(version, ".")
	if !found || strings.TrimLeft(a, "0123456789") != "" || strings.TrimLeft(b, "0123456789") != "" {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	return maj, minor, err1 == nil && err2 == nil
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// supportVersionsAPI is the slice of EKS needed to resolve support windows —
// just DescribeClusterVersions. Both the fleet Service and the standalone
// SupportResolver depend on this, so the resolution logic has one home.
type supportVersionsAPI interface {
	DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
}

// resolveSupportPosture is the shared support-resolution core: prefer
// DescribeClusterVersions, fall back to the compiled-in calendar, then classify
// relative to now. Pure given (api, version, now) — used by both the fleet
// Service and the exported SupportResolver.
func resolveSupportPosture(ctx context.Context, api supportVersionsAPI, version string, now time.Time) SupportPosture {
	if version == "" {
		return SupportPosture{Tier: SupportUnknown}
	}
	std, ext, ok := supportDatesFromAPI(ctx, api, version)
	if !ok {
		return fallbackPosture(version, now)
	}
	return classifySupport(std, ext, now, false)
}

// ApplySupportType adjusts a version's support posture for one cluster's
// upgrade policy (UpgradePolicy.SupportType). With STANDARD the cluster never
// enters extended support: EKS auto-upgrades it at the end of standard support
// and never bills the extended-support premium.
// https://docs.aws.amazon.com/eks/latest/userguide/disable-extended-support.html
func ApplySupportType(p SupportPosture, supportType ekstypes.SupportType) SupportPosture {
	if supportType != ekstypes.SupportTypeStandard {
		return p
	}
	p.AutoUpgradeAtStandardEnd = true
	p.ExtraCostUSDPerHour = 0
	return p
}

// SupportTypeOf returns a cluster's upgrade-policy support type, or "" when
// the cluster reports no policy.
func SupportTypeOf(c *ekstypes.Cluster) ekstypes.SupportType {
	if c == nil || c.UpgradePolicy == nil {
		return ""
	}
	return c.UpgradePolicy.SupportType
}

// resolveSupport returns the support posture for a Kubernetes version, caching
// per version so `status -A` resolves each version at most once.
func (s *Service) resolveSupport(ctx context.Context, version string) SupportPosture {
	if version == "" {
		return SupportPosture{Tier: SupportUnknown}
	}

	// Concurrent sweeps of clusters on the same version share one lookup.
	posture, err := s.support.Get(ctx, version, func(ctx context.Context) (SupportPosture, error) {
		return resolveSupportPosture(ctx, s.clusterAPI, version, s.clock()), nil
	})
	if err != nil {
		// Only a waiter whose ctx ended gets here; resolveSupportPosture
		// itself never fails, it falls back to the built-in table.
		return fallbackPosture(version, s.clock())
	}
	return posture
}

// SupportResolver resolves EKS version support posture — the same logic behind
// `refresh status` — for reuse by `cluster upgrade-check` and `cluster
// describe`. Stateless apart from the EKS client; safe to construct per command.
type SupportResolver struct {
	api supportVersionsAPI
	now func() time.Time
}

// NewSupportResolver builds a resolver over an EKS client (anything exposing
// DescribeClusterVersions).
func NewSupportResolver(api supportVersionsAPI) *SupportResolver {
	return &SupportResolver{api: api, now: time.Now}
}

// Resolve returns the support posture for a Kubernetes version (e.g. "1.32"),
// falling back to the compiled-in calendar when DescribeClusterVersions is
// unavailable.
func (r *SupportResolver) Resolve(ctx context.Context, version string) SupportPosture {
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	return resolveSupportPosture(ctx, r.api, version, now)
}

// supportDatesFromAPI fetches the standard/extended end dates for a version via
// DescribeClusterVersions. ok is false when the API errors or the dates are
// absent, so the caller falls back to the compiled-in calendar.
func supportDatesFromAPI(ctx context.Context, api supportVersionsAPI, version string) (std, ext time.Time, ok bool) {
	if api == nil {
		return time.Time{}, time.Time{}, false
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterVersionsOutput, error) {
		return api.DescribeClusterVersions(rc, &eks.DescribeClusterVersionsInput{
			ClusterVersions: []string{version},
		})
	})
	if err != nil || out == nil {
		return time.Time{}, time.Time{}, false
	}
	for _, cv := range out.ClusterVersions {
		if aws.ToString(cv.ClusterVersion) != version {
			continue
		}
		if cv.EndOfStandardSupportDate == nil {
			return time.Time{}, time.Time{}, false
		}
		std = *cv.EndOfStandardSupportDate
		if cv.EndOfExtendedSupportDate != nil {
			ext = *cv.EndOfExtendedSupportDate
		}
		return std, ext, true
	}
	return time.Time{}, time.Time{}, false
}

// classifySupport derives the tier, days-remaining, and extended-cost callout
// from the standard/extended end dates relative to now.
func classifySupport(standardEnd, extendedEnd, now time.Time, fallback bool) SupportPosture {
	posture := SupportPosture{Fallback: fallback}
	if !standardEnd.IsZero() {
		su := standardEnd
		posture.StandardUntil = &su
	}
	if !extendedEnd.IsZero() {
		eu := extendedEnd
		posture.ExtendedUntil = &eu
	}

	switch {
	case !standardEnd.IsZero() && now.Before(standardEnd):
		posture.Tier = SupportStandard
		posture.DaysRemaining = daysBetween(now, standardEnd)
	case !extendedEnd.IsZero() && now.Before(extendedEnd):
		posture.Tier = SupportExtended
		posture.DaysRemaining = daysBetween(now, extendedEnd)
		posture.ExtraCostUSDPerHour = extendedSupportPremiumUSDPerHour
	case !standardEnd.IsZero() || !extendedEnd.IsZero():
		posture.Tier = SupportUnsupported
	default:
		posture.Tier = SupportUnknown
	}
	return posture
}

func daysBetween(from, to time.Time) *int {
	d := int(to.Sub(from).Hours() / 24)
	return &d
}
