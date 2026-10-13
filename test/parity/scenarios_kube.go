//go:build unix

package parity

import (
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// workloads gives prod a Kubernetes side: two nodes per nodegroup, system
// pods, two web pods on ng-a and two api pods on ng-b, and one budget for
// each of the two, both with room for a disruption.
func workloads(_ *fleettest.State, c *fleettest.Cluster) {
	for _, ng := range c.Nodegroups {
		short := ng.Name[len(ng.Name)-1:]
		image := latestImage(ng.Version)
		if ng.ReleaseVersion != "" {
			image = oldImage(ng.Version)
		}
		for _, i := range []string{"1", "2"} {
			c.Nodes = append(c.Nodes, &fleettest.Node{Name: short + "-" + i, Nodegroup: ng.Name, ImageID: image, UsedMilliCPU: 400, UsedMemoryMi: 2048})
		}
	}
	c.Pods = []*fleettest.Pod{
		{Namespace: "kube-system", Name: "coredns-1", Node: "a-1", Labels: map[string]string{"k8s-app": "kube-dns"}},
		{Namespace: "kube-system", Name: "coredns-2", Node: "b-1", Labels: map[string]string{"k8s-app": "kube-dns"}},
		{Namespace: "default", Name: "web-1", Node: "a-1", Labels: map[string]string{"app": "web"}},
		{Namespace: "default", Name: "web-2", Node: "a-2", Labels: map[string]string{"app": "web"}},
		{Namespace: "default", Name: "api-1", Node: "b-1", Labels: map[string]string{"app": "api"}},
		{Namespace: "default", Name: "api-2", Node: "b-2", Labels: map[string]string{"app": "api"}},
	}
	c.PDBs = []fleettest.PDB{
		{Namespace: "default", Name: "web", Selector: map[string]string{"app": "web"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2},
		{Namespace: "default", Name: "api", Selector: map[string]string{"app": "api"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2},
	}
	c.Deployments = []fleettest.Deployment{
		{Namespace: "default", Name: "web", Replicas: 2, Selector: map[string]string{"app": "web"}},
		{Namespace: "default", Name: "api", Replicas: 2, Selector: map[string]string{"app": "api"}},
		{Namespace: "kube-system", Name: "coredns", Replicas: 2, Selector: map[string]string{"k8s-app": "kube-dns"}},
	}
}

// tight makes the named budget allow no disruption: both of its pods are
// needed.
func tight(name string) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) { tighten(c, name) }
}

func tighten(c *fleettest.Cluster, name string) {
	for i := range c.PDBs {
		if c.PDBs[i].Name == name {
			c.PDBs[i].MinAvailable, c.PDBs[i].DesiredHealthy, c.PDBs[i].DisruptionsAllowed = 2, 2, 0
		}
	}
}

// deny makes the Kubernetes API refuse the operation, as a role without
// the permission would be refused.
func deny(op fleettest.Operation) func(*fleettest.World) error {
	return fault(on(op), fleettest.Fault{Deny: true})
}

// rollOf sets what the next roll of a nodegroup does to its nodes and
// pods, over three polls.
func rollOf(nodegroup string, r fleettest.RollScript) edit {
	s := slow
	s.Roll = r
	return script(nodegroup, s)
}

func kubeNodegroupUpdate() []Scenario {
	with := args("nodegroup", "update", "-c", "prod", "-n", "ng-a", "--poll-interval", pollArg, "-o", "json")
	a := "UpdateNodegroupVersion prod/ng-a"
	return []Scenario{
		{
			Name: "nodegroup-update-kube-yes", Kube: true, Seed: fleet(workloads, scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(a), sawKube(fleettest.KubeListPDBs), sawKube(fleettest.KubeListNodes)},
		},
		{Name: "nodegroup-update-pdb-blocked", Behaviour: DrainBlocked, Kube: true, Seed: fleet(workloads, tight("web")), Legacy: with("--yes")},
		{
			Name: "nodegroup-update-pdb-forced", Behaviour: DrainForced, Kube: true, Seed: fleet(workloads, tight("web"), scripts(slow)),
			Legacy: with("--yes", "--force"), Checks: []Check{updatesAre(a)},
		},
		{
			// The budget that allows nothing covers pods of the other nodegroup only.
			Name: "nodegroup-update-pdb-elsewhere", Kube: true, Seed: fleet(workloads, tight("api"), scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(a)},
		},
		{
			Name: "nodegroup-update-pdbs-unreadable", Behaviour: GateUnreadable, Kube: true, Seed: fleet(workloads),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes"),
		},
		{
			Name: "nodegroup-update-pdbs-unreadable-forced", Behaviour: GateUnreadableForced, Kube: true, Seed: fleet(workloads, scripts(slow)),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes", "--force"),
		},
		{
			Name: "nodegroup-update-workload-failing", Kube: true, Legacy: with("--yes"), Checks: []Check{fails, noUpdates},
			Seed: fleet(workloads, func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Pods[0].Phase, c.Pods[0].Node = "Pending", ""
				c.Pods[1].NotReady = true
			}),
		},
		{
			// One node of the other nodegroup is NotReady: a warning, which
			// --yes accepts.
			Name: "nodegroup-update-node-not-ready", Kube: true, Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(a), sawKubeAnswer(fleettest.KubeListNodes, "b-2=NotReady")},
			Seed:   fleet(workloads, scripts(slow), func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes[3].NotReady = true }),
		},
		{
			Name: "nodegroup-update-node-not-ready-require-healthy", Kube: true, Legacy: with("--yes", "--require-healthy"),
			Checks: []Check{exits(2), noMutations, sawKubeAnswer(fleettest.KubeListNodes, "b-2=NotReady")},
			Seed:   fleet(workloads, func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes[3].NotReady = true }),
		},
		{
			// The new nodes never become Ready. The legacy verification reads
			// the nodegroup's status and the Pending pods, not the nodes, so
			// it passes.
			Name: "nodegroup-update-verify-node-not-ready", Kube: true, Legacy: with("--yes"),
			Seed:   fleet(workloads, rollOf("ng-a", fleettest.RollScript{NodesNeverReady: true})),
			Checks: []Check{exits(0), updatesAre(a), worldSays("ng-a NotReady")},
		},
		{
			Name: "nodegroup-update-verify-pending-pods", Kube: true, Legacy: with("--yes"),
			Seed:   fleet(workloads, rollOf("ng-a", fleettest.RollScript{PendingPods: true})),
			Checks: []Check{exits(5), updatesAre(a), worldSays("default/web-1-u1:Pending")},
		},
		{
			// The update succeeds and EKS leaves the nodegroup DEGRADED.
			Name: "nodegroup-update-verify-degraded", Kube: true, Legacy: with("--yes"),
			Seed: fleet(workloads, func(_ *fleettest.State, c *fleettest.Cluster) {
				s := slow
				s.ResourceStatus = fleettest.StatusDegraded
				s.ResourceIssues = []fleettest.Issue{{Code: "NodeCreationFailure", Message: "instances failed to join"}}
				c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{s}
			}),
			Checks: []Check{exits(5), updatesAre(a)},
		},
		{
			// The pod list is refused once the roll has started. The legacy
			// verification then skips the pod check, and passes.
			Name: "nodegroup-update-verify-pods-unreadable", Kube: true, Seed: fleet(workloads, scripts(slow)), Legacy: with("--yes"),
			Moves:  []Move{{Before: fleettest.EKSDescribeUpdate, Nth: 1, Do: deny(fleettest.KubeListPods)}},
			Checks: []Check{exits(0), updatesAre(a)},
		},
		{
			// DescribeNodegroup is refused once the update has ended, so the
			// state of the nodegroup after the roll is unknown.
			Name: "nodegroup-update-verify-unreadable", Kube: true, Seed: fleet(workloads, scripts(slow)), Legacy: with("--yes"),
			Moves:  []Move{{Before: fleettest.EKSDescribeUpdate, Nth: 4, Do: fault(on(fleettest.EKSDescribeNodegroup), fleettest.Fault{Deny: true})}},
			Checks: []Check{exits(4), updatesAre(a)},
		},
		{
			Name: "nodegroup-update-stuck-drain", Kube: true, Legacy: with("--yes"),
			Seed: fleet(workloads, func(_ *fleettest.State, c *fleettest.Cluster) {
				s := failing
				s.Roll = fleettest.RollScript{StuckDrain: true}
				s.Errors = []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "Reached max retries while trying to evict pods from nodes in node group ng-a", ResourceIDs: []string{"a-1"}}}
				c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{s}
			}),
			Checks: []Check{fails, updatesAre(a), anUpdateFailed},
		},
		{
			// The live node view, forced on without a terminal. It reads the
			// nodes through client-go informers.
			Name: "nodegroup-update-live-view", Kube: true, Live: true, Seed: fleet(workloads, scripts(slow)),
			Legacy: []string{"nodegroup", "update", "-c", "prod", "-n", "ng-a", "--poll-interval", pollArg, "--yes", "--live"},
			Checks: []Check{exits(0), updatesAre(a), called(fleettest.KubeWatchNodes), called(fleettest.KubeWatchEvents), worldSays("ng-a-u1-1:ng-a Ready")},
		},
		{
			// The dry run sees a budget with room. Before the real run the
			// budget tightens, and the real run's gate must see that.
			Name: "nodegroup-update-pdb-tightens-after-dry-run", Kube: true, Seed: fleet(workloads),
			LegacyPreview: with("--dry-run"), Legacy: with("--yes"),
			AfterPreview: func(w *fleettest.World) error {
				w.Edit(func(s *fleettest.State) { tighten(s.Cluster(Region, "prod"), "web") })
				return nil
			},
			Checks: []Check{fails, noUpdates, sawKubeAnswer(fleettest.KubeListPDBs, "default/web=allows0")},
		},
	}
}

