package cluster

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
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

func sampleSummaries() []clustersvc.ClusterSummary {
	return []clustersvc.ClusterSummary{
		{
			Name:      "prod",
			Status:    "ACTIVE",
			Version:   "1.30",
			Region:    "us-east-1",
			Health:    &health.HealthSummary{Decision: health.DecisionProceed},
			NodeCount: clustersvc.NodeCountInfo{Ready: 3, Total: 3},
		},
		{
			Name:      "stage",
			Status:    "UPDATING",
			Version:   "1.29",
			Region:    "",
			Health:    &health.HealthSummary{Decision: health.DecisionBlock},
			NodeCount: clustersvc.NodeCountInfo{Ready: 0, Total: 2},
		},
	}
}

// ── sortClusterSummaries ──────────────────────────────────────────────────────

func TestSortClusterSummaries_ByName(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "zebra"}, {Name: "alpha"}, {Name: "mango"}}
	got := sortClusterSummaries(items, "name", false)
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortClusterSummaries_ByNameDesc(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "alpha"}, {Name: "zebra"}, {Name: "mango"}}
	got := sortClusterSummaries(items, "name", true)
	want := []string{"zebra", "mango", "alpha"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortClusterSummaries_ByVersion(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Version: "1.29"}, {Name: "a", Version: "1.31"}, {Name: "c", Version: "1.30"}}
	got := sortClusterSummaries(items, "version", false)
	if got[0].Version != "1.29" {
		t.Errorf("first item should be 1.29, got %q", got[0].Version)
	}
}

func TestSortClusterSummaries_ByStatus(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Status: "UPDATING"}, {Name: "a", Status: "ACTIVE"}}
	got := sortClusterSummaries(items, "status", false)
	if got[0].Status != "ACTIVE" {
		t.Errorf("first item should be ACTIVE, got %q", got[0].Status)
	}
}

func TestSortClusterSummaries_ByRegion(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Region: "us-west-2"}, {Name: "a", Region: "ap-southeast-1"}}
	got := sortClusterSummaries(items, "region", false)
	if got[0].Region != "ap-southeast-1" {
		t.Errorf("region asc: first = %q, want ap-southeast-1", got[0].Region)
	}
}

func TestSortClusterSummaries_UnknownKeyFallsBackToName(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "z"}, {Name: "a"}}
	got := sortClusterSummaries(items, "bogus", false)
	if got[0].Name != "a" {
		t.Errorf("unknown key should sort by name, got %q", got[0].Name)
	}
}

// ── formatStatus ─────────────────────────────────────────────────────────────

func TestFormatStatus(t *testing.T) {
	cases := map[string]string{
		"ACTIVE":   "Active",
		"active":   "Active",
		"CREATING": "Creating",
		"UPDATING": "Updating",
		"DELETING": "Deleting",
		"FAILED":   "Failed",
		"unknown":  "unknown",
	}
	for input, want := range cases {
		got := formatStatus(input)
		if !strings.Contains(got, want) {
			t.Errorf("formatStatus(%q) = %q, want substring %q", input, got, want)
		}
	}
}

// ── truncateEndpoint ─────────────────────────────────────────────────────────

func TestTruncateEndpoint(t *testing.T) {
	short := "https://example.com"
	if truncateEndpoint(short) != short {
		t.Errorf("short endpoint should be unchanged")
	}
	long := strings.Repeat("x", 121)
	got := truncateEndpoint(long)
	if len(got) != 120 {
		t.Errorf("truncated endpoint should be 120 chars, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated endpoint should end with '...'")
	}
}

// ── formatAge ────────────────────────────────────────────────────────────────

func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{25 * time.Hour, "days"},
		{3 * time.Hour, "hours"},
		{45 * time.Minute, "minutes"},
	}
	for _, tc := range cases {
		got := formatAge(tc.d)
		if !strings.Contains(got, tc.want) {
			t.Errorf("formatAge(%v) = %q, want substring %q", tc.d, got, tc.want)
		}
	}
}

// ── filterDifferencesBySeverity ───────────────────────────────────────────────

func TestFilterDifferencesBySeverity(t *testing.T) {
	diffs := []clustersvc.Difference{
		{Severity: "critical"},
		{Severity: "warning"},
		{Severity: "info"},
		{Severity: "critical"},
	}
	if got := filterDifferencesBySeverity(diffs, "critical"); len(got) != 2 {
		t.Errorf("expected 2 critical differences, got %d", len(got))
	}
	if got := filterDifferencesBySeverity(diffs, "info"); len(got) != 1 {
		t.Errorf("expected 1 info difference, got %d", len(got))
	}
	if got := filterDifferencesBySeverity(diffs, "none"); len(got) != 0 {
		t.Errorf("expected 0 differences for unknown severity, got %d", len(got))
	}
}

