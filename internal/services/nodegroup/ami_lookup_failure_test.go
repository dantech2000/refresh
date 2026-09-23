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
// Unknown. That must be reported as a failure (so `refresh status` marks the
// row incomplete), not read as "nothing stale".
func TestListWithFailures_LatestAMILookupFailureIsReported(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng-b", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	var lookups atomic.Int32
	svc := newDeniedLookupService(api, &lookups)

	summaries, failures, err := svc.ListWithFailures(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListWithFailures: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2 (the rows still render)", len(summaries))
	}
	for _, s := range summaries {
		if s.AMIStatus != types.AMIUnknown {
			t.Errorf("%s: AMIStatus = %v, want Unknown", s.Name, s.AMIStatus)
		}
		if !strings.Contains(s.AMILookupError, "AccessDeniedException") {
			t.Errorf("%s: AMILookupError = %q, want the lookup error", s.Name, s.AMILookupError)
		}
	}
	if len(failures) != 2 {
		t.Fatalf("failures = %v, want one per nodegroup", failures)
	}
	for _, f := range failures {
		if !strings.Contains(f, "latest AMI lookup failed") {
			t.Errorf("failure %q does not name the AMI lookup", f)
		}
	}
	// Failures are not memoized, so each nodegroup retries the lookup.
	if n := lookups.Load(); n < 1 {
		t.Errorf("lookups = %d, want at least 1", n)
	}

	res, err := svc.ListDetailed(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Failures) != 0 || len(res.AMILookupFailures) != 2 {
		t.Errorf("ListDetailed Failures=%v AMILookupFailures=%v; want 0 and 2", res.Failures, res.AMILookupFailures)
	}
	var ae smithy.APIError
	if !errors.As(res.AMILookupErr, &ae) || ae.ErrorCode() != "AccessDeniedException" {
		t.Errorf("AMILookupErr = %v, want the unflattened AccessDeniedException", res.AMILookupErr)
	}
}

// Custom-AMI and updating nodegroups don't depend on the recommended AMI, so
// a failed lookup is not a failure for them.
func TestListWithFailures_LatestAMILookupFailureIgnoredWhenIrrelevant(t *testing.T) {
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

	summaries, failures, err := svc.ListWithFailures(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListWithFailures: %v", err)
	}
	if len(summaries) != 2 || len(failures) != 0 {
		t.Errorf("summaries=%d failures=%v; want 2 and none", len(summaries), failures)
	}
	for _, s := range summaries {
		if s.AMILookupError != "" {
			t.Errorf("%s: AMILookupError = %q, want empty", s.Name, s.AMILookupError)
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
	if d.AMILookupError == "" || d.LatestAMILookupErr() == nil {
		t.Errorf("lookup failure not reported: AMILookupError=%q err=%v", d.AMILookupError, d.LatestAMILookupErr())
	}
}
