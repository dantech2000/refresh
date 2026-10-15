package nodegroup_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// amiKind is a kind of nodegroup that looks its AMI up by another path.
type amiKind struct {
	name    string
	amiType string
	// path is the SSM parameter of the latest AMI for 1.32.
	path string
	edit func(ng *fleettest.Nodegroup)
}

func amiKinds() []amiKind {
	return []amiKind{
		{name: "gpu", amiType: "AL2023_x86_64_NVIDIA", path: "/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/nvidia/recommended/image_id",
			edit: func(ng *fleettest.Nodegroup) { ng.InstanceTypes = []string{"g5.xlarge"} }},
		{name: "al2gpu", amiType: "AL2_x86_64_GPU", path: "/aws/service/eks/optimized-ami/1.32/amazon-linux-2-gpu/recommended/image_id",
			edit: func(ng *fleettest.Nodegroup) { ng.InstanceTypes = []string{"p3.2xlarge"} }},
		{name: "spot", amiType: "AL2023_x86_64_STANDARD", path: "/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id",
			edit: func(ng *fleettest.Nodegroup) {
				ng.CapacityType, ng.InstanceTypes, ng.UpdateStrategy = "SPOT", []string{"m5.large", "m5a.large", "m6i.large"}, "MINIMAL"
			}},
		{name: "bottlerocket", amiType: "BOTTLEROCKET_x86_64", path: "/aws/service/bottlerocket/aws-k8s-1.32/x86_64/latest/image_id"},
		{name: "windows", amiType: "WINDOWS_CORE_2022_x86_64", path: "/aws/service/ami-windows-latest/Windows_Server-2022-English-Core-EKS_Optimized-1.32/image_id"},
	}
}

// amiWorld has, for each kind, a nodegroup on an old AMI ("<kind>-old")
// and one on the latest ("<kind>-new"). The fake derives the latest AMI
// from the version in the path of a parameter; the Windows path has the
// version in a name, so the world sets that parameter.
func amiWorld() *fleettest.World {
	return updateWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroups, c.Nodes, c.Pods, c.PDBs, c.Deployments = nil, nil, nil, nil, nil
		s.AutoScalingGroups, s.Instances = nil, nil
		for _, k := range amiKinds() {
			for _, current := range []bool{false, true} {
				name := k.name + "-old"
				if current {
					name = k.name + "-new"
				}
				addRollNodegroup(s, c, name, current)
				ng := c.Nodegroup(name)
				ng.AMIType = k.amiType
				if k.edit != nil {
					k.edit(ng)
				}
			}
		}
		s.Parameters = append(s.Parameters, fleettest.Parameter{
			Name: "/aws/service/ami-windows-latest/Windows_Server-2022-English-Core-EKS_Optimized-1.32/image_id", Value: fleettest.LatestImage("1.32"),
		})
		for _, t := range []string{"g5.xlarge", "p3.2xlarge", "m5a.large", "m6i.large"} {
			for _, az := range []string{"us-east-1a", "us-east-1b"} {
				s.Offerings = append(s.Offerings, fleettest.Offering{InstanceType: t, AvailabilityZone: az})
			}
		}
	})
}

// Every AMI family reads the latest AMI from its own parameter, and the
// plan rolls exactly the nodegroups that are behind it: GPU, Spot with
// mixed instance types and the MINIMAL strategy, Bottlerocket, and
// Windows alike. The run then sends those rolls and no other.
func TestUpdate_AMIKinds(t *testing.T) {
	e := cmdtest.NewFake(t, amiWorld(), east)
	e.UseWorldClock()
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "-c", "prod", "--dry-run", "-o", "json", "--skip-health-check"); err != nil {
		t.Logf("dry run: %v", err)
	}
	var plan struct {
		Clusters []struct {
			Steps []struct{ Name, Action string }
		}
	}
	if err := json.Unmarshal(e.Out.Bytes(), &plan); err != nil || len(plan.Clusters) != 1 {
		t.Fatalf("plan: %v\n%s", err, e.Out)
	}
	got := map[string]string{}
	for _, s := range plan.Clusters[0].Steps {
		got[s.Name] = s.Action
	}
	want := map[string]string{}
	for _, k := range amiKinds() {
		want[k.name+"-old"], want[k.name+"-new"] = "Submit", "Skip"
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the actions of the plan (-want +got):\n%s", diff)
	}
	var paths []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.SSMGetParameter {
			paths = append(paths, fmt.Sprint(r.Input["Name"]))
		}
	}
	for _, k := range amiKinds() {
		if !slices.Contains(paths, k.path) {
			t.Errorf("%s: no lookup of %s in %v", k.name, k.path, paths)
		}
	}

	run := cmdtest.NewFake(t, amiWorld(), east)
	run.UseWorldClock()
	if err := run.Run(cmdutiltest.Context(t), "nodegroup", "update", "-c", "prod", "--yes", "--skip-health-check", "-o", "json"); err != nil {
		t.Logf("run: %v", err)
	}
	var rolled []string
	for _, r := range rollsOf(run) {
		rolled = append(rolled, r.Nodegroup)
	}
	slices.Sort(rolled)
	var wantRolled []string
	for _, k := range amiKinds() {
		wantRolled = append(wantRolled, k.name+"-old")
	}
	slices.Sort(wantRolled)
	if diff := cmp.Diff(wantRolled, rolled); diff != "" {
		t.Errorf("the rolls (-want +got):\n%s", diff)
	}
}