// ── formatNodeCount ───────────────────────────────────────────────────────────

func TestFormatNodeCount(t *testing.T) {
	cases := []struct {
		n    clustersvc.NodeCountInfo
		want string
	}{
		{clustersvc.NodeCountInfo{Ready: 0, Total: 0}, "0/0"},
		{clustersvc.NodeCountInfo{Ready: 3, Total: 3}, "3/3"},
		{clustersvc.NodeCountInfo{Ready: 0, Total: 3}, "0/3"},
		{clustersvc.NodeCountInfo{Ready: 2, Total: 3}, "2/3"},
	}
	for _, tc := range cases {
		got := formatNodeCount(tc.n)
		if !strings.Contains(got, tc.want) {
			t.Errorf("formatNodeCount(%v) = %q, want substring %q", tc.n, got, tc.want)
		}
	}
}

// ── truncate ─────────────────────────────────────────────────────────────────

func TestTruncate_BelowMax(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestTruncate_AtMax(t *testing.T) {
	if got := truncate("hello", 5); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestTruncate_AboveMaxAddsEllipsis(t *testing.T) {
	if got := truncate("hello world", 8); got != "hello..." {
		t.Errorf("got %q, want %q", got, "hello...")
	}
}

// ── formatHealth ─────────────────────────────────────────────────────────────

func TestFormatHealth_Nil(t *testing.T) {
	if got := formatHealth(nil); !strings.Contains(got, "UNKNOWN") {
		t.Errorf("nil health summary: got %q, want UNKNOWN", got)
	}
}

func TestFormatHealth_Proceed(t *testing.T) {
	h := &health.HealthSummary{Decision: health.DecisionProceed, Results: []health.HealthResult{{Status: health.StatusPass}}}
	if got := formatHealth(h); !strings.Contains(got, "PASS") {
		t.Errorf("proceed decision: got %q, want PASS", got)
	}
}

func TestFormatHealth_Warn(t *testing.T) {
	h := &health.HealthSummary{Decision: health.DecisionWarn, Warnings: []string{"some warning"}}
	if got := formatHealth(h); !strings.Contains(got, "WARN") {
		t.Errorf("warn decision: got %q, want WARN", got)
	}
}

func TestFormatHealth_Block(t *testing.T) {
	h := &health.HealthSummary{Decision: health.DecisionBlock, Errors: []string{"critical issue"}}
	if got := formatHealth(h); !strings.Contains(got, "FAIL") {
		t.Errorf("block decision: got %q, want FAIL", got)
	}
}

func TestFormatHealth_DefaultDecision(t *testing.T) {
	h := &health.HealthSummary{Decision: "SOME_OTHER_DECISION"}
	if got := formatHealth(h); !strings.Contains(got, "UNKNOWN") {
		t.Errorf("unknown decision: got %q, want UNKNOWN", got)
	}
}

// ── formatAddonHealth ─────────────────────────────────────────────────────────

func TestFormatAddonHealth(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Healthy", "PASS"},
		{"Issues", "FAIL"},
		{"Failed", "FAIL"},
		{"Updating", "IN PROGRESS"},
		{"Unknown", "UNKNOWN"},
	}
	for _, c := range cases {
		got := formatAddonHealth(c.input)
		if !strings.Contains(got, c.want) {
			t.Errorf("formatAddonHealth(%q) = %q, want substring %q", c.input, got, c.want)
		}
	}
}

// ── formatClusterHealth ───────────────────────────────────────────────────────

func TestFormatClusterHealth_Nil(t *testing.T) {
	if got := formatClusterHealth(nil); !strings.Contains(got, "UNKNOWN") {
		t.Errorf("nil: got %q, want UNKNOWN", got)
	}
}

func TestFormatClusterHealth_Decisions(t *testing.T) {
	cases := []struct {
		d    health.Decision
		want string
	}{
		{health.DecisionProceed, "PASS"},
		{health.DecisionWarn, "WARN"},
		{health.DecisionBlock, "FAIL"},
		{"OTHER", "UNKNOWN"},
	}
	for _, c := range cases {
		h := &health.HealthSummary{Decision: c.d}
		got := formatClusterHealth(h)
		if !strings.Contains(got, c.want) {
			t.Errorf("formatClusterHealth(Decision=%q) = %q, want %q", c.d, got, c.want)
		}
	}
}

// ── formatDifferenceCount ─────────────────────────────────────────────────────

func TestFormatDifferenceCount_Zero(t *testing.T) {
	if got := formatDifferenceCount(0, "critical"); got != "0" {
		t.Errorf("zero count: got %q, want %q", got, "0")
	}
}

func TestFormatDifferenceCount_Nonzero(t *testing.T) {
	for _, sev := range []string{"critical", "warning", "info", "other"} {
		got := formatDifferenceCount(3, sev)
		if !strings.Contains(got, "3") {
			t.Errorf("formatDifferenceCount(3, %q) = %q, want to contain 3", sev, got)
		}
	}
}

// ── removeDuplicates ─────────────────────────────────────────────────────────

func TestRemoveDuplicates_Empty(t *testing.T) {
	if got := removeDuplicates(nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestRemoveDuplicates_NoDuplicates(t *testing.T) {
	in := []string{"a", "b", "c"}
	if got := removeDuplicates(in); len(got) != 3 {
		t.Errorf("no-dup input: got %v, want 3 items", got)
	}
}

func TestRemoveDuplicates_WithDuplicates(t *testing.T) {
	in := []string{"a", "b", "a", "c", "b"}
	if got := removeDuplicates(in); len(got) != 3 {
		t.Errorf("expected 3 unique items, got %d: %v", len(got), got)
	}
}

func TestRemoveDuplicates_PreservesOrder(t *testing.T) {
	in := []string{"c", "a", "b", "a"}
	got := removeDuplicates(in)
	if got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Errorf("order not preserved: got %v", got)
	}
}

// ── output functions ──────────────────────────────────────────────────────────

func TestClusterListOutputs(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputClustersJSON(sampleSummaries()) })
		if err != nil {
			t.Fatalf("JSON output error: %v", err)
		}
		for _, want := range []string{"prod", "stage", "1.30", "1.29", "us-east-1", "count"} {
			if !strings.Contains(out, want) {
				t.Errorf("JSON output missing %q: %q", want, out)
			}
		}
	})

	t.Run("yaml", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputClustersYAML(sampleSummaries()) })
		if err != nil {
			t.Fatalf("YAML output error: %v", err)
		}
		for _, want := range []string{"prod", "stage", "1.30", "1.29", "us-east-1"} {
			if !strings.Contains(out, want) {
				t.Errorf("YAML output missing %q: %q", want, out)
			}
		}
	})

	// Note: pterm table/tree rows are rendered directly to the original os.Stdout file
	// handle (not the captured pipe), so we can only assert on ui.Outf header/footer
	// content, not individual cluster names/versions within pterm-rendered rows.
	for _, tc := range []struct {
		name           string
		multiRegion    bool
		showHealth     bool
		items          []clustersvc.ClusterSummary
		headerContains string
	}{
		// "No EKS clusters found" is printed via color.Yellow (original stdout handle, not captured).
		{"empty", false, false, nil, ""},
		{"single-no-health", false, false, sampleSummaries()[:1], "1 cluster"},
		{"single-health", false, true, sampleSummaries(), "2 cluster"},
		{"multi-region-health", true, true, sampleSummaries(), "2 cluster"},
	} {
		t.Run("table/"+tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return OutputClustersTable(tc.items, time.Second, tc.multiRegion, tc.showHealth)
			})
			if err != nil {
				t.Fatalf("table error: %v", err)
			}
			if tc.headerContains != "" && !strings.Contains(out, tc.headerContains) {
				t.Errorf("table header missing %q: %q", tc.headerContains, out)
			}
		})

		t.Run("tree/"+tc.name, func(t *testing.T) {
			if _, err := captureStdout(t, func() error {
				return outputClustersTree(tc.items, time.Second, tc.multiRegion, tc.showHealth)
			}); err != nil {
				t.Fatalf("tree error: %v", err)
			}
		})
	}
}

