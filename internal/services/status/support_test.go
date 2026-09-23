package status

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestClassifySupport(t *testing.T) {
	now := date(2026, 6, 11)
	std := date(2026, 11, 26) // standard ends in the future
	ext := date(2027, 11, 26)

	t.Run("standard", func(t *testing.T) {
		p := classifySupport(std, ext, now, false)
		if p.Tier != SupportStandard {
			t.Fatalf("tier = %s, want standard", p.Tier)
		}
		if p.DaysRemaining == nil || *p.DaysRemaining <= 0 {
			t.Errorf("expected positive days remaining, got %v", p.DaysRemaining)
		}
		if p.ExtraCostUSDPerHour != 0 {
			t.Errorf("standard should have no extra cost, got %v", p.ExtraCostUSDPerHour)
		}
	})

	t.Run("extended", func(t *testing.T) {
		p := classifySupport(date(2026, 3, 23), date(2027, 3, 23), now, false)
		if p.Tier != SupportExtended {
			t.Fatalf("tier = %s, want extended", p.Tier)
		}
		if p.ExtraCostUSDPerHour != extendedSupportPremiumUSDPerHour {
			t.Errorf("extra cost = %v, want %v", p.ExtraCostUSDPerHour, extendedSupportPremiumUSDPerHour)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		p := classifySupport(date(2024, 11, 26), date(2025, 11, 26), now, false)
		if p.Tier != SupportUnsupported {
			t.Fatalf("tier = %s, want unsupported", p.Tier)
		}
	})

	t.Run("unknown when no dates", func(t *testing.T) {
		p := classifySupport(time.Time{}, time.Time{}, now, false)
		if p.Tier != SupportUnknown {
			t.Fatalf("tier = %s, want unknown", p.Tier)
		}
	})
}

func TestResolveSupport_FallbackCalendar(t *testing.T) {
	// API errors → resolver falls back to the compiled-in calendar and flags it.
	svc := &Service{
		clusterAPI: &fakeClusterAPI{versionsErr: mocks.AccessDenied()},
		now:        func() time.Time { return date(2026, 6, 11) },
	}
	p := svc.resolveSupport(context.Background(), "1.31")
	if !p.Fallback {
		t.Error("expected Fallback=true when API unavailable")
	}
	// 1.31 standard ended 2025-11-26 and extended ends 2026-11-26, so as of
	// 2026-06-11 the cluster is in extended support.
	if p.Tier != SupportExtended {
		t.Errorf("tier = %s, want extended (1.31 as of 2026-06-11)", p.Tier)
	}
}

func TestResolveSupport_FromAPI(t *testing.T) {
	api := &fakeClusterAPI{
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {
				ClusterVersion:           aws.String("1.32"),
				EndOfStandardSupportDate: timePtr(date(2027, 3, 23)),
				EndOfExtendedSupportDate: timePtr(date(2028, 3, 23)),
			},
		},
	}
	svc := &Service{clusterAPI: api, now: func() time.Time { return date(2026, 6, 11) }}
	p := svc.resolveSupport(context.Background(), "1.32")
	if p.Fallback {
		t.Error("expected API-derived posture, not fallback")
	}
	if p.Tier != SupportStandard {
		t.Errorf("tier = %s, want standard", p.Tier)
	}

	// Second call must hit the cache (no extra API calls).
	_ = svc.resolveSupport(context.Background(), "1.32")
	if got := api.versionCalls.Load(); got != 1 {
		t.Errorf("DescribeClusterVersions called %d times, want 1 (cached)", got)
	}
}

