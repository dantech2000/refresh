package cluster_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// endsDegraded makes the next roll of a nodegroup succeed and leave the
// nodegroup DEGRADED, which the check after the roll reports.
func endsDegraded(name string) func(*fleettest.State, *fleettest.Cluster) {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup(name).UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, ResourceStatus: fleettest.StatusDegraded}}
	}
}

// An issue that the check after a roll finds ends "cluster upgrade" and
// "cluster rollback" with exit code 5. The next nodegroup is not rolled,
// and a rollback does not move the add-ons or the control plane.
// --skip-verify leaves the check out, and the run goes on.
func TestEngineCommands_PostRollVerification(t *testing.T) {
	upgrade, rollback := []string{"cluster", "upgrade", "prod", "--to", "1.33"}, []string{"cluster", "rollback", "prod"}
	tests := []struct {
		command []string
		version string
		tc      engineCase
		want    string
	}{
		{upgrade, "1.32", engineCase{name: "upgrade", args: []string{"--yes"}, exit: cmdutil.ExitVerifyFailed, sent: []string{refresh, cp33, cniTo33, api33}}, "api is DEGRADED"},
		{rollback, "1.33", engineCase{name: "rollback", args: []string{"--yes"}, exit: cmdutil.ExitVerifyFailed, sent: []string{api32}}, "api is DEGRADED"},
		{upgrade, "1.32", engineCase{name: "upgrade with --skip-verify", args: []string{"--yes", "--skip-verify"},
			sent: []string{refresh, cp33, cniTo33, api33, web33, proxyTo3}}, ""},
		{rollback, "1.33", engineCase{name: "rollback with --skip-verify", args: []string{"--yes", "--skip-verify"},
			sent: []string{api32, web32, cniTo32, proxyTo32, cp32}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.tc.name, func(t *testing.T) {
			_, got := runEngine(t, tt.command, engineWorld(tt.version, tt.version == "1.33", endsDegraded("api")), tt.tc)
			if !strings.Contains(got, tt.want) {
				t.Errorf("the output has no %q:\n%s", tt.want, got)
			}
		})
	}
}

// A refresh of the insights that EKS refuses blocks "cluster upgrade"
// before any change, with exit code 4: the insights could not be read.
func TestUpgrade_InsightsRefreshRefused(t *testing.T) {
	tests := []struct {
		name  string
		fault fleettest.Fault
		op    fleettest.Operation
		want  []string
	}{
		{"the start is denied", fleettest.Fault{Deny: true}, fleettest.EKSStartInsightsRefresh,
			[]string{"could not refresh cluster insights for 1.33", "eks:StartInsightsRefresh", "--skip-insights-check"}},
		{"the cluster is in a state that refuses it", fleettest.Fault{Code: "InvalidRequestException"}, fleettest.EKSStartInsightsRefresh,
			[]string{"could not refresh cluster insights for 1.33", "InvalidRequestException"}},
		{"the poll is denied", fleettest.Fault{Deny: true}, fleettest.EKSDescribeInsightsRefresh,
			[]string{"could not refresh cluster insights for 1.33", "eks:DescribeInsightsRefresh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := cmdtest.NewFake(t, engineWorld("1.32", false, nil), east)
			e.UseWorldClock()
			e.World.SetFault(tt.op, tt.fault)
			err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade", "prod", "--to", "1.33", "--yes")
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want 4", got, err)
			}
			// The refresh is the one request that a blocked run sends.
			for _, r := range sentBy(e) {
				if r != refresh {
					t.Errorf("%s was sent by a run that the refresh blocked", r)
				}
			}
			got := e.Out.String() + e.Err.String()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("the output has no %q:\n%s", w, got)
				}
			}
			if n := e.Count(fleettest.EKSListInsights); n != 0 {
				t.Errorf("%d reads of the insights after a refresh that did not complete, want none", n)
			}
		})
	}
}

// withKubeconfigOf points KUBECONFIG at a file whose current context is
// the cluster prod of the world, as an EKS context.
func withKubeconfigOf(t *testing.T, world *fleettest.World) {
	t.Helper()
	srv := kubetest.NewServer(world, east, "prod")
	t.Cleanup(srv.Close)
	// An EKS context, as "aws eks update-kubeconfig" writes it: the cluster
	// entry is the ARN of prod. The fallback of a read command counts no
	// other kind of context.
	const arn = "arn:aws:eks:" + east + ":123456789012:cluster/prod"
	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: " + arn + "\n" +
		"clusters:\n- name: " + arn + "\n  cluster: {server: " + srv.URL + "}\n" +
		"contexts:\n- name: " + arn + "\n  context: {cluster: \"" + arn + "\", user: prod}\n" +
		"users:\n- name: prod\n  user: {token: fake-token}\n"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
}

// A command that changes a cluster never takes the cluster from the
// current kubeconfig context, with --yes or with --dry-run: a script with
// an empty cluster variable on a machine whose kubeconfig names prod must
// fail. A read command does take it.
func TestEngineCommands_NoClusterFromKubeconfig(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "upgrade", "--to", "1.33", "--yes"},
		{"cluster", "upgrade", "--to", "1.33", "--dry-run"},
		{"cluster", "rollback", "--yes"},
		{"cluster", "rollback", "--dry-run"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			e := cmdtest.NewFake(t, engineWorld("1.33", true, nil), east)
			withKubeconfigOf(t, e.World)
			err := e.Run(cmdutiltest.Context(t), args...)
			if err == nil || !strings.Contains(err.Error(), "no cluster specified") {
				t.Errorf("error = %v, want the no-cluster error", err)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("%d requests, want none", n)
			}
		})
	}
	t.Run("a read command takes it", func(t *testing.T) {
		e := cmdtest.NewFake(t, engineWorld("1.33", true, nil), east)
		withKubeconfigOf(t, e.World)
		if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "-o", "json"); err != nil {
			t.Fatalf("cluster describe: %v", err)
		}
		if !strings.Contains(e.Out.String(), `"name": "prod"`) {
			t.Errorf("the document is not of prod:\n%s", e.Out)
		}
	})
}

