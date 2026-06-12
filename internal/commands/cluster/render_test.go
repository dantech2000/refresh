package cluster

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"

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

// ── stepMarkerAndNote ──────────────────────────────────────────────────────

func TestStepMarkerAndNote(t *testing.T) {
	cases := []struct {
		status     upgrade.StepStatus
		wantMarker string
	}{
		{upgrade.StatusCompleted, "done"},
		{upgrade.StatusBlocked, "BLOCKED"},
		{upgrade.StatusManual, "manual"},
		{upgrade.StatusPending, "pending"},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			marker, note := stepMarkerAndNote(upgrade.Step{Status: tc.status, Reason: "because"})
			if !strings.Contains(marker, tc.wantMarker) {
				t.Errorf("marker = %q, want it to contain %q", marker, tc.wantMarker)
			}
			if note != "because" {
				t.Errorf("note = %q, want the step Reason", note)
			}
		})
	}
}

// ── renderPlan ─────────────────────────────────────────────────────────────

func TestRenderPlan_ShowsPathHopsAndWarnings(t *testing.T) {
	plan := &upgrade.Plan{
		ClusterName:    "prod",
		CurrentVersion: "1.30",
		TargetVersion:  "1.32",
		Warnings:       []string{"custom AMI nodegroups will be skipped"},
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
	for _, want := range []string{"prod", "1.30 → 1.31 → 1.32", "warning", "Hop 1.30 → 1.31", "control plane → 1.31", "2 blocking insights"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderPlan output missing %q; got:\n%s", want, out)
		}
	}
}

// ── renderReport ───────────────────────────────────────────────────────────

func TestRenderReport_Nil(t *testing.T) {
	out := captureStdout(t, func() { renderReport(nil) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("nil report should print nothing, got: %q", out)
	}
}

func TestRenderReport_CompletedFailedRemaining(t *testing.T) {
	report := &upgrade.Report{
		Completed: []string{"control plane → 1.31"},
		FailedAt:  "addon coredns update",
		Remaining: []string{"nodegroup workers"},
	}
	out := captureStdout(t, func() { renderReport(report) })
	for _, want := range []string{"completed:", "control plane → 1.31", "failed at:", "addon coredns update", "remaining:", "nodegroup workers"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderReport output missing %q; got:\n%s", want, out)
		}
	}
}
