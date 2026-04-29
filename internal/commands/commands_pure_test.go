package commands

import (
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
)

// ──────────────────────────────────────────────────────────────────────────────
// orDash
// ──────────────────────────────────────────────────────────────────────────────

func TestOrDash_EmptyReturnsHyphen(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(%q) = %q, want %q", "", got, "-")
	}
}

func TestOrDash_NonEmptyReturnsInput(t *testing.T) {
	if got := orDash("hello"); got != "hello" {
		t.Errorf("orDash(%q) = %q, want %q", "hello", got, "hello")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// removeDuplicates
// ──────────────────────────────────────────────────────────────────────────────

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
	got := removeDuplicates(in)
	if len(got) != 3 {
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

// ──────────────────────────────────────────────────────────────────────────────
// sortNodegroupSummaries
// ──────────────────────────────────────────────────────────────────────────────

func TestSortNodegroupSummaries_ByName(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "zebra"},
		{Name: "alpha"},
		{Name: "mango"},
	}
	got := sortNodegroupSummaries(items, "name", false)
	if got[0].Name != "alpha" {
		t.Errorf("first = %q, want alpha", got[0].Name)
	}
}

func TestSortNodegroupSummaries_ByNameDesc(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "alpha"},
		{Name: "zebra"},
	}
	got := sortNodegroupSummaries(items, "name", true)
	if got[0].Name != "zebra" {
		t.Errorf("desc first = %q, want zebra", got[0].Name)
	}
}

func TestSortNodegroupSummaries_ByStatus(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "b", Status: "UPDATING"},
		{Name: "a", Status: "ACTIVE"},
	}
	got := sortNodegroupSummaries(items, "status", false)
	if got[0].Status != "ACTIVE" {
		t.Errorf("status asc: first = %q, want ACTIVE", got[0].Status)
	}
}

func TestSortNodegroupSummaries_ByInstance(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "b", InstanceType: "t3.xlarge"},
		{Name: "a", InstanceType: "m5.large"},
	}
	got := sortNodegroupSummaries(items, "instance", false)
	if got[0].InstanceType != "m5.large" {
		t.Errorf("instance asc: first = %q, want m5.large", got[0].InstanceType)
	}
}

func TestSortNodegroupSummaries_ByNodes(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "b", ReadyNodes: 5},
		{Name: "a", ReadyNodes: 1},
	}
	got := sortNodegroupSummaries(items, "nodes", false)
	if got[0].ReadyNodes != 1 {
		t.Errorf("nodes asc: first ReadyNodes = %d, want 1", got[0].ReadyNodes)
	}
}

func TestSortNodegroupSummaries_ByCPU(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "b", Metrics: nodegroup.SummaryMetrics{CPU: 80}},
		{Name: "a", Metrics: nodegroup.SummaryMetrics{CPU: 20}},
	}
	got := sortNodegroupSummaries(items, "cpu", false)
	if got[0].Metrics.CPU != 20 {
		t.Errorf("cpu asc: first CPU = %f, want 20", got[0].Metrics.CPU)
	}
}

func TestSortNodegroupSummaries_ByCost(t *testing.T) {
	items := []nodegroup.NodegroupSummary{
		{Name: "b", Cost: nodegroup.SummaryCost{Monthly: 500}},
		{Name: "a", Cost: nodegroup.SummaryCost{Monthly: 100}},
	}
	got := sortNodegroupSummaries(items, "cost", false)
	if got[0].Cost.Monthly != 100 {
		t.Errorf("cost asc: first Monthly = %f, want 100", got[0].Cost.Monthly)
	}
}

func TestSortNodegroupSummaries_UnknownKeyByName(t *testing.T) {
	items := []nodegroup.NodegroupSummary{{Name: "z"}, {Name: "a"}}
	got := sortNodegroupSummaries(items, "bogus", false)
	if got[0].Name != "a" {
		t.Errorf("unknown key should sort by name, got %q", got[0].Name)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// mapAddonHealth (list_addons.go)
// ──────────────────────────────────────────────────────────────────────────────

func TestMapAddonHealth_Active(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusActive)
	if !strings.Contains(got, "PASS") {
		t.Errorf("Active: got %q, want PASS", got)
	}
}

func TestMapAddonHealth_Degraded(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusDegraded)
	if !strings.Contains(got, "FAIL") {
		t.Errorf("Degraded: got %q, want FAIL", got)
	}
}

func TestMapAddonHealth_Creating(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusCreating)
	if !strings.Contains(got, "IN PROGRESS") {
		t.Errorf("Creating: got %q, want IN PROGRESS", got)
	}
}

func TestMapAddonHealth_Unknown(t *testing.T) {
	got := mapAddonHealth("SOMETHING_ELSE")
	if !strings.Contains(got, "UNKNOWN") {
		t.Errorf("Unknown: got %q, want UNKNOWN", got)
	}
}
