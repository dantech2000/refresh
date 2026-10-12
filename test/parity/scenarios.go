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

// fleet returns a seed for one healthy cluster, "prod" on Kubernetes 1.32,
// changed by the edits:
//
//   - nodegroup ng-a is on an old AMI release, and ng-b is on the latest
//   - add-on vpc-cni is one version behind, and coredns is on the latest
//   - the catalog has the add-on builds for 1.31, 1.32, and 1.33
//   - each nodegroup has an Auto Scaling group of two instances with CPU
//     metrics, and the account has vCPU quota to spare, so the pre-flight
//     health checks that need no Kubernetes access pass
func fleet(edits ...edit) func(*fleettest.State) {
	return func(s *fleettest.State) {
		c := &fleettest.Cluster{
			Name: "prod", Version: "1.32", Region: Region, SupportType: "EXTENDED",
			SubnetIDs: []string{"subnet-a", "subnet-b"},
			Nodegroups: []*fleettest.Nodegroup{
				{Name: "ng-a", Version: "1.32", ReleaseVersion: "1.32.0-20250101", Subnets: []string{"subnet-a", "subnet-b"}, AutoScalingGroups: []string{"eks-ng-a"}},
				{Name: "ng-b", Version: "1.32", Subnets: []string{"subnet-a", "subnet-b"}, AutoScalingGroups: []string{"eks-ng-b"}},
			},
			Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: "v1.18.0-eksbuild.1"},
				{Name: "coredns", Version: "v1.11.4-eksbuild.2"},
			},
		}
		s.Clusters = []*fleettest.Cluster{c}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.33"}, {Version: "1.32"}, {Version: "1.31", Status: fleettest.ExtendedSupport},
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1.20.0-eksbuild.1", ClusterVersions: []string{"1.33"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.31", "1.32", "1.33"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.18.0-eksbuild.1", ClusterVersions: []string{"1.31", "1.32"}},
			{Addon: "coredns", Version: "v1.12.0-eksbuild.1", ClusterVersions: []string{"1.33"}, Default: true},
			{Addon: "coredns", Version: "v1.11.4-eksbuild.2", ClusterVersions: []string{"1.31", "1.32"}, Default: true},
		}
		s.Subnets = []fleettest.Subnet{
			{ID: "subnet-a", AvailabilityZone: "us-east-1a", VPCID: "vpc-1"},
			{ID: "subnet-b", AvailabilityZone: "us-east-1b", VPCID: "vpc-1"},
		}
		s.Offerings = []fleettest.Offering{
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1a"},
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1b"},
		}
		s.Images = []fleettest.Image{{ID: "ami-old132", Name: "old"}, {ID: "ami-latest132", Name: "latest"}}
		for _, g := range []struct{ group, image string }{{"eks-ng-a", "ami-old132"}, {"eks-ng-b", "ami-latest132"}} {
			ids := []string{"i-" + g.group + "-1", "i-" + g.group + "-2"}
			s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: g.group, InstanceIDs: ids})
			for _, id := range ids {
				s.Instances = append(s.Instances, fleettest.Instance{ID: id, ImageID: g.image, Type: "m5.large", AvailabilityZone: "us-east-1a"})
				s.Metrics = append(s.Metrics, fleettest.Metric{
					Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id}, Values: []float64{30, 35},
				})
			}
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

// updates scripts every update of the fleet that has no script of its own.
func updates(script fleettest.UpdateScript) edit {
	return func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = script }
}

// noMetrics removes the CPU metrics, which makes the capacity checks warn.
func noMetrics(s *fleettest.State, _ *fleettest.Cluster) { s.Metrics = nil }

