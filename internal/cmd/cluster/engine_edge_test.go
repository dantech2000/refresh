package cluster_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// never makes every update of the world run without an end.
func never(s *fleettest.State, _ *fleettest.Cluster) {
	s.DefaultUpdate = fleettest.UpdateScript{Never: true}
}

// thirdPoll accepts the third poll of an update: the wait is under way.
// The server calls it from one goroutine for each request.
func thirdPoll() func(r awsxtest.Request) bool {
	var polls atomic.Int32
	return func(r awsxtest.Request) bool {
		if r.Operation != fleettest.EKSDescribeUpdate {
			return false
		}
		return polls.Add(1) == 3
	}
}

// lose makes the fake accept the first request of op and lose its answer.
func lose(op fleettest.Operation) func(t *testing.T, w *fleettest.World) {
	return func(_ *testing.T, w *fleettest.World) { w.SetFault(op, fleettest.Fault{LoseResponse: 1}) }
}

// rolling starts a roll of a nodegroup of prod to a Kubernetes version,
// as a run before this one did: the nodegroup is UPDATING when the
// command starts, and its update ends 30 seconds later.
func rolling(nodegroup, version string) func(t *testing.T, w *fleettest.World) {
	return func(t *testing.T, w *fleettest.World) {
		t.Helper()
		if _, err := w.UpdateNodegroupVersion(east, "prod", nodegroup, fleettest.NodegroupVersionRequest{Version: &version}); err != nil {
			t.Fatal(err)
		}
	}
}

// at puts the parts of prod on versions, as an upgrade or a rollback that
// stopped part way left them. An empty value keeps what the world has.
func at(controlPlane, cni, proxy, web, api string) func(s *fleettest.State, c *fleettest.Cluster) {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		set := func(p *string, v string) {
			if v != "" {
				*p = v
			}
		}
		set(&c.Version, controlPlane)
		set(&c.Addon("vpc-cni").Version, cni)
		set(&c.Addon("kube-proxy").Version, proxy)
		set(&c.Nodegroup("web").Version, web)
		set(&c.Nodegroup("api").Version, api)
	}
}

// upgradeEdgeCases are the runs of "cluster upgrade" that end, or begin,
// in the middle of a change: an interrupt during a wait, an answer that
// is lost after EKS committed the update, an update that EKS accepted
// with no ID, and a second run after each phase of a first one, with a
// roll of the nodegroup api still under way.
func upgradeEdgeCases() []engineCase {
	return []engineCase{
		{name: "interrupt", args: []string{"--yes"}, exit: cmdutil.ExitFailure, seed: never, interruptAt: thirdPoll(), sent: []string{refresh, cp33}},
		{name: "lost-response", args: []string{"--yes"}, prepare: lose(fleettest.EKSUpdateClusterVersion),
			sent: []string{refresh, cp33, cp33, cniTo33, api33, web33, proxyTo3}},
		{name: "no-update-id", args: []string{"--yes"}, exit: cmdutil.ExitIncomplete, emptyAnswer: "/clusters/prod/updates", sent: []string{refresh}},
		// The control plane is at 1.33, and nothing else is.
		{name: "resume-after-control-plane", args: []string{"--yes"}, seed: at("1.33", "", "", "", ""), prepare: rolling("api", "1.33"),
			sent: []string{cniTo33, web33, proxyTo3}},
		// The add-on that 1.33 needs is updated, and the first roll runs.
		{name: "resume-after-addons-before", args: []string{"--yes"}, seed: at("1.33", cni33, "", "", ""), prepare: rolling("api", "1.33"),
			sent: []string{web33, proxyTo3}},
		// web is rolled, and the roll of api runs. Only kube-proxy is left.
		{name: "resume-after-nodegroups", args: []string{"--yes"}, seed: at("1.33", cni33, "", "1.33", ""), prepare: rolling("api", "1.33"),
			sent: []string{proxyTo3}},
	}
}

// The requests of the rollback of the engine world to 1.32.
const (
	api32     = "UpdateNodegroupVersion api 1.32"
	web32     = "UpdateNodegroupVersion web 1.32"
	cniTo32   = "UpdateAddon vpc-cni " + cni32
	proxyTo32 = "UpdateAddon kube-proxy " + proxy32
	cp32      = "UpdateClusterVersion 1.32"
)

