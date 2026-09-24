package nodegroup

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// withTerminal makes isInteractive report tty for the duration of the test.
func withTerminal(t *testing.T, tty bool) {
	t.Helper()
	orig := isInteractive
	isInteractive = func() bool { return tty }
	t.Cleanup(func() { isInteractive = orig })
}

// --quiet hides the health report, so on a terminal it must not accept
// warnings without --yes: it stops, and the error names the warnings.
func TestApplyHealthDecision_QuietOnTTYNeverAutoAccepts(t *testing.T) {
	withTerminal(t, true)
	summary := health.HealthSummary{
		Decision: health.DecisionWarn,
		Results:  []health.HealthResult{{Name: "Cluster Capacity", Status: health.StatusWarn, Message: "low headroom"}},
	}
	done, err := applyHealthDecision(t.Context(), summary, updateAMIFlags{quiet: true})
	if !done || err == nil {
		t.Fatalf("done=%v err=%v, want a stop", done, err)
	}
	for _, want := range []string{"Cluster Capacity: low headroom", "--quiet does not prompt", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	// --yes still proceeds under --quiet.
	if done, err := applyHealthDecision(t.Context(), summary, updateAMIFlags{quiet: true, yes: true}); done || err != nil {
		t.Errorf("--quiet --yes: done=%v err=%v, want proceed", done, err)
	}
}

// End to end: `nodegroup update prod web -q` on a terminal must not roll past
// a WARN verdict without --yes.
func TestUpdate_QuietOnTTYStopsAtWarnings(t *testing.T) {
	withTerminal(t, true)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	_, stderr, err := runNodegroup(t, "update", "prod", "web", "-q")
	if err == nil || !strings.Contains(err.Error(), "--quiet does not prompt") {
		t.Fatalf("err = %v, want the --quiet stop\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(err.Error(), "Cluster Capacity:") {
		t.Errorf("error does not name the warning check: %v", err)
	}
	if calledPath(srv, "/update-version") {
		t.Error("--quiet must not start an update past health warnings")
	}
}

// --quiet does not prompt for a nodegroup pattern either: on a terminal, a
// non-exact or ambiguous pattern needs --yes and names the candidates.
func TestUpdate_QuietOnTTYPatternNeedsYes(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    string
	}{
		{"web", "partial match: payments-web"},
		{"pay", "matched 2 nodegroups (payments-web, payments-api)"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			withTerminal(t, true)
			srv := fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "payments-web", Version: "1.31"},
				&fakeaws.Nodegroup{Name: "payments-api", Version: "1.31"},
			))
			_, stderr, err := runNodegroup(t, "update", "prod", tc.pattern, "-q", "--skip-health-check")
			if err == nil {
				t.Fatalf("err = nil, want a --yes requirement\nstderr:\n%s", stderr)
			}
			for _, want := range []string{tc.want, "--quiet does not prompt", "--yes"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(stderr, "[y/N]") || strings.Contains(stderr, "(y/N)") {
				t.Errorf("--quiet must not prompt; stderr:\n%s", stderr)
			}
			if calledPath(srv, "/update-version") {
				t.Error("--quiet must not start an update for an unconfirmed pattern")
			}
		})
	}
}

// A pattern that is not an exact nodegroup name never rolls without a
// confirmation: without a terminal or with -o json it needs --yes.
func TestUpdate_SingleNonExactPatternNeedsYes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json", []string{"-o", "json"}},
		{"yaml", []string{"-o", "yaml"}},
		{"table without a terminal", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTerminal(t, false)
			srv := fakeaws.New(t, prodCluster(
				&fakeaws.Nodegroup{Name: "payments-web", Version: "1.31"},
				&fakeaws.Nodegroup{Name: "api", Version: "1.31"},
			))
			args := append([]string{"update", "prod", "web", "--skip-health-check"}, tc.args...)
			stdout, _, err := runNodegroup(t, args...)
			if err == nil {
				t.Fatal("err = nil, want a --yes requirement")
			}
			for _, want := range []string{`no nodegroup named "web"`, "partial match: payments-web", "--yes"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if calledPath(srv, "/update-version") {
				t.Error("a non-exact pattern must not start an update without --yes")
			}
		})
	}
}

