package aws

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/mocks"
)

// Cluster resolution lists every page: an exact match that only appears on
// the last page must still win over the substring matches on earlier pages.
func TestResolveClusterName_FollowsNextToken(t *testing.T) {
	withTTY(t, false)
	b := mocks.NewEKSAPI().WithPageSize(2)
	for _, name := range []string{"prod-legacy", "prod-2", "staging", "prod"} {
		b.WithCluster(name, "1.32")
	}
	m := b.Build()

	got, err := resolveClusterName(context.Background(), m, "prod", ClusterNameOptions{})
	if err != nil {
		t.Fatalf("resolveClusterName: %v", err)
	}
	if got != "prod" {
		t.Fatalf("got %q, want prod from the last page", got)
	}
	if m.Calls.ListClusters != 2 {
		t.Fatalf("ListClusters calls = %d, want 2 pages", m.Calls.ListClusters)
	}

	names, err := listClusterNames(context.Background(), m)
	if err != nil || !slices.Equal(names, []string{"prod-legacy", "prod-2", "staging", "prod"}) {
		t.Fatalf("listClusterNames = %v, %v", names, err)
	}
}

// A failed page surfaces as an error, not a truncated list.
func TestListClusterNames_PageErrorIsReturned(t *testing.T) {
	m := mocks.NewEKSAPI().WithPageSize(1).WithCluster("a", "1.32").WithCluster("b", "1.32").Build()
	list := m.ListClustersFn
	calls := 0
	m.ListClustersFn = func(ctx context.Context, in *eks.ListClustersInput, opts ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		calls++
		if calls > 1 {
			return nil, mocks.AccessDenied()
		}
		return list(ctx, in, opts...)
	}
	if names, err := listClusterNames(context.Background(), m); err == nil {
		t.Fatalf("listClusterNames = %v, want the second page's error", names)
	}
}
