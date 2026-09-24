package addons

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

// updateMock is a cluster with vpc-cni installed whose first DescribeAddon
// call throttles and whose UpdateAddon is denied.
func updateMock() (*mocks.EKSAPI, *atomic.Int32) {
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("vpc-cni", "v1.14.0", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.15.0", "v1.14.0"}, "1.32").
		Build()
	var describes atomic.Int32
	describe := m.DescribeAddonFn
	m.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, opts ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		if describes.Add(1) == 1 {
			return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
		}
		return describe(ctx, in, opts...)
	}
	m.UpdateAddonFn = func(context.Context, *eks.UpdateAddonInput, ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform eks:UpdateAddon"}
	}
	return m, &describes
}

// The update path retries its DescribeAddon and formats an UpdateAddon
// failure, keeping the API error reachable.
func TestUpdate_RetriesDescribeAndFormatsUpdateError(t *testing.T) {
	m, describes := updateMock()
	svc := NewService(m, logger())

	_, err := svc.Update(t.Context(), "prod", "vpc-cni", UpdateOptions{Version: "v1.15.0"})
	if describes.Load() != 2 {
		t.Errorf("DescribeAddon calls = %d, want 2 (throttle retried)", describes.Load())
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) || ae.ErrorCode() != "AccessDeniedException" {
		t.Fatalf("err = %v, want a wrapped AccessDeniedException", err)
	}
	if !strings.Contains(err.Error(), "insufficient AWS permissions while updating addon vpc-cni") {
		t.Errorf("err = %v, want FormatAWSError permission guidance", err)
	}
}

// UpdateAll reports a failed add-on with the Failed status and a failure
// that names the reason and the IAM action, with one line of error text.
func TestUpdateAll_FailedAddonHasAFailure(t *testing.T) {
	m, _ := updateMock()
	svc := NewService(m, logger())

	results, err := svc.UpdateAll(t.Context(), "prod", UpdateAllOptions{})
	if err != nil {
		t.Fatalf("UpdateAll: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	r := results[0]
	if r.Status != StatusFailed || !r.Failed() {
		t.Fatalf("result = %+v, want Failed with a failure", r)
	}
	f := r.Failure
	if f.Kind != diag.KindAddon || f.Cluster != "prod" || f.Reason != diag.ReasonAccessDenied || f.Retryable ||
		!strings.HasPrefix(f.Error, "AccessDeniedException") || strings.Contains(f.Error, "\n") {
		t.Errorf("failure = %+v, want a one-line AccessDenied failure", f)
	}
}
