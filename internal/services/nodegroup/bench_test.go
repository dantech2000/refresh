package nodegroup

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/mocks"
)

// manyNodegroupsMock returns an EKS mock that lists n nodegroups, each a custom
// AMI nodegroup (so List/Describe avoid any EC2/ASG/SSM client calls — see
// stubNodegroup). No live AWS is touched.
func manyNodegroupsMock(n int) *mocks.EKSAPI {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("ng-%d", i))
	}
	return &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		ListNodegroupsFn: func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup(aws.ToString(in.NodegroupName), ekstypes.NodegroupStatusActive)}, nil
		},
	}
}

// BenchmarkNodegroupList measures the List read path over a cluster with many
// nodegroups, including the per-nodegroup DescribeNodegroup fan-out.
func BenchmarkNodegroupList(b *testing.B) {
	svc := newTestService(manyNodegroupsMock(50))
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := svc.List(ctx, "my-cluster", ListOptions{}); err != nil {
			b.Fatalf("List: %v", err)
		}
	}
}

// BenchmarkNodegroupDescribe measures the Describe read path for a single
// nodegroup.
func BenchmarkNodegroupDescribe(b *testing.B) {
	ng := stubNodegroup("workers", ekstypes.NodegroupStatusActive)
	mock := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.29"),
		DescribeNodegroupFn: func(_ context.Context, _ *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	svc := newTestService(mock)
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		if _, err := svc.Describe(ctx, "my-cluster", "workers", DescribeOptions{}); err != nil {
			b.Fatalf("Describe: %v", err)
		}
	}
}
