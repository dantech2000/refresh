//go:build unix

package parity

import (
	"fmt"
	"os"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// edit changes the seeded fleet. c is the cluster "prod".
type edit func(s *fleettest.State, c *fleettest.Cluster)

// The images of the fleet: what a node of a Kubernetes version runs when it
// is behind, and when it is on the latest AMI. The latest matches what the
// fake's SSM parameters recommend.
func oldImage(version string) string { return "ami-old" + version[2:] + "x" }

func latestImage(version string) string { return "ami-latest1" + version[2:] }

// fleet returns a seed for one healthy cluster, "prod" on Kubernetes 1.32,
// changed by the edits:
//
//   - nodegroup ng-a is on an old AMI release, and ng-b is on the latest
//   - add-on vpc-cni is one version behind, and coredns is on the latest
//   - the catalog has the add-on builds for 1.31 to 1.34
//   - each nodegroup has an Auto Scaling group of two instances with CPU
//     metrics, and the account has vCPU quota to spare, so the pre-flight
//     health checks that need no Kubernetes access pass
func fleet(edits ...edit) func(*fleettest.State) {
	return func(s *fleettest.State) {
		c := &fleettest.Cluster{
			Name: "prod", Version: "1.32", Region: Region, SupportType: "EXTENDED",
			SubnetIDs: []string{"subnet-a", "subnet-b"},
			Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: "v1.18.0-eksbuild.1"},
				{Name: "coredns", Version: "v1.11.4-eksbuild.2"},
			},
		}
		s.Clusters = []*fleettest.Cluster{c}
		addNodegroup(s, c, "ng-a", "1.32", false)
		addNodegroup(s, c, "ng-b", "1.32", true)
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.34"}, {Version: "1.33"}, {Version: "1.32"}, {Version: "1.31", Status: fleettest.ExtendedSupport},
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1.21.0-eksbuild.1", ClusterVersions: []string{"1.34"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.20.0-eksbuild.1", ClusterVersions: []string{"1.33"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.31", "1.32", "1.33"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.18.0-eksbuild.1", ClusterVersions: []string{"1.31", "1.32"}},
			{Addon: "coredns", Version: "v1.13.0-eksbuild.1", ClusterVersions: []string{"1.34"}, Default: true},
			{Addon: "coredns", Version: "v1.12.0-eksbuild.1", ClusterVersions: []string{"1.33"}, Default: true},
			{Addon: "coredns", Version: "v1.11.4-eksbuild.2", ClusterVersions: []string{"1.31", "1.32"}, Default: true},
			{Addon: "coredns", Version: "v1.11.3-eksbuild.1", ClusterVersions: []string{"1.31", "1.32"}},
		}
		s.Subnets = []fleettest.Subnet{
			{ID: "subnet-a", AvailabilityZone: "us-east-1a", VPCID: "vpc-1"},
			{ID: "subnet-b", AvailabilityZone: "us-east-1b", VPCID: "vpc-1"},
		}
		s.Offerings = []fleettest.Offering{
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1a"},
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1b"},
		}
		s.Quotas = []fleettest.Quota{{ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 64}}
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{8},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		})
		for _, e := range edits {
			e(s, c)
		}
	}
}

// addNodegroup adds a nodegroup to c with its Auto Scaling group, two
// instances, and their CPU metrics. A nodegroup that is not onLatest runs
// an old AMI release.
func addNodegroup(s *fleettest.State, c *fleettest.Cluster, name, version string, onLatest bool) {
	group := "eks-" + c.Name + "-" + name
	ng := &fleettest.Nodegroup{
		Name: name, Version: version, Subnets: []string{"subnet-a", "subnet-b"}, AutoScalingGroups: []string{group},
	}
	image := latestImage(version)
	if !onLatest {
		ng.ReleaseVersion, image = version+".0-20250101", oldImage(version)
	}
	c.Nodegroups = append(c.Nodegroups, ng)
	ids := []string{"i-" + group + "-1", "i-" + group + "-2"}
	s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: group, InstanceIDs: ids})
	s.Images = append(s.Images, fleettest.Image{ID: image, Name: image})
	for _, id := range ids {
		s.Instances = append(s.Instances, fleettest.Instance{ID: id, ImageID: image, Type: "m5.large", AvailabilityZone: "us-east-1a"})
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id}, Values: []float64{30, 35},
		})
	}
}

