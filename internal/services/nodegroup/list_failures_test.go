package nodegroup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func listNodegroupsFn(names ...string) func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
	}
}

// A nodegroup whose DescribeNodegroup fails is dropped from List (unchanged
// behavior) but reported by ListWithFailures.
func TestListWithFailures_DescribeError(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.32"),
		ListNodegroupsFn:  listNodegroupsFn("ng-ok", "ng-bad"),
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			if aws.ToString(in.NodegroupName) == "ng-bad" {
				return nil, &ekstypes.InvalidRequestException{Message: aws.String("not authorized")}
			}
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup("ng-ok", ekstypes.NodegroupStatusActive)}, nil
		},
	}
	svc := newTestService(m)

	summaries, failures, err := svc.ListWithFailures(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "ng-ok" {
		t.Errorf("summaries = %+v, want only ng-ok", summaries)
	}
	if len(failures) != 1 || !strings.HasPrefix(failures[0], "ng-bad: ") {
		t.Errorf("failures = %v, want one entry for ng-bad", failures)
	}

	legacy, err := svc.List(context.Background(), "prod", ListOptions{})
	if err != nil || len(legacy) != 1 {
		t.Errorf("List = %d summaries, err %v; want 1, nil", len(legacy), err)
	}
}

// Filtered-out nodegroups are not failures.
func TestListWithFailures_FilterIsNotFailure(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.32"),
		ListNodegroupsFn:  listNodegroupsFn("ng-a"),
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup("ng-a", ekstypes.NodegroupStatusActive)}, nil
		},
	}
	summaries, failures, err := newTestService(m).ListWithFailures(context.Background(), "prod",
		ListOptions{Filters: map[string]string{"status": "DEGRADED"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 0 || len(failures) != 0 {
		t.Errorf("summaries=%v failures=%v, want both empty", summaries, failures)
	}
}

// When ctx ends mid-list, every listed nodegroup is either summarized or
// reported; none silently vanishes.
func TestListWithFailures_CancelledContext(t *testing.T) {
	const n = 40
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("ng-%02d", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.32"),
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			cancel()
			return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup(aws.ToString(in.NodegroupName), ekstypes.NodegroupStatusActive)}, nil
		},
	}
	summaries, failures, err := newTestService(m).ListWithFailures(ctx, "prod", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(summaries) + len(failures); got != n {
		t.Errorf("summaries+failures = %d, want %d (no silent drops)", got, n)
	}
	if len(failures) == 0 {
		t.Error("expected failures after cancellation")
	}
}
