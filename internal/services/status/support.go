package status

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// Extended support roughly doubles the control-plane price: ~$0.60/hr vs the
// standard ~$0.10/hr, i.e. a ~$0.50/hr premium per cluster (~$4,380/yr). The
// premium is what we surface — it's the number that makes a lingering cluster
// worth upgrading.
const extendedSupportPremiumUSDPerHour = 0.50

// fallbackCalendar maps a Kubernetes minor version to its published EKS support
// window, used when DescribeClusterVersions is unavailable (missing permission,
// older API). Dates are AWS's published end-of-support calendar; rows derived
// from this table are flagged Fallback.
var fallbackCalendar = map[string]struct {
	standardEnd time.Time
	extendedEnd time.Time
}{
	"1.28": {date(2024, 11, 26), date(2025, 11, 26)},
	"1.29": {date(2025, 3, 23), date(2026, 3, 23)},
	"1.30": {date(2025, 7, 23), date(2026, 7, 23)},
	"1.31": {date(2025, 11, 26), date(2026, 11, 26)},
	"1.32": {date(2026, 3, 23), date(2027, 3, 23)},
	"1.33": {date(2026, 7, 23), date(2027, 7, 23)},
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// resolveSupport returns the support posture for a Kubernetes version, caching
// per version so `status -A` resolves each version at most once. It prefers
// DescribeClusterVersions and falls back to the compiled-in calendar.
func (s *Service) resolveSupport(ctx context.Context, version string) SupportPosture {
	if version == "" {
		return SupportPosture{Tier: SupportUnknown}
	}

	s.supportMu.Lock()
	if s.supportCache == nil {
		s.supportCache = make(map[string]SupportPosture)
	}
	if cached, ok := s.supportCache[version]; ok {
		s.supportMu.Unlock()
		return cached
	}
	s.supportMu.Unlock()

	posture := s.lookupSupport(ctx, version)

	s.supportMu.Lock()
	s.supportCache[version] = posture
	s.supportMu.Unlock()
	return posture
}

func (s *Service) lookupSupport(ctx context.Context, version string) SupportPosture {
	std, ext, ok := s.supportDatesFromAPI(ctx, version)
	fallback := false
	if !ok {
		cal, found := fallbackCalendar[version]
		if !found {
			return SupportPosture{Tier: SupportUnknown}
		}
		std, ext = cal.standardEnd, cal.extendedEnd
		fallback = true
	}
	return classifySupport(std, ext, s.clock(), fallback)
}

// supportDatesFromAPI fetches the standard/extended end dates for a version via
// DescribeClusterVersions. ok is false when the API errors or the dates are
// absent, so the caller falls back to the compiled-in calendar.
func (s *Service) supportDatesFromAPI(ctx context.Context, version string) (std, ext time.Time, ok bool) {
	if s.clusterAPI == nil {
		return time.Time{}, time.Time{}, false
	}
	out, err := s.clusterAPI.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{
		ClusterVersions: []string{version},
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
