package addon

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/services/addons"
)

func TestOutputAddonDetailsTable_FullDetails(t *testing.T) {
	d := &addons.AddonDetails{
		Name:               "vpc-cni",
		Version:            "v1.18.3",
		Status:             "ACTIVE",
		Health:             "PASS",
		ARN:                "arn:aws:eks:us-east-1:123:addon/x",
		ServiceAccountRole: "arn:aws:iam::123:role/cni",
		Issues: []addons.AddonIssue{
			{Code: "AccessDenied", Message: "missing permission"},
		},
		Configuration: map[string]any{"env": map[string]any{"ENABLE_PREFIX_DELEGATION": "true"}},
	}
	out, err := captureStdout(t, func() error { return outputAddonDetailsTable("prod", d) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"vpc-cni", "v1.18.3", "arn:aws:eks", "Service Account Role", "Issues:", "AccessDenied", "Configuration:", "ENABLE_PREFIX_DELEGATION"} {
		if !strings.Contains(out, want) {
			t.Errorf("details output missing %q; got:\n%s", want, out)
		}
	}
}

func TestOutputAddonDetailsTable_Minimal(t *testing.T) {
	d := &addons.AddonDetails{Name: "coredns", Version: "v1.10", Status: "ACTIVE"}
	out, err := captureStdout(t, func() error { return outputAddonDetailsTable("prod", d) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "coredns") {
		t.Errorf("expected addon name, got: %q", out)
	}
	// No optional sections should appear.
	if strings.Contains(out, "Issues:") || strings.Contains(out, "Configuration:") {
		t.Errorf("minimal details should omit empty sections, got:\n%s", out)
	}
}

func TestOutputUpdateAllResults_Empty(t *testing.T) {
	// The "No addons to update" notice goes via color.Yellow (color.Output),
	// which this helper doesn't capture; assert on the header (ui.Outf → stdout)
	// and that the call returns cleanly.
	out, err := captureStdout(t, func() error { return outputUpdateAllResults("prod", nil, false) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Addon Updates for cluster: prod") {
		t.Errorf("expected the cluster header, got: %q", out)
	}
}

func TestOutputUpdateAllResults_DryRunMode(t *testing.T) {
	results := []addons.AddonUpdateResult{
		{AddonName: "vpc-cni", PreviousVersion: "v1", NewVersion: "v2", Status: "DRY_RUN"},
	}
	out, err := captureStdout(t, func() error { return outputUpdateAllResults("prod", results, true) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "DRY RUN") {
		t.Errorf("dry-run header expected, got: %q", out)
	}
	// Dry run suppresses the success/fail summary line.
	if strings.Contains(out, "Summary:") {
		t.Errorf("dry run should not print a Summary line, got: %q", out)
	}
}

func TestOutputUpdateAllResults_SummaryCounts(t *testing.T) {
	results := []addons.AddonUpdateResult{
		{AddonName: "a", PreviousVersion: "v1", NewVersion: "v2", Status: "COMPLETED"},
		{AddonName: "b", PreviousVersion: "v1", NewVersion: "v1", Status: "UPDATE_FAILED"},
		{AddonName: "c", PreviousVersion: "v1", NewVersion: "v2", Status: "COMPLETED_WITH_ISSUES"},
	}
	out, err := captureStdout(t, func() error { return outputUpdateAllResults("prod", results, false) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Summary:") {
		t.Errorf("expected a summary line, got: %q", out)
	}
	for _, want := range []string{"successful", "with issues", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q; got: %q", want, out)
		}
	}
}
