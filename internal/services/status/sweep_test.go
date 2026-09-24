package status

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
)

// fleetFixture builds n clusters on one Kubernetes version, each running the
// same addons, with one of them behind.
func fleetFixture(n int, addonNames []string) (*fakeClusterAPI, *fakeNodegroups, *fakeAddons) {
	api := &fakeClusterAPI{describe: map[string]*ekstypes.Cluster{}}
	ng := &fakeNodegroups{byCluster: map[string][]nodegroup.NodegroupSummary{}}
	ad := &fakeAddons{installed: map[string][]addons.AddonSummary{}, available: map[string][]addons.AddonVersionInfo{}}
	for _, a := range addonNames {
		ad.available[a] = []addons.AddonVersionInfo{{Version: "v1.1.0"}, {Version: "v1.0.0"}}
	}
	for i := range n {
		name := fmt.Sprintf("c%02d", i)
		api.clusters = append(api.clusters, name)
		api.describe[name] = &ekstypes.Cluster{Name: aws.String(name), Version: aws.String("1.32")}
		for j, a := range addonNames {
			v := "v1.1.0"
			if j == 0 {
				v = "v1.0.0"
			}
			ad.installed[name] = append(ad.installed[name], addons.AddonSummary{Name: a, Version: v})
		}
	}
	return api, ng, ad
}

// A sweep looks up each (addon, version) once, not once per cluster, and
// hands the nodegroup lister the version it already has.
func TestListClusterStatuses_SharesLookupsAcrossClusters(t *testing.T) {
	addonNames := []string{"vpc-cni", "coredns", "kube-proxy"}
	api, ng, ad := fleetFixture(10, addonNames)
	svc := newTestService(api, ng, ad)

	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListClusterStatuses: %v", err)
	}
	if len(statuses) != 10 {
		t.Fatalf("statuses = %d, want 10", len(statuses))
	}
	for _, cs := range statuses {
		if cs.AddonsBehind.Behind != 1 || cs.AddonsBehind.Total != 3 || len(cs.Failures) != 0 {
			t.Errorf("%s: addons behind = %+v, failures = %v; want 1 of 3, none", cs.Name, cs.AddonsBehind, cs.Failures)
		}
	}
	if n := ad.versionCalls.Load(); n != int64(len(addonNames)) {
		t.Errorf("GetAvailableVersions calls = %d, want %d (one per addon)", n, len(addonNames))
	}
	if n := api.versionCalls.Load(); n != 0 && n != 1 {
		t.Errorf("DescribeClusterVersions calls = %d, want at most 1", n)
	}
	if n := ng.noVersion.Load(); n != 0 {
		t.Errorf("nodegroup listings without the cluster version = %d, want 0", n)
	}
}

// An addon with no published compatible version is an answer, not a
// failure, so it is memoized like one.
func TestListClusterStatuses_MemoizesNoVersionsFound(t *testing.T) {
	api, ng, ad := fleetFixture(4, []string{"custom"})
	delete(ad.available, "custom")
	svc := newTestService(api, ng, ad)

	statuses, err := svc.ListClusterStatuses(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListClusterStatuses: %v", err)
	}
	for _, cs := range statuses {
		if cs.AddonsBehind.Behind != 0 || cs.Incomplete {
			t.Errorf("%s: behind = %d, incomplete = %v; want 0, false", cs.Name, cs.AddonsBehind.Behind, cs.Incomplete)
		}
	}
	if n := ad.versionCalls.Load(); n != 1 {
		t.Errorf("GetAvailableVersions calls = %d, want 1", n)
	}
}

// A failed version lookup is reported on every cluster that needed it and is
// not memoized, so a later cluster can still succeed.
func TestListClusterStatuses_VersionLookupFailureNotMemoized(t *testing.T) {
	api, ng, ad := fleetFixture(3, []string{"vpc-cni"})
	ad.versionErr = map[string]error{"vpc-cni": fmt.Errorf("throttled")}
	svc := newTestService(api, ng, ad)

	statuses, _ := svc.ListClusterStatuses(context.Background(), ListOptions{MaxConcurrency: 1})
	for _, cs := range statuses {
		if !cs.Incomplete {
			t.Errorf("%s: want incomplete after a failed version lookup", cs.Name)
		}
	}
	if n := ad.versionCalls.Load(); n != 3 {
		t.Errorf("GetAvailableVersions calls = %d, want 3 (failures are retried per cluster)", n)
	}
}

// slowAddons adds a fixed latency to every AWS-backed call, standing in for
// the network round trip that dominates a real sweep.
type slowAddons struct {
	*fakeAddons
	delay time.Duration
}

func (s slowAddons) ListDetailed(ctx context.Context, cluster string, o addons.ListOptions) (addons.ListResult, error) {
	time.Sleep(s.delay)
	return s.fakeAddons.ListDetailed(ctx, cluster, o)
}

func (s slowAddons) GetAvailableVersions(ctx context.Context, addon, v string) ([]addons.AddonVersionInfo, error) {
	time.Sleep(s.delay)
	return s.fakeAddons.GetAvailableVersions(ctx, addon, v)
}

type slowNodegroups struct {
	*fakeNodegroups
	delay time.Duration
}

func (s slowNodegroups) ListDetailed(ctx context.Context, cluster string, o nodegroup.ListOptions) (nodegroup.ListResult, error) {
	// ListDetailed makes ListNodegroups, then DescribeNodegroup in parallel,
	// then the SSM lookup: three round trips, plus DescribeCluster when the
	// caller did not pass the version.
	trips := 3
	if o.ClusterVersion == "" {
		trips++
	}
	time.Sleep(time.Duration(trips) * s.delay)
	return s.fakeNodegroups.ListDetailed(ctx, cluster, o)
}

// BenchmarkListClusterStatuses sweeps 20 clusters with 6 addons each, with
// 5ms per simulated AWS round trip.
func BenchmarkListClusterStatuses(b *testing.B) {
	const delay = 5 * time.Millisecond
	api, ng, ad := fleetFixture(20, []string{"vpc-cni", "coredns", "kube-proxy", "ebs-csi", "pod-identity", "metrics-server"})
	for b.Loop() {
		svc := newTestService(api, nil, nil)
		svc.nodegroups = slowNodegroups{ng, delay}
		svc.addons = slowAddons{ad, delay}
		if _, err := svc.ListClusterStatuses(context.Background(), ListOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
