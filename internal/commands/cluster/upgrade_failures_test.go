package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// onlyFailure returns the one entry of a document's failures list.
func onlyFailure(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	fs, ok := doc["failures"].([]any)
	if !ok || len(fs) != 1 {
		t.Fatalf("failures = %#v, want one entry", doc["failures"])
	}
	return fs[0].(map[string]any)
}

// checkFailure fails t unless f has each key in want.
func checkFailure(t *testing.T, where string, f map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s.%s = %v, want %v (%v)", where, k, f[k], v, f)
		}
	}
	if e, _ := f["error"].(string); e == "" || strings.Contains(e, "\n") {
		t.Errorf("%s.error = %q, want one line", where, e)
	}
}

// Misclassification fix: "could not verify that EKS offers X" was a plan
// warning, and a dry run exited 0. It is a plan failure now: a dry run exits
// 4, and a real run proceeds as before but exits 4 and lists it.
func TestUpgrade_VersionCheckFailureIsAFailure(t *testing.T) {
	want := map[string]any{"kind": "Cluster", "name": "prod", "region": "us-east-1", "operation": "eks:DescribeClusterVersions", "reason": "AccessDenied", "retryable": false}
	line := "warning: cluster prod (us-east-1): AccessDenied: AccessDeniedException: fake DescribeClusterVersions failure"

	t.Run("dry run", func(t *testing.T) {
		for _, format := range []string{"json", "table"} {
			srv := fakeaws.New(t, upgradeWorld())
			srv.FailClusterVersions("AccessDeniedException")
			stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run", "-o", format)
			if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
				t.Fatalf("-o %s: exit code = %d (err %v), want 4\nstderr:\n%s", format, code, err, stderr)
			}
			if format == "table" {
				// The table view lists it in the INCOMPLETE DATA section.
				out := ui.StripANSI(stdout)
				if !strings.Contains(out, "INCOMPLETE DATA") || strings.Count(out, strings.TrimPrefix(line, "warning: ")) != 1 || strings.Contains(stderr, line) {
					t.Errorf("-o table: the failure is not listed once under INCOMPLETE DATA:\n%s\nstderr:\n%s", out, stderr)
				}
			} else if n := strings.Count(stderr, line); n != 1 {
				t.Errorf("-o %s: stderr has the failure line %d time(s), want once:\n%s", format, n, stderr)
			}
			if format == "json" {
				plan := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
				checkFailure(t, "plan.failures[0]", onlyFailure(t, plan), want)
				if _, ok := plan["warnings"]; ok {
					t.Errorf("plan has a warnings key: %v", plan)
				}
			}
		}
	})
	t.Run("real run", func(t *testing.T) {
		srv := fakeaws.New(t, upgradeWorld())
		srv.FailClusterVersions("AccessDeniedException")
		stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		checkFailure(t, "failures[0]", onlyFailure(t, doc), want)
		report := doc["report"].(map[string]any)
		if report["status"] != "Succeeded" || report["failure"] != nil {
			t.Errorf("report = %v, want Succeeded: the run proceeded", report)
		}
		if got := srv.Cluster("prod").Version; got != "1.32" {
			t.Errorf("control plane = %s, want 1.32: the failed check must not stop a real run", got)
		}
		if n := strings.Count(stderr, line); n != 1 {
			t.Errorf("stderr has the failure line %d time(s), want once:\n%s", n, stderr)
		}
	})
}

// Misclassification fix: a dry run that could not read the cluster insights
// was advisory (exit 0). It is a plan failure now (exit 4). A real run
// blocks on it (exit 3, fail closed) and lists it too.
func TestUpgrade_InsightsReadFailure(t *testing.T) {
	world := func() *fakeaws.Cluster {
		w := upgradeWorld()
		w.ListInsightsError = "ThrottlingException"
		return w
	}
	want := map[string]any{"kind": "Cluster", "name": "prod", "operation": "eks:ListInsights", "reason": "Throttled", "retryable": true}

	fakeaws.New(t, world())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("dry run: exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	checkFailure(t, "dry run failures[0]", onlyFailure(t, fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)), want)

	srv := fakeaws.New(t, world())
	stdout, stderr, err = runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitBlocked {
		t.Fatalf("real run: exit code = %d (err %v), want 3 (blocked)\nstderr:\n%s", code, err, stderr)
	}
	checkFailure(t, "real run failures[0]", onlyFailure(t, fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)), want)
	if got := srv.Cluster("prod").Version; got != "1.31" {
		t.Errorf("control plane = %s, want 1.31: a blocked plan changes nothing", got)
	}
}

// The report says why a run stopped: its status, the phase, and a failure
// with the reason, so a JSON consumer does not have to read stderr.
func TestUpgrade_ReportStopState(t *testing.T) {
	cases := []struct {
		name         string
		updateStatus string
		args         []string
		status       string
		failure      map[string]any
	}{
		{"update failed", "Failed", nil, "Failed", map[string]any{"kind": "Update", "name": "prod", "reason": "UpdateFailed", "retryable": false}},
		{"timed out", "InProgress", []string{"--wait-timeout", "1s"}, "TimedOut", map[string]any{"kind": "Cluster", "name": "prod", "reason": "Timeout", "retryable": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := upgradeWorld()
			world.UpdateStatus = tc.updateStatus
			fakeaws.New(t, world)
			args := append([]string{"upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json"}, tc.args...)
			stdout, stderr, err := runCluster(t, args...)
			if code := runner.ExitCodeOf(err); code != runner.ExitError {
				t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			report := doc["report"].(map[string]any)
			if report["status"] != tc.status || !strings.Contains(report["stoppedAt"].(string), "control plane") {
				t.Errorf("report = %v, want %s at the control-plane phase", report, tc.status)
			}
			f, _ := report["failure"].(map[string]any)
			checkFailure(t, "report.failure", f, tc.failure)
			if f["updateId"] == nil {
				t.Errorf("report.failure has no updateId: %v", f)
			}
			checkFailure(t, "failures[0]", onlyFailure(t, doc), tc.failure)
			if _, ok := report["failedAt"]; ok {
				t.Errorf("report has the removed failedAt key: %v", report)
			}
		})
	}
}