func TestUpdate_SingleNonExactPatternWithYes(t *testing.T) {
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "payments-web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if ng := nodegroupEntries(t, doc)["payments-web"]; ng["updateId"] == nil {
		t.Errorf("payments-web = %v, want a started update", ng)
	}
	if !strings.Contains(stderr, `using the only partial match "payments-web"`) {
		t.Errorf("stderr does not name the accepted match; got:\n%s", stderr)
	}
	if !calledPath(srv, "/node-groups/payments-web/update-version") {
		t.Error("--yes should start the update of the only match")
	}
}

// An exact name needs no confirmation, even with substring siblings.
func TestUpdate_ExactNodegroupNameNeedsNoConfirmation(t *testing.T) {
	srv := fakeaws.New(t, prodCluster(
		&fakeaws.Nodegroup{Name: "web", Version: "1.31"},
		&fakeaws.Nodegroup{Name: "payments-web", Version: "1.31"},
	))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if ngs := nodegroupEntries(t, doc); len(ngs) != 1 || ngs["web"]["updateId"] == nil {
		t.Errorf("nodegroups = %v, want web started", doc["nodegroups"])
	}
	if calledPath(srv, "/node-groups/payments-web/update-version") {
		t.Error("an exact name must not also roll a substring sibling")
	}
}

// With EKS_CLUSTER_NAME set, a lone positional is a nodegroup pattern. A
// cluster name typed there must not silently roll a lookalike nodegroup.
func TestUpdate_EnvClusterPositionalPatternNeedsConfirmation(t *testing.T) {
	withTerminal(t, false)
	srv := fakeaws.New(t, &fakeaws.Cluster{Name: "stage", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{
		{Name: "prod-mirror", Version: "1.31"},
	}})
	t.Setenv(clusterEnvVar, "stage") // after New, which clears it
	_, stderr, err := runNodegroup(t, "update", "prod", "--skip-health-check")
	if err == nil || !strings.Contains(err.Error(), "partial match: prod-mirror") {
		t.Fatalf("err = %v, want the partial-match error\nstderr:\n%s", err, stderr)
	}
	if calledPath(srv, "/update-version") {
		t.Error("prod-mirror must not roll without confirmation")
	}
}

// Fleet mode: the batch confirmation covers the pattern, so without a
// terminal and without --yes nothing rolls; with --yes the partial match
// rolls in each cluster.
func TestFleetUpdate_NonExactPatternNeedsYes(t *testing.T) {
	withTerminal(t, false)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "payments-web", Version: "1.31"}))
	_, _, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "-n", "web", "--skip-health-check", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want a --yes requirement", err)
	}
	if calledPath(srv, "/update-version") {
		t.Error("fleet mode must not roll a partial match without --yes")
	}

	stdout, stderr, err := runNodegroup(t, "update", "--all-clusters", "-r", "us-east-1", "-n", "web", "--skip-health-check", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("fleet --yes: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	entry := doc["clusters"].([]any)[0].(map[string]any)
	if ng := nodegroupEntries(t, entry)["payments-web"]; ng["updateId"] == nil || entry["status"] != "Succeeded" {
		t.Errorf("fleet entry = %v, want payments-web started and the cluster Succeeded", entry)
	}
	if !strings.Contains(stderr, `using the only partial match "payments-web"`) {
		t.Errorf("stderr does not name the accepted match; got:\n%s", stderr)
	}
}

// A blocked health gate exits 3, as documented, for the human view and
// -o json alike, and starts nothing.
func TestUpdate_HealthBlockExitsThree(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: "DEGRADED"}))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "web", "--yes", "-o", format)
			if code := exitCodeOf(err); code != 3 {
				t.Fatalf("exit code = %d (err %v), want 3\nstderr:\n%s", code, err, stderr)
			}
			if !strings.Contains(err.Error(), "pre-flight health checks failed: Node Health:") {
				t.Errorf("error does not name the blocking check: %v", err)
			}
			if format == "json" {
				doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
				if h, ok := doc["health"].(map[string]any); !ok || h["decision"] != "Block" {
					t.Errorf("health = %v, want the BLOCK verdict", doc["health"])
				}
			}
			if calledPath(srv, "/update-version") {
				t.Error("a blocked run must not start an update")
			}
		})
	}
}

