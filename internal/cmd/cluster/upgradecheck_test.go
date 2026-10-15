package cluster_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// newCheckFake returns a fake AWS with the cluster of the upgrade-check
// golden files: prod on 1.32, one nodegroup at the skew limit, one add-on
// behind, an ERROR and a WARNING insight, and a rollback window. edit
// changes the seed.
func newCheckFake(t *testing.T, edit func(s *fleettest.State, c *fleettest.Cluster)) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		c := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.32", SupportType: "STANDARD",
			Nodegroups: []*fleettest.Nodegroup{
				{Name: "ng-a", Version: "1.32"},
				{Name: "ng-legacy", Version: "1.29"},
			},
			Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"},
				{Name: "coredns", Version: "v1.11.3-eksbuild.1"},
			},
			Insights: []fleettest.Insight{
				{ID: "bc8b2f86-1111-4a4a-8b8b-000000000001", Name: "Deprecated APIs removed in 1.33", Status: fleettest.InsightError, Reason: "Deprecated APIs are in use",
					Description: "Checks for usage of deprecated APIs", Recommendation: "Update the manifests and the clients",
					Resources: []fleettest.InsightResource{{URI: "/apis/policy/v1beta1/poddisruptionbudgets", Status: fleettest.InsightError, Reason: "in use"}},
					Deprecations: []fleettest.Deprecation{{
						Usage: "/apis/policy/v1beta1/poddisruptionbudgets", ReplacedWith: "/apis/policy/v1/poddisruptionbudgets",
						StopServingVersion: "1.25", StartServingReplacementVersion: "1.21",
						Clients: []fleettest.ClientStat{
							{UserAgent: "kubectl/v1.24", Requests: 3, LastRequest: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
							{UserAgent: "old-controller/v2", Requests: 120, LastRequest: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)},
						},
					}},
				},
				{ID: "1a9f0c21-2222-4a4a-8b8b-000000000002", Name: "Kubelet version skew", Status: fleettest.InsightWarning, Reason: "A nodegroup is behind"},
				{ID: "7d3e5a10-3333-4a4a-8b8b-000000000003", Name: "EKS add-on version compatibility", Status: fleettest.InsightPassing},
			},
			Updates: []*fleettest.Update{
				{ID: "u-1", Type: fleettest.TypeVersionUpdate, Status: fleettest.UpdateSuccessful, Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.32"}},
					CreatedAt: cmdutiltest.Now.Add(-2 * 24 * time.Hour)},
			},
		}
		s.Clusters = []*fleettest.Cluster{c}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.32"}},
			{Addon: "coredns", Version: "v1.11.4-eksbuild.2", ClusterVersions: []string{"1.32"}},
			{Addon: "coredns", Version: "v1.11.3-eksbuild.1", ClusterVersions: []string{"1.32"}},
		}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.32", EndOfStandardSupport: time.Date(2027, 3, 23, 0, 0, 0, 0, time.UTC), EndOfExtendedSupport: time.Date(2028, 3, 23, 0, 0, 0, 0, time.UTC)},
		}
		s.Metrics = controlPlaneMetrics("prod", 1<<30, 48210, 5, 0)
		if edit != nil {
			edit(s, c)
		}
	})
	return cmdtest.NewFake(t, world, east)
}

// ready makes the cluster of newCheckFake ready: every insight passing,
// every nodegroup current, every add-on at its latest, and no rollback.
func ready(_ *fleettest.State, c *fleettest.Cluster) {
	c.Nodegroups[1].Version = "1.32"
	c.Addons[1].Version = "v1.11.4-eksbuild.2"
	for i := range c.Insights {
		c.Insights[i].Status = fleettest.InsightPassing
	}
	c.Updates = nil
}

func TestUpgradeCheck_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "upgrade-check.txt"}, {"plain", "upgrade-check.tsv"}, {"json", "upgrade-check.json"}, {"yaml", "upgrade-check.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newCheckFake(t, nil)
			err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "-o", tt.format)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitBlocked {
				t.Errorf("exit code = %d (%v), want 3", got, err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindUpgradeCheck, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindUpgradeCheck, e.Out.Bytes())
			}
		})
	}
}

