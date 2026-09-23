package status

import (
	"context"
	"net"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// The fleet sweep follows ListClusters' NextToken to the last page.
func TestListClusterStatuses_FollowsNextToken(t *testing.T) {
	want := []string{"c1", "c2", "c3", "c4", "c5"}
	b := mocks.NewEKSAPI().WithPageSize(2)
	for _, name := range want {
		b.WithCluster(name, "1.32")
	}
	api := b.Build()
	svc := newTestService(nil, &fakeNodegroups{}, &fakeAddons{})
	svc.clusterAPI = api

	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{MaxConcurrency: 2})
	if err != nil {
		t.Fatalf("ListClusterStatuses: %v", err)
	}
	var got []string
	for _, s := range statuses {
		if s.Incomplete() {
			t.Errorf("%s incomplete: %v", s.Name, s.Errors)
		}
		got = append(got, s.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("clusters = %v, want %v across pages", got, want)
	}
	if api.Calls.ListClusters != 3 {
		t.Fatalf("ListClusters calls = %d, want 3 pages", api.Calls.ListClusters)
	}
}

// An addon whose latest version can't be read because of an API error
// (throttling that outlasted retries, a missing permission, a network
// failure) is never counted as behind. The row is flagged incomplete with a
// "latest version unknown" reason, and the other addons are still compared.
func TestListClusterStatuses_AddonVersionLookupFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"throttled", mocks.Throttling()},
		{"access denied", mocks.AccessDenied()},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
			ad := &fakeAddons{
				installed: map[string][]addons.AddonSummary{"prod": {
					{Name: "vpc-cni", Version: "v1.10.0", Status: "ACTIVE"},
					{Name: "coredns", Version: "v1.11.1", Status: "ACTIVE"},
				}},
				// Listed oldest first: the fake sorts like the real service,
				// so v1.11.4 is the latest.
				available:  map[string][]addons.AddonVersionInfo{"coredns": {{Version: "v1.11.1"}, {Version: "v1.11.4"}}},
				versionErr: map[string]error{"vpc-cni": tc.err},
			}
			svc := newTestService(nil, &fakeNodegroups{}, ad)
			svc.clusterAPI = api

			statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
			if err != nil {
				t.Fatalf("ListClusterStatuses: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("rows = %d, want 1", len(statuses))
			}
			cs := statuses[0]
			if !cs.Incomplete() {
				t.Fatal("row must be incomplete when an addon's latest version is unknown")
			}
			if joined := strings.Join(cs.Errors, "; "); !strings.Contains(joined, "vpc-cni (latest version unknown)") {
				t.Fatalf("errors = %q, want vpc-cni latest version unknown", joined)
			}
			if cs.AddonsBehind.Total != 2 || cs.AddonsBehind.Behind != 1 || !slices.Equal(cs.AddonsBehind.Names, []string{"coredns"}) {
				t.Fatalf("addons behind = %+v, want only coredns (vpc-cni unknown, not behind)", cs.AddonsBehind)
			}
		})
	}
}