// scripts sets the script of every update of the fleet that has none of
// its own.
func scripts(script fleettest.UpdateScript) edit {
	return func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = script }
}

// script sets the scripts of the next updates of one nodegroup or add-on of
// prod, or of the control plane when name is "".
func script(name string, list ...fleettest.UpdateScript) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		switch {
		case name == "":
			c.UpdateScripts = list
		case c.Nodegroup(name) != nil:
			c.Nodegroup(name).UpdateScripts = list
		default:
			c.Addon(name).UpdateScripts = list
		}
	}
}

// noMetrics removes the CPU metrics, which makes the capacity checks warn.
func noMetrics(s *fleettest.State, _ *fleettest.Cluster) {
	var kept []fleettest.Metric
	for _, m := range s.Metrics {
		if m.Namespace != "AWS/EC2" {
			kept = append(kept, m)
		}
	}
	s.Metrics = kept
}

// bothStale puts ng-b on an old AMI release too.
func bothStale(s *fleettest.State, c *fleettest.Cluster) {
	c.Nodegroup("ng-b").ReleaseVersion = "1.32.0-20250101"
	for i := range s.Instances {
		if s.Instances[i].ImageID == latestImage("1.32") {
			s.Instances[i].ImageID = oldImage("1.32")
		}
	}
}

// bothBehind puts coredns one version behind too.
func bothBehind(_ *fleettest.State, c *fleettest.Cluster) {
	c.Addon("coredns").Version = "v1.11.3-eksbuild.1"
}

// upgradedAt moves the fleet to 1.33, as after "cluster upgrade --to 1.33",
// with the control-plane update in the cluster's history that long ago. The
// binary compares the update's time with the real clock, so the time is
// seeded from the real clock, and the trace shows it relative to the run.
func upgradedAt(ago time.Duration) edit {
	return func(s *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		for _, ng := range c.Nodegroups {
			ng.Version, ng.ReleaseVersion = "1.33", ""
		}
		for i := range s.Instances {
			s.Instances[i].ImageID = latestImage("1.33")
		}
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
		c.Updates = []*fleettest.Update{{
			ID: "11111111-1111-4111-8111-111111111111", Type: fleettest.TypeVersionUpdate,
			Params:    []fleettest.UpdateParam{{Type: "Version", Value: "1.33"}, {Type: "PlatformVersion", Value: "eks.1"}},
			CreatedAt: time.Now().Add(-ago),
		}}
	}
}

// upgraded is a fleet that was upgraded a day ago.
var upgraded = upgradedAt(24 * time.Hour)

// rolledBackNodes puts the nodegroups and the add-ons of an upgraded fleet
// back on 1.32, as a rollback does before it touches the control plane.
func rolledBackNodes(_ *fleettest.State, c *fleettest.Cluster) {
	for _, ng := range c.Nodegroups {
		ng.Version, ng.ReleaseVersion = "1.32", ""
	}
	c.Addon("vpc-cni").Version, c.Addon("coredns").Version = "v1.19.0-eksbuild.1", "v1.11.4-eksbuild.2"
}

// blockingInsight gives the cluster an ERROR insight for the next minor.
func blockingInsight(_ *fleettest.State, c *fleettest.Cluster) {
	c.Insights = []fleettest.Insight{{
		ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: fleettest.InsightError,
		Reason: "Deprecated API usage detected within last 30 days", Description: "Checks for usage of deprecated APIs",
		Recommendation: "Update the manifests and API clients",
	}}
}

// staging adds a second cluster in the same region, with one nodegroup on
// an old AMI release.
func staging(s *fleettest.State, _ *fleettest.Cluster) {
	c := &fleettest.Cluster{Name: "staging", Version: "1.32", Region: Region, SubnetIDs: []string{"subnet-a", "subnet-b"}}
	s.Clusters = append(s.Clusters, c)
	addNodegroup(s, c, "ng-s", "1.32", false)
}

