//go:build unix

package parity

import (
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// scenarios returns every scenario of the suite.
func scenarios() []Scenario {
	groups := map[Command][]Scenario{
		AddonUpdate:     addonUpdate(),
		NodegroupScale:  slices.Concat(nodegroupScale(), kubeNodegroupScale()),
		NodegroupUpdate: slices.Concat(nodegroupUpdate(), kubeNodegroupUpdate()),
		ClusterUpgrade:  slices.Concat(clusterUpgrade(), kubeClusterUpgrade()),
		ClusterRollback: slices.Concat(clusterRollback(), kubeClusterRollback()),
	}
	var out []Scenario
	for _, command := range commands {
		for _, sc := range groups[command] {
			sc.Command = command
			out = append(out, sc)
		}
	}
	return out
}

// commands are the rows of the matrix, in order.
var commands = []Command{AddonUpdate, NodegroupScale, NodegroupUpdate, ClusterUpgrade, ClusterRollback}

// behaviours are the columns of the matrix, in order.
var behaviours = []Behaviour{
	Success, DryRun, Unattended, PromptYes, PromptNo, PromptInterrupt, Blocked, Failed,
	Busy, Attach, AttachFailed, Deadline, Interrupt, LostResponse, ThrottledRead,
	DrainBlocked, DrainForced, GateUnreadable, GateUnreadableForced,
}

// noDrain is why an add-on update has no PDB cells.
const noDrain = "an add-on update drains no node, and the legacy command reads no PodDisruptionBudget for it"

// matrix says, for every command and behaviour, which scenario shows it or
// why none does. TestMatrix holds it to the scenarios.
var matrix = map[Command]map[Behaviour]cell{
	AddonUpdate: {
		Success: by("addon-update-yes"), DryRun: by("addon-update-dry-run"), Unattended: by("addon-update-unattended"),
		PromptYes: by("addon-update-prompt-yes"), PromptNo: by("addon-update-prompt-no"), PromptInterrupt: by("addon-update-prompt-interrupt"),
		Blocked: by("addon-update-incompatible"), Failed: by("addon-update-failed"), Busy: by("addon-update-busy"),
		Attach: by("addon-update-attach"), AttachFailed: by("addon-update-attach-failed"), Deadline: by("addon-update-deadline"),
		Interrupt: by("addon-update-interrupt"), LostResponse: by("addon-update-lost-response"), ThrottledRead: by("addon-update-throttled-read"),
		DrainBlocked: none(noDrain), DrainForced: none(noDrain), GateUnreadable: none(noDrain), GateUnreadableForced: none(noDrain),
	},
	NodegroupScale: {
		Success: by("nodegroup-scale-yes"), DryRun: by("nodegroup-scale-dry-run"), Unattended: by("nodegroup-scale-unattended"),
		PromptYes: by("nodegroup-scale-prompt-yes"), PromptNo: by("nodegroup-scale-prompt-no"), PromptInterrupt: by("nodegroup-scale-prompt-interrupt"),
		Blocked: by("nodegroup-scale-pdb-blocked"), Failed: by("nodegroup-scale-failed"), Busy: by("nodegroup-scale-busy"),
		Attach:       none("the legacy command refuses a nodegroup that is changing; it never attaches"),
		AttachFailed: none("the legacy command refuses a nodegroup that is changing; it never attaches"),
		Deadline:     by("nodegroup-scale-deadline"), Interrupt: by("nodegroup-scale-interrupt"),
		LostResponse: by("nodegroup-scale-lost-response"), ThrottledRead: by("nodegroup-scale-throttled-read"),
		DrainBlocked:   none("the PDB check is the command's only gate, so the blocked cell is this case: nodegroup-scale-pdb-blocked"),
		DrainForced:    by("nodegroup-scale-pdb-forced"),
		GateUnreadable: by("nodegroup-scale-pdbs-unreadable"), GateUnreadableForced: by("nodegroup-scale-pdbs-unreadable-forced"),
	},
	NodegroupUpdate: {
		Success: by("nodegroup-update-yes"), DryRun: by("nodegroup-update-dry-run"),
		Unattended: none("the legacy command does not refuse: nodegroup-update-no-yes shows that it starts the roll"),
		PromptYes:  by("nodegroup-update-prompt-yes"), PromptNo: by("nodegroup-update-prompt-no"), PromptInterrupt: by("nodegroup-update-prompt-interrupt"),
		Blocked: by("nodegroup-update-blocked"), Failed: by("nodegroup-update-failed"), Busy: by("nodegroup-update-busy"),
		Attach:       none("the legacy command skips a nodegroup that is UPDATING; it never attaches"),
		AttachFailed: none("the legacy command skips a nodegroup that is UPDATING; it never attaches"),
		Deadline:     by("nodegroup-update-deadline"), Interrupt: by("nodegroup-update-interrupt"),
		LostResponse: by("nodegroup-update-lost-response"), ThrottledRead: by("nodegroup-update-throttled-read"),
		DrainBlocked: by("nodegroup-update-pdb-blocked"), DrainForced: by("nodegroup-update-pdb-forced"),
		GateUnreadable: by("nodegroup-update-pdbs-unreadable"), GateUnreadableForced: by("nodegroup-update-pdbs-unreadable-forced"),
	},
	ClusterUpgrade: {
		Success: by("cluster-upgrade-yes"), DryRun: by("cluster-upgrade-dry-run"), Unattended: by("cluster-upgrade-unattended"),
		PromptYes: by("cluster-upgrade-prompt-yes"), PromptNo: by("cluster-upgrade-prompt-no"), PromptInterrupt: by("cluster-upgrade-prompt-interrupt"),
		Blocked: by("cluster-upgrade-blocked"), Failed: by("cluster-upgrade-failed"),
		Busy:   none("the legacy command has no busy check (CONTRACT.md lists it as behaviour the new CLI does not copy)"),
		Attach: by("cluster-upgrade-attach"), AttachFailed: by("cluster-upgrade-attach-failed"), Deadline: by("cluster-upgrade-deadline"),
		Interrupt: by("cluster-upgrade-interrupt"), LostResponse: by("cluster-upgrade-lost-response"), ThrottledRead: by("cluster-upgrade-throttled-read"),
		DrainBlocked: by("cluster-upgrade-pdb-blocked"), DrainForced: by("cluster-upgrade-pdb-forced"),
		GateUnreadable: by("cluster-upgrade-pdbs-unreadable"), GateUnreadableForced: by("cluster-upgrade-pdbs-unreadable-forced"),
	},
	ClusterRollback: {
		Success: by("cluster-rollback-yes"), DryRun: by("cluster-rollback-dry-run"), Unattended: by("cluster-rollback-unattended"),
		PromptYes: by("cluster-rollback-prompt-yes"), PromptNo: by("cluster-rollback-prompt-no"), PromptInterrupt: by("cluster-rollback-prompt-interrupt"),
		Blocked: by("cluster-rollback-blocked"), Failed: by("cluster-rollback-failed"),
		Busy:   none("the legacy command has no busy check"),
		Attach: by("cluster-rollback-attach"), AttachFailed: by("cluster-rollback-attach-failed"), Deadline: by("cluster-rollback-deadline"),
		Interrupt: by("cluster-rollback-interrupt"), LostResponse: by("cluster-rollback-lost-response"), ThrottledRead: by("cluster-rollback-throttled-read"),
		DrainBlocked: by("cluster-rollback-pdb-blocked"), DrainForced: by("cluster-rollback-pdb-forced"),
		GateUnreadable: by("cluster-rollback-pdbs-unreadable"), GateUnreadableForced: by("cluster-rollback-pdbs-unreadable-forced"),
	},
}

func addonUpdate() []Scenario {
	with := args("addon", "update", "-c", "prod", "-a", "vpc-cni", "-o", "json")
	all := args("addon", "update", "-c", "prod", "--all", "-o", "json")
	onTerminal := args("addon", "update", "-c", "prod", "-a", "vpc-cni")
	const target = "v1.19.0-eksbuild.1"
	one := updatesAre("UpdateAddon prod/vpc-cni")
	inFlight := func(script fleettest.UpdateScript) func(w *fleettest.World) error {
		return func(w *fleettest.World) error {
			w.Edit(func(s *fleettest.State) {
				s.Cluster(Region, "prod").Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{script}
			})
			_, err := w.UpdateAddon(Region, "prod", "vpc-cni", fleettest.AddonRequest{Version: new(target)})
			return err
		}
	}
	// The wait for an add-on that is already UPDATING polls DescribeAddon.
	addonClock := []fleettest.Operation{fleettest.EKSDescribeAddon}
	return []Scenario{
		{Name: "addon-update-yes", Behaviour: Success, Seed: fleet(), Legacy: with("--yes"), Checks: []Check{one, resourceIs("vpc-cni", target+" ACTIVE")}},
		{Name: "addon-update-dry-run", Behaviour: DryRun, Seed: fleet(), Legacy: with("--dry-run"), Checks: []Check{exits(0)}},
		{Name: "addon-update-unattended", Behaviour: Unattended, Seed: fleet(), Legacy: with()},
		{Name: "addon-update-prompt-yes", Behaviour: PromptYes, Seed: fleet(), Legacy: onTerminal(), Terminal: sayYes, Checks: []Check{exits(0), one}},
		{Name: "addon-update-prompt-no", Behaviour: PromptNo, Seed: fleet(), Legacy: onTerminal(), Terminal: sayNo},
		{Name: "addon-update-prompt-interrupt", Behaviour: PromptInterrupt, Seed: fleet(), Legacy: onTerminal(), Terminal: ctrlC},
		{Name: "addon-update-wait", Behaviour: Success, Seed: fleet(scripts(oneTick)), Legacy: with("--yes", "--wait"), Checks: []Check{one}},
		{
			Name: "addon-update-failed", Behaviour: Failed, Seed: fleet(scripts(failsInOneTick)), Legacy: with("--yes", "--wait"),
			Checks: []Check{one, resourceIs("vpc-cni", "v1.18.0-eksbuild.1")},
		},
		{
			Name: "addon-update-incompatible", Behaviour: Blocked, Seed: fleet(),
			Legacy: with("--yes", "--health-check", "--version", "v1.20.0-eksbuild.1"),
		},
		{
			// The legacy command repairs a degraded add-on: it warns and updates.
			Name: "addon-update-degraded", Legacy: with("--yes", "--health-check"), Checks: []Check{exits(0), one},
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addon("vpc-cni").Status = fleettest.StatusDegraded
				c.Addon("vpc-cni").HealthIssues = []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "0 of 2 replicas are ready"}}
			}),
		},
		{
			// UPDATING at the target version, with no update to follow.
			Name: "addon-update-busy", Behaviour: Busy, Legacy: with("--yes"), Checks: []Check{exits(0), resourceIs("vpc-cni", target+" UPDATING")},
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addon("vpc-cni").Status, c.Addon("vpc-cni").Version = fleettest.StatusUpdating, target
			}),
		},
		{
			Name: "addon-update-attach", Behaviour: Attach, Seed: fleet(), Prepare: inFlight(fleettest.UpdateScript{After: 4 * time.Minute}), Clock: addonClock,
			Legacy: with("--yes", "--wait"), Checks: []Check{resourceIs("vpc-cni", target+" ACTIVE")},
		},
		{
			Name: "addon-update-attach-failed", Behaviour: AttachFailed, Seed: fleet(), Clock: addonClock, Legacy: with("--yes", "--wait"),
			Checks: []Check{fails, noUpdates},
			Prepare: inFlight(fleettest.UpdateScript{
				After: 4 * time.Minute, Result: fleettest.UpdateFailed, ResourceStatus: fleettest.StatusUpdateFailed,
				ResourceIssues: []fleettest.Issue{{Code: "AdmissionRequestDenied", Message: "the webhook denied the request"}},
			}),
		},
		{Name: "addon-update-deadline", Behaviour: Deadline, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait", "--wait-timeout", "300ms"), Checks: []Check{one}},
		{
			Name: "addon-update-interrupt", Behaviour: Interrupt, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait"),
			Signal: interruptWait(1, 1), Checks: []Check{one},
		},
		{
			// The control plane is updating. The command's busy check sees it.
			Name: "addon-update-cluster-updating", Seed: fleet(scripts(never)), Prepare: startControlPlaneUpdate, Legacy: with("--yes"),
			Checks: []Check{fails, noMutations, sawAnswer(fleettest.EKSDescribeCluster, "UPDATING")},
		},
		{
			// The control plane starts to update just before the command
			// sends its update. EKS refuses it.
			Name: "addon-update-cluster-starts-updating", Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Moves:  []Move{{When: BeforeMutation(fleettest.EKSUpdateAddon, "prod/vpc-cni"), Do: startControlPlaneUpdate}},
			Checks: []Check{fails, refusedByEKS, resourceIs("vpc-cni", "v1.18.0-eksbuild.1 ACTIVE")},
		},
		{
			Name: "addon-update-lost-response", Behaviour: LostResponse, Seed: fleet(), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSUpdateAddon), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "addon-update-throttled-read", Behaviour: ThrottledRead, Seed: fleet(), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSDescribeAddonVersions), fleettest.Fault{Throttle: 1}),
		},
		{
			// ListAddons answers one name per page. The second add-on is on
			// the second page.
			Name: "addon-update-all-paged", Seed: fleet(bothBehind), Legacy: all("--yes"),
			Prepare: fault(on(fleettest.EKSListAddons), fleettest.Fault{PageSize: 1}),
			Checks:  []Check{exits(0), updatesAre("UpdateAddon prod/vpc-cni", "UpdateAddon prod/coredns")},
		},
		{
			Name: "addon-update-all", Seed: fleet(bothBehind), Legacy: all("--yes"),
			Checks: []Check{exits(0), updatesAre("UpdateAddon prod/vpc-cni", "UpdateAddon prod/coredns")},
		},
		{
			// coredns is listed first; the order flag puts vpc-cni before it.
			Name: "addon-update-all-dependency-order", Legacy: all("--yes", "--dependency-order"),
			Seed:   fleet(bothBehind, func(_ *fleettest.State, c *fleettest.Cluster) { slices.Reverse(c.Addons) }),
			Checks: []Check{exits(0), updatesAre("UpdateAddon prod/vpc-cni", "UpdateAddon prod/coredns")},
		},
		{
			Name: "addon-update-all-skip", Seed: fleet(bothBehind), Legacy: all("--yes", "--skip", "coredns"),
			Checks: []Check{exits(0), one, resourceIs("coredns", "v1.11.3-eksbuild.1")},
		},
		{
			Name: "addon-update-all-partial-failure", Seed: fleet(bothBehind, scripts(oneTick), script("vpc-cni", failsInOneTick)),
			Legacy: all("--yes", "--wait"),
			Checks: []Check{fails, updatesAre("UpdateAddon prod/vpc-cni", "UpdateAddon prod/coredns"), resourceIs("vpc-cni", "v1.18.0-eksbuild.1"), resourceIs("coredns", "v1.11.4-eksbuild.2")},
		},
	}
}

