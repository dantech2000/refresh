package addon

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

// Add-on health renders as the same tokens in every view (list and
// describe): IN_PROGRESS is in progress, like an UPDATING add-on.
func TestAddonHealthToken(t *testing.T) {
	cases := map[addons.Health]string{
		addons.HealthPass:       "● PASS",
		addons.HealthFail:       "✗ FAIL",
		addons.HealthInProgress: "◷ IN_PROGRESS",
		"Something":             "○ UNKNOWN",
	}
	th := render.New(render.ColorNone, true)
	for in, want := range cases {
		if got := addonHealthToken(th, in); got != want {
			t.Errorf("addonHealthToken(%q) = %q, want %q", in, got, want)
		}
	}
	if got := th.Token(render.StatusFromString("UPDATING"), "UPDATING"); got != "◷ UPDATING" {
		t.Errorf("UPDATING status = %q, want the in-progress token", got)
	}
}

func TestOutputAddonsTable_Empty(t *testing.T) {
	out, err := captureStdout(t, func() error { return outputAddonsTable("prod", nil, nil) })
	if err != nil {
		t.Fatalf("empty addons table: %v", err)
	}
	if !strings.Contains(out, "prod") {
		t.Errorf("empty table: missing cluster name 'prod' in output: %q", out)
	}
}

func TestOutputAddonsTable_WithRows(t *testing.T) {
	rows := []addons.AddonSummary{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: addons.HealthPass}}

	// Human path (render design system): ADD-ONS header + tokenized rows.
	out, err := captureStdout(t, func() error { return outputAddonsTable("prod", rows, nil) })
	if err != nil {
		t.Fatalf("addons table: %v", err)
	}
	for _, want := range []string{"ADD-ONS", "prod", "vpc-cni", "ACTIVE"} {
		if !strings.Contains(out, want) {
			t.Errorf("addons table missing %q: %q", want, out)
		}
	}

	// Plain path (-o plain): header + one TSV row per add-on, nothing else.
	// The header names match the human table's columns.
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	plain, err := captureStdout(t, func() error { return outputAddonsTable("prod", rows, nil) })
	if err != nil {
		t.Fatalf("addons plain: %v", err)
	}
	headers := []string{"NAME", "VERSION", "STATUS", "HEALTH"}
	got := plaintest.Check(t, plain, headers...)
	if len(got) != 1 || strings.Join(got[0], "|") != "vpc-cni|v1.18.3|ACTIVE|PASS" {
		t.Errorf("plain rows = %q", got)
	}
	for _, h := range headers {
		if !strings.Contains(out, h) {
			t.Errorf("human table has no %q column; plain header must match it:\n%s", h, out)
		}
	}
}

func TestOutputAddonsTable_PlainEmptyIsHeaderOnly(t *testing.T) {
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out, err := captureStdout(t, func() error { return outputAddonsTable("prod", nil, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "NAME\tVERSION\tSTATUS\tHEALTH\n" {
		t.Errorf("empty plain list should be the header only, got %q", out)
	}
}

func TestOutputAddonDetailsTable_Plain(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d := &addons.AddonDetails{
		Name: "vpc-cni", Version: "v1.18.3", Status: "DEGRADED", ARN: "arn:aws:eks:addon/vpc-cni",
		CreatedAt:     &created,
		Issues:        []addons.AddonIssue{{Code: "InsufficientNumberOfReplicas", Message: "2 of 3\nready", ResourceIDs: []string{"ds/aws-node"}}},
		Configuration: map[string]any{"env": map[string]any{"WARM_IP_TARGET": "5"}},
	}
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out, err := captureStdout(t, func() error { return outputAddonDetailsTable("prod", d) })
	if err != nil {
		t.Fatal(err)
	}
	rows := plaintest.Check(t, out, "FIELD", "VALUE")
	for f, v := range map[string]string{
		"name":                               "vpc-cni",
		"cluster":                            "prod",
		"status":                             "DEGRADED",
		"health":                             "-",
		"created":                            "2026-01-02T03:04:05Z",
		"issue/InsufficientNumberOfReplicas": "2 of 3 ready [ds/aws-node]",
		"configuration":                      `{"env":{"WARM_IP_TARGET":"5"}}`,
	} {
		if got, ok := plaintest.Field(rows, f); !ok || got != v {
			t.Errorf("field %q = %q (found=%v), want %q", f, got, ok, v)
		}
	}
}

func TestOutputUpdateAllResults_Plain(t *testing.T) {
	results := []addons.AddonUpdateResult{
		{AddonName: "vpc-cni", PreviousVersion: "v1.18.0", NewVersion: "v1.18.3", Status: "COMPLETED", UpdateID: "upd-123"},
		{AddonName: "coredns", PreviousVersion: "v1.11.1", NewVersion: "v1.11.3", Status: "COMPLETED_WITH_ISSUES", HealthIssues: "pods not ready"},
	}
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out, err := captureStdout(t, func() error { return outputUpdateAllResults("prod", results, false) })
	if err != nil {
		t.Fatal(err)
	}
	rows := plaintest.Check(t, out, "ADDON", "PREVIOUS", "NEW", "STATUS", "UPDATE ID")
	if len(rows) != 2 || strings.Join(rows[0], "|") != "vpc-cni|v1.18.0|v1.18.3|COMPLETED|upd-123" ||
		strings.Join(rows[1], "|") != "coredns|v1.11.1|v1.11.3|COMPLETED_WITH_ISSUES|-" {
		t.Errorf("plain rows = %q", rows)
	}

	// The human table is built from the same column set (pterm writes it to
	// its own writer, so compare the definitions).
	if got := strings.Join(columnTitles(updateResultColumns()), "|"); got != "ADDON|PREVIOUS|NEW|STATUS|UPDATE ID" {
		t.Errorf("human update table columns = %q", got)
	}
}