func TestClusterDescribeOutputs(t *testing.T) {
	details := &clustersvc.ClusterDetails{
		Name:            "prod",
		Status:          "ACTIVE",
		Version:         "1.30",
		PlatformVersion: "eks.1",
		Endpoint:        strings.Repeat("x", 140),
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		Health:          &health.HealthSummary{Decision: health.DecisionWarn},
		Networking: clustersvc.NetworkingInfo{
			VpcId:            "vpc-1",
			VpcCidr:          "10.0.0.0/16",
			SubnetIds:        []string{"subnet-1"},
			SecurityGroupIds: []string{"sg-1"},
		},
		Security: clustersvc.SecurityInfo{
			EncryptionEnabled:  true,
			LoggingEnabled:     []string{"api"},
			DeletionProtection: true,
		},
		Addons:     []clustersvc.AddonInfo{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE"}},
		Nodegroups: []clustersvc.NodegroupSummary{{Name: "ng-workers", Status: "ACTIVE", ReadyNodes: 2}},
	}

	wantFields := []string{"prod", "1.30", "eks.1", "vpc-1", "vpc-cni", "ng-workers"}

	t.Run("json", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputClusterDetailsJSON(details) })
		if err != nil {
			t.Fatalf("JSON error: %v", err)
		}
		for _, want := range wantFields {
			if !strings.Contains(out, want) {
				t.Errorf("JSON output missing %q: %q", want, out)
			}
		}
	})

	t.Run("yaml", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputClusterDetailsYAML(details) })
		if err != nil {
			t.Fatalf("YAML error: %v", err)
		}
		for _, want := range wantFields {
			if !strings.Contains(out, want) {
				t.Errorf("YAML output missing %q: %q", want, out)
			}
		}
	})

	t.Run("table", func(t *testing.T) {
		// outputClusterDetailsTable uses fmt.Printf / ui.Outf for section headers and key-value
		// pairs directly, so most content IS captured (unlike the pterm-based list tables).
		out, err := captureStdout(t, func() error { return outputClusterDetailsTable(details, time.Second) })
		if err != nil {
			t.Fatalf("table error: %v", err)
		}
		for _, want := range []string{"Cluster Information", "prod", "1.30", "vpc-1"} {
			if !strings.Contains(out, want) {
				t.Errorf("table output missing %q: %q", want, out)
			}
		}
	})
}

