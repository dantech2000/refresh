package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/mocks"
)

// benchService builds a ServiceImpl wired to an in-memory EKS mock, the same
// way the *_test.go files do. No live AWS is touched.
func benchService(b *testing.B, api *mocks.EKSAPI) *ServiceImpl {
	b.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return &ServiceImpl{
		eksClient: api,
		cache:     NewCache(1 * time.Minute),
		logger:    logger,
	}
}

// manyClustersMock returns a mock that lists n clusters, each backed by a
// single small nodegroup. Used to exercise the bounded-concurrency fan-out in
// List under load without any network calls.
func manyClustersMock(n int) *mocks.EKSAPI {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("cluster-%d", i))
	}
	return &mocks.EKSAPI{
		ListClustersFn: func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
			return &eks.ListClustersOutput{Clusters: names}, nil
		},
		DescribeClusterFn: func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
				Name:    in.Name,
				Version: aws.String("1.29"),
				Status:  ekstypes.ClusterStatusActive,
			}}, nil
		},
		ListNodegroupsFn: func(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{aws.ToString(in.ClusterName) + "-ng"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			desired := int32(3)
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
				NodegroupName: in.NodegroupName,
				Status:        ekstypes.NodegroupStatusActive,
				ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: &desired},
				InstanceTypes: []string{"m5.large"},
			}}, nil
		},
	}
}

// BenchmarkClusterList measures the List read path over a fleet of mock
// clusters, including the per-cluster DescribeCluster + nodegroup fan-out.
func BenchmarkClusterList(b *testing.B) {
	svc := benchService(b, manyClustersMock(50))
	ctx := context.Background()
	opts := ListOptions{}

	b.ResetTimer()
	for range b.N {
		// Use a fresh option set each iteration but reuse the service. The
		// cache is keyed on options; clearing it keeps every iteration doing
		// real work rather than serving the first run's cached slice.
		svc.cache = NewCache(1 * time.Minute)
		if _, err := svc.List(ctx, opts); err != nil {
			b.Fatalf("List: %v", err)
		}
	}
}

// BenchmarkClusterDescribe measures the Describe read path for a single
// cluster (DescribeCluster + nodegroup + addon enrichment).
func BenchmarkClusterDescribe(b *testing.B) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.29").
		WithNodegroup("ng-a", "1.29", ekstypes.AMITypesAl2X8664).
		WithNodegroup("ng-b", "1.29", ekstypes.AMITypesAl2X8664).
		WithAddon("coredns", "v1.10", ekstypes.AddonStatusActive).
		Build()
	svc := benchService(b, api)
	ctx := context.Background()
	opts := DescribeOptions{IncludeAddons: true}

	b.ResetTimer()
	for range b.N {
		svc.cache = NewCache(1 * time.Minute)
		if _, err := svc.Describe(ctx, "prod", opts); err != nil {
			b.Fatalf("Describe: %v", err)
		}
	}
}

// BenchmarkClusterListAllRegions exercises the bounded-concurrency multi-region
// fan-out's offline-drivable seam.
//
// ListAllRegionsWithMeta itself cannot run offline: forRegion → NewService
// builds real eks/ec2/iam/sts clients via *.NewFromConfig and the per-region
// List would hit the live EKS API. So this benchmark measures the pure
// per-region option/cache-key derivation (regionOptionsFor + buildListCacheKey)
// that the fan-out performs for every region, which is the part REF-7 can
// assert is deterministic and offline. The List-with-many-clusters path above
// covers the bounded fan-out behaviour itself.
func BenchmarkClusterListAllRegions(b *testing.B) {
	parent := ListOptions{
		Regions:    []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-1"},
		ShowHealth: true,
		Filters:    map[string]string{"name": "prod"},
	}

	b.ResetTimer()
	for range b.N {
		for _, r := range parent.Regions {
			opts := regionOptionsFor(parent, r)
			_ = buildListCacheKey(opts)
		}
	}
}
