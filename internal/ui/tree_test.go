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

func TestTreeBuilder_UpTo_SetsLevel(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.level = 3
	tb.UpTo(1)
	if tb.level != 1 {
		t.Errorf("UpTo(1): level = %d, want 1", tb.level)
	}
}

func TestTreeBuilder_UpTo_NoChangeIfHigher(t *testing.T) {
	tb := NewTreeBuilder()
	tb.AddRoot("r")
	tb.level = 1
	// level is 1; UpTo(2) should not change it
	tb.UpTo(2)
	if tb.level != 1 {
		t.Errorf("UpTo(2) should not increase level, got %d", tb.level)
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
	tb := NewTreeBuilder().AddRoot("root")
	tb.level = 1
	tb.AddStatus("", "child", "ACTIVE")
	if err := tb.Render(); err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if err := tb.RenderWithTitle("Title"); err != nil {
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
	if err := builder.RenderWithTitle("Regions"); err != nil {
		t.Fatalf("RenderWithTitle() = %v", err)
	}
}

// Regression: clusters must nest one level beneath their region header even
// though AddRegion (via AddRoot) resets the builder level to 0.
func TestRegionTreeBuilderNestsClustersUnderRegions(t *testing.T) {
	builder := NewRegionTreeBuilder().
		AddRegion("us-east-1", 2).
		AddClusterToRegion("prod", "ACTIVE", 3).
		AddClusterToRegion("staging", "ACTIVE", 1).
		FinishRegion().
		AddRegion("us-west-2", 1).
		AddClusterToRegion("dev", "ACTIVE", 1).
		FinishRegion()

	list := builder.builder.leveledList
	wantLevels := []int{0, 1, 1, 0, 1}
	if len(list) != len(wantLevels) {
		t.Fatalf("tree entries = %d, want %d", len(list), len(wantLevels))
	}
	for i, want := range wantLevels {
		if list[i].Level != want {
			t.Errorf("entry %d (%q) level = %d, want %d", i, list[i].Text, list[i].Level, want)
		}
	}
}