func nodegroupScale() []Scenario {
	to := func(desired string) func(extra ...string) []string {
		return args("nodegroup", "scale", "-c", "prod", "-n", "ng-a", "--desired", desired, "-o", "json")
	}
	with := to("3")
	onTerminal := args("nodegroup", "scale", "-c", "prod", "-n", "ng-a", "--desired", "3")
	one := updatesAre("UpdateNodegroupConfig prod/ng-a")
	return []Scenario{
		{Name: "nodegroup-scale-yes", Behaviour: Success, Seed: fleet(), Legacy: with("--yes"), Checks: []Check{one, resourceIs("ng-a", "ACTIVE 3/1/3")}},
		{Name: "nodegroup-scale-dry-run", Behaviour: DryRun, Seed: fleet(), Legacy: with("--dry-run"), Checks: []Check{exits(0)}},
		{Name: "nodegroup-scale-unattended", Behaviour: Unattended, Seed: fleet(), Legacy: with()},
		{Name: "nodegroup-scale-prompt-yes", Behaviour: PromptYes, Seed: fleet(), Legacy: onTerminal(), Terminal: sayYes, Checks: []Check{exits(0), one}},
		{Name: "nodegroup-scale-prompt-no", Behaviour: PromptNo, Seed: fleet(), Legacy: onTerminal(), Terminal: sayNo},
		{Name: "nodegroup-scale-prompt-interrupt", Behaviour: PromptInterrupt, Seed: fleet(), Legacy: onTerminal(), Terminal: ctrlC},
		{Name: "nodegroup-scale-wait", Behaviour: Success, Seed: fleet(scripts(oneTick)), Legacy: with("--yes", "--wait"), Checks: []Check{one}},
		{Name: "nodegroup-scale-down", Behaviour: Success, Seed: fleet(), Legacy: to("1")("--yes"), Checks: []Check{one, resourceIs("ng-a", "ACTIVE 1/1/3")}},
		{
			Name: "nodegroup-scale-failed", Behaviour: Failed, Seed: fleet(scripts(failsInOneTick)), Legacy: with("--yes", "--wait"),
			Checks: []Check{one, resourceIs("ng-a", "ACTIVE 2/1/3")},
		},
		{
			// The PDBs cannot be read without a Kubernetes API, and the gate fails closed.
			Name: "nodegroup-scale-check-pdbs-no-kube", Seed: fleet(), Legacy: to("1")("--check-pdbs", "--yes"),
			Checks: []Check{fails, noMutations},
		},
		{
			// The legacy command does not check the sizes itself. EKS refuses.
			Name: "nodegroup-scale-above-max", Seed: fleet(), Legacy: to("9")("--yes"),
			Checks: []Check{fails, refusedByEKS, resourceIs("ng-a", "ACTIVE 2/1/3")},
		},
		{
			Name: "nodegroup-scale-min-above-max", Seed: fleet(), Legacy: with("--yes", "--min", "5", "--max", "2"),
			Checks: []Check{fails, refusedByEKS, resourceIs("ng-a", "ACTIVE 2/1/3")},
		},
		{
			Name: "nodegroup-scale-busy", Behaviour: Busy, Legacy: with("--yes"), Checks: []Check{fails},
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("ng-a").Status = fleettest.StatusUpdating }),
		},
		{Name: "nodegroup-scale-deadline", Behaviour: Deadline, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait", "--wait-timeout", "300ms"), Checks: []Check{one}},
		{
			Name: "nodegroup-scale-interrupt", Behaviour: Interrupt, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait"),
			Signal: interruptWait(1, 1), Checks: []Check{one},
		},
		{
			// Someone else is scaling the nodegroup, and the describes still
			// say ACTIVE, as EKS can for a moment. The command cannot know.
			// It sends its update, EKS refuses it, and that is a failure.
			Name: "nodegroup-scale-busy-status-lags", Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Prepare: func(w *fleettest.World) error {
				w.ScriptStatus(Region, "prod", "ng-a", slices.Repeat([]fleettest.ResourceStatus{fleettest.StatusActive}, 50)...)
				return scaleElsewhere(w)
			},
			Checks: []Check{fails, refusedByEKS, resourceIs("ng-a", "UPDATING")},
		},
		{
			Name: "nodegroup-scale-lost-response", Behaviour: LostResponse, Seed: fleet(), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSUpdateNodegroupConfig), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "nodegroup-scale-throttled-read", Behaviour: ThrottledRead, Seed: fleet(), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSDescribeNodegroup), fleettest.Fault{Throttle: 1}),
		},
	}
}

