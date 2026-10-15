package main

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// durations are how long the updates of a generated fleet take on the
// fleet's clock. At -speed 1 a roll, an add-on update, and a control-plane
// upgrade end in these times.
type durations struct {
	roll, addon, upgrade time.Duration
}

// situation is one scripted state of a cluster that a tester can look
// for. The generator gives each situation of the mix to one cluster, in
// the order of the list, and the tool prints which cluster shows what.
type situation struct {
	name string
	// what says what the cluster shows, with the nodegroup it is about.
	what string
	// apply changes the generated cluster. Every situation's cluster has
	// an outdated AMI on ng-a, so a roll is offered there.
	apply func(g *generator, c *fleettest.Cluster)
	// start, when set, runs on the world after it is built: a situation
	// that is an update in flight needs the world's own update path.
	start func(w *fleettest.World, region, cluster string, d durations) error
}

// The situations, in the order the generator places them on the first
// clusters of the fleet.
var situations = []situation{
	{name: "pdb-blocker", what: "a PodDisruptionBudget allows no disruption: the drain of ng-a is blocked", apply: func(_ *generator, c *fleettest.Cluster) {
		for i := range c.PDBs {
			c.PDBs[i].MinAvailable, c.PDBs[i].DisruptionsAllowed = c.PDBs[i].ExpectedPods, 0
			c.PDBs[i].DesiredHealthy = c.PDBs[i].ExpectedPods
		}
	}},
	{name: "critical-failing", what: "the kube-system pods on the nodes of ng-a are not ready: the Workloads check fails", apply: func(_ *generator, c *fleettest.Cluster) {
		for _, p := range c.Pods {
			if p.Namespace == "kube-system" && onNodegroup(c, p.Node, "ng-a") {
				p.NotReady = true
			}
		}
	}},
	{name: "node-notready", what: "one node of ng-a is NotReady (NodeStatusUnknown)", apply: func(_ *generator, c *fleettest.Cluster) {
		for _, n := range c.Nodes {
			if n.Nodegroup == "ng-a" {
				n.NotReady, n.ReadyReason = true, "NodeStatusUnknown"
				break
			}
		}
	}},
	{name: "nodegroup-degraded", what: "ng-a is DEGRADED (AsgInstanceLaunchFailures)", apply: func(_ *generator, c *fleettest.Cluster) {
		c.Nodegroups[0].Status = fleettest.StatusDegraded
		c.Nodegroups[0].HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "Could not launch On-Demand Instances. InsufficientInstanceCapacity"}}
	}},
	{name: "roll-in-flight", what: "ng-a is UPDATING: a roll started before the tool, half done, that ends on the clock", start: func(w *fleettest.World, region, cluster string, d durations) error {
		var version string
		w.Edit(func(s *fleettest.State) {
			c := s.Cluster(region, cluster)
			version = c.Version
			c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{{After: 2 * d.roll}}
		})
		if _, err := w.UpdateNodegroupVersion(region, cluster, "ng-a", fleettest.NodegroupVersionRequest{Version: &version}); err != nil {
			return err
		}
		w.Advance(d.roll)
		return nil
	}},
	{name: "addon-degraded", what: "the coredns add-on is DEGRADED (InsufficientNumberOfReplicas); an update repairs it", apply: func(g *generator, c *fleettest.Cluster) {
		for _, a := range c.Addons {
			if a.Name == "coredns" {
				a.Status = fleettest.StatusDegraded
				a.HealthIssues = []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "The add-on is unhealthy because it doesn't have the desired number of replicas."}}
				// The next update clears the issue, as a repair does.
				a.UpdateScripts = []fleettest.UpdateScript{{After: g.d.addon, ResourceIssues: []fleettest.Issue{}}}
			}
		}
	}},
	{name: "insight-error", what: "an upgrade-readiness insight is ERROR (deprecated APIs in use)", apply: func(g *generator, c *fleettest.Cluster) {
		next := fmt.Sprintf("1.%d", minorOf(c.Version)+1)
		c.Insights = []fleettest.Insight{{
			// An ID as EKS gives one: "cluster upgrade-check" shows its
			// first characters, and --insight takes them.
			ID:   "5d0e1b7c-6a4f-4b0e-9c1d-3f2a8b7c6d5e",
			Name: "Deprecated APIs removed in Kubernetes v" + next, Status: fleettest.InsightError, KubernetesVersion: next,
			Reason:      "Deprecated API usage detected within the last 30 days.",
			Description: "Checks for usage of deprecated APIs that are scheduled for removal in Kubernetes v" + next,
			Deprecations: []fleettest.Deprecation{{
				Usage: "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas", ReplacedWith: "/apis/flowcontrol.apiserver.k8s.io/v1/flowschemas",
				StopServingVersion: next, StartServingReplacementVersion: c.Version,
				Clients: []fleettest.ClientStat{{UserAgent: "kube-controller-manager/v" + c.Version, Requests: 42, LastRequest: g.now.Add(-2 * time.Hour)}},
			}},
		}}
	}},
	{name: "rollback-window", what: "the control plane was upgraded two days ago: a rollback is possible", apply: func(g *generator, c *fleettest.Cluster) {
		c.Updates = []*fleettest.Update{{
			ID: "00000000-0000-4000-8000-seed" + fmt.Sprintf("%08d", len(c.Name)), Type: fleettest.TypeVersionUpdate, Status: fleettest.UpdateSuccessful,
			Params: []fleettest.UpdateParam{{Type: "Version", Value: c.Version}}, CreatedAt: g.now.Add(-48 * time.Hour),
		}}
	}},
	{name: "stuck-drain", what: "the next roll of ng-a cordons the old nodes and never finishes: the pods stay (a wait until --wait-timeout)", apply: func(_ *generator, c *fleettest.Cluster) {
		c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{{Never: true, Roll: fleettest.RollScript{StuckDrain: true}}}
	}},
	{name: "nodes-never-ready", what: "the next roll of ng-a succeeds in EKS, but its new nodes never become Ready (verification fails)", apply: func(g *generator, c *fleettest.Cluster) {
		c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{{After: g.d.roll, Roll: fleettest.RollScript{NodesNeverReady: true}}}
	}},
	{name: "pending-pods", what: "the next roll of ng-a leaves its pods Pending (verification fails)", apply: func(g *generator, c *fleettest.Cluster) {
		c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{{After: g.d.roll, Roll: fleettest.RollScript{PendingPods: true}}}
	}},
	{name: "update-fails", what: "the next roll of ng-a fails in EKS (NodeCreationFailure) and leaves it DEGRADED", apply: func(g *generator, c *fleettest.Cluster) {
		c.Nodegroups[0].UpdateScripts = []fleettest.UpdateScript{{
			After: g.d.roll / 2, Result: fleettest.UpdateFailed,
			Errors:         []fleettest.UpdateError{{Code: "NodeCreationFailure", Message: "Instances failed to join the kubernetes cluster", ResourceIDs: []string{"i-" + c.Name + "-ng-a-new"}}},
			ResourceStatus: fleettest.StatusDegraded,
			ResourceIssues: []fleettest.Issue{{Code: "NodeCreationFailure", Message: "Instances failed to join the kubernetes cluster"}},
		}}
	}},
}

