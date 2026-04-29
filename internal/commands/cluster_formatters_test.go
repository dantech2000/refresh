package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
)

// ── sortClusterSummaries ──────────────────────────────────────────────────────

func TestSortClusterSummaries_ByName(t *testing.T) {
	items := []cluster.ClusterSummary{
		{Name: "zebra"},
		{Name: "alpha"},
		{Name: "mango"},
	}
	got := sortClusterSummaries(items, "name", false)
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortClusterSummaries_ByNameDesc(t *testing.T) {
	items := []cluster.ClusterSummary{
		{Name: "alpha"},
		{Name: "zebra"},
		{Name: "mango"},
	}
	got := sortClusterSummaries(items, "name", true)
	if got[0].Name != "zebra" {
		t.Errorf("first item should be zebra, got %q", got[0].Name)
	}
}

func TestSortClusterSummaries_ByVersion(t *testing.T) {
	items := []cluster.ClusterSummary{
		{Name: "b", Version: "1.29"},
		{Name: "a", Version: "1.31"},
		{Name: "c", Version: "1.30"},
	}
	got := sortClusterSummaries(items, "version", false)
	if got[0].Version != "1.29" {
		t.Errorf("first item should be 1.29, got %q", got[0].Version)
	}
}

func TestSortClusterSummaries_ByStatus(t *testing.T) {
	items := []cluster.ClusterSummary{
		{Name: "b", Status: "UPDATING"},
		{Name: "a", Status: "ACTIVE"},
	}
	got := sortClusterSummaries(items, "status", false)
	if got[0].Status != "ACTIVE" {
		t.Errorf("first item should be ACTIVE, got %q", got[0].Status)
	}
}

func TestSortClusterSummaries_ByRegion(t *testing.T) {
	items := []cluster.ClusterSummary{
		{Name: "b", Region: "us-west-2"},
		{Name: "a", Region: "ap-southeast-1"},
	}
	got := sortClusterSummaries(items, "region", false)
	if got[0].Region != "ap-southeast-1" {
		t.Errorf("region asc: first = %q, want ap-southeast-1", got[0].Region)
	}
}

func TestSortClusterSummaries_UnknownKeyFallsBackToName(t *testing.T) {
	items := []cluster.ClusterSummary{{Name: "z"}, {Name: "a"}}
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
	diffs := []cluster.Difference{
		{Severity: "critical"},
		{Severity: "warning"},
		{Severity: "info"},
		{Severity: "critical"},
	}

	got := filterDifferencesBySeverity(diffs, "critical")
	if len(got) != 2 {
		t.Errorf("expected 2 critical differences, got %d", len(got))
	}

	got = filterDifferencesBySeverity(diffs, "info")
	if len(got) != 1 {
		t.Errorf("expected 1 info difference, got %d", len(got))
	}

	got = filterDifferencesBySeverity(diffs, "none")
	if len(got) != 0 {
		t.Errorf("expected 0 differences for unknown severity, got %d", len(got))
	}
}

// ── formatNodeCount ───────────────────────────────────────────────────────────

func TestFormatNodeCount(t *testing.T) {
	cases := []struct {
		n    cluster.NodeCountInfo
		want string
	}{
		{cluster.NodeCountInfo{Ready: 0, Total: 0}, "0/0"},
		{cluster.NodeCountInfo{Ready: 3, Total: 3}, "3/3"},
		{cluster.NodeCountInfo{Ready: 0, Total: 3}, "0/3"},
		{cluster.NodeCountInfo{Ready: 2, Total: 3}, "2/3"},
	}
	for _, tc := range cases {
		got := formatNodeCount(tc.n)
		if !strings.Contains(got, tc.want) {
			t.Errorf("formatNodeCount(%v) = %q, want substring %q", tc.n, got, tc.want)
		}
	}
}

// ── truncateString ────────────────────────────────────────────────────────────

func TestTruncateString_BelowMax(t *testing.T) {
	if got := truncateString("hello", 10); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestTruncateString_AtMax(t *testing.T) {
	if got := truncateString("hello", 5); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestTruncateString_AboveMaxAddsEllipsis(t *testing.T) {
	got := truncateString("hello world", 8)
	if got != "hello..." {
		t.Errorf("got %q, want %q", got, "hello...")
	}
}

// ── formatHealth ─────────────────────────────────────────────────────────────

func TestFormatHealth_Nil(t *testing.T) {
	got := formatHealth(nil)
	if !strings.Contains(got, "UNKNOWN") {
		t.Errorf("nil health summary: got %q, want UNKNOWN", got)
	}
}

func TestFormatHealth_Proceed(t *testing.T) {
	h := &health.HealthSummary{
		Decision: health.DecisionProceed,
		Results:  []health.HealthResult{{Status: health.StatusPass}},
	}
	got := formatHealth(h)
	if !strings.Contains(got, "PASS") {
		t.Errorf("proceed decision: got %q, want PASS", got)
	}
}

func TestFormatHealth_Warn(t *testing.T) {
	h := &health.HealthSummary{
		Decision: health.DecisionWarn,
		Warnings: []string{"some warning"},
	}
	got := formatHealth(h)
	if !strings.Contains(got, "WARN") {
		t.Errorf("warn decision: got %q, want WARN", got)
	}
}

func TestFormatHealth_Block(t *testing.T) {
	h := &health.HealthSummary{
		Decision: health.DecisionBlock,
		Errors:   []string{"critical issue"},
	}
	got := formatHealth(h)
	if !strings.Contains(got, "FAIL") {
		t.Errorf("block decision: got %q, want FAIL", got)
	}
}

func TestFormatHealth_DefaultDecision(t *testing.T) {
	h := &health.HealthSummary{Decision: "SOME_OTHER_DECISION"}
	got := formatHealth(h)
	if !strings.Contains(got, "UNKNOWN") {
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
	got := formatClusterHealth(nil)
	if !strings.Contains(got, "UNKNOWN") {
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
