package nodegroup

import (
	"context"
	"sync/atomic"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/mocks"
)

// A caller that already described the cluster passes its version, and
// ListDetailed skips its own DescribeCluster.
func TestListDetailed_ClusterVersionSkipsDescribeCluster(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(api)
	svc.currentAMIFn = func(context.Context, *ekstypes.Nodegroup) string { return "ami-current" }
	svc.latestAMIFn = func(context.Context, string, ekstypes.AMITypes) (string, error) { return "ami-current", nil }

	res, err := svc.ListDetailed(context.Background(), "prod", ListOptions{ClusterVersion: "1.32"})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(res.Summaries))
	}
	if n := api.Calls.DescribeCluster; n != 0 {
		t.Errorf("DescribeCluster calls = %d, want 0", n)
	}
}

// A shared LatestAMI cache carries lookups from one ListDetailed call to the
// next, so a fleet sweep asks SSM once per (version, AMI type).
func TestListDetailed_SharedLatestAMICache(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-b", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(api)
	svc.currentAMIFn = func(context.Context, *ekstypes.Nodegroup) string { return "ami-current" }
	var lookups atomic.Int32
	shared := awsinternal.NewLatestAMICache(func(context.Context, string, ekstypes.AMITypes) (string, error) {
		lookups.Add(1)
		return "ami-current", nil
	})

	for range 3 {
		if _, err := svc.ListDetailed(context.Background(), "prod", ListOptions{LatestAMI: shared}); err != nil {
			t.Fatalf("ListDetailed: %v", err)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("latest-AMI lookups = %d, want 1 across three calls", n)
	}
}
