package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// useGateChecker makes the pre-roll gate of the commands use checker, as a
// run with Kubernetes access.
func useGateChecker(t *testing.T, checker healthGateChecker) {
	t.Helper()
	orig := gateConnect
	gateConnect = func(context.Context, *cli.Command, aws.Config, string) (healthGateChecker, bool) {
		return checker, true
	}
	t.Cleanup(func() { gateConnect = orig })
}

// nodegroupsBehind is a cluster at 1.32 with one nodegroup to roll.
func nodegroupsBehind() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}}}
}

// --force rolls past PDBs that could not be read, but the read is still
// missing data: the document lists it, stderr names it, and the run exits 4.
func TestUpgrade_ForcedPastUnreadablePDBsExitsFour(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			srv := fakeaws.New(t, nodegroupsBehind())
			useGateChecker(t, &fakeGateChecker{drainErr: errors.New("poddisruptionbudgets is forbidden")})
			stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--force", "--quiet", "--poll-interval", "5ms", "-o", format)
			if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
				t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			f := onlyFailure(t, doc)
			checkFailure(t, "failures[0]", f, map[string]any{"kind": "Cluster", "name": "prod", "region": "us-east-1"})
			if e, _ := f["error"].(string); !strings.Contains(e, "PodDisruptionBudgets") || !strings.Contains(e, "is forbidden") {
				t.Errorf("failures[0].error = %q, want the failed PDB read", e)
			}
			if report := doc["report"].(map[string]any); report["status"] != "Succeeded" {
				t.Errorf("report = %v, want Succeeded: the forced roll ran", report)
			}
			if got := srv.Cluster("prod").Nodegroups[0]; got.Version != "1.32" || !got.UpdateForce {
				t.Errorf("web = %s (force %v), want rolled to 1.32 with force", got.Version, got.UpdateForce)
			}
			if !strings.Contains(stderr, "warning: cluster prod (us-east-1): ") || !strings.Contains(stderr, "is forbidden") {
				t.Errorf("stderr does not report the failure:\n%s", stderr)
			}
		})
	}
}

// The table view lists the read under INCOMPLETE DATA and exits 4 too.
func TestUpgrade_ForcedPastUnreadablePDBsTable(t *testing.T) {
	fakeaws.New(t, nodegroupsBehind())
	useGateChecker(t, &fakeGateChecker{drainErr: errors.New("poddisruptionbudgets is forbidden")})
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--force", "--poll-interval", "5ms")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	if out := ui.StripANSI(stdout); !strings.Contains(out, "INCOMPLETE DATA") || !strings.Contains(out, "is forbidden") {
		t.Errorf("stdout does not list the failed read:\n%s", out)
	}
}

// The reads the health checks of the gate could not make reach the document
// too, when --yes rolls past their warning, also with --quiet.
func TestUpgrade_GateHealthFailuresReachTheDocument(t *testing.T) {
	srv := fakeaws.New(t, nodegroupsBehind())
	unread := diag.New(diag.KindCluster, "prod", diag.ReasonAccessDenied, "listing pods in kube-system: pods is forbidden")
	unread.Region = "us-east-1"
	useGateChecker(t, &fakeGateChecker{decision: health.DecisionWarn, failures: diag.List{unread}})
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--quiet", "--poll-interval", "5ms", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	checkFailure(t, "failures[0]", onlyFailure(t, doc), map[string]any{"kind": "Cluster", "name": "prod", "reason": "AccessDenied"})
	if got := srv.Cluster("prod").Nodegroups[0].Version; got != "1.32" {
		t.Errorf("web = %s, want 1.32: --yes rolls past the warning", got)
	}
	if !strings.Contains(stderr, "listing pods in kube-system") {
		t.Errorf("stderr does not report the failure:\n%s", stderr)
	}
}

// `cluster rollback` uses the same gate and reports its failures the same
// way.
func TestRollback_ForcedPastUnreadablePDBsExitsFour(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			srv := fakeaws.New(t, rollbackWorld())
			useGateChecker(t, &fakeGateChecker{drainErr: errors.New("poddisruptionbudgets is forbidden")})
			stdout, stderr, err := runCluster(t, "rollback", "prod", "--yes", "--force", "--poll-interval", "5ms", "-o", format)
			if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
				t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout).(map[string]any)
			checkFailure(t, "failures[0]", onlyFailure(t, doc), map[string]any{"kind": "Cluster", "name": "prod", "region": "us-east-1"})
			if report := doc["report"].(map[string]any); report["status"] != "Succeeded" {
				t.Errorf("report = %v, want Succeeded", report)
			}
			if got := srv.Cluster("prod").Version; got != "1.33" {
				t.Errorf("control plane = %s, want 1.33: the forced rollback ran", got)
			}
			if !strings.Contains(stderr, "is forbidden") {
				t.Errorf("stderr does not report the failure:\n%s", stderr)
			}
		})
	}
}

// A run that fails for another reason keeps that error: the gate's failure
// is listed, but it does not lower the exit code to 4.
func TestUpgrade_GateFailureDoesNotHideAFailedRoll(t *testing.T) {
	world := nodegroupsBehind()
	world.Nodegroups[0].UpdateStatus = "Failed"
	fakeaws.New(t, world)
	useGateChecker(t, &fakeGateChecker{drainErr: errors.New("poddisruptionbudgets is forbidden")})
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--force", "--poll-interval", "5ms", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitError {
		t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if fs, _ := doc["failures"].([]any); len(fs) != 2 {
		t.Errorf("failures = %v, want the failed update and the failed PDB read", doc["failures"])
	}
}
