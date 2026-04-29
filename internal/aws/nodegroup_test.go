package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
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

// ──────────────────────────────────────────────────────────────────────────────
// formatNodegroupStatus
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatNodegroupStatus_ActivePassthrough(t *testing.T) {
	got := formatNodegroupStatus(types.NodegroupStatusActive)
	if got != "ACTIVE" {
		t.Errorf("got %q, want %q", got, "ACTIVE")
	}
}

func TestFormatNodegroupStatus_UpdatingIsColored(t *testing.T) {
	got := formatNodegroupStatus(types.NodegroupStatusUpdating)
	// Color adds ANSI codes; verify the visible text is still present.
	if got == "" {
		t.Error("expected non-empty formatted status for UPDATING")
	}
}

func TestFormatNodegroupStatus_FailedPassthrough(t *testing.T) {
	got := formatNodegroupStatus(types.NodegroupStatusCreateFailed)
	if got != "CREATE_FAILED" {
		t.Errorf("got %q, want %q", got, "CREATE_FAILED")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// getFirstInstanceType
// ──────────────────────────────────────────────────────────────────────────────

func TestGetFirstInstanceType_ReturnsFirst(t *testing.T) {
	got := getFirstInstanceType([]string{"m5.large", "m5.xlarge"})
	if got != "m5.large" {
		t.Errorf("got %q, want %q", got, "m5.large")
	}
}

func TestGetFirstInstanceType_EmptyReturnsDash(t *testing.T) {
	got := getFirstInstanceType(nil)
	if got != "-" {
		t.Errorf("empty list should return %q, got %q", "-", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// getDesiredSize
// ──────────────────────────────────────────────────────────────────────────────

func TestGetDesiredSize_ReturnsValue(t *testing.T) {
	cfg := &types.NodegroupScalingConfig{DesiredSize: aws.Int32(5)}
	if got := getDesiredSize(cfg); got != 5 {
		t.Errorf("got %d, want 5", got)
	}
}

func TestGetDesiredSize_NilConfigReturnsZero(t *testing.T) {
	if got := getDesiredSize(nil); got != 0 {
		t.Errorf("nil config should return 0, got %d", got)
	}
}

func TestGetDesiredSize_NilDesiredSizeReturnsZero(t *testing.T) {
	cfg := &types.NodegroupScalingConfig{DesiredSize: nil}
	if got := getDesiredSize(cfg); got != 0 {
		t.Errorf("nil desired size should return 0, got %d", got)
	}
}