func kubeNodegroupScale() []Scenario {
	to := func(desired string) func(extra ...string) []string {
		return args("nodegroup", "scale", "-c", "prod", "-n", "ng-a", "--desired", desired, "--check-pdbs", "-o", "json")
	}
	one := updatesAre("UpdateNodegroupConfig prod/ng-a")
	return []Scenario{
		{
			Name: "nodegroup-scale-pdb-safe", Kube: true, Seed: fleet(workloads), Legacy: to("1")("--yes"),
			Checks: []Check{exits(0), one, sawKube(fleettest.KubeListPDBs)},
		},
		{
			Name: "nodegroup-scale-pdb-blocked", Behaviour: Blocked, Kube: true, Seed: fleet(workloads, tight("web")), Legacy: to("1")("--yes"),
			Checks: []Check{exits(3), noMutations, sawKubeAnswer(fleettest.KubeListPDBs, "default/web=allows0")},
		},
		{
			Name: "nodegroup-scale-pdb-forced", Behaviour: DrainForced, Kube: true, Seed: fleet(workloads, tight("web")),
			Legacy: to("1")("--yes", "--force"), Checks: []Check{one},
		},
		{
			Name: "nodegroup-scale-pdbs-unreadable", Behaviour: GateUnreadable, Kube: true, Seed: fleet(workloads),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: to("1")("--yes"),
		},
		{
			Name: "nodegroup-scale-pdbs-unreadable-forced", Behaviour: GateUnreadableForced, Kube: true, Seed: fleet(workloads),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: to("1")("--yes", "--force"),
		},
		{
			// A scale-up removes no node, so a tight budget does not matter.
			Name: "nodegroup-scale-up-ignores-pdbs", Kube: true, Seed: fleet(workloads, tight("web")), Legacy: to("3")("--yes"),
			Checks: []Check{exits(0), one},
		},
	}
}

