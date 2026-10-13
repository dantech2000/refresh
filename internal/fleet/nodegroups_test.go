package fleet_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// latest133 is the AMI that the fake recommends for Kubernetes 1.33.
const latest133 = "ami-latest133"

// nodegroupWorld returns a fleet with the cluster prod on 1.33 and one
// nodegroup for each AMI status.
func nodegroupWorld(t *testing.T) (*fleettest.World, *awsxtest.Server, *awsx.Provider) {
	t.Helper()
	world, srv, provider := newFleet(t, 8, east+"/prod")
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Nodegroups = []*fleettest.Nodegroup{
			{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}, InstanceTypes: []string{"m5.large", "m5a.large"}, Desired: 2, Min: 1, Max: 4},
			{Name: "api", Version: "1.33", AutoScalingGroups: []string{"asg-api"}},
			{Name: "gpu", Version: "1.33", AMIType: "CUSTOM", LaunchTemplate: &fleettest.LaunchTemplate{ID: "lt-gpu", Version: "3"}, AutoScalingGroups: []string{"asg-gpu"}},
			{Name: "batch", Version: "1.33", Status: fleettest.StatusUpdating, AutoScalingGroups: []string{"asg-batch"}},
			{Name: "empty", Version: "1.33", AutoScalingGroups: []string{"asg-empty"}},
			{Name: "old", Version: "1.32", AutoScalingGroups: []string{"asg-old"}},
		}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "asg-web", InstanceIDs: []string{"i-web-1", "i-web-2"}},
			{Name: "asg-api", InstanceIDs: []string{"i-api-1"}},
			{Name: "asg-gpu", InstanceIDs: []string{"i-gpu-1"}},
			{Name: "asg-batch", InstanceIDs: []string{"i-batch-1"}},
			{Name: "asg-empty"},
			{Name: "asg-old", InstanceIDs: []string{"i-old-1"}},
		}
		s.Instances = []fleettest.Instance{
			{ID: "i-web-1", ImageID: "ami-stale", Type: "m5.large", AvailabilityZone: "us-east-1a"},
			{ID: "i-web-2", ImageID: "ami-stale", Type: "m5a.large", Lifecycle: "spot", AvailabilityZone: "us-east-1b"},
			{ID: "i-api-1", ImageID: latest133},
			{ID: "i-gpu-1", ImageID: "ami-custom"},
			{ID: "i-batch-1", ImageID: "ami-stale"},
			{ID: "i-old-1", ImageID: "ami-latest132"},
		}
		s.LaunchTemplateVersions = []fleettest.LaunchTemplateVersion{{TemplateID: "lt-gpu", Version: "3", ImageID: "ami-custom"}}
	})
	return world, srv, provider
}

// amiRow is what a test compares of one nodegroup.
type amiRow struct {
	Name            string
	Status          apidoc.AMIStatus
	Known           bool
	Current, Latest string
}

func amiRows(snap *fleet.Snapshot) []amiRow {
	var rows []amiRow
	for _, n := range snap.Nodegroups {
		status, known := n.AMIStatus()
		rows = append(rows, amiRow{n.Name, status, known, n.AMI.Current, n.AMI.Latest})
	}
	return rows
}

func count(srv *awsxtest.Server, op fleettest.Operation) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Operation == op {
			n++
		}
	}
	return n
}

