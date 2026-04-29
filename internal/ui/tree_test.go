package ui

import (
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// FormatTreeSummary
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatTreeSummary_ContainsCount(t *testing.T) {
	s := FormatTreeSummary(3, "clusters", 1.23)
	if !strings.Contains(s, "3") {
		t.Errorf("FormatTreeSummary should include count, got %q", s)
	}
	if !strings.Contains(s, "clusters") {
		t.Errorf("FormatTreeSummary should include item type, got %q", s)
	}
}

func TestFormatTreeSummary_Duration(t *testing.T) {
	s := FormatTreeSummary(1, "x", 2.5)
	if !strings.Contains(s, "2.5") {
		t.Errorf("FormatTreeSummary should include duration, got %q", s)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// TreePrefix
// ──────────────────────────────────────────────────────────────────────────────

func TestTreePrefixes_AllNonEmpty(t *testing.T) {
	cases := map[string]string{
		"Cluster":   Prefixes.Cluster(),
		"Nodegroup": Prefixes.Nodegroup(),
		"Instance":  Prefixes.Instance(),
		"Region":    Prefixes.Region(),
		"World":     Prefixes.World(),
		"Addon":     Prefixes.Addon(),
		"Network":   Prefixes.Network(),
		"Security":  Prefixes.Security(),
		"Config":    Prefixes.Config(),
		"Compare":   Prefixes.Compare(),
		"Success":   Prefixes.Success(),
		"Warning":   Prefixes.Warning(),
		"Error":     Prefixes.Error(),
		"Unknown":   Prefixes.Unknown(),
	}
	for name, val := range cases {
		if val == "" {
			t.Errorf("Prefixes.%s() returned empty string", name)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// TreeBuilder structural methods
// ──────────────────────────────────────────────────────────────────────────────

func TestNewTreeBuilder_EmptyList(t *testing.T) {
	tb := NewTreeBuilder()
	if len(tb.leveledList) != 0 {
		t.Errorf("new builder should have empty list, got %d items", len(tb.leveledList))
	}
}

func TestTreeBuilder_AddRoot_LevelZero(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("root")
	if len(tb.leveledList) != 1 {
		t.Fatalf("expected 1 item, got %d", len(tb.leveledList))
	}
	if tb.leveledList[0].Level != 0 {
		t.Errorf("root level = %d, want 0", tb.leveledList[0].Level)
	}
	if tb.leveledList[0].Text != "root" {
		t.Errorf("root text = %q, want %q", tb.leveledList[0].Text, "root")
	}
}

func TestTreeBuilder_AddChild_IncreasesLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("root").AddChild("child")
	if len(tb.leveledList) != 2 {
		t.Fatalf("expected 2 items, got %d", len(tb.leveledList))
	}
	if tb.leveledList[1].Level != 1 {
		t.Errorf("child level = %d, want 1", tb.leveledList[1].Level)
	}
}

func TestTreeBuilder_AddSibling_SameLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("root").AddChild("child1").AddSibling("child2")
	// child1 and child2 should be at level 1
	if tb.leveledList[1].Level != 1 || tb.leveledList[2].Level != 1 {
		t.Errorf("siblings should be at same level, got %d and %d",
			tb.leveledList[1].Level, tb.leveledList[2].Level)
	}
}

func TestTreeBuilder_Up_DecreasesLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("root").AddChild("c1").AddChild("c2")
	// now at level 2
	tb.Up()
	if tb.level != 1 {
		t.Errorf("level after Up = %d, want 1", tb.level)
	}
}

func TestTreeBuilder_Up_NeverBelowZero(t *testing.T) {
	tb := NewTreeBuilder()
	tb.Up().Up().Up()
	if tb.level != 0 {
		t.Errorf("level should not go below 0, got %d", tb.level)
	}
}

func TestTreeBuilder_UpTo_SetsLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r").AddChild("c1").AddChild("c2").AddChild("c3")
	// level is 3 now
	tb.UpTo(1)
	if tb.level != 1 {
		t.Errorf("UpTo(1): level = %d, want 1", tb.level)
	}
}

func TestTreeBuilder_UpTo_NoChangeIfHigher(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r").AddChild("c")
	// level is 1; UpTo(2) should not change it
	tb.UpTo(2)
	if tb.level != 1 {
		t.Errorf("UpTo(2) should not increase level, got %d", tb.level)
	}
}

func TestTreeBuilder_AddNode_CurrentLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r").AddChild("c")
	// level is now 1
	tb.AddNode("sibling")
	last := tb.leveledList[len(tb.leveledList)-1]
	if last.Level != 1 {
		t.Errorf("AddNode level = %d, want 1", last.Level)
	}
	if last.Text != "sibling" {
		t.Errorf("AddNode text = %q, want %q", last.Text, "sibling")
	}
}

func TestTreeBuilder_AddNodeWithIcon_NoColor(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.AddNodeWithIcon("★", "mynode", nil)
	last := tb.leveledList[len(tb.leveledList)-1]
	if !strings.Contains(last.Text, "★") || !strings.Contains(last.Text, "mynode") {
		t.Errorf("AddNodeWithIcon text = %q, expected icon and text present", last.Text)
	}
}

func TestTreeBuilder_AddNodeWithIcon_WithColor(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.AddNodeWithIcon("★", "mynode", func(s string) string { return "[" + s + "]" })
	last := tb.leveledList[len(tb.leveledList)-1]
	if !strings.HasPrefix(last.Text, "[") || !strings.HasSuffix(last.Text, "]") {
		t.Errorf("AddNodeWithIcon with color fn: text = %q, want wrapped in []", last.Text)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// AddStatus
// ──────────────────────────────────────────────────────────────────────────────

func testAddStatusText(status string) string {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.AddStatus("★", "mynode", status)
	return tb.leveledList[len(tb.leveledList)-1].Text
}

func TestAddStatus_ActiveContainsPass(t *testing.T) {
	for _, s := range []string{"ACTIVE", "RUNNING", "HEALTHY", "PASS", "SUCCESS", "ENABLED"} {
		text := testAddStatusText(s)
		if !strings.Contains(text, "PASS") {
			t.Errorf("AddStatus(%q): expected PASS in text, got %q", s, text)
		}
	}
}

func TestAddStatus_WarningContainsWarn(t *testing.T) {
	for _, s := range []string{"UPDATING", "PENDING", "SCALING", "WARN", "WARNING"} {
		text := testAddStatusText(s)
		if !strings.Contains(text, "WARN") {
			t.Errorf("AddStatus(%q): expected WARN in text, got %q", s, text)
		}
	}
}

func TestAddStatus_FailureContainsFail(t *testing.T) {
	for _, s := range []string{"FAILED", "ERROR", "CRITICAL", "FAIL", "DISABLED"} {
		text := testAddStatusText(s)
		if !strings.Contains(text, "FAIL") {
			t.Errorf("AddStatus(%q): expected FAIL in text, got %q", s, text)
		}
	}
}

func TestAddStatus_UnknownNoPrefix(t *testing.T) {
	text := testAddStatusText("SOME_RANDOM_STATUS")
	// Unknown status: no PASS/WARN/FAIL bracket prefix, just icon + text
	if strings.Contains(text, "[PASS]") || strings.Contains(text, "[WARN]") || strings.Contains(text, "[FAIL]") {
		t.Errorf("AddStatus(unknown): unexpected bracket prefix in %q", text)
	}
}

func TestAddStatus_LowercaseIsCaseInsensitive(t *testing.T) {
	text := testAddStatusText("active")
	if !strings.Contains(text, "PASS") {
		t.Errorf("AddStatus(active): expected PASS (case-insensitive), got %q", text)
	}
}

func TestAddStatus_EmptyIconAndStatus(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.AddStatus("", "mytext", "SOME_RANDOM")
	last := tb.leveledList[len(tb.leveledList)-1]
	if !strings.Contains(last.Text, "mytext") {
		t.Errorf("AddStatus with empty icon: text should contain 'mytext', got %q", last.Text)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Render — empty list returns error
// ──────────────────────────────────────────────────────────────────────────────

func TestTreeBuilder_Render_EmptyReturnsError(t *testing.T) {
	tb := NewTreeBuilder()
	if err := tb.Render(); err == nil {
		t.Error("Render on empty tree should return an error")
	}
}

func TestTreeBuilder_RenderWithContent(t *testing.T) {
	tb := NewTreeBuilder().AddRoot("root").AddChild("child")
	if err := tb.Render(); err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := tb.RenderWithTitle("Title"); err != nil {
		t.Fatalf("RenderWithTitle() = %v", err)
	}
}

func TestClusterTreeBuilder(t *testing.T) {
	builder := NewClusterTreeBuilder().
		AddCluster("prod", "ACTIVE", "1.30", 3).
		AddNodegroup("ng", "ACTIVE", "m5.large", 2, 3).
		AddInstance("i-123", "running", "us-east-1a").
		FinishNodegroup().
		AddAddon("vpc-cni", "v1", "ACTIVE")

	if len(builder.builder.leveledList) != 4 {
		t.Fatalf("cluster tree entries = %d", len(builder.builder.leveledList))
	}
	if err := builder.Render(); err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := builder.RenderWithTitle("Cluster"); err != nil {
		t.Fatalf("RenderWithTitle() = %v", err)
	}
}

func TestRegionTreeBuilder(t *testing.T) {
	builder := NewRegionTreeBuilder().
		AddRegion("us-east-1", 1).
		AddClusterToRegion("prod", "ACTIVE", 3).
		FinishRegion()

	if len(builder.builder.leveledList) != 2 {
		t.Fatalf("region tree entries = %d", len(builder.builder.leveledList))
	}
	if err := builder.Render(); err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := builder.RenderWithTitle("Regions"); err != nil {
		t.Fatalf("RenderWithTitle() = %v", err)
	}
}

func TestComparisonTreeBuilder(t *testing.T) {
	builder := NewComparisonTreeBuilder().AddComparisonRoot([]string{"a", "b"})
	for _, category := range []string{"configuration", "networking", "security", "nodegroups", "addons", "other"} {
		builder.AddDifferenceCategory(category).
			AddDifference("field", []string{"a", "b"}, "WARN").
			AddSimilarity("same", "value").
			FinishCategory()
	}

	if len(builder.builder.leveledList) == 0 {
		t.Fatal("comparison tree should have entries")
	}
	if err := builder.Render(); err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := builder.RenderWithTitle("Comparison"); err != nil {
		t.Fatalf("RenderWithTitle() = %v", err)
	}
}