func kubeClusterUpgrade() []Scenario {
	with := args("cluster", "upgrade", "-c", "prod", "--to", "1.33", "--poll-interval", pollArg, "-o", "json")
	const (
		control = "UpdateClusterVersion prod"
		cni     = "UpdateAddon prod/vpc-cni"
		dns     = "UpdateAddon prod/coredns"
		a       = "UpdateNodegroupVersion prod/ng-a"
		b       = "UpdateNodegroupVersion prod/ng-b"
	)
	return []Scenario{
		{
			Name: "cluster-upgrade-kube-yes", Kube: true, Seed: fleet(workloads, scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(control, cni, dns, a, b), sawKube(fleettest.KubeListPDBs)},
		},
		{
			// The budget of the first nodegroup's pods allows nothing.
			// The legacy command reads the budgets only when it reaches the
			// nodegroup, after the control plane and the add-ons are done.
			Name: "cluster-upgrade-pdb-blocked", Behaviour: DrainBlocked, Kube: true, Seed: fleet(workloads, tight("web"), scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{updatesAre(control, cni, dns)},
		},
		{
			Name: "cluster-upgrade-pdb-forced", Behaviour: DrainForced, Kube: true, Seed: fleet(workloads, tight("web"), scripts(slow)),
			Legacy: with("--yes", "--force"), Checks: []Check{exits(0), updatesAre(control, cni, dns, a, b)},
		},
		{
			// The budget of the second nodegroup's pods allows nothing.
			Name: "cluster-upgrade-pdb-blocks-second-nodegroup", Kube: true, Seed: fleet(workloads, tight("api"), scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{fails, updatesAre(control, cni, dns, a), resourceIs("ng-a", "1.33 "), resourceIs("ng-b", "1.32 ")},
		},
		{
			Name: "cluster-upgrade-pdbs-unreadable", Behaviour: GateUnreadable, Kube: true, Seed: fleet(workloads, scripts(slow)),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes"), Checks: []Check{updatesAre(control, cni, dns)},
		},
		{
			Name: "cluster-upgrade-pdbs-unreadable-forced", Behaviour: GateUnreadableForced, Kube: true, Seed: fleet(workloads, scripts(slow)),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes", "--force"),
		},
		{
			// While the first nodegroup rolls, the second one degrades and its
			// nodes stop being Ready.
			Name: "cluster-upgrade-nodegroup-degrades-mid-run", Kube: true, Seed: fleet(workloads, scripts(slow)), Legacy: with("--yes"),
			Moves: []Move{{Before: fleettest.EKSUpdateNodegroupVersion, Nth: 1, Do: func(w *fleettest.World) error {
				w.Edit(func(s *fleettest.State) {
					c := s.Cluster(Region, "prod")
					ng := c.Nodegroup("ng-b")
					ng.Status = fleettest.StatusDegraded
					ng.HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "could not launch instances"}}
					for _, n := range c.Nodes {
						if n.Nodegroup == "ng-b" {
							n.NotReady = true
						}
					}
				})
				return nil
			}}},
			Checks: []Check{fails, updatesAre(control, cni, dns, a), resourceIs("ng-b", "1.32 ")},
		},
		{
			// After the plan is made, and before the add-on phase, someone
			// else starts an update of vpc-cni.
			// The foreign update outlasts the control-plane phase, so it is
			// still in flight when the add-on phase starts. The legacy
			// command waits for it and sends no update of its own.
			Name: "cluster-upgrade-addon-updated-elsewhere", Kube: true, Seed: fleet(workloads, scripts(slow), script("vpc-cni", long)), Legacy: with("--yes"),
			Clock: []fleettest.Operation{fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh, fleettest.EKSDescribeAddon},
			Moves: []Move{{Before: fleettest.EKSUpdateClusterVersion, Nth: 1, Do: func(w *fleettest.World) error {
				_, err := w.UpdateAddon(Region, "prod", "vpc-cni", fleettest.AddonRequest{Version: new("v1.20.0-eksbuild.1")})
				return err
			}}},
			Checks: []Check{exits(0), updatesAre(control, dns, a, b), sawAnswer(fleettest.EKSDescribeAddon, "v1.20.0-eksbuild.1 UPDATING"), resourceIs("vpc-cni", "v1.20.0-eksbuild.1 ACTIVE")},
		},
	}
}

