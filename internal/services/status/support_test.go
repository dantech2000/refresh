package status

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
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
		clusterAPI: &fakeClusterAPI{versionsErr: errors.New("access denied")},
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
	if api.versionCalls != 1 {
		t.Errorf("DescribeClusterVersions called %d times, want 1 (cached)", api.versionCalls)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// fakeClusterAPI implements ClusterAPI for tests.
type fakeClusterAPI struct {
	clusters     []string
	describe     map[string]*ekstypes.Cluster
	versions     map[string]ekstypes.ClusterVersionInformation
	versionsErr  error
	versionCalls int
}

func (f *fakeClusterAPI) ListClusters(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: f.clusters}, nil
}

func (f *fakeClusterAPI) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	c, ok := f.describe[aws.ToString(in.Name)]
	if !ok {
		return nil, errors.New("cluster not found")
	}
	return &eks.DescribeClusterOutput{Cluster: c}, nil
}

func (f *fakeClusterAPI) DescribeClusterVersions(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	f.versionCalls++
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