// REFRESH_CLUSTER never beats the cluster that the command line names.
func TestClusterCommands_PositionalBeatsTheVariable(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "describe", "prod", "-o", "json"},
		{"cluster", "upgrade-check", "prod", "-o", "json", "--exit-zero"},
		{"cluster", "upgrade", "prod", "--to", "1.33", "--dry-run", "-o", "json"},
		{"cluster", "rollback", "prod", "--dry-run", "-o", "json"},
	} {
		t.Run(args[1], func(t *testing.T) {
			version, upgraded := "1.32", false
			if args[1] == "rollback" {
				version, upgraded = "1.33", true
			}
			e := cmdtest.NewFake(t, engineWorld(version, upgraded, nil), east)
			t.Setenv(cmdutil.EnvCluster, "other")
			err := e.Run(cmdutiltest.Context(t), args...)
			if err != nil && strings.Contains(err.Error(), "other") {
				t.Fatalf("the run looked for the cluster of the variable: %v", err)
			}
			if !strings.Contains(e.Out.String(), `"prod"`) || strings.Contains(e.Out.String(), `"other"`) {
				t.Errorf("the document is not of prod (error %v):\n%s", err, e.Out)
			}
			for _, r := range e.Server.Requests() {
				if name, ok := r.Input["name"]; ok && name != "prod" {
					t.Errorf("%s asked for the cluster %v", r.Operation, name)
				}
			}
		})
	}
}

// A leaf command has no help subcommand: "h" and "help" are the cluster
// to act on, so the command looks for a cluster of that name and does not
// print help and exit 0.
func TestLeafTreatsHelpAsACluster(t *testing.T) {
	for _, word := range []string{"h", "help"} {
		for _, command := range []string{"upgrade-check", "describe"} {
			t.Run(command+" "+word, func(t *testing.T) {
				e := newCheckFake(t, nil)
				err := e.Run(cmdutiltest.Context(t), "cluster", command, word)
				if err == nil || !strings.Contains(err.Error(), `"`+word+`"`) {
					t.Errorf("error = %v, want one about the cluster %q", err, word)
				}
				if strings.Contains(e.Out.String(), "Usage:") {
					t.Errorf("the command printed its help:\n%s", e.Out)
				}
			})
		}
	}
}

// --insight looks in the category of --category: an insight that
// --category MISCONFIGURATION lists opens with the same category, and the
// default category does not find it.
func TestUpgradeCheck_InsightUsesTheCategory(t *testing.T) {
	misconfigured := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Insights = append(c.Insights, fleettest.Insight{
			ID: "c0ffee00-4444-4a4a-8b8b-000000000004", Name: "EKS upgrade policy", Category: "MISCONFIGURATION", Status: fleettest.InsightWarning, Reason: "Extended support is on",
		})
	}
	t.Run("listed", func(t *testing.T) {
		e := newCheckFake(t, misconfigured)
		err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--category", "misconfiguration", "-o", "plain")
		if got := cmdutil.ExitCode(err); got != cmdutil.ExitNeedsAttention && got != cmdutil.ExitBlocked {
			t.Errorf("exit code = %d (%v)", got, err)
		}
		if !strings.Contains(e.Out.String(), "c0ffee00") || strings.Contains(e.Out.String(), "bc8b2f86") {
			t.Errorf("want the insight of the category alone:\n%s", e.Out)
		}
	})
	t.Run("opened with the same category", func(t *testing.T) {
		for _, id := range []string{"c0ffee00", "c0ffee00-4444-4a4a-8b8b-000000000004"} {
			e := newCheckFake(t, misconfigured)
			err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--category", "MISCONFIGURATION", "--insight", id, "-o", "plain")
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitNeedsAttention {
				t.Errorf("--insight %s: exit code = %d (%v), want 2", id, got, err)
			}
			if !strings.Contains(e.Out.String(), "c0ffee00-4444") || !strings.Contains(e.Out.String(), "MISCONFIGURATION") {
				t.Errorf("--insight %s: the document is not of the insight:\n%s", id, e.Out)
			}
		}
	})
	t.Run("not found in the default category", func(t *testing.T) {
		e := newCheckFake(t, misconfigured)
		err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--insight", "c0ffee00")
		if err == nil || !strings.Contains(err.Error(), "no UPGRADE_READINESS insight of cluster prod has the ID") {
			t.Errorf("error = %v, want that no UPGRADE_READINESS insight has the ID", err)
		}
	})
	t.Run("not found in another category", func(t *testing.T) {
		e := newCheckFake(t, misconfigured)
		err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade-check", "prod", "--category", "misconfiguration", "--insight", "bc8b2f86")
		if err == nil || !strings.Contains(err.Error(), "no MISCONFIGURATION insight of cluster prod has the ID") {
			t.Errorf("error = %v, want that no MISCONFIGURATION insight has the ID", err)
		}
	})
}