func TestFetch_NodegroupsAndAMIs(t *testing.T) {
	_, srv, provider := nodegroupWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Have.Nodegroups != fleet.Collected || snap.Incomplete() {
		t.Fatalf("Have.Nodegroups = %v, failures = %v", snap.Have.Nodegroups, snap.Failures)
	}
	want := []amiRow{
		{"api", apidoc.AMILatest, true, latest133, latest133},
		{"batch", apidoc.AMIUpdating, true, "ami-stale", latest133},
		{"empty", apidoc.AMIUnknown, true, "", latest133},
		// A custom AMI has no recommended AMI: no SSM call for it.
		{"gpu", apidoc.AMICustom, true, "ami-custom", ""},
		// The latest AMI is the one for the version of the nodegroup.
		{"old", apidoc.AMILatest, true, "ami-latest132", "ami-latest132"},
		{"web", apidoc.AMIOutdated, true, "ami-stale", latest133},
	}
	if diff := cmp.Diff(want, amiRows(snap)); diff != "" {
		t.Errorf("nodegroups (-want +got):\n%s", diff)
	}

	web := snap.Nodegroups[5]
	if diff := cmp.Diff([]string{"m5.large", "m5a.large"}, web.InstanceTypes); diff != "" || web.Desired != 2 || web.Min != 1 || web.Max != 4 {
		t.Errorf("web = %+v", web)
	}
	if behind, known := snap.VersionBehind(snap.Nodegroups[4]); !behind || !known {
		t.Errorf("VersionBehind(old) = %v, %v, want behind", behind, known)
	}
	if behind, known := snap.VersionBehind(web); behind || !known {
		t.Errorf("VersionBehind(web) = %v, %v, want not behind", behind, known)
	}

	// The calls: the cluster, the list, six nodegroups, one SSM call for
	// each of the two versions, one template, one call for every group,
	// and one call for one instance of each group that needs it.
	for op, want := range map[fleettest.Operation]int{
		fleettest.EKSDescribeCluster: 1, fleettest.EKSListNodegroups: 1, fleettest.EKSDescribeNodegroup: 6,
		fleettest.SSMGetParameter: 2, fleettest.EC2DescribeLaunchTemplateVersions: 1,
		fleettest.AutoScalingDescribeAutoScalingGroups: 1, fleettest.EC2DescribeInstances: 1,
	} {
		if got := count(srv, op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}
	if n := len(srv.Requests()); n != 13 {
		t.Errorf("requests = %d, want 13", n)
	}
}

// Without AMI in the need, a nodegroup has no AMI group and costs no call
// beyond its describe.
func TestFetch_NodegroupsWithoutAMIs(t *testing.T) {
	_, srv, provider := nodegroupWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{Nodegroups: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Requests()); n != 8 {
		t.Errorf("requests = %d, want 8: the cluster, the list, and six nodegroups", n)
	}
	for _, n := range snap.Nodegroups {
		doc := snap.NodegroupDoc(n)
		if doc.AMI != nil || doc.Instances != nil || doc.Incomplete {
			t.Errorf("document of %s = %+v, want no AMI and no instances", n.Name, doc)
		}
	}
	if _, known := snap.Nodegroups[5].AMIStatus(); known {
		t.Error("AMIStatus is known for a nodegroup whose AMIs were not read")
	}
}

// An instance that is gone between the group call and the instance call
// costs its own nodegroup the current AMI and nothing else: no failure, and
// every other nodegroup keeps its status.
func TestFetch_InstanceThatIsGone(t *testing.T) {
	world, _, provider := nodegroupWorld(t)
	world.Edit(func(s *fleettest.State) {
		for i, inst := range s.Instances {
			if inst.ID == "i-api-1" {
				s.Instances = append(s.Instances[:i], s.Instances[i+1:]...)
				break
			}
		}
	})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Incomplete() {
		t.Errorf("failures = %+v, want none", snap.Failures)
	}
	rows := amiRows(snap)
	if want := (amiRow{"api", apidoc.AMIUnknown, true, "", latest133}); rows[0] != want {
		t.Errorf("api = %+v, want %+v", rows[0], want)
	}
	if rows[5].Status != apidoc.AMIOutdated || rows[4].Status != apidoc.AMILatest {
		t.Errorf("the other nodegroups lost their status: %+v", rows)
	}
}

// A lookup that fails makes the AMI of its nodegroups unknown, with a
// failure. A nodegroup whose status does not depend on the AMIs keeps it.
func TestFetch_AMILookupFails(t *testing.T) {
	tests := []struct {
		name string
		op   fleettest.Operation
		want []string // the failures
	}{
		{
			name: "the latest AMI",
			op:   fleettest.SSMGetParameter,
			want: []string{
				"Nodegroup api AccessDenied ssm:GetParameter", "Nodegroup empty AccessDenied ssm:GetParameter",
				"Nodegroup old AccessDenied ssm:GetParameter", "Nodegroup web AccessDenied ssm:GetParameter",
			},
		},
		{
			name: "the groups",
			op:   fleettest.AutoScalingDescribeAutoScalingGroups,
			want: []string{
				"Nodegroup api AccessDenied autoscaling:DescribeAutoScalingGroups", "Nodegroup empty AccessDenied autoscaling:DescribeAutoScalingGroups",
				"Nodegroup old AccessDenied autoscaling:DescribeAutoScalingGroups", "Nodegroup web AccessDenied autoscaling:DescribeAutoScalingGroups",
			},
		},
		{
			name: "the instances",
			op:   fleettest.EC2DescribeInstances,
			want: []string{
				// The group of "empty" has no instance to read.
				"Nodegroup api AccessDenied ec2:DescribeInstances",
				"Nodegroup old AccessDenied ec2:DescribeInstances", "Nodegroup web AccessDenied ec2:DescribeInstances",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world, _, provider := nodegroupWorld(t)
			world.SetFault(tt.op, fleettest.Fault{Deny: true})
			var fetcher fleet.Fetcher
			snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, failureKeys(snap.Failures)); diff != "" {
				t.Errorf("Failures (-want +got):\n%s", diff)
			}
			if snap.Have.Nodegroups != fleet.Partial {
				t.Errorf("Have.Nodegroups = %v, want Partial", snap.Have.Nodegroups)
			}
			for _, n := range snap.Nodegroups {
				status, known := n.AMIStatus()
				doc := snap.NodegroupDoc(n)
				unaffected := n.Name == "batch" || n.Name == "gpu" || (n.Name == "empty" && tt.op == fleettest.EC2DescribeInstances)
				switch {
				case unaffected:
					if !known || doc.Incomplete || doc.AMI == nil {
						t.Errorf("%s: status %s, known %v, document %+v; its status does not need the lookup", n.Name, status, known, doc)
					}
				default:
					if known || !doc.Incomplete || doc.AMI != nil || doc.Status == "" {
						t.Errorf("%s: status %s, known %v, document %+v; want unknown, incomplete, and the rest of the nodegroup", n.Name, status, known, doc)
					}
				}
			}
		})
	}
}

