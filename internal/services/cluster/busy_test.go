package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestChangesInProgress(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32", mocks.ClusterStatus(ekstypes.ClusterStatusUpdating)).
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-b", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusUpdating).
		WithAddon("coredns", "v1.11.1", ekstypes.AddonStatusActive).
		WithPageSize(1).
		Build()
	describe := api.DescribeNodegroupFn
	api.DescribeNodegroupFn = func(ctx context.Context, in *eks.DescribeNodegroupInput, o ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		out, err := describe(ctx, in, o...)
		if err == nil && aws.ToString(in.NodegroupName) == "ng-b" {
			out.Nodegroup.Status = ekstypes.NodegroupStatusUpdating
		}
		return out, err
	}

	got, err := ChangesInProgress(t.Context(), api, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cluster UPDATING, nodegroup ng-b UPDATING, add-on vpc-cni UPDATING"; got.String() != want {
		t.Fatalf("changes = %q, want %q", got, want)
	}
}

func TestChangesInProgressSettled(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("calm", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	got, err := ChangesInProgress(t.Context(), api, "calm")
	if err != nil || len(got) != 0 {
		t.Fatalf("changes = %v, %v", got, err)
	}
}

func TestChangesInProgressError(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	api.DescribeClusterFn = func(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return nil, mocks.AccessDenied()
	}
	_, err := ChangesInProgress(t.Context(), api, "prod")
	var ae interface{ ErrorCode() string }
	if !errors.As(err, &ae) || ae.ErrorCode() != "AccessDeniedException" {
		t.Fatalf("err = %v, want AccessDenied", err)
	}
}