// situationNames are the names a -situations flag accepts, in order.
func situationNames() []string {
	names := make([]string, len(situations))
	for i, s := range situations {
		names[i] = s.name
	}
	return names
}

// parseSituations returns the situations that a -situations value names:
// "all", "none", or names separated by commas.
func parseSituations(value string) ([]situation, error) {
	switch strings.TrimSpace(value) {
	case "all":
		return situations, nil
	case "none", "":
		return nil, nil
	}
	var out []situation
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		i := slices.IndexFunc(situations, func(s situation) bool { return s.name == name })
		if i < 0 {
			return nil, fmt.Errorf("unknown situation %q; the situations are %s", name, strings.Join(situationNames(), ", "))
		}
		out = append(out, situations[i])
	}
	return out, nil
}

// placement says which cluster shows a situation.
type placement struct {
	cluster, region string
	situation       situation
}

// generator holds what the generation of one fleet shares.
type generator struct {
	rng  *rand.Rand
	now  time.Time
	d    durations
	s    *fleettest.State
	kube bool
	// regionsDone are the regions whose VPC, subnets, offerings, and quota
	// exist already.
	regionsDone map[string]bool
	// vcpus counts the vCPUs in use in each region, for the usage metric
	// of the quota check.
	vcpus map[string]int
}

