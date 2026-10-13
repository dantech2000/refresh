package fleet_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var (
	// now is the time of the fetcher in these tests.
	now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// everything is what "refresh status" reads.
	everything = fleet.Need{AMI: true, AMIAge: true, LatestAddons: true, Support: true, Compute: true, VPC: true}
)

// statusWorld returns a fleet for the reads of "refresh status": prod with
// a stale nodegroup and an add-on that is behind, and three clusters with
// no nodegroup: one on Auto Mode, one with Karpenter instances, one bare.
func statusWorld(t *testing.T) (*fleettest.World, *awsxtest.Server, *awsx.Provider) {
	t.Helper()
	world, srv, provider := newFleet(t, 32, east+"/prod", east+"/auto", east+"/karp", east+"/bare")
	world.Edit(func(s *fleettest.State) {
		prod := s.Clusters[0]
		prod.VPCID = "vpc-1"
		prod.SupportType = "STANDARD"
		prod.Nodegroups = []*fleettest.Nodegroup{
			{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}},
			{Name: "api", Version: "1.33", AutoScalingGroups: []string{"asg-api"}},
		}
		prod.Addons = []*fleettest.Addon{
			{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"},
			{Name: "coredns", Version: "v1.11.3-eksbuild.2"},
			{Name: "custom", Version: "v0.1.0"},
		}
		s.Clusters[1].AutoMode = true
		s.VPCs = []fleettest.VPC{{ID: "vpc-1", CIDR: "10.0.0.0/16"}}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "asg-web", InstanceIDs: []string{"i-web"}},
			{Name: "asg-api", InstanceIDs: []string{"i-api"}},
		}
		s.Instances = []fleettest.Instance{
			{ID: "i-web", ImageID: "ami-stale"},
			{ID: "i-api", ImageID: latest133},
			{ID: "i-karp", ImageID: "ami-x", Tags: map[string]string{"eks:eks-cluster-name": "karp", "karpenter.sh/nodepool": "default"}},
		}
		s.Images = []fleettest.Image{{ID: "ami-stale", Created: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.33", EndOfStandardSupport: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), EndOfExtendedSupport: time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC)},
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.33"}},
			{Addon: "vpc-cni", Version: "v1.20.0-eksbuild.1", ClusterVersions: []string{"1.33"}},
			{Addon: "vpc-cni", Version: "v1.21.0-eksbuild.1", ClusterVersions: []string{"1.34"}},
			{Addon: "coredns", Version: "v1.11.3-eksbuild.2", ClusterVersions: []string{"1.33"}},
			{Addon: "coredns", Version: "v1.11.3-eksbuild.1", ClusterVersions: []string{"1.33"}},
		}
	})
	return world, srv, provider
}

func fetchAll(t *testing.T, provider *awsx.Provider, cluster string) *fleet.Snapshot {
	t.Helper()
	fetcher := fleet.Fetcher{Now: func() time.Time { return now }}
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), cluster, everything)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestFetch_Everything(t *testing.T) {
	_, srv, provider := statusWorld(t)
	snap := fetchAll(t, provider, "prod")
	if snap.Incomplete() {
		t.Fatalf("failures = %+v", snap.Failures)
	}
	wantSupport := fleet.Support{
		Tier:          apidoc.SupportStandard,
		StandardEnd:   time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		ExtendedEnd:   time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC),
		DaysRemaining: 61, // 60 days and 12 hours: a part of a day counts
		AutoUpgrade:   true,
	}
	if diff := cmp.Diff(wantSupport, snap.Cluster.Support); diff != "" {
		t.Errorf("Support (-want +got):\n%s", diff)
	}
	if snap.Cluster.Compute != apidoc.ComputeManagedNodegroups || snap.Cluster.Network.VPCCIDR != "10.0.0.0/16" {
		t.Errorf("Compute = %q, VPC CIDR = %q", snap.Cluster.Compute, snap.Cluster.Network.VPCCIDR)
	}

	type addon struct {
		Name, Latest  string
		Behind, Known bool
	}
	var addons []addon
	for _, a := range snap.Addons {
		behind, known := a.Behind()
		addons = append(addons, addon{a.Name, a.Latest, behind, known})
	}
	wantAddons := []addon{
		{"coredns", "v1.11.3-eksbuild.2", false, true},
		// EKS publishes no version of this add-on: nothing to compare.
		{"custom", "", false, false},
		// The newest version for 1.33, not the one for 1.34.
		{"vpc-cni", "v1.20.0-eksbuild.1", true, true},
	}
	if diff := cmp.Diff(wantAddons, addons); diff != "" {
		t.Errorf("add-ons (-want +got):\n%s", diff)
	}

	web := snap.Nodegroups[1]
	if status, _ := web.AMIStatus(); status != apidoc.AMIOutdated || !web.AMI.CurrentCreatedAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("web: AMI status %s, created %v", status, web.AMI.CurrentCreatedAt)
	}
	if api := snap.Nodegroups[0]; !api.AMI.CurrentCreatedAt.IsZero() {
		t.Errorf("api: the age of an AMI that is not stale was read: %v", api.AMI.CurrentCreatedAt)
	}
	for op, want := range map[fleettest.Operation]int{
		fleettest.EKSDescribeClusterVersions: 1, fleettest.EKSDescribeAddonVersions: 3,
		fleettest.EC2DescribeVpcs: 1, fleettest.EC2DescribeImages: 1, fleettest.EC2DescribeInstances: 1,
	} {
		if got := count(srv, op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}

	doc := snap.ClusterDoc()
	if doc.Support == nil || doc.Support.DaysRemaining != 61 || doc.Compute != apidoc.ComputeManagedNodegroups || doc.Network.VPCCIDR != "10.0.0.0/16" {
		t.Errorf("document: support %+v, compute %q, network %+v", doc.Support, doc.Compute, doc.Network)
	}
	if l := doc.Addons[2].Latest; l == nil || l.Behind == nil || !*l.Behind || l.Version != "v1.20.0-eksbuild.1" {
		t.Errorf("document: latest of vpc-cni = %+v", l)
	}
	if l := doc.Addons[1].Latest; l == nil || l.Behind != nil || l.Version != "" {
		t.Errorf("document: latest of custom = %+v, want the group with no version", l)
	}
}