// startControlPlaneUpdate starts an update of prod's control plane to 1.33,
// as someone else would.
func startControlPlaneUpdate(w *fleettest.World) error {
	_, err := w.UpdateClusterVersion(Region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33")})
	return err
}

// deadlineArg is the --wait-timeout of the deadline scenarios.
const deadlineArg = 300 * time.Millisecond

// fault sets one fault on the world.
func fault(scope fleettest.Scope, f fleettest.Fault) func(*fleettest.World) error {
	return func(w *fleettest.World) error {
		w.SetScopedFault(scope, f)
		return nil
	}
}

func on(op fleettest.Operation) fleettest.Scope { return fleettest.Scope{Operation: op} }

// The update scripts of the scenarios, in polls at the default step.
var (
	slow    = fleettest.UpdateScript{After: 3 * time.Minute}
	failing = fleettest.UpdateScript{
		After: 2 * time.Minute, Result: fleettest.UpdateFailed,
		Errors: []fleettest.UpdateError{{Code: "NodeCreationFailure", Message: "instances failed to join the cluster", ResourceIDs: []string{"i-0abc"}}},
	}
	never = fleettest.UpdateScript{Never: true}
	// long and longFailing are for an update that is in flight when the
	// binary starts: they outlast the reads of its planning, which move the
	// clock in those scenarios.
	long        = fleettest.UpdateScript{After: 40 * time.Minute}
	longFailing = fleettest.UpdateScript{After: 40 * time.Minute, Result: fleettest.UpdateFailed, Errors: failing.Errors}
	// oneTick and failsInOneTick have ended when the binary first polls.
	// They are for the commands whose poll interval is fixed at five real
	// seconds.
	oneTick        = fleettest.UpdateScript{}
	failsInOneTick = fleettest.UpdateScript{Result: fleettest.UpdateFailed, Errors: failing.Errors}
)

// interrupt sends SIGINT when the nth request of op arrives.
func interrupt(op fleettest.Operation, nth int) *Signal {
	return &Signal{Signal: os.Interrupt, Nth: nth, Match: func(r awsxtest.Request) bool { return r.Operation == op }}
}

// interruptAfterPrompt sends SIGINT when the first request of op arrives
// after the prompt with the number was answered: the binary was told to go
// on, and reads again before it changes anything.
func interruptAfterPrompt(prompt int, op fleettest.Operation) *Signal {
	s := interrupt(op, 1)
	s.AfterPrompt = prompt
	return s
}

// updateID is the ID of the nth update a world starts.
func updateID(nth int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", nth) }

// interruptWait sends SIGINT at the poll number poll of the nth update the
// world starts: the binary is then in the wait for that update.
func interruptWait(nth, poll int) *Signal {
	id := updateID(nth)
	return &Signal{Signal: os.Interrupt, Nth: poll, Match: func(r awsxtest.Request) bool {
		return r.Operation == fleettest.EKSDescribeUpdate && r.Input["updateId"] == id
	}}
}

// The replies of a terminal scenario to every "[y/N]" prompt.
var (
	sayYes  = &Terminal{Expect: "[y/N]", Replies: []string{"y\n"}}
	sayNo   = &Terminal{Expect: "[y/N]", Replies: []string{"n\n"}}
	ctrlC   = &Terminal{Expect: "[y/N]", Replies: []string{"\x03"}}
	pollArg = "20ms"
)

// verifyWaitArg is the --wait-timeout of a scenario in which the binary
// waits on its own clock until that deadline. A roll ends after a few
// polls of pollArg, so no run reaches it before its verification.
const verifyWaitArg = "5s"

// args returns a function that appends to a base command line.
func args(base ...string) func(extra ...string) []string {
	return func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
}

// scaleElsewhere starts a scaling update of ng-a, as someone else would.
func scaleElsewhere(w *fleettest.World) error {
	three := int32(3)
	_, err := w.UpdateNodegroupConfig(Region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}})
	return err
}