func kubeClusterRollback() []Scenario {
	with := args("cluster", "rollback", "-c", "prod", "--poll-interval", pollArg, "-o", "json")
	const (
		control = "UpdateClusterVersion prod"
		cni     = "UpdateAddon prod/vpc-cni"
		dns     = "UpdateAddon prod/coredns"
		a       = "UpdateNodegroupVersion prod/ng-a"
		b       = "UpdateNodegroupVersion prod/ng-b"
	)
	return []Scenario{
		{
			Name: "cluster-rollback-kube-yes", Kube: true, Seed: fleet(upgraded, workloads, scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(a, b, cni, dns, control), sawKube(fleettest.KubeListPDBs)},
		},
		{Name: "cluster-rollback-pdb-blocked", Behaviour: DrainBlocked, Kube: true, Seed: fleet(upgraded, workloads, tight("web"), scripts(slow)), Legacy: with("--yes")},
		{
			Name: "cluster-rollback-pdb-forced", Behaviour: DrainForced, Kube: true, Seed: fleet(upgraded, workloads, tight("web"), scripts(slow)),
			Legacy: with("--yes", "--force"), Checks: []Check{exits(0), updatesAre(a, b, cni, dns, control)},
		},
		{
			Name: "cluster-rollback-pdb-blocks-second-nodegroup", Kube: true, Seed: fleet(upgraded, workloads, tight("api"), scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{fails, updatesAre(a), resourceIs("ng-a", "1.32 "), resourceIs("ng-b", "1.33 ")},
		},
		{
			Name: "cluster-rollback-pdbs-unreadable", Behaviour: GateUnreadable, Kube: true, Seed: fleet(upgraded, workloads, scripts(slow)),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes"),
		},
		{
			Name: "cluster-rollback-pdbs-unreadable-forced", Behaviour: GateUnreadableForced, Kube: true, Seed: fleet(upgraded, workloads, scripts(slow)),
			Prepare: deny(fleettest.KubeListPDBs), Legacy: with("--yes", "--force"),
		},
	}
}