// The verdicts and their exit codes.
func TestUpgradeCheck_Verdicts(t *testing.T) {
	tests := []struct {
		name string
		edit func(s *fleettest.State, c *fleettest.Cluster)
		args []string
		file string
		code int
		want string // a part of stdout
	}{
		{name: "ready", edit: ready, file: "upgrade-check-ready.txt", code: 0, want: "[OK] READY"},
		{name: "review", edit: func(s *fleettest.State, c *fleettest.Cluster) {
			ready(s, c)
			c.Insights[1].Status = fleettest.InsightWarning
		}, file: "upgrade-check-review.txt", code: 2, want: "[!] REVIEW"},
		{name: "review for an add-on behind", edit: func(s *fleettest.State, c *fleettest.Cluster) {
			ready(s, c)
			c.Addons[1].Version = "v1.11.3-eksbuild.1"
		}, code: 2, want: "VersionSkew: 1 add-on behind latest"},
		{name: "review for extended support", edit: func(s *fleettest.State, c *fleettest.Cluster) {
			ready(s, c)
			c.SupportType = "EXTENDED"
			s.ClusterVersions[0].EndOfStandardSupport = cmdutiltest.Now.Add(-24 * time.Hour)
		}, code: 2, want: "Support: the cluster is in extended support"},
		{name: "blocked by an UNKNOWN insight", edit: func(s *fleettest.State, c *fleettest.Cluster) {
			ready(s, c)
			c.Insights[0].Status = fleettest.InsightUnknown
		}, code: 3, want: "1 UNKNOWN insight that EKS could not evaluate"},
		{name: "not evaluated", edit: func(s *fleettest.State, c *fleettest.Cluster) {
			ready(s, c)
			c.Insights = []fleettest.Insight{}
		}, file: "upgrade-check-not-evaluated.txt", code: 2, want: "EKS has not evaluated the upgrade insights"},
		{name: "status filters what is shown, not what is judged", args: []string{"--status", "WARNING"}, code: 3, want: "1a9f0c21"},
		{name: "status PASSING shows the passing insights", args: []string{"--status", "passing"}, code: 3, want: "7d3e5a10"},
		{name: "show-passing", edit: ready, args: []string{"--show-passing"}, code: 0, want: "7d3e5a10"},
		{name: "exit-zero", args: []string{"--exit-zero"}, code: 0, want: "[X] NOT READY"},
		{name: "another category", edit: ready, args: []string{"--category", "misconfiguration"}, code: 0, want: "[OK] READY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCheckFake(t, tt.edit)
			err := e.Run(cmdutiltest.Context(t), append([]string{"cluster", "upgrade-check", "prod"}, tt.args...)...)
			if got := cmdutil.ExitCode(err); got != tt.code {
				t.Errorf("exit code = %d (%v), want %d", got, err, tt.code)
			}
			if !strings.Contains(e.Out.String(), tt.want) {
				t.Errorf("stdout has no %q:\n%s", tt.want, e.Out.String())
			}
			if tt.file != "" {
				cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			}
		})
	}
}

// An unread nodegroup makes the check incomplete, exit 4, and a blocker
// beside it still exits 4: the highest code wins.
func TestUpgradeCheck_Incomplete(t *testing.T) {
	e := newCheckFake(t, ready)
	e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-legacy"}, fleettest.Fault{Deny: true})
	err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want 4", got, err)
	}
	cmdutiltest.Golden(t, "upgrade-check-incomplete.json", e.Out.String(), *update)
	cmdutiltest.ValidateJSON(t, apidoc.KindUpgradeCheck, e.Out.Bytes())
	if !strings.Contains(e.Err.String(), "warning: nodegroup prod/ng-legacy") {
		t.Errorf("stderr = %q, want the failure", e.Err.String())
	}

	e = newCheckFake(t, nil)
	e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-a"}, fleettest.Fault{Deny: true})
	err = e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want 4", got, err)
	}
	if !strings.Contains(e.Out.String(), "[X] NOT READY") {
		t.Errorf("stdout should still read NOT READY:\n%s", e.Out.String())
	}
	if !strings.HasPrefix(err.Error(), "upgrade blocked: Insights: 1 ERROR insight") || !strings.Contains(err.Error(), "; incomplete data: ") {
		t.Errorf("error = %q, want the blocker and the failures", err)
	}

	// The insights cannot be read: the check is unreadable, exit 4, and
	// unread is not "not evaluated".
	e = newCheckFake(t, ready)
	e.World.SetFault(fleettest.EKSListInsights, fleettest.Fault{Deny: true})
	err = e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "-o", "plain")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want 4", got, err)
	}
	for _, want := range []string{"checks.Insights.outcome\tUnreadable\n", "insights.evaluated\tunreadable; the insights could not be read\n"} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("stdout has no %q:\n%s", want, e.Out.String())
		}
	}
	if !strings.Contains(err.Error(), "upgrade check incomplete: Insights:") || !strings.Contains(err.Error(), "incomplete data: 1 failure") {
		t.Errorf("error = %q, want the verdict and the failures", err)
	}
	e = newCheckFake(t, ready)
	e.World.SetFault(fleettest.EKSListInsights, fleettest.Fault{Deny: true})
	_ = e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "-o", "json")
	if strings.Contains(e.Out.String(), "insightsEvaluated") {
		t.Errorf("the key must be left out when the insights were not read:\n%s", e.Out.String())
	}

	// The update history is an extra: without it there is no rollback
	// line, and no failure.
	e = newCheckFake(t, ready)
	e.World.SetFault(fleettest.EKSListUpdates, fleettest.Fault{Deny: true})
	if err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod"); err != nil {
		t.Errorf("run: %v", err)
	}
}

