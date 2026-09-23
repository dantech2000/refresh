package health

import (
	"context"
	"slices"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// The node health check reads every page of ListNodegroups.
func TestListNodegroupNames_FollowsNextToken(t *testing.T) {
	want := []string{"ng-1", "ng-2", "ng-3", "ng-4"}
	b := mocks.NewEKSAPI().WithPageSize(3).WithCluster("prod", "1.32")
	for _, n := range want {
		b.WithNodegroup(n, "1.32", ekstypes.AMITypesAl2023X8664Standard)
	}
	m := b.Build()
	hc := &HealthChecker{eksClient: m}

	got, err := hc.listNodegroupNames(context.Background(), "prod")
	if err != nil {
		t.Fatalf("listNodegroupNames: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("nodegroups = %v, want %v", got, want)
	}
	if m.Calls.ListNodegroups != 2 {
		t.Fatalf("ListNodegroups calls = %d, want 2 pages", m.Calls.ListNodegroups)
	}

	// An unknown cluster fails the check instead of reporting zero nodes.
	if r := hc.CheckNodeHealth(context.Background(), "staging"); r.Status != StatusFail {
		t.Fatalf("CheckNodeHealth(staging) = %+v, want fail", r)
	}
}
