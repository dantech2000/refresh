package aws

import (
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// MatchingNodegroups
// ──────────────────────────────────────────────────────────────────────────────

func TestMatchingNodegroups_EmptyPatternReturnsAll(t *testing.T) {
	ngs := []string{"workers", "gpu-nodes", "spot-workers"}
	got := MatchingNodegroups(ngs, "")
	if len(got) != 3 {
		t.Errorf("empty pattern should return all %d nodegroups, got %d", len(ngs), len(got))
	}
}

func TestMatchingNodegroups_SubstringMatch(t *testing.T) {
	ngs := []string{"workers", "gpu-workers", "spot-nodes"}
	got := MatchingNodegroups(ngs, "workers")
	if len(got) != 2 {
		t.Errorf("expected 2 matches for 'workers', got %d: %v", len(got), got)
	}
}

func TestMatchingNodegroups_NoMatch(t *testing.T) {
	ngs := []string{"workers", "gpu-nodes"}
	got := MatchingNodegroups(ngs, "spot")
	if len(got) != 0 {
		t.Errorf("expected 0 matches, got %v", got)
	}
}

func TestMatchingNodegroups_EmptyListReturnsEmpty(t *testing.T) {
	got := MatchingNodegroups(nil, "workers")
	if len(got) != 0 {
		t.Errorf("expected empty result for nil input, got %v", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ConfirmNodegroupSelection
// ──────────────────────────────────────────────────────────────────────────────

func TestConfirmNodegroupSelection_EmptyReturnsError(t *testing.T) {
	_, err := ConfirmNodegroupSelection(nil, "workers")
	if err == nil {
		t.Error("expected error for 0 matches")
	}
}

func TestConfirmNodegroupSelection_SingleMatchReturnsIt(t *testing.T) {
	got, err := ConfirmNodegroupSelection([]string{"workers"}, "work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "workers" {
		t.Errorf("expected [workers], got %v", got)
	}
}

func TestConfirmNodegroupSelection_EmptyPatternReturnsAll(t *testing.T) {
	ngs := []string{"workers", "gpu-nodes"}
	got, err := ConfirmNodegroupSelection(ngs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("empty pattern should return all %d nodegroups, got %d", len(ngs), len(got))
	}
}