func nodegroupUpdate() []Scenario {
	every := args("nodegroup", "update", "-c", "prod", "--poll-interval", pollArg, "-o", "json")
	with := func(extra ...string) []string { return every(append([]string{"-n", "ng-a"}, extra...)...) }
	// On a terminal the command prompts only for health warnings.
	onTerminal := args("nodegroup", "update", "-c", "prod", "-n", "ng-a", "--poll-interval", pollArg)
	a, b := "UpdateNodegroupVersion prod/ng-a", "UpdateNodegroupVersion prod/ng-b"
	rolled := resourceIs("ng-a", "1.32.0-20260101 ACTIVE")
	return []Scenario{
		{Name: "nodegroup-update-yes", Behaviour: Success, Seed: fleet(scripts(slow)), Legacy: with("--yes"), Checks: []Check{updatesAre(a), rolled}},
		{Name: "nodegroup-update-dry-run", Behaviour: DryRun, Seed: fleet(), Legacy: with("--dry-run"), Checks: []Check{exits(0)}},
		{
			// The release notes come from api.github.com, which the runner
			// keeps the binary away from. The dry run must still finish.
			Name: "nodegroup-update-dry-run-changelog", Seed: fleet(), Legacy: with("--dry-run", "--changelog"),
			Checks: []Check{exits(0), noMutations, sawRead(fleettest.EKSDescribeNodegroup)},
		},
		{
			// Someone else starts to scale the nodegroup while the command
			// waits at its prompt. The health checks it showed the user are
			// stale by the time the user says yes. A command that reads the
			// nodegroup again after the answer sees the change and leaves
			// the nodegroup alone, as the legacy command does. One that does
			// not sends its update.
			Name: "nodegroup-update-changed-at-the-prompt", Seed: fleet(noMetrics, scripts(never)), Legacy: onTerminal(), Terminal: sayYes,
			Moves:  []Move{{When: AfterPrompt(1), Do: scaleElsewhere}},
			Checks: []Check{exits(0), noMutations, sawAfterPrompt(Fact{Subject: "nodegroup prod/ng-a", Attribute: "status", Value: "UPDATING"})},
		},
		{
			// The same, at the last moment: the command has decided and its
			// update is on the way. EKS refuses the update, and the command
			// must report that as a failure.
			Name: "nodegroup-update-changed-under-the-gate", Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Moves:  []Move{{When: BeforeMutation(fleettest.EKSUpdateNodegroupVersion, "prod/ng-a"), Do: scaleElsewhere}},
			Checks: []Check{fails, refusedByEKS, resourceIs("ng-a", "1.32.0-20250101 UPDATING")},
		},
		{
			// The control plane starts to update just before the command
			// sends its roll. EKS refuses the roll.
			Name: "nodegroup-update-cluster-starts-updating", Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Moves:  []Move{{When: BeforeMutation(fleettest.EKSUpdateNodegroupVersion, "prod/ng-a"), Do: startControlPlaneUpdate}},
			Checks: []Check{fails, refusedByEKS, resourceIs("ng-a", "1.32.0-20250101 ACTIVE")},
		},
		{
			Name: "nodegroup-update-cluster-updating", Seed: fleet(scripts(never)), Prepare: startControlPlaneUpdate, Legacy: with("--yes"),
			Checks: []Check{noMutations, sawAnswer(fleettest.EKSDescribeCluster, "UPDATING")},
		},
		{
			// The legacy command does not refuse without --yes when health is clean.
			// The new command must refuse before it calls AWS at all.
			Name: "nodegroup-update-no-yes", Seed: fleet(scripts(slow)), Legacy: with(), Checks: []Check{exits(0), updatesAre(a)},
			V2Checks: []Check{noRequests},
		},
		{Name: "nodegroup-update-warnings-unattended", Seed: fleet(noMetrics), Legacy: with(), Checks: []Check{fails, noMutations}},
		{Name: "nodegroup-update-require-healthy", Seed: fleet(noMetrics), Legacy: with("--yes", "--require-healthy"), Checks: []Check{exits(2), noMutations}},
		{
			// On a terminal the command draws the live roll view.
			Name: "nodegroup-update-prompt-yes", Behaviour: PromptYes, Live: true, Seed: fleet(noMetrics, scripts(slow)),
			Legacy: onTerminal(), Terminal: sayYes, Checks: []Check{exits(0), updatesAre(a)},
		},
		{Name: "nodegroup-update-prompt-no", Behaviour: PromptNo, Seed: fleet(noMetrics), Legacy: onTerminal(), Terminal: sayNo},
		{Name: "nodegroup-update-prompt-interrupt", Behaviour: PromptInterrupt, Seed: fleet(noMetrics), Legacy: onTerminal(), Terminal: ctrlC},
		{
			// Every nodegroup is DEGRADED: no node counts as ready.
			Name: "nodegroup-update-blocked", Behaviour: Blocked, Legacy: with("--yes"),
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) {
				for _, ng := range c.Nodegroups {
					ng.Status = fleettest.StatusDegraded
					ng.HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "could not launch instances"}}
				}
			}),
		},
		{
			// One other nodegroup is DEGRADED: a warning, which --yes accepts.
			Name: "nodegroup-update-degraded-neighbour", Legacy: with("--yes"), Checks: []Check{exits(0), updatesAre(a)},
			Seed: fleet(scripts(slow), func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodegroup("ng-b").Status = fleettest.StatusDegraded
				c.Nodegroup("ng-b").HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "could not launch instances"}}
			}),
		},
		{Name: "nodegroup-update-failed", Behaviour: Failed, Seed: fleet(scripts(failing)), Legacy: with("--yes"), Checks: []Check{updatesAre(a)}},
		{
			// UPDATING, with no update to follow: the legacy command skips it.
			Name: "nodegroup-update-busy", Behaviour: Busy, Legacy: with("--yes"), Checks: []Check{exits(0)},
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("ng-a").Status = fleettest.StatusUpdating }),
		},
		{Name: "nodegroup-update-deadline", Behaviour: Deadline, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait-timeout", "300ms"), Checks: []Check{updatesAre(a), worldMoved}},
		{
			Name: "nodegroup-update-interrupt", Behaviour: Interrupt, Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Signal: interruptWait(1, 2), Checks: []Check{updatesAre(a)},
		},
		{
			Name: "nodegroup-update-lost-response", Behaviour: LostResponse, Seed: fleet(scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSUpdateNodegroupVersion), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "nodegroup-update-throttled-read", Behaviour: ThrottledRead, Seed: fleet(scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.SSMGetParameter), fleettest.Fault{Throttle: 1}),
		},
		{Name: "nodegroup-update-no-wait", Seed: fleet(scripts(slow)), Legacy: with("--yes", "--no-wait"), Checks: []Check{exits(0), updatesAre(a), resourceIs("ng-a", "UPDATING")}},
		{
			// No -n: every nodegroup. ng-b is on the latest AMI and is left alone.
			Name: "nodegroup-update-all-nodegroups", Seed: fleet(scripts(slow)), Legacy: every("--yes"), Checks: []Check{exits(0), updatesAre(a)},
		},
		{Name: "nodegroup-update-several", Seed: fleet(bothStale, scripts(slow)), Legacy: every("--yes"), Checks: []Check{exits(0), updatesAre(a, b)}},
		{
			Name: "nodegroup-update-partial-failure", Seed: fleet(bothStale, scripts(slow), script("ng-b", failing)), Legacy: every("--yes"),
			Checks: []Check{fails, updatesAre(a, b), rolled, resourceIs("ng-b", "1.32.0-20250101")},
		},
		{Name: "nodegroup-update-force", Seed: fleet(scripts(slow)), Legacy: with("--yes", "--force"), Checks: []Check{exits(0), updatesAre(a)}},
		{
			Name: "nodegroup-update-reroll", Seed: fleet(scripts(slow)), Legacy: every("-n", "ng-b", "--yes", "--reroll"),
			Checks: []Check{exits(0), updatesAre(b)},
		},
		{
			// Without --reroll a nodegroup on the latest AMI is not rolled.
			Name: "nodegroup-update-up-to-date", Seed: fleet(), Legacy: every("-n", "ng-b", "--yes"), Checks: []Check{exits(0), noMutations},
		},
		{
			// --force on a nodegroup that is on the latest AMI. The legacy
			// command rolls it.
			Name: "nodegroup-update-force-up-to-date", Seed: fleet(scripts(slow)), Legacy: every("-n", "ng-b", "--yes", "--force"),
			Checks: []Check{exits(0), updatesAre(b)},
		},
		{
			// ListNodegroups answers one name per page. The second stale
			// nodegroup is on the second page.
			Name: "nodegroup-update-paged", Seed: fleet(bothStale, scripts(slow)), Legacy: every("--yes"),
			Prepare: fault(on(fleettest.EKSListNodegroups), fleettest.Fault{PageSize: 1}),
			Checks:  []Check{exits(0), updatesAre(a, b)},
		},
		{
			Name: "nodegroup-update-custom-ami", Legacy: with("--yes"), Checks: []Check{noMutations},
			Seed: fleet(func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("ng-a").AMIType = "CUSTOM" }),
		},
		{
			Name: "nodegroup-update-all-clusters", Seed: fleet(staging, scripts(slow)),
			Legacy: []string{"nodegroup", "update", "--all-clusters", "-r", Region, "--yes", "--poll-interval", pollArg, "-o", "json"},
			Checks: []Check{exits(0), updatesAre(a, "UpdateNodegroupVersion staging/ng-s")},
		},
	}
}