// generate makes n clusters spread over regions: a mix of versions (some
// in extended support), one to four nodegroups each (some a minor behind,
// some on an outdated AMI), the usual add-ons (some behind), and with kube
// the nodes, pods, PDBs, deployments, and node metrics behind them. The
// situations of the mix go to the first clusters, one each, and the
// placements say which. The world's clock reads now.
func generate(rng *rand.Rand, n int, regions []string, now time.Time, d durations, mix []situation, kube bool) (fleettest.State, []placement) {
	const oldest, newest = 31, 36
	minors := []int{31, 32, 33, 34, 34, 35, 35, 36}
	teams := []string{"payments", "search", "checkout", "identity", "data", "ml", "edge", "platform", "billing", "catalog", "orders", "notify"}
	envs := []string{"prod", "staging", "dev"}
	amiTypes := []string{"AL2023_x86_64_STANDARD", "AL2023_ARM_64_STANDARD", "BOTTLEROCKET_x86_64", "AL2_x86_64"}
	addons := []string{"vpc-cni", "coredns", "kube-proxy", "eks-pod-identity-agent"}
	version := func(minor int) string { return fmt.Sprintf("1.%d", minor) }

	g := &generator{rng: rng, now: now, d: d, s: &fleettest.State{}, kube: kube, regionsDone: map[string]bool{}, vcpus: map[string]int{}}
	s := g.s
	s.DefaultUpdate = fleettest.UpdateScript{After: d.upgrade}
	s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: d.roll}
	s.DefaultAddonUpdate = &fleettest.UpdateScript{After: d.addon}
	s.InsightsRefresh = 20 * time.Second
	for minor := oldest; minor <= newest; minor++ {
		cv := fleettest.ClusterVersion{Version: version(minor)}
		if minor <= oldest+1 {
			cv.Status = fleettest.ExtendedSupport
		}
		s.ClusterVersions = append(s.ClusterVersions, cv)
		// The catalog by Kubernetes version, as EKS lists it: a 1.34
		// cluster never sees a build for 1.36.
		for _, a := range addons {
			for build := 2; build >= 1; build-- {
				s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{
					Addon: a, Version: fmt.Sprintf("v%s.%d-eksbuild.1", version(minor), build),
					ClusterVersions: []string{version(minor)}, Default: build == 2,
				})
			}
		}
		// The AMIs of a version: the latest, and an older one that a stale
		// nodegroup runs. The age shows in "refresh status".
		s.Images = append(s.Images,
			fleettest.Image{ID: fleettest.LatestImage(version(minor)), Name: "amazon-eks-node-" + version(minor) + "-v20260101", Created: now.Add(-10 * 24 * time.Hour)},
			fleettest.Image{ID: oldImage(version(minor)), Name: "amazon-eks-node-" + version(minor) + "-v20250901", Created: now.Add(-130 * 24 * time.Hour)},
		)
	}
	var placements []placement
	for i := range n {
		minor := minors[rng.IntN(len(minors))]
		var sit *situation
		if i < len(mix) {
			sit = &mix[i]
			// A situation's cluster is in standard support, so its roll is
			// a roll and not an upgrade.
			minor = 34 + i%2
		}
		c := &fleettest.Cluster{
			Name:    fmt.Sprintf("%s-%s-%02d", envs[i%len(envs)], teams[rng.IntN(len(teams))], i),
			Version: version(minor), Region: regions[i%len(regions)],
		}
		g.region(c.Region)
		for j := range 1 + rng.IntN(4) {
			ngMinor := minor
			if sit == nil && rng.IntN(4) == 0 {
				ngMinor-- // a nodegroup left behind
			}
			ami := amiTypes[rng.IntN(len(amiTypes))]
			if ami == "AL2_x86_64" && ngMinor > 32 {
				ami = "AL2023_x86_64_STANDARD"
			}
			stale := rng.IntN(4) == 0 || (sit != nil && j == 0)
			g.nodegroup(c, fmt.Sprintf("ng-%c", 'a'+j), version(ngMinor), ami, stale)
		}
		if sit == nil && rng.IntN(20) == 0 {
			c.Nodegroups[0].Status = fleettest.StatusUpdating
		}
		for _, a := range addons {
			build := 2
			if rng.IntN(5) == 0 {
				build = 1 // behind
			}
			c.Addons = append(c.Addons, &fleettest.Addon{Name: a, Version: fmt.Sprintf("v%s.%d-eksbuild.1", version(minor), build)})
		}
		if kube {
			g.workloads(c)
		}
		if sit != nil && sit.apply != nil {
			sit.apply(g, c)
		}
		if sit != nil {
			placements = append(placements, placement{cluster: c.Name, region: c.Region, situation: *sit})
		}
		s.Clusters = append(s.Clusters, c)
	}
	// In the order of the region names: a map has no order, and one seed
	// gives one fleet.
	for _, region := range slices.Sorted(maps.Keys(g.vcpus)) {
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Region: region, Namespace: "AWS/Usage", Name: "ResourceCount",
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
			Values:     []float64{float64(g.vcpus[region])},
		})
	}
	return *s, placements
}

