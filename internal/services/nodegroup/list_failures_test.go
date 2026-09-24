package nodegroup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

func listNodegroupsFn(names ...string) func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
	}
}

// A nodegroup whose DescribeNodegroup fails is dropped from List (unchanged
// behavior) but reported by ListDetailed as a structured failure.
func TestListDetailed_DescribeError(t *testing.T) {
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
	svc.awsConfig.Region = "us-east-1"

	res, err := svc.ListDetailed(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Summaries) != 1 || res.Summaries[0].Name != "ng-ok" {
		t.Errorf("summaries = %+v, want only ng-ok", res.Summaries)
	}
	want := diag.Failure{
		Kind: diag.KindNodegroup, Name: "ng-bad", Cluster: "prod", Region: "us-east-1",
		Operation: diag.OpDescribeNodegroup, Reason: diag.ReasonInvalidRequest,
		Error: "InvalidRequestException: not authorized", AWSErrorCode: "InvalidRequestException",
	}
	if len(res.Failures) != 1 || res.Failures[0] != want {
		t.Errorf("failures = %+v, want [%+v]", res.Failures, want)
	}

	legacy, err := svc.List(context.Background(), "prod", ListOptions{})
	if err != nil || len(legacy) != 1 {
		t.Errorf("List = %d summaries, err %v; want 1, nil", len(legacy), err)
	}
}

// Filtered-out nodegroups are not failures.
func TestListDetailed_FilterIsNotFailure(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.32"),
		ListNodegroupsFn:  listNodegroupsFn("ng-a"),
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup("ng-a", ekstypes.NodegroupStatusActive)}, nil
		},
	}
	res, err := newTestService(m).ListDetailed(context.Background(), "prod",
		ListOptions{Filters: map[string]string{"status": "DEGRADED"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Summaries) != 0 || len(res.Failures) != 0 {
		t.Errorf("summaries=%v failures=%v, want both empty", res.Summaries, res.Failures)
	}
}

// When ctx ends mid-list, every listed nodegroup is either summarized or
// reported; none silently vanishes.
func TestListDetailed_CancelledContext(t *testing.T) {
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
	res, err := newTestService(m).ListDetailed(ctx, "prod", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(res.Summaries) + len(res.Failures); got != n {
		t.Errorf("summaries+failures = %d, want %d (no silent drops)", got, n)
	}
	if len(res.Failures) == 0 {
		t.Fatal("expected failures after cancellation")
	}
	for _, f := range res.Failures {
		if f.Kind != diag.KindNodegroup || f.Cluster != "prod" || !strings.HasPrefix(f.Name, "ng-") {
			t.Errorf("failure %+v does not name a nodegroup of prod", f)
		}
		if f.Reason != diag.ReasonNotAttempted && f.Reason != diag.ReasonInterrupted {
			t.Errorf("failure %+v: reason = %s, want NotAttempted or Interrupted", f, f.Reason)
		}
	}
}