func clusterUpgrade() []Scenario {
	to := func(version string) func(extra ...string) []string {
		return args("cluster", "upgrade", "-c", "prod", "--to", version, "--poll-interval", pollArg, "-o", "json")
	}
	with := to("1.33")
	onTerminal := args("cluster", "upgrade", "-c", "prod", "--to", "1.33", "--poll-interval", pollArg)
	const (
		control = "UpdateClusterVersion prod"
		cni     = "UpdateAddon prod/vpc-cni"
		dns     = "UpdateAddon prod/coredns"
		a       = "UpdateNodegroupVersion prod/ng-a"
		b       = "UpdateNodegroupVersion prod/ng-b"
	)
	whole := updatesAre(control, cni, dns, a, b)
	inFlight := func(script fleettest.UpdateScript) func(w *fleettest.World) error {
		return func(w *fleettest.World) error {
			w.Edit(func(s *fleettest.State) { s.Cluster(Region, "prod").UpdateScripts = []fleettest.UpdateScript{script} })
			_, err := w.UpdateClusterVersion(Region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33")})
			return err
		}
	}
	// The wait for a control plane that is already UPDATING polls DescribeCluster.
	clusterClock := []fleettest.Operation{fleettest.EKSDescribeCluster, fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh}
	return []Scenario{
		{Name: "cluster-upgrade-yes", Behaviour: Success, Seed: fleet(scripts(slow)), Legacy: with("--yes"), Checks: []Check{whole}},
		{Name: "cluster-upgrade-dry-run", Behaviour: DryRun, Seed: fleet(), Legacy: with("--dry-run"), Checks: []Check{exits(0)}},
		{Name: "cluster-upgrade-unattended", Behaviour: Unattended, Seed: fleet(), Legacy: with()},
		{Name: "cluster-upgrade-prompt-yes", Behaviour: PromptYes, Seed: fleet(scripts(slow)), Legacy: onTerminal(), Terminal: sayYes, Checks: []Check{exits(0), whole}},
		{Name: "cluster-upgrade-prompt-no", Behaviour: PromptNo, Seed: fleet(), Legacy: onTerminal(), Terminal: sayNo},
		{Name: "cluster-upgrade-prompt-interrupt", Behaviour: PromptInterrupt, Seed: fleet(), Legacy: onTerminal(), Terminal: ctrlC},
		{Name: "cluster-upgrade-blocked", Behaviour: Blocked, Seed: fleet(blockingInsight), Legacy: with("--yes"), Checks: []Check{exits(3)}},
		{
			// ListInsights answers one insight per page. The one that blocks
			// is on the second page.
			Name: "cluster-upgrade-blocked-on-second-page", Legacy: with("--yes"), Checks: []Check{exits(3), noUpdates},
			Seed: fleet(blockingInsight, func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Insights = append([]fleettest.Insight{{ID: "in-0", Name: "Kubelet version skew", Status: fleettest.InsightPassing}}, c.Insights...)
			}),
			Prepare: fault(on(fleettest.EKSListInsights), fleettest.Fault{PageSize: 1}),
		},
		{
			Name: "cluster-upgrade-skip-insights", Seed: fleet(blockingInsight, scripts(slow)), Legacy: with("--yes", "--skip-insights-check"),
			Checks: []Check{exits(0), whole},
		},
		{Name: "cluster-upgrade-failed", Behaviour: Failed, Seed: fleet(scripts(slow), script("", failing)), Legacy: with("--yes"), Checks: []Check{updatesAre(control)}},
		{
			Name: "cluster-upgrade-addon-failed", Seed: fleet(scripts(slow), script("vpc-cni", failing)), Legacy: with("--yes"),
			Checks: []Check{fails, updatesAre(control, cni), resourceIs("ng-a", "1.32 ")},
		},
		{
			Name: "cluster-upgrade-nodegroup-failed", Seed: fleet(scripts(slow), script("ng-a", failing)), Legacy: with("--yes"),
			Checks: []Check{fails, updatesAre(control, cni, dns, a), resourceIs("ng-b", "1.32 ")},
		},
		{
			// The control plane and the add-ons are done; the nodegroups are not.
			Name: "cluster-upgrade-resume", Legacy: with("--yes"), Checks: []Check{exits(0), updatesAre(a, b)},
			Seed: fleet(scripts(slow), func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Version = "1.33"
				c.Addon("vpc-cni").Version, c.Addon("coredns").Version = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
			}),
		},
		{
			Name: "cluster-upgrade-attach", Behaviour: Attach, Seed: fleet(scripts(slow)), Prepare: inFlight(long), Clock: clusterClock,
			Legacy: with("--yes", "--only", "control-plane"),
		},
		{
			// The in-flight control-plane update ends, and the run goes on with the rest.
			Name: "cluster-upgrade-attach-continue", Seed: fleet(scripts(slow)), Prepare: inFlight(long), Clock: clusterClock,
			Legacy: with("--yes"), Checks: []Check{exits(0), updatesAre(cni, dns, a, b)},
		},
		{
			// The control plane is updating, and ListUpdates does not list
			// the update yet, as EKS can for a moment.
			Name: "cluster-upgrade-attach-update-not-listed", Seed: fleet(scripts(slow)), Clock: clusterClock,
			Prepare: func(w *fleettest.World) error {
				w.HideUpdates(Region, "prod", 1)
				return inFlight(long)(w)
			},
			Legacy: with("--yes", "--only", "control-plane"), Checks: []Check{exits(0), noUpdates, resourceIs("ng-a", "1.32 ")},
		},
		{
			// The in-flight update fails. The legacy command then starts its own.
			Name: "cluster-upgrade-attach-failed", Behaviour: AttachFailed, Seed: fleet(scripts(slow)), Prepare: inFlight(longFailing), Clock: clusterClock,
			Legacy: with("--yes", "--only", "control-plane"), Checks: []Check{exits(0), updatesAre(control), anUpdateFailed},
			// The new command must have seen the attached update fail, and
			// must say so before it starts its own.
			V2Require: []Fact{{Subject: "update prod 1", Attribute: "status", Value: "Failed"}},
			V2Checks:  []Check{says(updateID(1)), says("failed")},
		},
		{Name: "cluster-upgrade-deadline", Behaviour: Deadline, Seed: fleet(scripts(never)), Legacy: with("--yes", "--wait-timeout", "300ms"), Checks: []Check{updatesAre(control), worldMoved}},
		{
			Name: "cluster-upgrade-interrupt", Behaviour: Interrupt, Seed: fleet(scripts(never)), Legacy: with("--yes"),
			Signal: interruptWait(1, 2), Checks: []Check{updatesAre(control)},
		},
		{
			// The signal lands in the wait for the first nodegroup roll, the
			// fourth update of the run.
			Name: "cluster-upgrade-interrupt-nodegroup", Seed: fleet(scripts(slow), script("ng-a", never)), Legacy: with("--yes"),
			Signal: interruptWait(4, 2), Checks: []Check{exits(1), updatesAre(control, cni, dns, a), nothingAfterSignal, resourceIs("ng-b", "1.32 ")},
		},
		{
			Name: "cluster-upgrade-lost-response", Behaviour: LostResponse, Seed: fleet(scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSUpdateClusterVersion), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "cluster-upgrade-throttled-read", Behaviour: ThrottledRead, Seed: fleet(scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSListInsights), fleettest.Fault{Throttle: 1}),
		},
		{
			Name: "cluster-upgrade-two-hops", Seed: fleet(scripts(slow)), Legacy: to("1.34")("--yes"),
			Checks: []Check{exits(0), updatesAre(control, cni, dns, a, b, control, cni, dns, a, b), resourceIs("ng-a", "1.34 ")},
		},
		{Name: "cluster-upgrade-only-control-plane", Seed: fleet(scripts(slow)), Legacy: with("--yes", "--only", "control-plane"), Checks: []Check{exits(0), updatesAre(control)}},
		{
			Name: "cluster-upgrade-skip-addon", Seed: fleet(scripts(slow)), Legacy: with("--yes", "--skip", "coredns"),
			Checks: []Check{updatesAre(control, cni, a, b)},
		},
		{
			Name: "cluster-upgrade-skip-nodegroup", Seed: fleet(scripts(slow)), Legacy: with("--yes", "--skip-nodegroup", "ng-b"),
			Checks: []Check{updatesAre(control, cni, dns, a), resourceIs("ng-b", "1.32 ")},
		},
		{
			Name: "cluster-upgrade-one-nodegroup", Seed: fleet(scripts(slow)), Legacy: with("--yes", "-n", "ng-a"),
			Checks: []Check{updatesAre(control, cni, dns, a), resourceIs("ng-b", "1.32 ")},
		},
		{
			Name: "cluster-upgrade-skip-health-check", Seed: fleet(noMetrics, scripts(slow)), Legacy: with("--yes", "--skip-health-check"),
			Checks: []Check{exits(0), whole},
		},
	}
}

