package fleet_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet"
)

// benchSnapshots returns n snapshots as "refresh status" reads them: each
// with two nodegroups and four add-ons.
func benchSnapshots(n int) []*fleet.Snapshot {
	snaps := make([]*fleet.Snapshot, n)
	for i := range snaps {
		s := &fleet.Snapshot{
			Cluster: fleet.Cluster{
				Name: fmt.Sprintf("cluster-%03d", i), Region: "us-east-1", Status: "ACTIVE",
				Version: fleet.Version{Major: 1, Minor: 33}, Tags: map[string]string{"team": "a"},
				Network: fleet.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}},
			},
			Have: fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected, Support: fleet.Collected, Compute: fleet.Collected},
		}
		for _, name := range []string{"a", "b"} {
			s.Nodegroups = append(s.Nodegroups, fleet.Nodegroup{
				Name: name, State: fleet.Collected, Status: "ACTIVE", Version: fleet.Version{Major: 1, Minor: 33},
				InstanceTypes: []string{"m5.large"}, AMIType: "AL2023_x86_64_STANDARD",
				AMI: fleet.AMI{State: fleet.Collected, Current: "ami-1", Latest: "ami-2", CurrentCreatedAt: time.Unix(1, 0)},
			})
		}
		for _, name := range []string{"vpc-cni", "coredns", "kube-proxy", "ebs-csi"} {
			s.Addons = append(s.Addons, fleet.Addon{
				Name: name, State: fleet.Collected, Status: "ACTIVE", Version: "v1.19.0-eksbuild.1",
				Latest: "v1.20.0-eksbuild.1", LatestState: fleet.Collected,
			})
		}
		snaps[i] = s
	}
	return snaps
}

func BenchmarkClusterDoc(b *testing.B) {
	snaps := benchSnapshots(200)
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range snaps {
			s.ClusterDoc()
		}
	}
}

func BenchmarkParseVersion(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := fleet.ParseVersion("v1.33.2-eks-abc"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompareAddonVersions(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		fleet.CompareAddonVersions("v1.19.0-eksbuild.1", "v1.19.0-eksbuild.10")
	}
}

// The gates below bound the allocations of the three document mappers for
// one cluster of a fleet. They catch a mapper that starts to copy what it
// could share, which a sweep of some hundred clusters would show.

func TestMapperAllocations(t *testing.T) {
	snap := benchSnapshots(1)[0]
	tests := []struct {
		name  string
		limit float64
		run   func()
	}{
		// The cluster with its two nodegroups and four add-ons.
		{"ClusterDoc", 40, func() { snap.ClusterDoc() }},
		{"NodegroupDoc", 8, func() { snap.NodegroupDoc(snap.Nodegroups[0]) }},
		// Four of them are the comparison of the two versions.
		{"AddonDoc", 8, func() { fleet.AddonDoc(snap.Addons[0]) }},
	}
	for _, tt := range tests {
		got := testing.AllocsPerRun(100, tt.run)
		if got > tt.limit {
			t.Errorf("%s made %.0f allocations, want at most %.0f", tt.name, got, tt.limit)
		}
		t.Logf("%s: %.0f allocations", tt.name, got)
	}
}

// ParseVersion and the comparison of two add-on versions run for every row
// of a sort. The first must not allocate.
func TestParseVersionDoesNotAllocate(t *testing.T) {
	if got := testing.AllocsPerRun(100, func() { _, _ = fleet.ParseVersion("1.33") }); got != 0 {
		t.Errorf("ParseVersion made %.0f allocations, want 0", got)
	}
}
