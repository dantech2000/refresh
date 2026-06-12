package clusterview

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

// ── SortClusterSummaries ──────────────────────────────────────────────────────

func TestSortClusterSummaries_ByName(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "zebra"}, {Name: "alpha"}, {Name: "mango"}}
	got := SortClusterSummaries(items, "name", false)
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortClusterSummaries_ByNameDesc(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "alpha"}, {Name: "zebra"}, {Name: "mango"}}
	got := SortClusterSummaries(items, "name", true)
	want := []string{"zebra", "mango", "alpha"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortClusterSummaries_ByVersion(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Version: "1.29"}, {Name: "a", Version: "1.31"}, {Name: "c", Version: "1.30"}}
	got := SortClusterSummaries(items, "version", false)
	if got[0].Version != "1.29" {
		t.Errorf("first item should be 1.29, got %q", got[0].Version)
	}
}

func TestSortClusterSummaries_ByStatus(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Status: "UPDATING"}, {Name: "a", Status: "ACTIVE"}}
	got := SortClusterSummaries(items, "status", false)
	if got[0].Status != "ACTIVE" {
		t.Errorf("first item should be ACTIVE, got %q", got[0].Status)
	}
}

func TestSortClusterSummaries_ByRegion(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "b", Region: "us-west-2"}, {Name: "a", Region: "ap-southeast-1"}}
	got := SortClusterSummaries(items, "region", false)
	if got[0].Region != "ap-southeast-1" {
		t.Errorf("region asc: first = %q, want ap-southeast-1", got[0].Region)
	}
}

func TestSortClusterSummaries_UnknownKeyFallsBackToName(t *testing.T) {
	items := []clustersvc.ClusterSummary{{Name: "z"}, {Name: "a"}}
	got := SortClusterSummaries(items, "bogus", false)
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

// ── treeStatusWithHealth ──────────────────────────────────────────────────────

// Regression: when the health checker returns a HealthSummary whose Decision
// is empty/unrecognized, the tree view must NOT mask the underlying cluster
// status as "UNKNOWN". Show the raw status plus a "(health unknown)" hint
// so the operator can still tell the cluster is ACTIVE/CREATING/etc.
func TestTreeStatusWithHealth_UnknownDecisionPreservesClusterStatus(t *testing.T) {
	h := &health.HealthSummary{Decision: ""} // health checker returned no decision
	got := treeStatusWithHealth("ACTIVE", h)
	if got != "ACTIVE (health unknown)" {
		t.Errorf("got %q, want %q", got, "ACTIVE (health unknown)")
	}
}

func TestTreeStatusWithHealth_KnownDecisionReplacesStatus(t *testing.T) {
	cases := []struct {
		d    health.Decision
		want string
	}{
		{health.DecisionProceed, "HEALTHY"},
		{health.DecisionWarn, "WARNING"},
		{health.DecisionBlock, "CRITICAL"},
	}
	for _, c := range cases {
		got := treeStatusWithHealth("ACTIVE", &health.HealthSummary{Decision: c.d})
		if got != c.want {
			t.Errorf("decision %q: got %q, want %q", c.d, got, c.want)
		}
	}
}

func TestTreeStatusWithHealth_NilSummaryReturnsClusterStatus(t *testing.T) {
	got := treeStatusWithHealth("CREATING", nil)
	if got != "CREATING" {
		t.Errorf("got %q, want CREATING (nil summary should pass through)", got)
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

// ── table/tree outputs ────────────────────────────────────────────────────────

func TestOutputClustersTable(t *testing.T) {
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
				return OutputClustersTree(tc.items, time.Second, tc.multiRegion, tc.showHealth)
			}); err != nil {
				t.Fatalf("tree error: %v", err)
			}
		})
	}
}

func TestOutputClusterDetailsTable(t *testing.T) {
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

	out, err := captureStdout(t, func() error { return OutputClusterDetailsTable(details, time.Second) })
	if err != nil {
		t.Fatalf("table error: %v", err)
	}
	for _, want := range []string{"Cluster Information", "prod", "1.30", "vpc-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q: %q", want, out)
		}
	}
}
