package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/mocks"
)

// ──────────────────────────────────────────────────────────────────────────────
// buildListCacheKey / buildDescribeCacheKey
// ──────────────────────────────────────────────────────────────────────────────

func TestBuildListCacheKey_Deterministic(t *testing.T) {
	opts := ListOptions{
		Regions:    []string{"us-east-1", "us-west-2"},
		Filters:    map[string]string{"name": "prod"},
		ShowHealth: true,
	}
	k1 := buildListCacheKey(opts)
	k2 := buildListCacheKey(opts)
	if k1 != k2 {
		t.Errorf("cache key not deterministic: %q vs %q", k1, k2)
	}
}

func TestBuildListCacheKey_DifferentOptionsDifferentKeys(t *testing.T) {
	k1 := buildListCacheKey(ListOptions{ShowHealth: true})
	k2 := buildListCacheKey(ListOptions{ShowHealth: false})
	if k1 == k2 {
		t.Errorf("different options produced the same cache key: %q", k1)
	}
}

func TestBuildListCacheKey_RegionOrderStable(t *testing.T) {
	// Reversed region order should produce the same key (sorted internally).
	k1 := buildListCacheKey(ListOptions{Regions: []string{"us-east-1", "us-west-2"}})
	k2 := buildListCacheKey(ListOptions{Regions: []string{"us-west-2", "us-east-1"}})
	if k1 != k2 {
		t.Errorf("region order should not affect cache key: %q vs %q", k1, k2)
	}
}

// Regression: ListAllRegionsWithMeta must narrow each per-region goroutine's
// options.Regions to a single region. Without this, every goroutine computes
// the same cache key (hashed from the parent's full region slice), the second
// to run hits a cache populated by the first, and clusters from one region
// surface under the other after the post-fetch Region relabel.
func TestRegionOptionsFor_NarrowsRegionsAndCacheKey(t *testing.T) {
	parent := ListOptions{
		Regions:    []string{"us-east-1", "us-west-2"},
		ShowHealth: true,
	}
	east := regionOptionsFor(parent, "us-east-1")
	west := regionOptionsFor(parent, "us-west-2")

	if got := east.Regions; len(got) != 1 || got[0] != "us-east-1" {
		t.Errorf("east Regions = %v, want [us-east-1]", got)
	}
	if got := west.Regions; len(got) != 1 || got[0] != "us-west-2" {
		t.Errorf("west Regions = %v, want [us-west-2]", got)
	}
	if east.AllRegions || west.AllRegions {
		t.Error("AllRegions must be cleared to prevent recursive fan-out")
	}
	if parent.Regions[0] != "us-east-1" || parent.Regions[1] != "us-west-2" {
		t.Errorf("parent.Regions mutated: %v", parent.Regions)
	}
	if buildListCacheKey(east) == buildListCacheKey(west) {
		t.Error("per-region cache keys must differ to avoid cross-region collision")
	}
}