// oldImage is the AMI that a nodegroup with an outdated AMI runs.
func oldImage(version string) string {
	return "ami-old" + strings.ReplaceAll(version, ".", "")
}

// minorOf returns the minor of a "1.N" version.
func minorOf(version string) int {
	var major, minor int
	_, _ = fmt.Sscanf(version, "%d.%d", &major, &minor)
	return minor
}

// region adds what a region holds once: a VPC with two subnets, the
// offerings of the instance type, and the vCPU quota.
func (g *generator) region(region string) {
	if g.regionsDone[region] {
		return
	}
	g.regionsDone[region] = true
	g.s.VPCs = append(g.s.VPCs, fleettest.VPC{Region: region, ID: "vpc-" + region, CIDR: "10.0.0.0/16"})
	for _, az := range []string{"a", "b"} {
		g.s.Subnets = append(g.s.Subnets, fleettest.Subnet{Region: region, ID: "subnet-" + region + "-" + az, AvailabilityZone: region + az, VPCID: "vpc-" + region})
		g.s.Offerings = append(g.s.Offerings, fleettest.Offering{Region: region, InstanceType: "m5.large", AvailabilityZone: region + az})
	}
	g.s.Quotas = append(g.s.Quotas, fleettest.Quota{Region: region, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 256})
}

