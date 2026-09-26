package cluster

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// rollbackWorld is a cluster upgraded in place from 1.33 to 1.34 a day ago:
// nodegroup web still at 1.34, legacy at 1.33, kube-proxy at a 1.34-only
// build, and vpc-cni at a build 1.33 also lists.
func rollbackWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.34",
		Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.34", AmiType: "AL2023_x86_64_STANDARD"}, {Name: "legacy", Version: "1.33", AmiType: "AL2023_x86_64_STANDARD"}},
		Addons: []*fakeaws.Addon{
			{Name: "vpc-cni", Version: "v1.20.0-eksbuild.1", Available: []string{"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}},
			{Name: "kube-proxy", Version: "v1.34.0-eksbuild.1", AvailableFor: map[string][]string{
				"1.34": {"v1.34.0-eksbuild.1"},
				"1.33": {"v1.33.1-eksbuild.1", "v1.33.0-eksbuild.1"},
			}},
		},
		History: []fakeaws.Update{
			{ID: "u-1", Type: "VersionUpdate", Version: "1.33", CreatedAt: time.Now().Add(-30 * 24 * time.Hour)},
			{ID: "u-2", Type: "VersionUpdate", Version: "1.34", CreatedAt: time.Now().Add(-24 * time.Hour)},
		},
	}
}

func TestRollback_DryRunPlan(t *testing.T) {
	srv := fakeaws.New(t, rollbackWorld())
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}
	out := ui.StripANSI(stdout)
	for _, want := range []string{
		"Rollback plan: prod 1.34 → 1.33",
		"rollback available until about",
		"nodegroup web 1.34 → 1.33",
		"addon kube-proxy v1.34.0-eksbuild.1 → v1.33.1-eksbuild.1 (newest compatible with 1.33)",
		"v1.20.0-eksbuild.1 is compatible with 1.33",
		"control plane rollback 1.34 → 1.33",
		"self-managed and hybrid nodes are not rolled back",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	web, kp, cp := strings.Index(out, "nodegroup web"), strings.Index(out, "addon kube-proxy"), strings.Index(out, "control plane rollback")
	if web > kp || kp > cp {
		t.Errorf("plan order wrong, want nodegroups, add-ons, control plane:\n%s", out)
	}
	if got := mutations(srv.Calls()); len(got) != 0 {
		t.Errorf("a dry run changed the cluster: %v", got)
	}
}

func TestRollback_DryRunJSON(t *testing.T) {
	fakeaws.New(t, rollbackWorld())
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}
	plan := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if plan["kind"] != "RollbackPlan" || plan["targetVersion"] != "1.33" || plan["availableUntil"] == nil {
		t.Fatalf("plan = %v", plan)
	}
	var got []string
	for _, s := range plan["steps"].([]any) {
		m := s.(map[string]any)
		e := m["type"].(string)
		if tgt, _ := m["target"].(string); tgt != "" {
			e += " " + tgt
		}
		got = append(got, e+"="+m["status"].(string))
	}
	want := "Readiness=Pending,Readiness=Pending,Nodegroup web=Pending,Nodegroup legacy=Completed,Addon vpc-cni=Completed,Addon kube-proxy=Pending,ControlPlane=Pending"
	if strings.Join(got, ",") != want {
		t.Fatalf("steps = %v\nwant %s", got, want)
	}
}

