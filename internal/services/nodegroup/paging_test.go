package nodegroup

import (
	"context"
	"slices"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

// With the mock paging ListNodegroups two at a time, every nodegroup still
// comes back: the service follows NextToken to the last page.
func TestListDetailed_FollowsNextToken(t *testing.T) {
	want := []string{"ng-1", "ng-2", "ng-3", "ng-4", "ng-5"}
	b := mocks.NewEKSAPI().WithPageSize(2).WithCluster("prod", "1.32")
	for _, name := range want {
		// Custom AMIs skip the SSM latest-AMI lookup.
		b.WithNodegroup(name, "1.32", ekstypes.AMITypesCustom)
	}
	m := b.Build()

	res, err := newTestService(m).ListDetailed(context.Background(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %v, want none", res.Failures)
	}
	var got []string
	for _, s := range res.Summaries {
		got = append(got, s.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("nodegroups = %v, want all of %v across pages", got, want)
	}
	if m.Calls.ListNodegroups != 3 {
		t.Fatalf("ListNodegroups calls = %d, want 3 pages", m.Calls.ListNodegroups)
	}
}

// An unknown cluster surfaces EKS's typed not-found error instead of an
// empty list.
func TestListDetailed_UnknownClusterIsError(t *testing.T) {
	m := mocks.NewEKSAPI().WithCluster("prod", "1.32").
		WithNodegroup("ng-1", "1.32", ekstypes.AMITypesCustom).Build()

	_, err := newTestService(m).ListDetailed(context.Background(), "staging", ListOptions{})
	if err == nil {
		t.Fatal("ListDetailed on an unknown cluster returned no error")
	}
	// The error names the call that failed, so a caller can build the
	// failure without knowing the service's call order.
	if op := diag.OperationOf(err); op != diag.OpDescribeCluster {
		t.Errorf("operation = %q, want %q", op, diag.OpDescribeCluster)
	}
}