// nodegroup adds a nodegroup to c with its Auto Scaling group, its
// instances, their CPU metrics, and with kube its nodes and their system
// pods. A stale nodegroup runs the older AMI of its version.
func (g *generator) nodegroup(c *fleettest.Cluster, name, version, amiType string, stale bool) {
	region := c.Region
	ng := &fleettest.Nodegroup{
		Name: name, Version: version, AMIType: amiType,
		Desired: 2 + g.rng.Int32N(4), Min: 1, Max: 12,
		AutoScalingGroups: []string{"eks-" + c.Name + "-" + name},
		Subnets:           []string{"subnet-" + region + "-a", "subnet-" + region + "-b"},
	}
	image := fleettest.LatestImage(version)
	if stale {
		image = oldImage(version)
	}
	group := fleettest.AutoScalingGroup{Region: region, Name: ng.AutoScalingGroups[0], AvailabilityZones: []string{region + "a", region + "b"}}
	for k := range int(ng.Desired) {
		az := region + string(rune('a'+k%2))
		id := fmt.Sprintf("i-%s-%s-%d", c.Name, name, k+1)
		group.InstanceIDs = append(group.InstanceIDs, id)
		g.s.Instances = append(g.s.Instances, fleettest.Instance{
			Region: region, ID: id, ImageID: image, Type: "m5.large", State: "running", Lifecycle: "on-demand",
			AvailabilityZone: az, LaunchTime: g.now.Add(-time.Duration(1+g.rng.IntN(40)) * 24 * time.Hour),
			Tags: map[string]string{"eks:cluster-name": c.Name, "eks:nodegroup-name": name},
		})
		g.s.Metrics = append(g.s.Metrics, fleettest.Metric{
			Region: region, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id},
			Values: []float64{float64(10 + g.rng.IntN(40)), float64(10 + g.rng.IntN(40)), float64(10 + g.rng.IntN(40))},
		})
		g.vcpus[region] += 2
		if g.kube {
			node := fmt.Sprintf("ip-10-%d-%d-%d.ec2.internal", len(c.Nodegroups)+1, k+1, 10+g.rng.IntN(240))
			c.Nodes = append(c.Nodes, &fleettest.Node{
				Name: node, Nodegroup: name, ImageID: image,
				MilliCPU: 2000, MemoryMi: 8192, UsedMilliCPU: int64(300 + g.rng.IntN(900)), UsedMemoryMi: int64(1024 + g.rng.IntN(4096)),
				CreatedAt: g.now.Add(-time.Duration(1+g.rng.IntN(40)) * 24 * time.Hour),
			})
			for _, system := range []string{"aws-node", "kube-proxy"} {
				c.Pods = append(c.Pods, &fleettest.Pod{
					Namespace: "kube-system", Name: fmt.Sprintf("%s-%s", system, suffix(g.rng)), Node: node,
					Labels: map[string]string{"k8s-app": system},
				})
			}
		}
	}
	g.s.AutoScalingGroups = append(g.s.AutoScalingGroups, group)
	c.Nodegroups = append(c.Nodegroups, ng)
}

// workloads adds the application of a cluster: coredns in kube-system, and
// a deployment "web" in the namespace "app" with one pod on each node and
// a PodDisruptionBudget that allows one disruption.
func (g *generator) workloads(c *fleettest.Cluster) {
	if len(c.Nodes) == 0 {
		return
	}
	for i := range 2 {
		c.Pods = append(c.Pods, &fleettest.Pod{
			Namespace: "kube-system", Name: "coredns-" + suffix(g.rng), Node: c.Nodes[i%len(c.Nodes)].Name,
			Labels: map[string]string{"k8s-app": "kube-dns"},
		})
	}
	replicas := int32(len(c.Nodes)) //nolint:gosec // a node count
	for _, n := range c.Nodes {
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "app", Name: "web-" + suffix(g.rng), Node: n.Name, Labels: map[string]string{"app": "web"}})
	}
	c.Deployments = []fleettest.Deployment{{Namespace: "app", Name: "web", Replicas: replicas, Selector: map[string]string{"app": "web"}}}
	c.PDBs = []fleettest.PDB{{
		Namespace: "app", Name: "web", Selector: map[string]string{"app": "web"},
		MinAvailable: replicas - 1, DisruptionsAllowed: 1, CurrentHealthy: replicas, DesiredHealthy: replicas - 1, ExpectedPods: replicas,
	}}
}

// onNodegroup reports whether node is a node of the nodegroup of c.
func onNodegroup(c *fleettest.Cluster, node, nodegroup string) bool {
	for _, n := range c.Nodes {
		if n.Name == node {
			return n.Nodegroup == nodegroup
		}
	}
	return false
}

// suffix returns a five-letter pod name suffix, as a ReplicaSet makes one.
func suffix(rng *rand.Rand) string {
	const letters = "bcdfghjklmnpqrstvwxz2456789"
	b := make([]byte, 5)
	for i := range b {
		b[i] = letters[rng.IntN(len(letters))]
	}
	return string(b)
}