func TestComparisonOutputs(t *testing.T) {
	comparison := &clustersvc.ClusterComparison{
		Clusters: []clustersvc.ClusterDetails{
			{Name: "prod", Status: "ACTIVE", Version: "1.30", Health: &health.HealthSummary{Decision: health.DecisionProceed}},
			{Name: "stage", Status: "ACTIVE", Version: "1.29", Health: &health.HealthSummary{Decision: health.DecisionBlock}},
		},
		Differences: []clustersvc.Difference{
			{Field: "version", Severity: "critical", Description: "version differs", Values: []clustersvc.ValuePair{{ClusterName: "prod", Value: "1.30"}}},
			{Field: "logging", Severity: "warning", Description: "logging differs"},
			{Field: "tag", Severity: "info", Description: "tag differs"},
		},
		Summary: clustersvc.ComparisonSummary{
			TotalDifferences:    3,
			CriticalDifferences: 1,
			WarningDifferences:  1,
			InfoDifferences:     1,
		},
	}

	wantFields := []string{"prod", "stage", "version", "logging", "critical"}

	t.Run("json", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputComparisonJSON(comparison) })
		if err != nil {
			t.Fatalf("JSON error: %v", err)
		}
		for _, want := range wantFields {
			if !strings.Contains(out, want) {
				t.Errorf("comparison JSON missing %q: %q", want, out)
			}
		}
	})

	t.Run("yaml", func(t *testing.T) {
		out, err := captureStdout(t, func() error { return outputComparisonYAML(comparison) })
		if err != nil {
			t.Fatalf("YAML error: %v", err)
		}
		for _, want := range wantFields {
			if !strings.Contains(out, want) {
				t.Errorf("comparison YAML missing %q: %q", want, out)
			}
		}
	})

	t.Run("table", func(t *testing.T) {
		// outputComparisonTable uses ui.Outf for headers and printDifferences for detail rows.
		out, err := captureStdout(t, func() error { return outputComparisonTable(comparison, time.Second) })
		if err != nil {
			t.Fatalf("comparison table error: %v", err)
		}
		// The header and differences section headers come from ui.Outf calls.
		if !strings.Contains(out, "Comparison") {
			t.Errorf("comparison table missing header 'Comparison': %q", out)
		}
	})
	comparison.Differences = nil
	comparison.Summary = clustersvc.ComparisonSummary{ClustersAreEquivalent: true}
	if _, err := captureStdout(t, func() error { return outputComparisonTable(comparison, time.Second) }); err != nil {
		t.Fatalf("identical comparison table error: %v", err)
	}
}
