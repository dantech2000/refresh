package change

import (
	"fmt"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// The add-on builds of the engine world, by the Kubernetes version that
// runs them. vpc-cni and coredns run on one minor version only, so a new
// control plane cannot run the installed build. kube-proxy keeps running
// on the next one.
const (
	cni32, cni33, cni34       = "v1.19.0-eksbuild.1", "v1.20.0-eksbuild.1", "v1.21.0-eksbuild.1"
	dns32, dns33, dns34       = "v1.11.4-eksbuild.2", "v1.12.0-eksbuild.1", "v1.13.0-eksbuild.1"
	proxy32, proxy33, proxy34 = "v1.32.0-eksbuild.1", "v1.33.0-eksbuild.1", "v1.34.0-eksbuild.1"
)

// engineWorld returns a world with the cluster prod on Kubernetes 1.32:
// the nodegroups ng-a and ng-b with instances and metrics, so the health
// checks that need no Kubernetes access pass, and three add-ons. EKS
// offers 1.31 to 1.34. Every update takes 30 seconds. edit changes it.
func engineWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		c := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.32", SupportType: "EXTENDED",
			Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: cni32},
				{Name: "coredns", Version: dns32},
				{Name: "kube-proxy", Version: proxy32},
			},
		}
		s.Clusters = append(s.Clusters, c)
		addRollNodegroup(s, c, "ng-a", true)
		addRollNodegroup(s, c, "ng-b", true)
		s.ClusterVersions = []fleettest.ClusterVersion{{Version: "1.34"}, {Version: "1.33"}, {Version: "1.32"}, {Version: "1.31"}}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: cni34, ClusterVersions: []string{"1.34"}},
			{Addon: "vpc-cni", Version: cni33, ClusterVersions: []string{"1.33"}},
			{Addon: "vpc-cni", Version: cni32, ClusterVersions: []string{"1.31", "1.32"}},
			{Addon: "coredns", Version: dns34, ClusterVersions: []string{"1.34"}},
			{Addon: "coredns", Version: dns33, ClusterVersions: []string{"1.33"}},
			{Addon: "coredns", Version: dns32, ClusterVersions: []string{"1.31", "1.32"}},
			{Addon: "kube-proxy", Version: proxy34, ClusterVersions: []string{"1.34"}},
			{Addon: "kube-proxy", Version: proxy33, ClusterVersions: []string{"1.33", "1.34"}},
			{Addon: "kube-proxy", Version: proxy32, ClusterVersions: []string{"1.31", "1.32", "1.33"}},
		}
		s.Quotas = []fleettest.Quota{{ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{40},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		})
		s.DefaultUpdate = fleettest.UpdateScript{After: 30 * time.Second}
		if edit != nil {
			edit(s, c)
		}
	})
	return world
}

// upgradedWorld returns the engine world after an upgrade to 1.33 a day
// ago: everything runs 1.33, and the update is in the history.
func upgradedWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	at := fleettest.New().Now().Add(-24 * time.Hour)
	return engineWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		c.Version = "1.33"
		for _, ng := range c.Nodegroups {
			ng.Version = "1.33"
		}
		c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
		c.Updates = []*fleettest.Update{{
			ID: "11111111-1111-4111-8111-111111111111", Type: fleettest.TypeVersionUpdate,
			Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.33"}}, CreatedAt: at,
		}}
		if edit != nil {
			edit(s, c)
		}
	})
}

// upgradeTo returns an upgrade of prod to a version, with --yes.
func upgradeTo(version string) UpgradeRequest {
	v, err := fleet.ParseVersion(version)
	if err != nil {
		panic(err)
	}
	return UpgradeRequest{
		Common: Common{Policy: health.Policy{Yes: true}, Limits: Limits{Poll: 15 * time.Second}},
		Target: Target{Cluster: "prod"},
		To:     v,
	}
}

// rollbackOf returns a rollback of prod, with --yes.
func rollbackOf() RollbackRequest {
	return RollbackRequest{
		Common: Common{Policy: health.Policy{Yes: true}, Limits: Limits{Poll: 15 * time.Second}},
		Target: Target{Cluster: "prod"},
	}
}

// sent returns the mutating requests that the fake got, in order, each as
// the operation, its target, and the fields of its payload that a plan
// shows: "eks:UpdateAddon vpc-cni v1.20.0-eksbuild.1".
func (e *env) sent() []string {
	var out []string
	for _, r := range e.srv.Requests() {
		if !r.Mutates {
			continue
		}
		line := string(r.Operation)
		for _, key := range []string{"nodegroupName", "addonName", "version", "addonVersion"} {
			if v, ok := r.Input[key]; ok {
				line += " " + fmt.Sprint(v)
			}
		}
		if force, _ := r.Input["force"].(bool); force {
			line += " force"
		}
		if cfg, ok := r.Input["rollbackConfig"].(map[string]any); ok {
			line += fmt.Sprintf(" timeout=%v", cfg["timeoutMinutes"])
		}
		out = append(out, line)
	}
	return out
}

// The mutating requests of the engine tests.
const (
	sentRefresh = "eks:StartInsightsRefresh"
	sentCP33    = "eks:UpdateClusterVersion 1.33"
	sentCNI33   = "eks:UpdateAddon vpc-cni " + cni33
	sentDNS33   = "eks:UpdateAddon coredns " + dns33
	sentProxy33 = "eks:UpdateAddon kube-proxy " + proxy33
	sentA33     = "eks:UpdateNodegroupVersion ng-a 1.33"
	sentB33     = "eks:UpdateNodegroupVersion ng-b 1.33"
)

// wholeUpgrade is every request of the upgrade of the engine world to
// 1.33, in order: the control plane, the add-ons that 1.33 cannot run as
// they are, the nodegroups, and the add-on that it can.
func wholeUpgrade() []string {
	return []string{sentRefresh, sentCP33, sentCNI33, sentDNS33, sentA33, sentB33, sentProxy33}
}

// stepStatuses returns the status of each step by its ref, as text.
func stepStatuses(res Result) map[string]StepStatus {
	out := map[string]StepStatus{}
	for _, c := range res.Clusters {
		for _, s := range c.Steps {
			out[s.Ref.String()] = s.Status
		}
	}
	return out
}

// mustRun runs a change and fails the test for an error of Run.
func mustRun(t *testing.T, e *env, req Request, h Hooks) Result {
	t.Helper()
	res, err := Run(testContext(t), e.deps, req, h)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}
