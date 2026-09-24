package addon

import (
	"context"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// These tests run `refresh addon ...` end to end against the fake AWS
// endpoint: flags, AWS setup, the service, output, and the exit code.

func runAddon(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// -o plain switches process-wide output state; restore it for the next test.
	t.Cleanup(func() { ui.SetPlainOutput(false); pterm.EnableColor() })
	return fakeaws.Run(t, fakeaws.App(Command()), append([]string{"refresh", "addon"}, args...)...)
}

// exitCodeOf mirrors how the CLI exits: an unwrapped cli.ExitCoder sets the
// code, any other error exits 1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
		return ec.ExitCode()
	}
	return 1
}

func addonCluster(addons ...*fakeaws.Addon) *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.31", Addons: addons}
}

// updateCalls counts UpdateAddon requests the fake served.
func updateCalls(srv *fakeaws.Server) int {
	n := 0
	for _, c := range srv.Calls() {
		if strings.HasPrefix(c, "eks POST ") && strings.HasSuffix(c, "/update") && strings.Contains(c, "/addons/") {
			n++
		}
	}
	return n
}

// withTTY sets whether stdin looks like a terminal and what the prompt
// answers, for the duration of the test.
func withTTY(t *testing.T, tty bool, answer string) {
	t.Helper()
	origTTY, origPrompt := runner.StdinIsTerminal, runner.PromptLine
	runner.StdinIsTerminal = func() bool { return tty }
	runner.PromptLine = func(context.Context) (string, error) { return answer, nil }
	t.Cleanup(func() { runner.StdinIsTerminal, runner.PromptLine = origTTY, origPrompt })
}

// --all with an add-on name or version fails before any AWS call.
func TestUpdateAll_RejectsNameOrVersion(t *testing.T) {
	cases := [][]string{
		{"update", "prod", "coredns", "--all"},
		{"update", "prod", "coredns", "v1.11.1-eksbuild.1", "--all"},
		{"update", "--all", "-c", "prod", "coredns"},
		{"update", "prod", "--all", "--addon", "coredns"},
		{"update", "prod", "--all", "--version", "v1.11.1-eksbuild.1"},
		{"update-all", "prod", "coredns"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.1-eksbuild.1"}))
			_, _, err := runAddon(t, args...)
			if err == nil || !strings.Contains(err.Error(), "cannot be combined with an add-on name or version") {
				t.Fatalf("err = %v, want the --all conflict error", err)
			}
			if calls := srv.Calls(); len(calls) != 0 {
				t.Errorf("AWS calls = %v, want none", calls)
			}
		})
	}
}

// A failed wait still prints the result (with the update ID) as one JSON
// document, then exits 1.
func TestUpdate_WaitFailedEncodesResult(t *testing.T) {
	fakeaws.New(t, addonCluster(&fakeaws.Addon{
		Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, UpdateStatus: "Failed",
	}))
	stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json", "--yes")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	id, _ := doc["updateId"].(string)
	if doc["status"] != "WaitFailed" || id == "" {
		t.Errorf("result = %v, want WaitFailed with the update ID", doc)
	}
	f, _ := doc["failure"].(map[string]any)
	if f["kind"] != "Update" || f["reason"] != "UpdateFailed" || f["updateId"] != id || f["cluster"] != "prod" ||
		!strings.Contains(f["error"].(string), "AdmissionRequestDenied") {
		t.Errorf("failure = %v, want an UpdateFailed failure with the EKS details", f)
	}
	fs, _ := doc["failures"].([]any)
	if len(fs) != 1 || fs[0].(map[string]any)["updateId"] != id {
		t.Errorf("failures = %v, want the one failure", doc["failures"])
	}
	if n := strings.Count(stderr, "warning: update prod/vpc-cni (us-east-1): UpdateFailed: "); n != 1 {
		t.Errorf("stderr names the failure %d time(s), want once:\n%s", n, stderr)
	}
}