// upgraded moves the fleet to 1.33, as after "cluster upgrade --to 1.33",
// with the control-plane update in the cluster's history a day ago.
func upgraded(_ *fleettest.State, c *fleettest.Cluster) {
	c.Version = "1.33"
	for _, ng := range c.Nodegroups {
		ng.Version, ng.ReleaseVersion = "1.33", ""
	}
	c.Addons[0].Version, c.Addons[1].Version = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
	c.Updates = []*fleettest.Update{{
		ID: "11111111-1111-4111-8111-111111111111", Type: fleettest.TypeVersionUpdate,
		Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.33"}, {Type: "PlatformVersion", Value: "eks.1"}},
		// The world starts at 2026-01-01; the binary compares with the real
		// clock, so the scenario's rollback window is a matter of the
		// binary's own rules, which the trace records.
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}}
}

// blockingInsight gives the cluster an ERROR insight for the next minor.
func blockingInsight(_ *fleettest.State, c *fleettest.Cluster) {
	c.Insights = []fleettest.Insight{{
		ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: fleettest.InsightError,
		Reason: "Deprecated API usage detected within last 30 days", Description: "Checks for usage of deprecated APIs",
		Recommendation: "Update the manifests and API clients",
	}}
}

// fault sets one fault on the world.
func fault(scope fleettest.Scope, f fleettest.Fault) func(*fleettest.World) {
	return func(w *fleettest.World) { w.SetScopedFault(scope, f) }
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
	// oneTick and failsInOneTick end at the first poll. They are for the
	// commands whose poll interval is fixed at five real seconds.
	oneTick        = fleettest.UpdateScript{After: time.Minute}
	failsInOneTick = fleettest.UpdateScript{After: time.Minute, Result: fleettest.UpdateFailed, Errors: failing.Errors}
)

// interrupt sends SIGINT when the nth request of op arrives.
func interrupt(op fleettest.Operation, nth int) *Signal {
	return &Signal{Signal: os.Interrupt, Nth: nth, Match: func(r awsxtest.Request) bool { return r.Operation == op }}
}

// interruptWait sends SIGINT at the second poll of the nth update the world
// starts: the binary is then in the wait for that update.
func interruptWait(nth int) *Signal {
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", nth)
	return &Signal{Signal: os.Interrupt, Nth: 2, Match: func(r awsxtest.Request) bool {
		return r.Operation == fleettest.EKSDescribeUpdate && r.Input["updateId"] == id
	}}
}

// poll is the poll interval of the commands that take one. They reject
// zero; the wait is real time, so it is as short as a ticker handles well.
const poll = "20ms"

// scenarios returns every scenario of the suite.
func scenarios() []Scenario {
	var out []Scenario
	for _, group := range [][]Scenario{addonUpdate(), nodegroupScale(), nodegroupUpdate(), clusterUpgrade(), clusterRollback()} {
		out = append(out, group...)
	}
	return out
}

// args returns a function that appends to a base command line.
func args(base ...string) func(extra ...string) []string {
	return func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
}

func addonUpdate() []Scenario {
	with := args("addon", "update", "-c", "prod", "-a", "vpc-cni", "-o", "json")
	return []Scenario{
		{Name: "addon-update-yes", Seed: fleet(), Legacy: with("--yes")},
		{Name: "addon-update-dry-run", Seed: fleet(), Legacy: with("--dry-run")},
		{Name: "addon-update-unattended", Seed: fleet(), Legacy: with()},
		{Name: "addon-update-all", Seed: fleet(), Legacy: []string{"addon", "update", "-c", "prod", "--all", "--yes", "-o", "json"}},
		{Name: "addon-update-wait", Seed: fleet(updates(oneTick)), Legacy: with("--yes", "--wait")},
		{Name: "addon-update-failed", Seed: fleet(updates(failsInOneTick)), Legacy: with("--yes", "--wait")},
		{Name: "addon-update-incompatible", Seed: fleet(), Legacy: with("--yes", "--health-check", "--version", "v1.20.0-eksbuild.1")},
		{
			Name: "addon-update-degraded", Legacy: with("--yes", "--health-check"),
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addons[0].Status = fleettest.StatusDegraded
				c.Addons[0].HealthIssues = []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "0 of 2 replicas are ready"}}
			}),
		},
		{
			Name: "addon-update-lost-response", Seed: fleet(), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSUpdateAddon), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "addon-update-throttled-read", Seed: fleet(), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSDescribeAddonVersions), fleettest.Fault{Throttle: 1}),
		},
		{
			Name: "addon-update-resume", Legacy: with("--yes"),
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addons[0].Status, c.Addons[0].Version = fleettest.StatusUpdating, "v1.19.0-eksbuild.1"
			}),
		},
	}
}

