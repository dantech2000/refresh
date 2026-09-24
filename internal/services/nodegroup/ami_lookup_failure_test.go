package nodegroup

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/types"
)

// ssmAccessDenied is the error shape the SDK returns when the caller lacks
// ssm:GetParameter, wrapped the way LatestAmiIDForType wraps it.
func ssmAccessDenied() error {
	return errors.Join(errors.New("reading SSM parameter /aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id"),
		&smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: ssm:GetParameter"})
}

// newDeniedLookupService wires a latest-AMI lookup that always fails and a
// current-AMI lookup that always succeeds.
func newDeniedLookupService(api EKSAPI, lookups *atomic.Int32) *ServiceImpl {
	svc := newTestService(api)
	svc.currentAMIFn = func(context.Context, *ekstypes.Nodegroup) string { return "ami-current" }
	svc.latestAMIFn = func(context.Context, string, ekstypes.AMITypes) (string, error) {
		lookups.Add(1)
		return "", ssmAccessDenied()
	}
	return svc
}

// With no ssm:GetParameter permission every managed nodegroup's AMI status is
// Unknown. That must be reported on the summary (so `refresh status` marks the
// row incomplete), not read as "nothing stale".
func TestListDetailed_LatestAMILookupFailureIsReported(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-b", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	var lookups atomic.Int32
	svc := newDeniedLookupService(api, &lookups)

	res, err := svc.ListDetailed(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Summaries) != 2 {
		t.Fatalf("summaries = %d, want 2 (the rows still render)", len(res.Summaries))
	}
	// The lookup is on the summary, not a listing failure: the nodegroup was
	// described.
	if len(res.Failures) != 0 {
		t.Errorf("failures = %+v, want none", res.Failures)
	}
	for _, s := range res.Summaries {
		if s.AMIStatus != types.AMIUnknown {
			t.Errorf("%s: AMIStatus = %v, want Unknown", s.Name, s.AMIStatus)
		}
		f := s.AMILookupFailure
		if f == nil {
			t.Fatalf("%s: AMILookupFailure = nil, want the lookup failure", s.Name)
		}
		if f.Kind != diag.KindNodegroup || f.Name != s.Name || f.Cluster != "prod" ||
			f.Operation != diag.OpGetParameter || f.Reason != diag.ReasonAccessDenied || f.AWSErrorCode != "AccessDeniedException" {
			t.Errorf("%s: AMILookupFailure = %+v, want an AccessDenied ssm:GetParameter failure of prod/%s", s.Name, *f, s.Name)
		}
		if strings.ContainsAny(f.Error, "\n") {
			t.Errorf("%s: Error is not one line: %q", s.Name, f.Error)
		}
	}
	// Failures are not memoized, so each nodegroup retries the lookup.
	if n := lookups.Load(); n < 1 {
		t.Errorf("lookups = %d, want at least 1", n)
	}
}

// Custom-AMI and updating nodegroups don't depend on the recommended AMI, so
// a failed lookup is not a failure for them.
func TestListDetailed_LatestAMILookupFailureIgnoredWhenIrrelevant(t *testing.T) {
	api := &mocks.EKSAPI{
		DescribeClusterFn: clusterFn("1.32"),
		ListNodegroupsFn:  listNodegroupsFn("ng-custom", "ng-updating"),
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			name := aws.ToString(in.NodegroupName)
			ng := stubNodegroup(name, ekstypes.NodegroupStatusActive) // AmiType CUSTOM
			if name == "ng-updating" {
				ng = stubNodegroup(name, ekstypes.NodegroupStatusUpdating)
				ng.AmiType = ekstypes.AMITypesAl2023X8664Standard
			}
			return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
		},
	}
	var lookups atomic.Int32
	svc := newDeniedLookupService(api, &lookups)

	res, err := svc.ListDetailed(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Summaries) != 2 || len(res.Failures) != 0 {
		t.Errorf("summaries=%d failures=%v; want 2 and none", len(res.Summaries), res.Failures)
	}
	for _, s := range res.Summaries {
		if s.AMILookupFailure != nil {
			t.Errorf("%s: AMILookupFailure = %+v, want nil", s.Name, *s.AMILookupFailure)
		}
	}
}

func TestDescribe_LatestAMILookupFailureIsReported(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	var lookups atomic.Int32
	svc := newDeniedLookupService(api, &lookups)

	d, err := svc.Describe(context.Background(), "prod", "ng-a", DescribeOptions{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d.AMIStatus != types.AMIUnknown || d.LatestAMI != "" {
		t.Errorf("AMIStatus=%v LatestAMI=%q; want Unknown and empty", d.AMIStatus, d.LatestAMI)
	}
	if f := d.AMILookupFailure; f == nil || f.Operation != diag.OpGetParameter || f.Reason != diag.ReasonAccessDenied {
		t.Errorf("lookup failure not reported: AMILookupFailure = %+v", f)
	}
}
