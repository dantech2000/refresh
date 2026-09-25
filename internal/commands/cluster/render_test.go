package cluster

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// captureStdout redirects both os.Stdout and color.Output so colorized
// renderer output is captured.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	originalStdout := os.Stdout
	originalColor := color.Output
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	color.Output = w
	t.Cleanup(func() {
		os.Stdout = originalStdout
		color.Output = originalColor
	})
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// ── Command structure ──────────────────────────────────────────────────────

func TestClusterCommand_HasUpgradeSubcommands(t *testing.T) {
	cmd := Command()
	has := func(name string) bool {
		for _, sc := range cmd.Commands {
			if sc.Name == name {
				return true
			}
			for _, a := range sc.Aliases {
				if a == name {
					return true
				}
			}
		}
		return false
	}
	for _, name := range []string{"upgrade", "upgrade-check"} {
		if !has(name) {
			t.Errorf("cluster: missing subcommand %q", name)
		}
	}
}

func TestClusterListCommand_HasFormatFlag(t *testing.T) {
	lc := listCommand()
	if lc.Name != "list" {
		t.Fatalf("expected name 'list', got %q", lc.Name)
	}
	found := false
	for _, f := range lc.Flags {
		for _, n := range f.Names() {
			if n == "format" || n == "o" {
				found = true
			}
		}
	}
	if !found {
		t.Error("list command should expose a -o/--format flag")
	}
}

// ── stepToken ──────────────────────────────────────────────────────────────

func TestStepToken(t *testing.T) {
	cases := []struct {
		status         upgrade.StepStatus
		unicode, ascii string
	}{
		{upgrade.StatusCompleted, "● done", "[OK] done"},
		{upgrade.StatusBlocked, "✗ BLOCKED", "[X] BLOCKED"},
		{upgrade.StatusManual, "▲ manual", "[!] manual"},
		{upgrade.StatusPending, "• pending", "- pending"},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := stepToken(render.New(render.ColorNone, true), tc.status); got != tc.unicode {
				t.Errorf("token = %q, want %q", got, tc.unicode)
			}
			if got := stepToken(render.New(render.ColorNone, false), tc.status); got != tc.ascii {
				t.Errorf("ASCII token = %q, want %q", got, tc.ascii)
			}
		})
	}
}

// The step descriptions line up whatever the token width, and the step
// reason follows the description.
func TestPlanLines_AlignedTokens(t *testing.T) {
	plan := &upgrade.Plan{
		ClusterName: "prod", CurrentVersion: "1.30", TargetVersion: "1.31",
		Hops: []upgrade.Hop{{From: "1.30", To: "1.31", Steps: []upgrade.Step{
			{Description: "readiness check", Status: upgrade.StatusCompleted, Reason: "no blockers"},
			{Description: "control plane → 1.31", Status: upgrade.StatusBlocked},
		}}},
	}
	lines := planLines(render.New(render.ColorNone, true), plan)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"▸ Hop 1.30 → 1.31",
		"   1. ● done    readiness check — no blockers",
		"   2. ✗ BLOCKED control plane → 1.31",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plan missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "\x1b") {
		t.Error("ColorNone plan has ANSI escapes")
	}
}

func TestOutcomeLines(t *testing.T) {
	th := render.New(render.ColorNone, false)
	done := strings.Join(outcomeLines(th, "prod", &upgrade.Plan{TargetVersion: "1.32"}, true), "\n")
	if !strings.Contains(done, "[OK] Upgrade complete: prod is at 1.32.") {
		t.Errorf("outcome = %q", done)
	}
}

// ── renderPlan ─────────────────────────────────────────────────────────────

func TestRenderPlan_ShowsPathHopsAndNotices(t *testing.T) {
	plan := &upgrade.Plan{
		ClusterName:    "prod",
		CurrentVersion: "1.30",
		TargetVersion:  "1.32",
		Notices:        []string{"custom AMI nodegroups will be skipped"},
		Hops: []upgrade.Hop{
			{
				From: "1.30", To: "1.31",
				Steps: []upgrade.Step{
					{Type: upgrade.StepReadiness, Description: "readiness check", Status: upgrade.StatusBlocked, Reason: "2 blocking insights"},
					{Type: upgrade.StepControlPlane, Description: "control plane → 1.31", Status: upgrade.StatusPending},
				},
			},
			{
				From: "1.31", To: "1.32",
				Steps: []upgrade.Step{
					{Type: upgrade.StepControlPlane, Description: "control plane → 1.32", Status: upgrade.StatusPending},
				},
			},
		},
	}
	out := captureStdout(t, func() { renderPlan(plan) })
	for _, want := range []string{"prod", "1.30 → 1.31 → 1.32", "notice", "Hop 1.30 → 1.31", "control plane → 1.31", "2 blocking insights"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderPlan output missing %q; got:\n%s", want, out)
		}
	}
}

// ── renderReport ───────────────────────────────────────────────────────────

func TestRenderReport_Nil(t *testing.T) {
	out := captureStdout(t, func() { renderReport(os.Stdout, nil) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("nil report should print nothing, got: %q", out)
	}
}

func TestRenderReport_CompletedFailedRemaining(t *testing.T) {
	report := &upgrade.Report{
		Completed: []string{"control plane → 1.31"},
		Status:    upgrade.RunFailed,
		StoppedAt: "addon coredns update",
		Remaining: []string{"nodegroup workers"},
	}
	out := captureStdout(t, func() { renderReport(os.Stdout, report) })
	for _, want := range []string{"completed:", "control plane → 1.31", "stopped at:", "addon coredns update (Failed)", "remaining:", "nodegroup workers"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderReport output missing %q; got:\n%s", want, out)
		}
	}
}