// rollbackEdgeCases are upgradeEdgeCases for "cluster rollback". Its
// phases are the nodegroups, the add-ons, and then the control plane.
func rollbackEdgeCases() []engineCase {
	return []engineCase{
		{name: "interrupt", args: []string{"--yes"}, exit: cmdutil.ExitFailure, seed: never, interruptAt: thirdPoll(), sent: []string{api32}},
		{name: "lost-response", args: []string{"--yes"}, prepare: lose(fleettest.EKSUpdateClusterVersion),
			sent: []string{api32, web32, cniTo32, proxyTo32, cp32, cp32}},
		{name: "no-update-id", args: []string{"--yes"}, exit: cmdutil.ExitIncomplete, emptyAnswer: "/node-groups/api/update-version"},
		// The first roll of the first run still runs.
		{name: "resume-in-nodegroups", args: []string{"--yes"}, prepare: rolling("api", "1.32"), sent: []string{web32, cniTo32, proxyTo32, cp32}},
		// web is back on 1.32, and the roll of api runs.
		{name: "resume-after-nodegroups", args: []string{"--yes"}, seed: at("", "", "", "1.32", ""), prepare: rolling("api", "1.32"),
			sent: []string{cniTo32, proxyTo32, cp32}},
		// The add-ons are back too. Only the control plane is left.
		{name: "resume-after-addons", args: []string{"--yes"}, seed: at("", cni32, proxy32, "1.32", ""), prepare: rolling("api", "1.32"),
			sent: []string{cp32}},
	}
}

// TestUpgrade_Edges and TestRollback_Edges compare the exit code, stdout,
// and stderr of each case with a golden file, and hold the world to what
// the case means.
func TestUpgrade_Edges(t *testing.T) {
	for _, tc := range upgradeEdgeCases() {
		t.Run(tc.name, func(t *testing.T) {
			e, got := runEngine(t, []string{"cluster", "upgrade", "prod", "--to", "1.33"}, engineWorld("1.32", false, tc.seed), tc)
			cmdutiltest.Golden(t, "upgrade-"+tc.name+".txt", got, *update)
			checkEdge(t, e, tc, "1.33")
		})
	}
}

func TestRollback_Edges(t *testing.T) {
	for _, tc := range rollbackEdgeCases() {
		t.Run(tc.name, func(t *testing.T) {
			e, got := runEngine(t, []string{"cluster", "rollback", "prod"}, engineWorld("1.33", true, tc.seed), tc)
			cmdutiltest.Golden(t, "rollback-"+tc.name+".txt", got, *update)
			checkEdge(t, e, tc, "1.32")
		})
	}
}

// checkEdge holds the world after an edge case to what the case means. A
// run that succeeded left every part of the cluster at the goal and
// ACTIVE. A lost answer started one control-plane update, and its retry
// carried the token of the first request.
func checkEdge(t *testing.T, e *cmdtest.Fake, tc engineCase, goal string) {
	t.Helper()
	if tc.exit == cmdutil.ExitOK {
		e.World.View(func(s *fleettest.State) {
			c := s.Cluster(east, "prod")
			if c.Version != goal || c.Status != fleettest.StatusActive {
				t.Errorf("the control plane is %s at %s, want ACTIVE at %s", c.Status, c.Version, goal)
			}
			for _, ng := range c.Nodegroups {
				if ng.Version != goal || ng.Status != fleettest.StatusActive {
					t.Errorf("nodegroup %s is %s at %s, want ACTIVE at %s", ng.Name, ng.Status, ng.Version, goal)
				}
			}
		})
	}
	if tc.name != "lost-response" {
		return
	}
	var tokens []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateClusterVersion {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
	e.World.View(func(s *fleettest.State) {
		started := 0
		for _, u := range s.Cluster(east, "prod").Updates {
			// The seed of a rollback has the update of the upgrade.
			if u.Nodegroup == "" && u.Addon == "" && u.ID != "11111111-1111-4111-8111-111111111111" {
				started++
			}
		}
		if started != 1 {
			t.Errorf("%d control-plane updates in the world, want 1", started)
		}
	})
}