func clusterRollback() []Scenario {
	with := args("cluster", "rollback", "-c", "prod", "--poll-interval", pollArg, "-o", "json")
	onTerminal := args("cluster", "rollback", "-c", "prod", "--poll-interval", pollArg)
	const (
		control = "UpdateClusterVersion prod"
		cni     = "UpdateAddon prod/vpc-cni"
		dns     = "UpdateAddon prod/coredns"
		a       = "UpdateNodegroupVersion prod/ng-a"
		b       = "UpdateNodegroupVersion prod/ng-b"
	)
	whole := updatesAre(a, b, cni, dns, control)
	inFlight := func(script fleettest.UpdateScript) func(w *fleettest.World) error {
		return func(w *fleettest.World) error {
			w.Edit(func(s *fleettest.State) { s.Cluster(Region, "prod").UpdateScripts = []fleettest.UpdateScript{script} })
			_, err := w.UpdateClusterVersion(Region, "prod", fleettest.ClusterVersionRequest{Version: new("1.32")})
			return err
		}
	}
	clusterClock := []fleettest.Operation{fleettest.EKSDescribeCluster, fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh}
	return []Scenario{
		{Name: "cluster-rollback-yes", Behaviour: Success, Seed: fleet(upgraded, scripts(slow)), Legacy: with("--yes"), Checks: []Check{whole}},
		{Name: "cluster-rollback-dry-run", Behaviour: DryRun, Seed: fleet(upgraded), Legacy: with("--dry-run"), Checks: []Check{exits(0)}},
		{Name: "cluster-rollback-unattended", Behaviour: Unattended, Seed: fleet(upgraded), Legacy: with()},
		{Name: "cluster-rollback-prompt-yes", Behaviour: PromptYes, Seed: fleet(upgraded, scripts(slow)), Legacy: onTerminal(), Terminal: sayYes, Checks: []Check{exits(0), whole}},
		{Name: "cluster-rollback-prompt-no", Behaviour: PromptNo, Seed: fleet(upgraded), Legacy: onTerminal(), Terminal: sayNo},
		{Name: "cluster-rollback-prompt-interrupt", Behaviour: PromptInterrupt, Seed: fleet(upgraded), Legacy: onTerminal(), Terminal: ctrlC},
		{
			// A cluster with no in-place upgrade in its history cannot roll back.
			Name: "cluster-rollback-blocked", Behaviour: Blocked, Seed: fleet(), Legacy: with("--yes"), Checks: []Check{exits(3)},
		},
		{
			// The upgrade was ten days ago; EKS allows a rollback for seven.
			Name: "cluster-rollback-expired", Seed: fleet(upgradedAt(10 * 24 * time.Hour)), Legacy: with("--yes"), Checks: []Check{exits(3), noMutations},
		},
		{
			// The request carries force and the rollback timeout.
			Name: "cluster-rollback-force-and-timeout", Seed: fleet(upgraded, scripts(slow)),
			Legacy: with("--yes", "--skip-insights-check", "--rollback-timeout", "3h"), Checks: []Check{exits(0), whole},
		},
		{
			Name: "cluster-rollback-failed", Behaviour: Failed, Seed: fleet(upgraded, scripts(slow), script("", failing)), Legacy: with("--yes"),
			Checks: []Check{whole},
		},
		{
			Name: "cluster-rollback-nodegroup-failed", Seed: fleet(upgraded, scripts(slow), script("ng-a", failing)), Legacy: with("--yes"),
			Checks: []Check{fails, updatesAre(a)},
		},
		{
			// The nodegroups and the add-ons are back; the control plane is not.
			Name: "cluster-rollback-resume", Seed: fleet(upgraded, rolledBackNodes, scripts(slow)), Legacy: with("--yes"),
			Checks: []Check{exits(0), updatesAre(control)},
		},
		{
			Name: "cluster-rollback-attach", Behaviour: Attach, Seed: fleet(upgraded, rolledBackNodes), Prepare: inFlight(long), Clock: clusterClock,
			Legacy: with("--yes"),
		},
		{
			// The in-flight rollback fails. The legacy command then starts its own.
			Name: "cluster-rollback-attach-failed", Behaviour: AttachFailed, Seed: fleet(upgraded, rolledBackNodes), Prepare: inFlight(longFailing), Clock: clusterClock,
			Legacy: with("--yes"), Checks: []Check{exits(0), updatesAre(control), anUpdateFailed},
		},
		{Name: "cluster-rollback-deadline", Behaviour: Deadline, Seed: fleet(upgraded, scripts(never)), Legacy: with("--yes", "--wait-timeout", "300ms"), Checks: []Check{updatesAre(a), worldMoved}},
		{
			// The signal lands while the plan is checked again, before any update.
			Name: "cluster-rollback-interrupt-before-start", Seed: fleet(upgraded, scripts(never)), Legacy: with("--yes"),
			Signal: interrupt(fleettest.EKSListInsights, 2), Checks: []Check{exits(1), noMutations},
		},
		{
			Name: "cluster-rollback-interrupt", Behaviour: Interrupt, Seed: fleet(upgraded, scripts(never)), Legacy: with("--yes"),
			Signal: interruptWait(1, 2), Checks: []Check{updatesAre(a)},
		},
		{
			Name: "cluster-rollback-lost-response", Behaviour: LostResponse, Seed: fleet(upgraded, scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSUpdateClusterVersion), fleettest.Fault{LoseResponse: 1}),
		},
		{
			Name: "cluster-rollback-throttled-read", Behaviour: ThrottledRead, Seed: fleet(upgraded, scripts(slow)), Legacy: with("--yes"),
			Prepare: fault(on(fleettest.EKSListUpdates), fleettest.Fault{Throttle: 1}),
		},
	}
}