// Two round trips, three with a rollback window, and no call lists
// clusters.
func TestUpgradeCheck_Waves(t *testing.T) {
	e := newCheckFake(t, nil)
	gate := awsxtest.NewGate(t, e.Server.Handler)
	done := make(chan error, 1)
	go func() { done <- e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "-o", "json") }()
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups, fleettest.EKSListAddons, fleettest.EKSDescribeClusterVersions,
		fleettest.EKSListInsights, fleettest.EKSListUpdates, fleettest.CloudWatchGetMetricData)
	gate.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon,
		fleettest.EKSDescribeAddonVersions, fleettest.EKSDescribeAddonVersions, fleettest.EKSDescribeUpdate)
	gate.Wave(fleettest.EKSListInsights)
	gate.Done()
	if err := <-done; cmdutil.ExitCode(err) != cmdutil.ExitBlocked {
		t.Fatalf("run: %v", err)
	}
	if n := e.Count(fleettest.EKSListClusters); n != 0 {
		t.Errorf("ListClusters calls = %d, want none", n)
	}
}

func TestUpgradeCheck_Insight(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "insight.txt"}, {"plain", "insight.tsv"}, {"json", "insight.json"}, {"yaml", "insight.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newCheckFake(t, nil)
			err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--insight", "bc8b2f86", "-o", tt.format)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitBlocked {
				t.Errorf("exit code = %d (%v), want 3", got, err)
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindInsightDescription, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindInsightDescription, e.Out.Bytes())
			}
		})
	}
	t.Run("a full ID, a warning, and exit-zero", func(t *testing.T) {
		e := newCheckFake(t, nil)
		err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--insight", "1a9f0c21-2222-4a4a-8b8b-000000000002", "-o", "plain")
		if got := cmdutil.ExitCode(err); got != cmdutil.ExitNeedsAttention {
			t.Errorf("exit code = %d (%v), want 2", got, err)
		}
		e = newCheckFake(t, nil)
		if err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--insight", "7d3e5a10", "--exit-zero"); err != nil {
			t.Errorf("a passing insight: %v", err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		for _, tt := range []struct{ insight, want string }{
			{"bc8b", "too short"},
			{"ffffffff", "no UPGRADE_READINESS insight of cluster prod has the ID"},
		} {
			e := newCheckFake(t, nil)
			err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--insight", tt.insight)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("--insight %s: error = %v, want %q", tt.insight, err, tt.want)
			}
		}
	})
}

func TestUpgradeCheck_InvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"--category", "nope"},
		{"--status", "maybe"},
		{"a", "b"},
	} {
		e := newCheckFake(t, nil)
		err := e.Run(cmdutiltest.Context(t), append([]string{"cluster", "upgrade-check", "prod"}, args...)...)
		if err == nil || len(e.Server.Requests()) != 0 {
			t.Errorf("%v: error = %v, requests = %v; want an error before any request", args, err, e.Operations())
		}
	}
}
