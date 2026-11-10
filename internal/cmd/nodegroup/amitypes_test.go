package nodegroup_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
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

// A nodegroup of an AMI type that this version of refresh does not know
// has no recommended AMI to compare with. nodegroup update skips it with a
// notice that names the type, and sends no roll for it, run after run.
// nodegroup list shows its AMI status as Unknown. Another nodegroup of the
// cluster is still rolled.
func TestUpdate_AMITypeNewerThanRefresh(t *testing.T) {
	const newType = "AL2023_x86_64_SOMETHING_NEW"
	world := func() *fleettest.World {
		return updateWorld(func(s *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroups, c.Nodes, c.Pods, c.PDBs, c.Deployments = nil, nil, nil, nil, nil
			s.AutoScalingGroups, s.Instances = nil, nil
			addRollNodegroup(s, c, "future", true)
			c.Nodegroup("future").AMIType = newType
			addRollNodegroup(s, c, "web", false)
		})
	}

	e := cmdtest.NewFake(t, world(), east)
	e.UseWorldClock()
	err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "-c", "prod", "--dry-run", "-o", "json", "--skip-health-check")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitOK {
		t.Errorf("dry run: exit code = %d (%v), want %d", got, err, cmdutil.ExitOK)
	}
	var plan struct {
		Clusters []struct {
			Steps   []struct{ Name, Action, Reason string }
			Notices []string
		}
	}
	if err := json.Unmarshal(e.Out.Bytes(), &plan); err != nil || len(plan.Clusters) != 1 {
		t.Fatalf("plan: %v\n%s", err, e.Out)
	}
	steps := map[string]string{}
	for _, s := range plan.Clusters[0].Steps {
		steps[s.Name] = s.Action + " " + s.Reason
	}
	if diff := cmp.Diff(map[string]string{"future": "Skip AMITypeUnknown", "web": "Submit AMIOutdated"}, steps); diff != "" {
		t.Errorf("the steps of the plan (-want +got):\n%s", diff)
	}
	want := "nodegroup future has the AMI type " + newType + ", which this version of refresh does not know, and is not rolled; upgrade refresh to roll it"
	if !slices.Contains(plan.Clusters[0].Notices, want) {
		t.Errorf("notices = %q, want %q", plan.Clusters[0].Notices, want)
	}

	// Two runs on one world: neither rolls the nodegroup of the new type.
	run := cmdtest.NewFake(t, world(), east)
	run.UseWorldClock()
	for i := range 2 {
		run.Out.Reset()
		err := run.Run(cmdutiltest.Context(t), "nodegroup", "update", "-c", "prod", "--yes", "--skip-health-check", "-o", "json")
		if got := cmdutil.ExitCode(err); got != cmdutil.ExitOK {
			t.Errorf("run %d: exit code = %d (%v), want %d", i+1, got, err, cmdutil.ExitOK)
		}
	}
	var rolled []string
	for _, r := range rollsOf(run) {
		rolled = append(rolled, r.Nodegroup)
	}
	if diff := cmp.Diff([]string{"web"}, rolled); diff != "" {
		t.Errorf("the rolls of two runs (-want +got):\n%s", diff)
	}

	list := cmdtest.NewFake(t, world(), east)
	if err := list.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "-o", "plain"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if row := "future\tACTIVE\tm5.large\t1.32\tUnknown\t2\n"; !strings.Contains(list.Out.String(), row) {
		t.Errorf("the list has no row %q:\n%s", row, list.Out)
	}
}
