package addon

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"

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
	origTTY, origPrompt := stdinIsTerminal, promptLine
	stdinIsTerminal = func() bool { return tty }
	promptLine = func(context.Context) (string, error) { return answer, nil }
	t.Cleanup(func() { stdinIsTerminal, promptLine = origTTY, origPrompt })
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
	stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit code = %d (err %v), want 1\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["status"] != "WAIT_FAILED" || doc["updateId"] == "" || doc["updateId"] == nil {
		t.Errorf("result = %v, want WAIT_FAILED with the update ID", doc)
	}
	if e, _ := doc["error"].(string); !strings.Contains(e, "Failed") || !strings.Contains(e, "AdmissionRequestDenied") {
		t.Errorf("error = %q, want the EKS failure details", e)
	}
}

// A successful wait confirms the version and exits 0; post-update health
// issues exit 5 (post-action verification failed).
func TestUpdate_WaitOutcomeExitCodes(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json")
		if err != nil {
			t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "COMPLETED" || doc["newVersion"] != "v1.19.0" {
			t.Errorf("result = %v, want COMPLETED at v1.19.0", doc)
		}
		if got := srv.Cluster("prod").Addons[0].Version; got != "v1.19.0" {
			t.Errorf("installed = %s, want v1.19.0", got)
		}
	})
	t.Run("completed with issues", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{
			Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, HealthIssue: "1 of 2 replicas ready",
		}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "--wait", "-o", "json")
		if code := exitCodeOf(err); code != 5 {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "COMPLETED_WITH_ISSUES" {
			t.Errorf("status = %v, want COMPLETED_WITH_ISSUES", doc["status"])
		}
	})
	t.Run("all with issues", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{
			Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, HealthIssue: "1 of 2 replicas ready",
		}))
		_, stderr, err := runAddon(t, "update", "prod", "--all", "--wait", "-o", "json")
		if code := exitCodeOf(err); code != 5 {
			t.Fatalf("exit code = %d (err %v), want 5\nstderr:\n%s", code, err, stderr)
		}
	})
	t.Run("all with a failed wait keeps the update ID", func(t *testing.T) {
		fakeaws.New(t, addonCluster(
			&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0"}, UpdateStatus: "Cancelled"},
			&fakeaws.Addon{Name: "coredns", Version: "v1.11.4", Available: []string{"v1.11.3"}},
		))
		stdout, _, err := runAddon(t, "update", "prod", "--all", "--wait", "-o", "json")
		if code := exitCodeOf(err); code != 4 {
			t.Fatalf("exit code = %d (err %v), want 4 (a failed add-on in --all)", code, err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		statuses := map[string]map[string]any{}
		for _, r := range doc["results"].([]any) {
			row := r.(map[string]any)
			statuses[row["addonName"].(string)] = row
		}
		if r := statuses["vpc-cni"]; r["status"] != "WAIT_FAILED" || r["updateId"] == "" {
			t.Errorf("vpc-cni = %v, want WAIT_FAILED with its update ID", r)
		}
		if r := statuses["coredns"]; r["status"] != "UP_TO_DATE" {
			t.Errorf("coredns = %v, want UP_TO_DATE (latest never downgrades)", r)
		}
	})
}

// An add-on already at the target is UP_TO_DATE with no UpdateAddon call; a
// pinned older version proceeds with a warning.
func TestUpdate_VersionGuard(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4", Available: []string{"v1.11.3"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "coredns", "-o", "json")
		if err != nil {
			t.Fatalf("update: %v\nstderr:\n%s", err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["status"] != "UP_TO_DATE" {
			t.Errorf("status = %v, want UP_TO_DATE", doc["status"])
		}
		if n := updateCalls(srv); n != 0 {
			t.Errorf("UpdateAddon calls = %d, want 0", n)
		}
	})
	t.Run("up to date plain", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.3", Available: []string{"v1.11.3"}}))
		stdout, _, err := runAddon(t, "update", "prod", "coredns", "-o", "plain")
		if err != nil || !strings.Contains(stdout, "UP_TO_DATE") {
			t.Fatalf("stdout = %q, err = %v; want an UP_TO_DATE row", stdout, err)
		}
	})
	t.Run("pinned downgrade", func(t *testing.T) {
		srv := fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", Available: []string{"v1.19.0", "v1.18.0"}}))
		stdout, stderr, err := runAddon(t, "update", "prod", "vpc-cni", "v1.18.0", "-o", "json")
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
		{name: "no tty", args: []string{"cni"}, wantErr: "no add-on named \"cni\" (partial match: vpc-cni)"},
		{name: "no tty with --yes", args: []string{"cni", "--yes"}, wantUpdate: true},
		{name: "tty yes", tty: true, answer: "y", format: "table", args: []string{"cni"}, wantUpdate: true},
		{name: "tty no", tty: true, answer: "n", format: "table", args: []string{"cni"}, wantErr: "cancelled"},
		{name: "tty with -o json never prompts", tty: true, answer: "y", args: []string{"cni"}, wantErr: "no add-on named \"cni\" (partial match: vpc-cni)"},
		{name: "case-insensitive exact", args: []string{"VPC-CNI"}, wantUpdate: true},
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

// addon list: add-ons that could not be described are named on stderr and
// under "failures", and the command exits 4 after printing the rest.
func TestList_DescribeFailures(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return addonCluster(
			&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"},
			&fakeaws.Addon{Name: "kube-proxy", Version: "v1.31.0", DescribeAddonError: "AccessDeniedException"},
			&fakeaws.Addon{Name: "vpc-cni", Version: "v1.19.0", DescribeAddonError: "AccessDeniedException"},
		)
	}
	t.Run("json", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := runAddon(t, "list", "prod", "-o", "json")
		if err == nil || !strings.Contains(err.Error(), "2 add-on(s) could not be described") {
			t.Fatalf("err = %v, want the incomplete-list error", err)
		}
		if code := exitCodeOf(err); code != 4 {
			t.Errorf("exit code = %d, want 4 (incomplete data)", code)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["count"] != float64(1) {
			t.Errorf("count = %v, want 1", doc["count"])
		}
		failures, _ := doc["failures"].([]any)
		if len(failures) != 2 || !strings.HasPrefix(failures[0].(string), "kube-proxy: AccessDeniedException") {
			t.Errorf("failures = %v, want kube-proxy and vpc-cni with the reason", failures)
		}
		for _, want := range []string{"warning: add-on kube-proxy", "warning: add-on vpc-cni"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q; got:\n%s", want, stderr)
			}
		}
	})
	t.Run("no failures key when complete", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"}))
		stdout, _, err := runAddon(t, "list", "prod", "-o", "json")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if _, ok := doc["failures"]; ok {
			t.Errorf("doc = %v, want no failures key", doc)
		}
	})
	t.Run("plain has no unknown rows", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, _, _ := runAddon(t, "list", "prod", "-o", "plain")
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 2 || strings.Contains(stdout, "UNKNOWN") || strings.Contains(stdout, "Unknown") {
			t.Errorf("stdout = %q, want the header and the coredns row only", stdout)
		}
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "coredns") }) {
			t.Errorf("stdout = %q, want a coredns row", stdout)
		}
	})
}