// A run changes the cluster in the documented order and sends the rollback
// options; a rerun has nothing left to do.
func TestRollback_RunThenRerun(t *testing.T) {
	srv := fakeaws.New(t, rollbackWorld())
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--yes", "--skip-health-check", "--skip-insights-check", "--rollback-timeout", "3h", "--poll-interval", "5ms")
	if err != nil {
		t.Fatalf("rollback: %v\nstderr:\n%s", err, stderr)
	}
	if got := strings.Join(mutations(srv.Calls()), ","); got != "ng web,addon kube-proxy,cp" {
		t.Fatalf("mutations = %s, want ng web,addon kube-proxy,cp", got)
	}
	c := srv.Cluster("prod")
	if c.Version != "1.33" || c.Nodegroups[0].Version != "1.33" || c.Addons[1].Version != "v1.33.1-eksbuild.1" || c.Addons[0].Version != "v1.20.0-eksbuild.1" {
		t.Errorf("after rollback: cp %s, web %s, kube-proxy %s, vpc-cni %s", c.Version, c.Nodegroups[0].Version, c.Addons[1].Version, c.Addons[0].Version)
	}
	if !c.UpdateForce || c.RollbackTimeoutMinutes != 180 {
		t.Errorf("UpdateClusterVersion force=%v timeout=%d, want force and 180", c.UpdateForce, c.RollbackTimeoutMinutes)
	}
	if !strings.Contains(ui.StripANSI(stdout), "Rollback complete: prod is at 1.33") {
		t.Errorf("no outcome line:\n%s", stdout)
	}

	before := len(mutations(srv.Calls()))
	stdout, stderr, err = runCluster(t, "rollback", "prod", "--yes", "--poll-interval", "5ms")
	if err != nil {
		t.Fatalf("rerun: %v\nstderr:\n%s", err, stderr)
	}
	if got := mutations(srv.Calls())[before:]; len(got) != 0 {
		t.Fatalf("rerun mutations = %v, want none", got)
	}
	if !strings.Contains(ui.StripANSI(stdout), "Nothing to do: prod is rolled back to 1.33") {
		t.Errorf("rerun output:\n%s", stdout)
	}
}

// Without --skip-insights-check, EKS is not told to skip its checks, and no
// RollbackConfig is sent.
func TestRollback_RunJSONDefaults(t *testing.T) {
	srv := fakeaws.New(t, rollbackWorld())
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--yes", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("rollback: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	report := doc["report"].(map[string]any)
	if doc["kind"] != "RollbackRun" || report["status"] != "Succeeded" || len(report["completed"].([]any)) != 3 {
		t.Fatalf("document = %v", doc)
	}
	if c := srv.Cluster("prod"); c.UpdateForce || c.RollbackTimeoutMinutes != 0 {
		t.Errorf("force=%v timeout=%d, want neither", c.UpdateForce, c.RollbackTimeoutMinutes)
	}
}

// A rerun after the nodegroup phase continues with the add-ons.
func TestRollback_ResumeAfterNodegroups(t *testing.T) {
	w := rollbackWorld()
	w.Nodegroups[0].Version = "1.33"
	srv := fakeaws.New(t, w)
	if _, stderr, err := runCluster(t, "rollback", "prod", "--yes", "--poll-interval", "5ms", "-o", "json"); err != nil {
		t.Fatalf("rollback: %v\nstderr:\n%s", err, stderr)
	}
	if got := strings.Join(mutations(srv.Calls()), ","); got != "addon kube-proxy,cp" {
		t.Fatalf("mutations = %s, want addon kube-proxy,cp", got)
	}
}