// Misclassification fix: a post-update DescribeAddon that fails was a
// health issue (COMPLETED_WITH_ISSUES, exit 5). The add-on's health is
// unknown, so it is a failure now: status Unverified, exit 4.
func TestUpdate_PostUpdateReadFailureExitsFour(t *testing.T) {
	fakeaws.New(t, addonCluster(&fakeaws.Addon{
		Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, DescribeErrorAfterUpdate: "AccessDeniedException",
	}))
	stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json", "--yes")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["status"] != "Unverified" || doc["healthIssues"] != nil {
		t.Errorf("result = %v, want Unverified with no health issues", doc)
	}
	fs, _ := doc["failures"].([]any)
	if len(fs) != 1 {
		t.Fatalf("failures = %v, want one", doc["failures"])
	}
	f := fs[0].(map[string]any)
	if f["kind"] != "Addon" || f["name"] != "vpc-cni" || f["reason"] != "AccessDenied" || f["operation"] != "eks:DescribeAddon" || f["updateId"] == nil {
		t.Errorf("failure = %v, want an AccessDenied eks:DescribeAddon failure", f)
	}
	if !strings.Contains(stderr, "warning: addon prod/vpc-cni (us-east-1): AccessDenied: ") {
		t.Errorf("stderr does not name the failure:\n%s", stderr)
	}
}

// A successful wait confirms the version and exits 0; post-update health
// issues exit 5 (post-action verification failed).
func TestUpdate_WaitOutcomeExitCodes(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json", "--yes")
		if err != nil {
			t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "Completed" || doc["newVersion"] != "v1.19.0" {
			t.Errorf("result = %v, want Completed at v1.19.0", doc)
		}
		if fs, ok := doc["failures"].([]any); !ok || len(fs) != 0 {
			t.Errorf("failures = %#v, want []", doc["failures"])
		}
		if got := srv.Cluster("prod").Addons[0].Version; got != "v1.19.0" {
			t.Errorf("installed = %s, want v1.19.0", got)
		}
	})
	t.Run("completed with issues", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{
			Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, HealthIssue: "1 of 2 replicas ready",
		}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json", "--yes")
		if code := exitCodeOf(err); code != 5 {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "CompletedWithIssues" {
			t.Errorf("status = %v, want CompletedWithIssues", doc["status"])
		}
	})
	t.Run("all with issues", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{
			Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, HealthIssue: "1 of 2 replicas ready",
		}))
		_, stderr, err := runAddon(t, "update", "prod", "--all", "--wait", "-o", "json", "--yes")
		if code := exitCodeOf(err); code != 5 {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
	})
	t.Run("all with a failed wait keeps the update ID", func(t *testing.T) {
		fakeaws.New(t, addonCluster(
			&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0"}, UpdateStatus: "Cancelled"},
			&fakeaws.Addon{Name: "coredns", Version: "v1.11.4", Available: []string{"v1.11.3"}},
		))
		stdout, _, err := runAddon(t, "update", "prod", "--all", "--wait", "-o", "json", "--yes")
		if code := exitCodeOf(err); code != 4 {
			t.Fatalf("exit code = %d (err %v), want 4 (a failed add-on in --all)", code, err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		statuses := map[string]map[string]any{}
		for _, r := range doc["results"].([]any) {
			row := r.(map[string]any)
			statuses[row["addonName"].(string)] = row
		}
		if r := statuses["vpc-cni"]; r["status"] != "WaitFailed" || r["updateId"] == "" || r["failure"] == nil {
			t.Errorf("vpc-cni = %v, want WaitFailed with its update ID and failure", r)
		}
		if r := statuses["coredns"]; r["status"] != "UpToDate" {
			t.Errorf("coredns = %v, want UpToDate (latest never downgrades)", r)
		}
		if fs, _ := doc["failures"].([]any); len(fs) != 1 || fs[0].(map[string]any)["reason"] != "UpdateCancelled" {
			t.Errorf("failures = %v, want the vpc-cni UpdateCancelled failure", doc["failures"])
		}
	})
}

// Every run of `update --all`, whatever the format, names each failed
// add-on once on stderr, as one failure line.
func TestUpdateAll_MachineRunNamesFailures(t *testing.T) {
	fakeaws.New(t, addonCluster(
		&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0"}, UpdateStatus: "Cancelled"},
		&fakeaws.Addon{Name: "kube-proxy", Version: "v1.31.0"},
		&fakeaws.Addon{Name: "coredns", Version: "v1.11.3", Available: []string{"v1.11.4"}},
	))
	for _, format := range []string{"json", "yaml"} {
		stdout, stderr, err := runAddon(t, "update", "prod", "--all", "--wait", "-o", format, "--yes")
		if code := exitCodeOf(err); code != 4 {
			t.Fatalf("-o %s: exit code = %d (err %v), want 4\nstderr:\n%s", format, code, err, stderr)
		}
		fakeaws.RequireOneDocument(t, format, stdout)
		for _, want := range []string{
			"warning: update prod/vpc-cni (us-east-1): UpdateCancelled: addon vpc-cni update update-",
			"warning: addon prod/kube-proxy (us-east-1): NotFound: resolving latest version: no versions found for addon kube-proxy",
		} {
			if n := strings.Count(stderr, want); n != 1 {
				t.Errorf("-o %s: stderr has %q %d time(s), want once:\n%s", format, want, n, stderr)
			}
		}
		if strings.Contains(stderr, "coredns") {
			t.Errorf("-o %s: stderr names the add-on that updated:\n%s", format, stderr)
		}
	}

	// The table view lists the failures in its INCOMPLETE DATA section, not
	// on stderr.
	stdout, stderr, _ := runAddon(t, "update", "prod", "--all", "--wait", "--yes")
	out := ui.StripANSI(stdout)
	if !strings.Contains(out, "Failed") {
		t.Errorf("table lacks the Failed status:\n%s", out)
	}
	if !strings.Contains(out, "INCOMPLETE DATA") || strings.Count(out, "addon prod/kube-proxy (us-east-1): NotFound: ") != 1 {
		t.Errorf("table does not list kube-proxy once under INCOMPLETE DATA:\n%s", out)
	}
	if strings.Contains(stderr, "warning: addon prod/kube-proxy") {
		t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
	}
}

// An add-on already at the target is UP_TO_DATE with no UpdateAddon call; a
// pinned older version proceeds with a warning.
func TestUpdate_VersionGuard(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4", Available: []string{"v1.11.3"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "coredns", "-o", "json", "--yes")
		if err != nil {
			t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "UpToDate" {
			t.Errorf("status = %v, want UpToDate", doc["status"])
		}
		if n := updateCalls(srv); n != 0 {
			t.Errorf("UpdateAddon calls = %d, want 0", n)
		}
	})
	t.Run("up to date plain", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.3", Available: []string{"v1.11.3"}}))
		stdout, _, err := runAddon(t, "update", "prod", "coredns", "-o", "plain", "--yes")
		if err != nil || !strings.Contains(stdout, "UpToDate") {
			t.Fatalf("stdout = %q, err = %v; want an UpToDate row", stdout, err)
		}
	})
	t.Run("pinned downgrade", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", Available: []string{"v1.19.0", "v1.18.0"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "v1.18.0", "-o", "json", "--yes")
		if err != nil {
			t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "warning: downgrading vpc-cni from v1.19.0 to v1.18.0") {
			t.Errorf("stderr = %q, want the downgrade warning", stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if w, _ := doc["warning"].(string); !strings.Contains(w, "downgrading") {
			t.Errorf("warning = %q, want the downgrade in the result", w)
		}
		if n := updateCalls(srv); n != 1 {
			t.Errorf("UpdateAddon calls = %d, want 1", n)
		}
	})
}