func TestFetch_Compute(t *testing.T) {
	tests := []struct {
		cluster   string
		want      apidoc.ComputeType
		wantCalls int // DescribeInstances calls: the two Karpenter probes
	}{
		{"prod", apidoc.ComputeManagedNodegroups, 0},
		{"auto", apidoc.ComputeAutoMode, 0},
		{"karp", apidoc.ComputeKarpenter, 2},
		{"bare", apidoc.ComputeNone, 2},
	}
	for _, tt := range tests {
		t.Run(tt.cluster, func(t *testing.T) {
			_, srv, provider := statusWorld(t)
			fetcher := fleet.Fetcher{}
			snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), tt.cluster, fleet.Need{Compute: true})
			if err != nil {
				t.Fatal(err)
			}
			if snap.Cluster.Compute != tt.want || snap.Have.Compute != fleet.Collected {
				t.Errorf("Compute = %q, state %v, want %q", snap.Cluster.Compute, snap.Have.Compute, tt.want)
			}
			if got := count(srv, fleettest.EC2DescribeInstances); got != tt.wantCalls {
				t.Errorf("DescribeInstances calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

// The advisory reads: one that fails loses an extra, and the output marks
// what is missing. It is not a failure, so a user with a narrow IAM policy
// does not get exit 4 from every run.
func TestFetch_AdvisoryReads(t *testing.T) {
	tests := []struct {
		name    string
		op      fleettest.Operation
		cluster string
		check   func(*testing.T, *fleet.Snapshot, apidoc.Cluster)
	}{
		{
			name: "the support calendar falls back to the built-in dates", op: fleettest.EKSDescribeClusterVersions, cluster: "prod",
			check: func(t *testing.T, snap *fleet.Snapshot, doc apidoc.Cluster) {
				t.Helper()
				s := snap.Cluster.Support
				// 1.33 left standard support on 2026-07-29 in the
				// built-in calendar.
				if !s.BuiltIn || s.Tier != apidoc.SupportExtended || doc.Support == nil || !doc.Support.BuiltIn {
					t.Errorf("Support = %+v, document %+v", s, doc.Support)
				}
			},
		},
		{
			name: "the Karpenter probe leaves the compute type unknown", op: fleettest.EC2DescribeInstances, cluster: "karp",
			check: func(t *testing.T, snap *fleet.Snapshot, doc apidoc.Cluster) {
				t.Helper()
				if snap.Have.Compute != fleet.Failed || doc.Compute != "" {
					t.Errorf("Have.Compute = %v, document compute %q, want Failed and no key", snap.Have.Compute, doc.Compute)
				}
			},
		},
		{
			name: "the VPC leaves the CIDR out", op: fleettest.EC2DescribeVpcs, cluster: "prod",
			check: func(t *testing.T, snap *fleet.Snapshot, doc apidoc.Cluster) {
				t.Helper()
				if snap.Have.VPC != fleet.Failed || doc.Network.VPCCIDR != "" || doc.Network.VPCID != "vpc-1" {
					t.Errorf("Have.VPC = %v, network %+v", snap.Have.VPC, doc.Network)
				}
			},
		},
		{
			name: "the image leaves the age out", op: fleettest.EC2DescribeImages, cluster: "prod",
			check: func(t *testing.T, snap *fleet.Snapshot, doc apidoc.Cluster) {
				t.Helper()
				if snap.Incomplete() {
					t.Error("the snapshot is incomplete")
				}
				web := doc.Nodegroups[1]
				if web.AMI == nil || web.AMI.Status != apidoc.AMIOutdated || !web.AMI.CurrentCreatedAt.IsZero() {
					t.Errorf("AMI of web = %+v, want outdated with no age", web.AMI)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world, _, provider := statusWorld(t)
			world.SetFault(tt.op, fleettest.Fault{Deny: true})
			snap := fetchAll(t, provider, tt.cluster)
			if snap.Incomplete() {
				t.Errorf("failures = %+v, want none: the read is advisory", snap.Failures)
			}
			doc := snap.ClusterDoc()
			if doc.Incomplete {
				t.Error("the document is incomplete")
			}
			tt.check(t, snap, doc)
		})
	}
}

// A stale AMI that was deregistered has no age, and nothing fails.
func TestFetch_DeregisteredAMIHasNoAge(t *testing.T) {
	world, _, provider := statusWorld(t)
	world.Edit(func(s *fleettest.State) { s.Images = nil })
	snap := fetchAll(t, provider, "prod")
	if snap.Incomplete() || !snap.Nodegroups[1].AMI.CurrentCreatedAt.IsZero() {
		t.Errorf("failures = %+v, created = %v", snap.Failures, snap.Nodegroups[1].AMI.CurrentCreatedAt)
	}
}

// The reads that are not advisory: the newest version of an add-on is what
// "refresh status" shows, and so is the support of a version that no
// calendar has.
func TestFetch_ReadsThatAreFailures(t *testing.T) {
	t.Run("the newest add-on version", func(t *testing.T) {
		world, _, provider := statusWorld(t)
		world.SetFault(fleettest.EKSDescribeAddonVersions, fleettest.Fault{Deny: true})
		snap := fetchAll(t, provider, "prod")
		want := []string{
			"Addon coredns AccessDenied eks:DescribeAddonVersions",
			"Addon custom AccessDenied eks:DescribeAddonVersions",
			"Addon vpc-cni AccessDenied eks:DescribeAddonVersions",
		}
		if diff := cmp.Diff(want, failureKeys(snap.Failures)); diff != "" {
			t.Errorf("Failures (-want +got):\n%s", diff)
		}
		doc := snap.ClusterDoc()
		if a := doc.Addons[0]; a.Latest != nil || !a.Incomplete || a.Version == "" {
			t.Errorf("document of coredns = %+v, want the add-on without latest, incomplete", a)
		}
	})
	t.Run("a version that no calendar has", func(t *testing.T) {
		world, _, provider := statusWorld(t)
		world.Edit(func(s *fleettest.State) { s.Clusters[0].Version = "1.99" })
		snap := fetchAll(t, provider, "prod")
		if snap.Have.Support != fleet.Failed || len(snap.Failures) != 1 || snap.Failures[0].Kind != apidoc.FailureCluster {
			t.Errorf("Have.Support = %v, failures = %+v", snap.Have.Support, snap.Failures)
		}
		if doc := snap.ClusterDoc(); doc.Support != nil || !doc.Incomplete {
			t.Errorf("document: support %+v, incomplete %v", doc.Support, doc.Incomplete)
		}
	})
}

// The lookups that do not depend on the cluster are made once for the
// clusters of a sweep.
func TestSweep_SharesTheLookups(t *testing.T) {
	var clusters []string
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		clusters = append(clusters, east+"/"+name)
	}
	world, srv, provider := newFleet(t, 32, clusters...)
	world.Edit(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			c.Nodegroups = []*fleettest.Nodegroup{{Name: "ng", Version: "1.33"}}
			c.Addons = []*fleettest.Addon{{Name: "vpc-cni", Version: "v1"}, {Name: "coredns", Version: "v1"}}
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1", ClusterVersions: []string{"1.33"}},
			{Addon: "coredns", Version: "v1", ClusterVersions: []string{"1.33"}},
		}
	})
	fetcher := fleet.Fetcher{Now: func() time.Time { return now }}
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east}}, fleet.SweepOptions{Need: everything})
	for _, snap := range res.Snapshots {
		if snap.Incomplete() {
			t.Fatalf("%s: failures = %+v", snap.Cluster.Name, snap.Failures)
		}
	}
	for op, want := range map[fleettest.Operation]int{
		fleettest.EKSDescribeClusterVersions: 1, // one calendar for the region
		fleettest.EKSDescribeAddonVersions:   2, // one for each add-on and version
		fleettest.SSMGetParameter:            1, // one for each version and AMI type
	} {
		if got := count(srv, op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}
}

func TestSupportOfTheBuiltInCalendar(t *testing.T) {
	world, _, provider := statusWorld(t)
	world.SetFault(fleettest.EKSDescribeClusterVersions, fleettest.Fault{Deny: true})
	tests := []struct {
		version string
		want    apidoc.SupportTier
	}{
		{"1.36", apidoc.SupportStandard},
		{"1.33", apidoc.SupportExtended},
		{"1.29", apidoc.SupportUnsupported},
		// Older than the first row: past every date.
		{"1.20", apidoc.SupportUnsupported},
	}
	for _, tt := range tests {
		world.Edit(func(s *fleettest.State) { s.Clusters[0].Version = tt.version })
		fetcher := fleet.Fetcher{Now: func() time.Time { return now }}
		snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{Support: true})
		if err != nil {
			t.Fatal(err)
		}
		if s := snap.Cluster.Support; s.Tier != tt.want || !s.BuiltIn || snap.Incomplete() {
			t.Errorf("%s: support %+v, failures %+v, want %s from the built-in calendar", tt.version, s, snap.Failures, tt.want)
		}
	}
}