func TestRollback_Blocked(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *fakeaws.Cluster)
		args  []string
		want  string
	}{
		{"window closed", func(c *fakeaws.Cluster) { c.History[1].CreatedAt = time.Now().Add(-8 * 24 * time.Hour) }, nil, "the 7-day rollback window closed"},
		{"created at its version", func(c *fakeaws.Cluster) { c.History = nil }, nil, "a cluster created at its version cannot roll back"},
		{"blocking insight", func(c *fakeaws.Cluster) {
			c.Insights = []*fakeaws.Insight{{ID: "i-1", Name: "API usage", Status: "ERROR", Category: "ROLLBACK_READINESS"}}
		}, nil, "1 blocking insight(s): API usage (ERROR)"},
		{"blocking insight on a real run", func(c *fakeaws.Cluster) {
			c.Insights = []*fakeaws.Insight{{ID: "i-1", Name: "API usage", Status: "UNKNOWN", Category: "ROLLBACK_READINESS"}}
		}, []string{"--yes"}, "API usage (UNKNOWN)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := rollbackWorld()
			tt.setup(w)
			srv := fakeaws.New(t, w)
			args := append([]string{"rollback", "prod"}, tt.args...)
			if len(tt.args) == 0 {
				args = append(args, "--dry-run")
			}
			stdout, stderr, err := runCluster(t, args...)
			if code := runner.ExitCodeOf(err); code != runner.ExitBlocked {
				t.Fatalf("exit %d (%v), want 3\nstderr:\n%s", code, err, stderr)
			}
			if !strings.Contains(ui.StripANSI(stdout), tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, stdout)
			}
			if got := mutations(srv.Calls()); len(got) != 0 {
				t.Errorf("a blocked rollback changed the cluster: %v", got)
			}
		})
	}
}

// --skip-insights-check turns a blocking insight into a notice.
func TestRollback_SkipInsightsCheck(t *testing.T) {
	w := rollbackWorld()
	w.Insights = []*fakeaws.Insight{{ID: "i-1", Name: "API usage", Status: "ERROR", Category: "ROLLBACK_READINESS"}}
	fakeaws.New(t, w)
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--dry-run", "--skip-insights-check")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(ui.StripANSI(stdout), "bypassed with --skip-insights-check") {
		t.Errorf("output:\n%s", stdout)
	}
}

func TestRollback_ExtendedSupportPolicy(t *testing.T) {
	w := rollbackWorld()
	w.SupportType = "STANDARD"
	srv := fakeaws.New(t, w)
	srv.SetVersionStatus("1.33", "EXTENDED_SUPPORT")
	stdout, _, err := runCluster(t, "rollback", "prod", "--dry-run")
	if code := runner.ExitCodeOf(err); code != runner.ExitBlocked || !strings.Contains(ui.StripANSI(stdout), "change it to EXTENDED first") {
		t.Fatalf("exit %d, output:\n%s", code, stdout)
	}
}

func TestRollback_FlagValidation(t *testing.T) {
	fakeaws.New(t, rollbackWorld())
	if _, _, err := runCluster(t, "rollback", "prod", "--dry-run", "--rollback-timeout", "1h"); err == nil || !strings.Contains(err.Error(), "--rollback-timeout") {
		t.Errorf("1h timeout: %v", err)
	}
	if _, _, err := runCluster(t, "rollback", "prod", "-o", "json"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("-o json without --yes: %v", err)
	}
	if _, _, err := runCluster(t, "rollback", "prod", "-o", "plain", "--dry-run"); err == nil {
		t.Errorf("-o plain accepted")
	}
}

// upgrade-check shows the rollback window while it is open.
func TestUpgradeCheck_ShowsRollbackWindow(t *testing.T) {
	fakeaws.New(t, rollbackWorld())
	stdout, stderr, err := runCluster(t, "upgrade-check", "prod", "--exit-zero")
	if err != nil {
		t.Fatalf("upgrade-check: %v\nstderr:\n%s", err, stderr)
	}
	if out := ui.StripANSI(stdout); !strings.Contains(out, "to 1.33 available until about") {
		t.Errorf("no rollback line:\n%s", out)
	}
	stdout, _, _ = runCluster(t, "upgrade-check", "prod", "--exit-zero", "-o", "json")
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	rb, ok := doc["rollback"].(map[string]any)
	if !ok || rb["previousVersion"] != "1.33" {
		t.Errorf("rollback = %v", doc["rollback"])
	}

	w := rollbackWorld()
	w.History = nil
	fakeaws.New(t, w)
	stdout, _, _ = runCluster(t, "upgrade-check", "prod", "--exit-zero", "-o", "json")
	if doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any); doc["rollback"] != nil {
		t.Errorf("rollback without an upgrade: %v", doc["rollback"])
	}
}
