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

// Regression: the EKS API marks Nodegroup.scalingConfig as optional ("Required: No"),
// so getClusterNodegroups must not panic when a nodegroup comes back without one.
func TestGetClusterNodegroups_NilScalingConfig(t *testing.T) {
	mock := &mocks.EKSAPI{
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
				NodegroupName: aws.String("workers"),
				Status:        ekstypes.NodegroupStatusActive,
				ScalingConfig: nil,
			}}, nil
		},
	}
	svc := &ServiceImpl{eksClient: mock, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	nodegroups, err := svc.getClusterNodegroups(context.Background(), "my-cluster")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodegroups) != 1 {
		t.Fatalf("expected 1 nodegroup, got %d", len(nodegroups))
	}
	if nodegroups[0].DesiredSize != 0 {
		t.Errorf("DesiredSize = %d, want 0 for nil ScalingConfig", nodegroups[0].DesiredSize)
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
// shouldIncludeField
// ──────────────────────────────────────────────────────────────────────────────

func TestShouldIncludeField_EmptyInclude(t *testing.T) {
	svc := &ServiceImpl{}
	if !svc.shouldIncludeField("anything", nil) {
		t.Error("empty include list should include all fields")
	}
}

func TestShouldIncludeField_Match(t *testing.T) {
	svc := &ServiceImpl{}
	if !svc.shouldIncludeField("security", []string{"versions", "security"}) {
		t.Error("field in include list should be included")
	}
}

func TestShouldIncludeField_NoMatch(t *testing.T) {
	svc := &ServiceImpl{}
	if svc.shouldIncludeField("addons", []string{"versions", "security"}) {
		t.Error("field not in include list should not be included")
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

// ──────────────────────────────────────────────────────────────────────────────
// Compare
// ──────────────────────────────────────────────────────────────────────────────

func TestCompare_RequiresAtLeast2Clusters(t *testing.T) {
	svc := &ServiceImpl{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := svc.Compare(context.Background(), []string{"only-one"}, CompareOptions{})
	if err == nil {
		t.Fatal("expected error for < 2 clusters, got nil")
	}
}

func TestCompare_IdenticalVersionsAndAddons(t *testing.T) {
	fake := &fakeEKSClient{
		nodegroupsPages: map[string][][]string{"a": {{}}, "b": {{}}},
		addonsPages:     map[string][][]string{"a": {{"coredns"}}, "b": {{"coredns"}}},
	}
	svc := newTestServiceWithFake(t, fake)

	// Limit to versions+addons to avoid security diffs (fakeEKSClient has no encryption config).
	result, err := svc.Compare(context.Background(), []string{"a", "b"}, CompareOptions{Include: []string{"versions", "addons"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.CriticalDifferences > 0 {
		t.Errorf("expected no critical diffs for same-version clusters, got %d", result.Summary.CriticalDifferences)
	}
	if len(result.Clusters) != 2 {
		t.Errorf("expected 2 clusters in result, got %d", len(result.Clusters))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// analyzeDifferences
// ──────────────────────────────────────────────────────────────────────────────

func TestAnalyzeDifferences_VersionMismatch(t *testing.T) {
	svc := &ServiceImpl{}
	clusters := []ClusterDetails{
		{Name: "a", Version: "1.28"},
		{Name: "b", Version: "1.29"},
	}
	diffs := svc.analyzeDifferences(clusters, CompareOptions{})
	found := false
	for _, d := range diffs {
		if d.Field == "version" {
			found = true
		}
	}
	if !found {
		t.Error("expected a version difference, found none")
	}
}

func TestAnalyzeDifferences_NoVersionMismatch(t *testing.T) {
	svc := &ServiceImpl{}
	clusters := []ClusterDetails{
		{Name: "a", Version: "1.29"},
		{Name: "b", Version: "1.29"},
	}
	diffs := svc.analyzeDifferences(clusters, CompareOptions{Include: []string{"versions"}})
	for _, d := range diffs {
		if d.Field == "version" {
			t.Error("unexpected version difference for identical versions")
		}
	}
}

func TestAnalyzeDifferences_EncryptionMissingIsCritical(t *testing.T) {
	svc := &ServiceImpl{}
	clusters := []ClusterDetails{
		{Name: "a", Security: SecurityInfo{EncryptionEnabled: false}},
		{Name: "b", Security: SecurityInfo{EncryptionEnabled: true}},
	}
	diffs := svc.analyzeDifferences(clusters, CompareOptions{Include: []string{"security"}})
	for _, d := range diffs {
		if d.Field == "security.encryption" && d.Severity != "critical" {
			t.Errorf("encryption diff should be critical, got %q", d.Severity)
		}
	}
}

func TestAnalyzeDifferences_AddonMissingFromOneCluster(t *testing.T) {
	svc := &ServiceImpl{}
	clusters := []ClusterDetails{
		{Name: "a", Addons: []AddonInfo{{Name: "coredns", Version: "v1.9"}}},
		{Name: "b", Addons: []AddonInfo{}},
	}
	diffs := svc.analyzeDifferences(clusters, CompareOptions{Include: []string{"addons"}})
	found := false
	for _, d := range diffs {
		if d.Field == "addons.coredns" {
			found = true
		}
	}
	if !found {
		t.Error("expected addon 'coredns' missing diff, found none")
	}
}

func TestAnalyzeDifferences_AddonVersionDiff(t *testing.T) {
	svc := &ServiceImpl{}
	clusters := []ClusterDetails{
		{Name: "a", Addons: []AddonInfo{{Name: "coredns", Version: "v1.9"}}},
		{Name: "b", Addons: []AddonInfo{{Name: "coredns", Version: "v1.10"}}},
	}
	diffs := svc.analyzeDifferences(clusters, CompareOptions{Include: []string{"addons"}})
	found := false
	for _, d := range diffs {
		if d.Field == "addons.coredns.version" {
			found = true
		}
	}
	if !found {
		t.Error("expected coredns version diff, found none")
	}
}
