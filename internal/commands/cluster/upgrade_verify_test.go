package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// degradedAfterRoll is a cluster at 1.32 whose nodegroup web is behind and
// is DEGRADED once its roll is applied. api rolls after web.
func degradedAfterRoll() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.31", StatusAfterUpdate: "DEGRADED"},
		{Name: "api", Version: "1.31"},
	}}
}

// `cluster upgrade` verifies each nodegroup after its roll, as `nodegroup
// update` does. Issues stop the run and exit 5.
func TestUpgrade_PostRollVerificationIssuesExitFive(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		srv := fakeaws.New(t, degradedAfterRoll())
		stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitVerifyFailed {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
		if !strings.Contains(err.Error(), "nodegroup web status is DEGRADED") || !strings.Contains(err.Error(), "resume with: refresh cluster upgrade") {
			t.Errorf("err = %v, want the issue and the resume command", err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		report := doc["report"].(map[string]any)
		if report["status"] != "VerifyFailed" || report["failure"] != nil {
			t.Errorf("report = %v, want VerifyFailed with no failure", report)
		}
		v, _ := report["verification"].(map[string]any)
		issues, _ := v["issues"].([]any)
		if len(issues) != 1 || !strings.Contains(issues[0].(string), "nodegroup web status is DEGRADED") {
			t.Errorf("report.verification = %v, want the DEGRADED issue", report["verification"])
		}
		if fs, _ := doc["failures"].([]any); len(fs) != 0 {
			t.Errorf("failures = %v, want none: an issue is not a failed read", doc["failures"])
		}
		c := srv.Cluster("prod")
		if c.Nodegroups[0].Version != "1.32" || c.Nodegroups[1].Version != "1.31" {
			t.Errorf("nodegroups = %s, %s; want web rolled and api not attempted", c.Nodegroups[0].Version, c.Nodegroups[1].Version)
		}
	})
	t.Run("table", func(t *testing.T) {
		fakeaws.New(t, degradedAfterRoll())
		stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--skip-health-check", "--poll-interval", "5ms")
		if code := runner.ExitCodeOf(err); code != runner.ExitVerifyFailed {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
		out := ui.StripANSI(stdout)
		if !strings.Contains(out, "(VerifyFailed)") || !strings.Contains(out, "Resume with: refresh cluster upgrade") {
			t.Errorf("stdout does not show the stop and the resume command:\n%s", out)
		}
	})
}

// A clean verification changes nothing: exit 0, and the checks are in the
// progress lines.
func TestUpgrade_PostRollVerificationPasses(t *testing.T) {
	fakeaws.New(t, upgradeWorld())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "post-roll verification: nodegroup web is ACTIVE") {
		t.Errorf("stderr has no verification line:\n%s", stderr)
	}
	report := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)["report"].(map[string]any)
	if report["status"] != "Succeeded" || report["verification"] != nil {
		t.Errorf("report = %v, want Succeeded with no verification key", report)
	}
}

// A nodegroup that cannot be read after its roll is incomplete data (exit
// 4), not a verification issue.
func TestUpgrade_PostRollVerificationUnreadExitsFour(t *testing.T) {
	fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "web", Version: "1.31", DescribeErrorAfterUpdate: "AccessDeniedException"},
	}})
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if report := doc["report"].(map[string]any); report["status"] != "Failed" {
		t.Errorf("report = %v, want Failed", report)
	}
	checkFailure(t, "failures[0]", onlyFailure(t, doc), map[string]any{
		"kind": "Nodegroup", "name": "web", "cluster": "prod", "region": "us-east-1", "operation": "eks:DescribeNodegroup", "reason": "AccessDenied",
	})
}

// --skip-verify turns the verification off, as in `nodegroup update`.
func TestUpgrade_SkipVerify(t *testing.T) {
	srv := fakeaws.New(t, degradedAfterRoll())
	_, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--skip-health-check", "--skip-verify", "--nodegroup", "web", "--only", "nodegroups", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("upgrade --skip-verify: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "post-roll verification") {
		t.Errorf("verification ran with --skip-verify:\n%s", stderr)
	}
	if got := srv.Cluster("prod").Nodegroups[0].Version; got != "1.32" {
		t.Errorf("web = %s, want 1.32", got)
	}
}

// `cluster rollback` rolls its nodegroups through the same phase, so it
// verifies them too: issues exit 5, and the control plane is not rolled
// back.
func TestRollback_PostRollVerificationIssuesExitFive(t *testing.T) {
	world := rollbackWorld()
	world.Nodegroups[0].StatusAfterUpdate = "DEGRADED"
	srv := fakeaws.New(t, world)
	stdout, stderr, err := runCluster(t, "rollback", "prod", "--yes", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitVerifyFailed {
		t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
	}
	if !strings.Contains(err.Error(), "resume with: refresh cluster rollback") {
		t.Errorf("err = %v, want the resume command", err)
	}
	report := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)["report"].(map[string]any)
	if report["status"] != "VerifyFailed" || report["verification"] == nil {
		t.Errorf("report = %v, want VerifyFailed with the verification", report)
	}
	if got := srv.Cluster("prod").Version; got != "1.34" {
		t.Errorf("control plane = %s, want 1.34: verification stopped the run before it", got)
	}
}