func nodegroupScale() []Scenario {
	with := args("nodegroup", "scale", "-c", "prod", "-n", "ng-a", "--desired", "3", "-o", "json")
	return []Scenario{
		{Name: "nodegroup-scale-yes", Seed: fleet(), Legacy: with("--yes")},
		{Name: "nodegroup-scale-dry-run", Seed: fleet(), Legacy: with("--dry-run")},
		{Name: "nodegroup-scale-unattended", Seed: fleet(), Legacy: with()},
		{Name: "nodegroup-scale-wait", Seed: fleet(updates(oneTick)), Legacy: with("--yes", "--wait")},
		{Name: "nodegroup-scale-failed", Seed: fleet(updates(failsInOneTick)), Legacy: with("--yes", "--wait")},
		{Name: "nodegroup-scale-check-pdbs", Seed: fleet(), Legacy: []string{"nodegroup", "scale", "-c", "prod", "-n", "ng-a", "--desired", "1", "--check-pdbs", "--yes", "-o", "json"}},
		{
			Name: "nodegroup-scale-lost-response", Seed: fleet(), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSUpdateNodegroupConfig), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "nodegroup-scale-throttled-read", Seed: fleet(), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSDescribeNodegroup), fleettest.Fault{Throttle: 1}),
		},
		{
			Name: "nodegroup-scale-resume", Legacy: with("--yes"),
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroups[0].Status = fleettest.StatusUpdating }),
		},
	}
}

func nodegroupUpdate() []Scenario {
	with := args("nodegroup", "update", "-c", "prod", "-n", "ng-a", "--poll-interval", poll, "-o", "json")
	return []Scenario{
		{Name: "nodegroup-update-yes", Seed: fleet(updates(slow)), Legacy: with("--yes")},
		{Name: "nodegroup-update-no-yes", Seed: fleet(updates(slow)), Legacy: with()},
		{Name: "nodegroup-update-dry-run", Seed: fleet(), Legacy: with("--dry-run")},
		{Name: "nodegroup-update-warnings-unattended", Seed: fleet(noMetrics), Legacy: with()},
		{Name: "nodegroup-update-require-healthy", Seed: fleet(noMetrics), Legacy: with("--yes", "--require-healthy")},
		{Name: "nodegroup-update-failed", Seed: fleet(updates(failing)), Legacy: with("--yes")},
		{
			Name: "nodegroup-update-interrupt", Seed: fleet(updates(never)), Legacy: with("--yes"),
			Signal: interruptWait(1),
		},
		{
			Name: "nodegroup-update-lost-response", Seed: fleet(updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSUpdateNodegroupVersion), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "nodegroup-update-throttled-read", Seed: fleet(updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.SSMGetParameter), fleettest.Fault{Throttle: 1}),
		},
		{
			Name: "nodegroup-update-resume", Legacy: with("--yes"),
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroups[0].Status = fleettest.StatusUpdating }),
		},
		{Name: "nodegroup-update-no-wait", Seed: fleet(updates(slow)), Legacy: with("--yes", "--no-wait")},
	}
}