// A partial add-on name needs --yes without a terminal or with -o json, and
// is confirmed on a terminal otherwise. Exact and case-insensitive names
// proceed.
func TestUpdate_PartialAddonName(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}})
	}
	cases := []struct {
		name       string
		tty        bool
		answer     string
		format     string // default json
		args       []string
		wantErr    string
		wantUpdate bool
	}{
		{name: "no tty needs --yes", args: []string{"cni"}, wantErr: "add --yes to proceed"},
		{name: "no tty dry run names the match", args: []string{"cni", "--dry-run"}, wantErr: "no add-on named \"cni\" (partial match: vpc-cni)"},
		{name: "no tty with --yes", args: []string{"cni", "--yes"}, wantUpdate: true},
		{name: "tty yes", tty: true, answer: "y", format: "table", args: []string{"cni"}, wantUpdate: true},
		{name: "tty no", tty: true, answer: "n", format: "table", args: []string{"cni"}, wantErr: "cancelled"},
		{name: "tty with -o json never prompts", tty: true, answer: "y", args: []string{"cni"}, wantErr: "-o json does not prompt for confirmation"},
		{name: "case-insensitive exact", args: []string{"VPC-CNI", "--yes"}, wantUpdate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTTY(t, tc.tty, tc.answer)
			srv := fakeaws.New(t, world())
			format := tc.format
			if format == "" {
				format = "json"
			}
			args := append([]string{"update", "prod"}, tc.args...)
			args = append(args, "-o", format)
			_, stderr, err := runAddon(t, args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
			}
			if got := updateCalls(srv) == 1; got != tc.wantUpdate {
				t.Errorf("UpdateAddon calls = %d, want update %v", updateCalls(srv), tc.wantUpdate)
			}
		})
	}
}