// TestSupportResolver_Reuse verifies the exported resolver (used by
// cluster upgrade-check / describe) resolves the same posture as the fleet
// Service, from the API and from the fallback calendar. (REF-145)
func TestSupportResolver_Reuse(t *testing.T) {
	api := &fakeClusterAPI{
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {
				ClusterVersion:           aws.String("1.32"),
				EndOfStandardSupportDate: timePtr(date(2027, 3, 23)),
				EndOfExtendedSupportDate: timePtr(date(2028, 3, 23)),
			},
		},
	}
	r := NewSupportResolver(api)
	r.now = func() time.Time { return date(2026, 6, 11) }

	p := r.Resolve(context.Background(), "1.32")
	if p.Tier != SupportStandard || p.Fallback {
		t.Errorf("API posture = %s (fallback=%v), want standard/non-fallback", p.Tier, p.Fallback)
	}
	if p.DaysRemaining == nil || *p.DaysRemaining <= 0 {
		t.Errorf("expected positive days remaining, got %v", p.DaysRemaining)
	}

	// Empty version → unknown, no API call.
	if got := r.Resolve(context.Background(), ""); got.Tier != SupportUnknown {
		t.Errorf("empty version tier = %s, want unknown", got.Tier)
	}

	// API failure → compiled-in fallback calendar.
	rf := NewSupportResolver(&fakeClusterAPI{versionsErr: mocks.AccessDenied()})
	rf.now = func() time.Time { return date(2026, 6, 11) }
	pf := rf.Resolve(context.Background(), "1.31")
	if !pf.Fallback || pf.Tier != SupportExtended {
		t.Errorf("fallback posture = %s (fallback=%v), want extended/fallback", pf.Tier, pf.Fallback)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// The fallback calendar must run oldest to newest with no minor skipped, and
// each row's dates must be ordered: a gap would resolve a real version to
// "unknown", and a misordered row would break the older-than-oldest rule.
func TestFallbackCalendar_SortedAndComplete(t *testing.T) {
	if len(fallbackCalendar) == 0 {
		t.Fatal("fallback calendar is empty")
	}
	for i, e := range fallbackCalendar {
		maj, minor, ok := parseMinor(e.version)
		if !ok {
			t.Fatalf("row %d: unparseable version %q", i, e.version)
		}
		if !e.standardEnd.Before(e.extendedEnd) {
			t.Errorf("%s: standard end %v is not before extended end %v", e.version, e.standardEnd, e.extendedEnd)
		}
		if i == 0 {
			continue
		}
		prev := fallbackCalendar[i-1]
		pmaj, pminor, _ := parseMinor(prev.version)
		if maj != pmaj || minor != pminor+1 {
			t.Errorf("row %d: %s does not follow %s (calendar must be contiguous)", i, e.version, prev.version)
		}
		if !prev.standardEnd.Before(e.standardEnd) || !prev.extendedEnd.Before(e.extendedEnd) {
			t.Errorf("%s: end dates are not after %s's", e.version, prev.version)
		}
	}
}

// Spot-check the rows against the AWS release calendar
// (https://docs.aws.amazon.com/eks/latest/userguide/kubernetes-versions.html).
func TestFallbackCalendar_PublishedDates(t *testing.T) {
	want := map[string][2]time.Time{
		"1.33": {date(2026, 7, 29), date(2027, 7, 29)},
		"1.34": {date(2026, 12, 2), date(2027, 12, 2)},
		"1.35": {date(2027, 3, 27), date(2028, 3, 27)},
		"1.36": {date(2027, 8, 2), date(2028, 8, 2)},
	}
	for _, e := range fallbackCalendar {
		if w, ok := want[e.version]; ok {
			if !e.standardEnd.Equal(w[0]) || !e.extendedEnd.Equal(w[1]) {
				t.Errorf("%s = %v / %v, want %v / %v", e.version, e.standardEnd, e.extendedEnd, w[0], w[1])
			}
			delete(want, e.version)
		}
	}
	for v := range want {
		t.Errorf("fallback calendar has no row for %s", v)
	}
}

func TestFallbackPosture(t *testing.T) {
	now := date(2026, 9, 23)
	cases := []struct {
		version  string
		tier     SupportTier
		fallback bool
	}{
		{"1.36", SupportStandard, true},
		{"1.34", SupportStandard, true},
		{"1.33", SupportExtended, true}, // standard ended 2026-07-29
		{"1.29", SupportUnsupported, true},
		{"1.28", SupportUnsupported, true},
		{"1.27", SupportUnsupported, true}, // older than the oldest row
		{"1.9", SupportUnsupported, true},
		{"0.99", SupportUnsupported, true},
		{"1.37", SupportUnknown, false}, // newer than the table
		{"2.0", SupportUnknown, false},
		{"latest", SupportUnknown, false},
	}
	for _, tc := range cases {
		p := fallbackPosture(tc.version, now)
		if p.Tier != tc.tier || p.Fallback != tc.fallback {
			t.Errorf("fallbackPosture(%s) = %s (fallback=%v), want %s (fallback=%v)", tc.version, p.Tier, p.Fallback, tc.tier, tc.fallback)
		}
	}

	// Through the resolver: an API failure on a pre-calendar version is
	// Unsupported, not Unknown.
	rf := NewSupportResolver(&fakeClusterAPI{versionsErr: mocks.AccessDenied()})
	rf.now = func() time.Time { return now }
	if p := rf.Resolve(context.Background(), "1.25"); p.Tier != SupportUnsupported {
		t.Errorf("1.25 via resolver = %s, want unsupported", p.Tier)
	}
}

// A cluster with upgrade policy STANDARD never pays the extended-support
// premium; EKS auto-upgrades it at the end of standard support.
func TestApplySupportType(t *testing.T) {
	ext := classifySupport(date(2026, 3, 23), date(2027, 3, 23), date(2026, 6, 11), false)
	if ext.ExtraCostUSDPerHour == 0 {
		t.Fatal("precondition: extended posture should carry the premium")
	}

	if got := ApplySupportType(ext, ""); got != ext {
		t.Errorf("no policy changed the posture: %+v", got)
	}
	if got := ApplySupportType(ext, ekstypes.SupportTypeExtended); got != ext {
		t.Errorf("EXTENDED policy changed the posture: %+v", got)
	}

	got := ApplySupportType(ext, ekstypes.SupportTypeStandard)
	if !got.AutoUpgradeAtStandardEnd || got.ExtraCostUSDPerHour != 0 {
		t.Errorf("STANDARD policy = %+v, want auto-upgrade and no premium", got)
	}
	if SupportTypeOf(nil) != "" || SupportTypeOf(&ekstypes.Cluster{}) != "" {
		t.Error("SupportTypeOf without a policy should be empty")
	}
	if got.Tier != ext.Tier {
		t.Errorf("tier changed: %s", got.Tier)
	}
}

// The fleet sweep applies each cluster's own policy to the shared per-version
// posture: two clusters on the same version can differ.
func TestAssembleCluster_UpgradePolicy(t *testing.T) {
	api := &fakeClusterAPI{
		describe: map[string]*ekstypes.Cluster{
			"std": {Name: aws.String("std"), Version: aws.String("1.32"),
				UpgradePolicy: &ekstypes.UpgradePolicyResponse{SupportType: ekstypes.SupportTypeStandard}},
			"ext": {Name: aws.String("ext"), Version: aws.String("1.32"),
				UpgradePolicy: &ekstypes.UpgradePolicyResponse{SupportType: ekstypes.SupportTypeExtended}},
		},
		versions: map[string]ekstypes.ClusterVersionInformation{
			"1.32": {
				ClusterVersion:           aws.String("1.32"),
				EndOfStandardSupportDate: timePtr(date(2026, 3, 23)),
				EndOfExtendedSupportDate: timePtr(date(2027, 3, 23)),
			},
		},
	}
	svc := newTestService(api, &fakeNodegroups{}, &fakeAddons{})
	ctx := context.Background()

	ext := svc.assembleCluster(ctx, "ext")
	if ext.Support.ExtraCostUSDPerHour != extendedSupportPremiumUSDPerHour || ext.Support.AutoUpgradeAtStandardEnd {
		t.Errorf("EXTENDED cluster support = %+v, want the premium", ext.Support)
	}
	std := svc.assembleCluster(ctx, "std")
	if std.Support.ExtraCostUSDPerHour != 0 || !std.Support.AutoUpgradeAtStandardEnd {
		t.Errorf("STANDARD cluster support = %+v, want auto-upgrade and no premium", std.Support)
	}
}

// fakeClusterAPI implements ClusterAPI for tests.
type fakeClusterAPI struct {
	clusters    []string
	describe    map[string]*ekstypes.Cluster
	versions    map[string]ekstypes.ClusterVersionInformation
	versionsErr error
	// versionCalls is atomic: a fleet sweep resolves support from multiple
	// assembleCluster goroutines concurrently (concurrent cache misses for the
	// same version each hit the API once).
	versionCalls atomic.Int64
}

func (f *fakeClusterAPI) ListClusters(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: f.clusters}, nil
}

func (f *fakeClusterAPI) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	c, ok := f.describe[aws.ToString(in.Name)]
	if !ok {
		return nil, mocks.NotFound()
	}
	return &eks.DescribeClusterOutput{Cluster: c}, nil
}

func (f *fakeClusterAPI) DescribeClusterVersions(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	f.versionCalls.Add(1)
	if f.versionsErr != nil {
		return nil, f.versionsErr
	}
	out := &eks.DescribeClusterVersionsOutput{}
	for _, v := range in.ClusterVersions {
		if cv, ok := f.versions[v]; ok {
			out.ClusterVersions = append(out.ClusterVersions, cv)
		}
	}
	return out, nil
}