func TestBuildDescribeCacheKey_ContainsClusterName(t *testing.T) {
	key := buildDescribeCacheKey("my-cluster", DescribeOptions{ShowHealth: true})
	if key == "" {
		t.Error("expected non-empty key")
	}
	// Different clusters must produce different keys.
	key2 := buildDescribeCacheKey("other-cluster", DescribeOptions{ShowHealth: true})
	if key == key2 {
		t.Errorf("different cluster names produced the same key: %q", key)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// shouldSkipCluster
// ──────────────────────────────────────────────────────────────────────────────

func TestShouldSkipCluster_NoFilters(t *testing.T) {
	svc := &ServiceImpl{}
	if svc.shouldSkipCluster("anything", nil) {
		t.Error("no filters: should not skip any cluster")
	}
}

func TestShouldSkipCluster_NameMatches(t *testing.T) {
	svc := &ServiceImpl{}
	if svc.shouldSkipCluster("prod-cluster", map[string]string{"name": "prod"}) {
		t.Error("name matches filter: should not be skipped")
	}
}

func TestShouldSkipCluster_NameNoMatch(t *testing.T) {
	svc := &ServiceImpl{}
	if !svc.shouldSkipCluster("dev-cluster", map[string]string{"name": "prod"}) {
		t.Error("name does not match filter: should be skipped")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// List
// ──────────────────────────────────────────────────────────────────────────────

func TestList_ReturnsClusterSummaries(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages:   [][]string{{"prod", "dev"}},
		nodegroupsPages: map[string][][]string{"prod": {{}}, "dev": {{}}},
	}
	svc := newTestServiceWithFake(t, fake)

	summaries, err := svc.List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
}

func TestList_ListClustersError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mock := &mocks.EKSAPI{
		ListClustersFn: func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
			return nil, errors.New("access denied")
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: logger}

	_, err := svc.List(context.Background(), ListOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestList_FilterByNameExcludesNonMatching(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages:   [][]string{{"prod-east", "dev-east", "staging"}},
		nodegroupsPages: map[string][][]string{"prod-east": {{}}, "dev-east": {{}}, "staging": {{}}},
	}
	svc := newTestServiceWithFake(t, fake)

	summaries, err := svc.List(context.Background(), ListOptions{Filters: map[string]string{"name": "east"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 clusters matching 'east', got %d", len(summaries))
	}
	for _, s := range summaries {
		if s.Name == "staging" {
			t.Error("staging should have been filtered out")
		}
	}
}

func TestList_NodeCountUsesManagedNodegroupTotals(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages: [][]string{{"staging-blue"}},
		nodegroupsPages: map[string][][]string{
			"staging-blue": {{"group-c", "group-d", "monolith-b"}},
		},
	}
	svc := newTestServiceWithFake(t, fake)

	summaries, err := svc.List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].NodeCount.Ready != 6 || summaries[0].NodeCount.Total != 6 {
		t.Fatalf("NodeCount = %+v, want 6/6", summaries[0].NodeCount)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Describe
// ──────────────────────────────────────────────────────────────────────────────

func TestDescribe_ReturnsClusterDetails(t *testing.T) {
	fake := &fakeEKSClient{
		nodegroupsPages: map[string][][]string{"my-cluster": {{}}},
		addonsPages:     map[string][][]string{"my-cluster": {{}}},
	}
	svc := newTestServiceWithFake(t, fake)

	details, err := svc.Describe(context.Background(), "my-cluster", DescribeOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.Name != "my-cluster" {
		t.Errorf("Name = %q, want %q", details.Name, "my-cluster")
	}
	if details.Version != "1.27" {
		t.Errorf("Version = %q, want %q", details.Version, "1.27")
	}
}

func TestDescribe_DescribeClusterError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return nil, errors.New("not found")
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: logger}

	_, err := svc.Describe(context.Background(), "my-cluster", DescribeOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// REF-57: a DescribeCluster response with a nil Cluster must yield a clear
// error, not a panic on the struct-pointer deref.
func TestDescribe_EmptyClusterResponseErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, _ *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{}, nil // nil Cluster
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: logger}

	_, err := svc.Describe(context.Background(), "my-cluster", DescribeOptions{})
	if err == nil {
		t.Fatal("expected an error for an empty DescribeCluster response, got nil")
	}
}

func TestDescribe_CachesResultOnSecondCall(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	callCount := 0
	mock := &mocks.EKSAPI{
		DescribeClusterFn: func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			callCount++
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
				Name:    in.Name,
				Version: aws.String("1.29"),
				Status:  ekstypes.ClusterStatusActive,
			}}, nil
		},
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{}, nil
		},
	}
	svc := &ServiceImpl{eksClient: mock, cache: NewCache(time.Minute), logger: logger}
	opts := DescribeOptions{}

	if _, err := svc.Describe(context.Background(), "my-cluster", opts); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := svc.Describe(context.Background(), "my-cluster", opts); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("DescribeCluster called %d times, want 1 (cache should serve second call)", callCount)
	}
}

func TestDescribe_WithAddonsIncludesAddonList(t *testing.T) {
	fake := &fakeEKSClient{
		nodegroupsPages: map[string][][]string{"my-cluster": {{}}},
		addonsPages:     map[string][][]string{"my-cluster": {{"coredns", "vpc-cni"}}},
	}
	svc := newTestServiceWithFake(t, fake)

	details, err := svc.Describe(context.Background(), "my-cluster", DescribeOptions{IncludeAddons: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(details.Addons) != 2 {
		t.Errorf("expected 2 addons, got %d", len(details.Addons))
	}
}
