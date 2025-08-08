package cluster

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	eksTypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// fakeEKSClient implements EKSAPI and returns paginated data for tests.
type fakeEKSClient struct {
	clustersPages   [][]string
	nodegroupsPages map[string][][]string
	addonsPages     map[string][][]string
}

func (f *fakeEKSClient) ListClusters(ctx context.Context, in *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	// Use NextToken to index pages: "" -> 0, "t1" -> 1, etc.
	idx := 0
	if in.NextToken != nil && *in.NextToken != "" {
		if *in.NextToken == "t2" {
			idx = 1
		}
	}
	out := &eks.ListClustersOutput{Clusters: []string{}}
	if idx < len(f.clustersPages) {
		out.Clusters = append(out.Clusters, f.clustersPages[idx]...)
		if idx+1 < len(f.clustersPages) {
			token := "t2"
			out.NextToken = &token
		}
	}
	return out, nil
}

func (f *fakeEKSClient) DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	name := aws.ToString(in.Name)
	return &eks.DescribeClusterOutput{Cluster: &eksTypes.Cluster{
		Name:            &name,
		Status:          eksTypes.ClusterStatusActive,
		Version:         aws.String("1.27"),
		PlatformVersion: aws.String("eks.1"),
		Endpoint:        aws.String("https://example"),
		Tags:            map[string]string{},
		ResourcesVpcConfig: &eksTypes.VpcConfigResponse{
			EndpointPublicAccess:  true,
			EndpointPrivateAccess: false,
		},
	}}, nil
}

func (f *fakeEKSClient) ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	cluster := aws.ToString(in.ClusterName)
	pages := f.nodegroupsPages[cluster]
	idx := 0
	if in.NextToken != nil && *in.NextToken != "" {
		if *in.NextToken == "t2" {
			idx = 1
		}
	}
	out := &eks.ListNodegroupsOutput{Nodegroups: []string{}}
	if idx < len(pages) {
		out.Nodegroups = append(out.Nodegroups, pages[idx]...)
		if idx+1 < len(pages) {
			token := "t2"
			out.NextToken = &token
		}
	}
	return out, nil
}

func (f *fakeEKSClient) DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	name := aws.ToString(in.NodegroupName)
	desired := int32(2)
	return &eks.DescribeNodegroupOutput{Nodegroup: &eksTypes.Nodegroup{
		NodegroupName: &name,
		Status:        eksTypes.NodegroupStatusActive,
		ScalingConfig: &eksTypes.NodegroupScalingConfig{DesiredSize: &desired},
		InstanceTypes: []string{"t3.medium"},
	}}, nil
}

func (f *fakeEKSClient) ListAddons(ctx context.Context, in *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	cluster := aws.ToString(in.ClusterName)
	pages := f.addonsPages[cluster]
	idx := 0
	if in.NextToken != nil && *in.NextToken != "" {
		if *in.NextToken == "t2" {
			idx = 1
		}
	}
	out := &eks.ListAddonsOutput{Addons: []string{}}
	if idx < len(pages) {
		out.Addons = append(out.Addons, pages[idx]...)
		if idx+1 < len(pages) {
			token := "t2"
			out.NextToken = &token
		}
	}
	return out, nil
}

func (f *fakeEKSClient) DescribeAddon(ctx context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	name := aws.ToString(in.AddonName)
	return &eks.DescribeAddonOutput{Addon: &eksTypes.Addon{
		AddonName:    &name,
		AddonVersion: aws.String("v1"),
		Status:       eksTypes.AddonStatusActive,
	}}, nil
}

func newTestServiceWithFake(t *testing.T, fake *fakeEKSClient) *ServiceImpl {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return &ServiceImpl{
		eksClient: fake,
		cache:     NewCache(1 * time.Minute),
		logger:    logger,
	}
}

func TestListClusters_PaginationMergesResults(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages: [][]string{{"dev", "prod"}, {"qa"}},
		nodegroupsPages: map[string][][]string{
			"dev":  {{}},
			"prod": {{}},
			"qa":   {{}},
		},
	}
	svc := newTestServiceWithFake(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	summaries, err := svc.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 clusters from paginated results, got %d", len(summaries))
	}
	names := map[string]bool{}
	for _, s := range summaries {
		names[s.Name] = true
	}
	for _, n := range []string{"dev", "prod", "qa"} {
		if !names[n] {
			t.Fatalf("expected cluster %s present", n)
		}
	}
}

func TestGetClusterNodegroups_Pagination(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages: [][]string{{"test"}},
		nodegroupsPages: map[string][][]string{
			"test": {{"ng-a", "ng-b"}, {"ng-c"}},
		},
	}
	svc := newTestServiceWithFake(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ngs, err := svc.getClusterNodegroups(ctx, "test")
	if err != nil {
		t.Fatalf("getClusterNodegroups failed: %v", err)
	}
	if len(ngs) != 3 {
		t.Fatalf("expected 3 nodegroups from paginated results, got %d", len(ngs))
	}
}

func TestGetClusterAddons_Pagination(t *testing.T) {
	fake := &fakeEKSClient{
		clustersPages: [][]string{{"test"}},
		nodegroupsPages: map[string][][]string{
			"test": {{}},
		},
		addonsPages: map[string][][]string{
			"test": {{"coredns", "kube-proxy"}, {"vpc-cni"}},
		},
	}
	svc := newTestServiceWithFake(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addons, err := svc.getClusterAddons(ctx, "test")
	if err != nil {
		t.Fatalf("getClusterAddons failed: %v", err)
	}
	if len(addons) != 3 {
		t.Fatalf("expected 3 addons from paginated results, got %d", len(addons))
	}
}