// A launch template that cannot be read is not a failure when an instance
// answers: one source is enough.
func TestFetch_OneAMISourceIsEnough(t *testing.T) {
	world, _, provider := nodegroupWorld(t)
	world.SetFault(fleettest.EC2DescribeLaunchTemplateVersions, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Incomplete() {
		t.Errorf("failures = %+v, want none", snap.Failures)
	}
	if gpu := snap.Nodegroups[3]; gpu.AMI.Current != "ami-custom" {
		t.Errorf("gpu: current AMI = %q, want the image of its instance", gpu.AMI.Current)
	}
}

func TestFetch_Instances(t *testing.T) {
	_, srv, provider := nodegroupWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{OnlyNodegroups: []string{"web"}, AMI: true, Instances: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodegroups) != 1 || snap.Incomplete() {
		t.Fatalf("nodegroups = %+v, failures = %+v", snap.Nodegroups, snap.Failures)
	}
	web := snap.Nodegroups[0]
	want := []fleet.Instance{
		{ID: "i-web-1", ImageID: "ami-stale", Type: "m5.large", State: "running", Lifecycle: "on-demand", AvailabilityZone: "us-east-1a"},
		{ID: "i-web-2", ImageID: "ami-stale", Type: "m5a.large", State: "running", Lifecycle: "spot", AvailabilityZone: "us-east-1b"},
	}
	if diff := cmp.Diff(want, web.Instances); diff != "" || web.InstancesState != fleet.Collected {
		t.Errorf("instances, state %v (-want +got):\n%s", web.InstancesState, diff)
	}
	// The group and the instances are read once, for the AMI and for the
	// list. No list call: the nodegroup was named.
	for op, want := range map[fleettest.Operation]int{
		fleettest.EKSListNodegroups: 0, fleettest.AutoScalingDescribeAutoScalingGroups: 1, fleettest.EC2DescribeInstances: 1,
	} {
		if got := count(srv, op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}
}

// The batched calls are cut in chunks: 50 group names, 100 instance IDs.
func TestFetch_ManyNodegroupsAreChunked(t *testing.T) {
	const n = 120
	world, srv, provider := newFleet(t, 32, east+"/big")
	world.Edit(func(s *fleettest.State) {
		for i := range n {
			name := fmt.Sprintf("ng-%03d", i)
			s.Clusters[0].Nodegroups = append(s.Clusters[0].Nodegroups, &fleettest.Nodegroup{Name: name, Version: "1.33", AutoScalingGroups: []string{"asg-" + name}})
			s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: "asg-" + name, InstanceIDs: []string{"i-" + name}})
			s.Instances = append(s.Instances, fleettest.Instance{ID: "i-" + name, ImageID: latest133})
		}
	})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "big", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Incomplete() {
		t.Fatalf("failures = %+v", snap.Failures[:1])
	}
	for _, ng := range snap.Nodegroups {
		if status, _ := ng.AMIStatus(); status != apidoc.AMILatest {
			t.Fatalf("%s: AMI status = %s, want Latest", ng.Name, status)
		}
	}
	for op, want := range map[fleettest.Operation]int{
		fleettest.AutoScalingDescribeAutoScalingGroups: 3, fleettest.EC2DescribeInstances: 2, fleettest.SSMGetParameter: 1,
	} {
		if got := count(srv, op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}
}

// The latest AMI is read once for the clusters of a run, and a lookup that
// failed is read again.
func TestFetch_LatestAMIIsShared(t *testing.T) {
	world, srv, provider := newFleet(t, 8, east+"/a", east+"/b")
	world.Edit(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			c.Nodegroups = []*fleettest.Nodegroup{{Name: "ng", Version: "1.33"}}
		}
	})
	world.SetFault(fleettest.SSMGetParameter, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	if snap, _ := fetcher.Fetch(testContext(t), provider.Clients(east), "a", fleet.Need{AMI: true}); !snap.Incomplete() {
		t.Fatal("no failure for a denied lookup")
	}
	world.SetFault(fleettest.SSMGetParameter, fleettest.Fault{})
	for _, cluster := range []string{"a", "b", "a"} {
		if snap, _ := fetcher.Fetch(testContext(t), provider.Clients(east), cluster, fleet.Need{AMI: true}); snap.Incomplete() {
			t.Fatalf("cluster %s: failures = %+v", cluster, snap.Failures)
		}
	}
	if got := count(srv, fleettest.SSMGetParameter); got != 2 {
		t.Errorf("GetParameter calls = %d, want 2: the one that failed, and one for the three reads after it", got)
	}
}

func TestFetch_OneNodegroupFails(t *testing.T) {
	world, _, provider := nodegroupWorld(t)
	world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "web"}, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"Nodegroup web AccessDenied eks:DescribeNodegroup"}, failureKeys(snap.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	web := snap.Nodegroups[5]
	if doc := snap.NodegroupDoc(web); web.State != fleet.Failed || !doc.Incomplete || doc.Scaling != nil || doc.Status != "" {
		t.Errorf("web: state %v, document %+v", web.State, doc)
	}
	if snap.Failures[0].Cluster != "prod" || snap.Failures[0].Region != east {
		t.Errorf("failure = %+v, want the cluster and the region", snap.Failures[0])
	}
}