func clusterUpgrade() []Scenario {
	with := args("cluster", "upgrade", "-c", "prod", "--to", "1.33", "--poll-interval", poll, "-o", "json")
	return []Scenario{
		{Name: "cluster-upgrade-yes", Seed: fleet(updates(slow)), Legacy: with("--yes")},
		{Name: "cluster-upgrade-dry-run", Seed: fleet(), Legacy: with("--dry-run")},
		{Name: "cluster-upgrade-unattended", Seed: fleet(), Legacy: with()},
		{Name: "cluster-upgrade-blocked", Seed: fleet(blockingInsight), Legacy: with("--yes")},
		{
			Name: "cluster-upgrade-failed", Legacy: with("--yes"),
			Seed: fleet(updates(slow), func(_ *fleettest.State, c *fleettest.Cluster) { c.UpdateScripts = []fleettest.UpdateScript{failing} }),
		},
		{
			Name: "cluster-upgrade-nodegroup-failed", Legacy: with("--yes"),
			Seed: fleet(updates(slow), func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{failing}
			}),
		},
		{
			// The control plane and the add-ons are done; the nodegroups are not.
			Name: "cluster-upgrade-resume", Legacy: with("--yes"),
			Seed: fleet(updates(slow), func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Version = "1.33"
				c.Addons[0].Version, c.Addons[1].Version = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
			}),
		},
		{
			Name: "cluster-upgrade-interrupt", Seed: fleet(updates(never)), Legacy: with("--yes"),
			Signal: interruptWait(1),
		},
		{
			Name: "cluster-upgrade-lost-response", Seed: fleet(updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSUpdateClusterVersion), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "cluster-upgrade-throttled-read", Seed: fleet(updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSListInsights), fleettest.Fault{Throttle: 1}),
		},
	}
}

func clusterRollback() []Scenario {
	with := args("cluster", "rollback", "-c", "prod", "--poll-interval", poll, "-o", "json")
	return []Scenario{
		{Name: "cluster-rollback-yes", Seed: fleet(upgraded, updates(slow)), Legacy: with("--yes")},
		{Name: "cluster-rollback-dry-run", Seed: fleet(upgraded), Legacy: with("--dry-run")},
		{Name: "cluster-rollback-unattended", Seed: fleet(upgraded), Legacy: with()},
		{Name: "cluster-rollback-blocked", Seed: fleet(), Legacy: with("--yes")},
		{
			Name: "cluster-rollback-timeout", Seed: fleet(upgraded, updates(slow)),
			Legacy: with("--yes", "--skip-insights-check", "--rollback-timeout", "3h"),
		},
		{
			Name: "cluster-rollback-failed", Legacy: with("--yes"),
			Seed: fleet(upgraded, updates(slow), func(_ *fleettest.State, c *fleettest.Cluster) { c.UpdateScripts = []fleettest.UpdateScript{failing} }),
		},
		{
			// The signal lands while the plan is checked again, before any update.
			Name: "cluster-rollback-interrupt-before-start", Seed: fleet(upgraded, updates(never)), Legacy: with("--yes"),
			Signal: interrupt(fleettest.EKSListInsights, 2),
		},
		{
			Name: "cluster-rollback-interrupt", Seed: fleet(upgraded, updates(never)), Legacy: with("--yes"),
			Signal: interruptWait(1),
		},
		{
			// The nodegroups and the add-ons are back; the control plane is not.
			Name: "cluster-rollback-resume", Legacy: with("--yes"),
			Seed: fleet(upgraded, updates(slow), func(_ *fleettest.State, c *fleettest.Cluster) {
				for _, ng := range c.Nodegroups {
					ng.Version, ng.ReleaseVersion = "1.32", ""
				}
				c.Addons[0].Version, c.Addons[1].Version = "v1.19.0-eksbuild.1", "v1.11.4-eksbuild.2"
			}),
		},
		{
			Name: "cluster-rollback-throttled-read", Seed: fleet(upgraded, updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSListUpdates), fleettest.Fault{Throttle: 1}),
		},
		{
			Name: "cluster-rollback-lost-response", Seed: fleet(upgraded, updates(slow)), Legacy: with("--yes"),
			Faults: fault(on(fleettest.EKSUpdateClusterVersion), fleettest.Fault{LoseResponse: 1}),
		},
	}
}