// The dry-run preview names the same skip the real run takes for a
// custom-AMI nodegroup, with and without --force.
func TestUpdate_DryRunSkipsCustomAMI(t *testing.T) {
	for _, force := range []bool{false, true} {
		fakeaws.New(t, prodCluster(
			&fakeaws.Nodegroup{Name: "custom", Version: "1.31", AmiType: "CUSTOM"},
			&fakeaws.Nodegroup{Name: "busy", Version: "1.31", Status: "UPDATING"},
		))
		args := []string{"update", "prod", "--dry-run", "-o", "json"}
		if force {
			args = append(args, "--force")
		}
		stdout, stderr, err := runNodegroup(t, args...)
		if err != nil {
			t.Fatalf("dry-run (force=%v): %v\nstderr:\n%s", force, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		actions := map[string]any{}
		for _, ng := range doc["nodegroups"].([]any) {
			m := ng.(map[string]any)
			actions[m["name"].(string)] = m["action"]
		}
		if actions["custom"] != "SkipCustom" || actions["busy"] != "SkipUpdating" {
			t.Errorf("force=%v: actions = %v, want custom=skip-custom busy=skip-updating", force, actions)
		}
	}
}

// --reroll bypasses the already-on-latest skip without an AMI lookup, the
// same way --force does.
func TestLatestAMISkipChecker_RerollNeverSkips(t *testing.T) {
	// A nil EKS client proves the check returns before any AWS call.
	skip := newLatestAMISkipChecker(t.Context(), aws.Config{}, nil, "prod", updateAMIFlags{reroll: true})
	if skip(&ekstypes.Nodegroup{}) {
		t.Error("--reroll must not skip a nodegroup already on the latest AMI")
	}
}

// --reroll rolls without force: UpdateNodegroupVersion gets force=false, so
// EKS still honors PodDisruptionBudgets. --force keeps sending force=true.
func TestUpdate_RerollDoesNotForce(t *testing.T) {
	for _, tc := range []struct {
		flag      string
		wantForce bool
	}{{"--reroll", false}, {"--force", true}} {
		t.Run(tc.flag, func(t *testing.T) {
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
			stdout, stderr, err := runNodegroup(t, "update", "prod", "web", tc.flag, "--skip-health-check", "--poll-interval", "5ms", "-o", "json")
			if err != nil {
				t.Fatalf("update %s: %v\nstderr:\n%s", tc.flag, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			if ng := nodegroupEntries(t, doc)["web"]; ng["updateId"] == nil {
				t.Fatalf("web = %v, want a started update", ng)
			}
			if got := srv.Cluster("prod").Nodegroups[0].UpdateForce; got != tc.wantForce {
				t.Errorf("UpdateNodegroupVersion force = %v, want %v", got, tc.wantForce)
			}
		})
	}
}

// The -o json dry-run plan records --reroll.
func TestUpdate_DryRunRecordsReroll(t *testing.T) {
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31"}))
	stdout, stderr, err := runNodegroup(t, "update", "prod", "--dry-run", "--reroll", "-o", "json")
	if err != nil {
		t.Fatalf("dry-run: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["reroll"] != true || doc["force"] != false {
		t.Errorf("plan reroll=%v force=%v, want true/false", doc["reroll"], doc["force"])
	}
}

// Fleet mode keeps the single-cluster exit codes for the health gate: a
// warning stop is 2 (HealthWarned), a block is 3 (HealthBlocked).
func TestFleetUpdate_HealthVerdictExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		args       []string
		wantCode   int
		wantStatus string
	}{
		{"health-only warn", "", []string{"--health-only"}, 2, "HealthWarned"},
		{"require-healthy warn", "", []string{"--require-healthy", "--yes"}, 2, "HealthWarned"},
		{"health-only block", "DEGRADED", []string{"--health-only"}, 3, "HealthBlocked"},
		{"block", "DEGRADED", []string{"--yes"}, 3, "HealthBlocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: tc.status}))
			args := append([]string{"update", "--all-clusters", "-r", "us-east-1", "-o", "json"}, tc.args...)
			stdout, stderr, err := runNodegroup(t, args...)
			if code := exitCodeOf(err); code != tc.wantCode {
				t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", code, err, tc.wantCode, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			entry := doc["clusters"].([]any)[0].(map[string]any)
			if entry["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %s", entry["status"], tc.wantStatus)
			}
			requireNoFailures(t, doc)
			if calledPath(srv, "/update-version") {
				t.Error("a cluster stopped by the health gate must not start an update")
			}
		})
	}
}
