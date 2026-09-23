package aws

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/ui"
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
	got := MatchingNodegroups(ngs, "worker")
	if len(got) != 2 {
		t.Errorf("expected 2 matches for 'worker', got %d: %v", len(got), got)
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
	_, err := ConfirmNodegroupSelection(t.Context(), nil, "workers")
	if err == nil {
		t.Error("expected error for 0 matches")
	}
}

func TestConfirmNodegroupSelection_SingleMatchReturnsIt(t *testing.T) {
	got, err := ConfirmNodegroupSelection(t.Context(), []string{"workers"}, "work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "workers" {
		t.Errorf("expected [workers], got %v", got)
	}
}

func TestConfirmNodegroupSelection_EmptyPatternReturnsAll(t *testing.T) {
	ngs := []string{"workers", "gpu-nodes"}
	got, err := ConfirmNodegroupSelection(t.Context(), ngs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("empty pattern should return all %d nodegroups, got %d", len(ngs), len(got))
	}
}

// The nodegroup-choice prompt must write to its prompt stream (stderr), never
// to stdout, so -o json/yaml output stays one document.
func TestConfirmNodegroupSelection_PromptsOnPromptStream(t *testing.T) {
	var prompt bytes.Buffer
	origOut, origPrompt := nodegroupPromptOut, promptLine
	t.Cleanup(func() { nodegroupPromptOut, promptLine = origOut, origPrompt })
	nodegroupPromptOut = &prompt
	promptLine = func(context.Context) (string, error) { return "y", nil }

	got, err := ConfirmNodegroupSelection(t.Context(), []string{"web-a", "web-b"}, "web")
	if err != nil || len(got) != 2 {
		t.Fatalf("ConfirmNodegroupSelection = %v, %v; want both matches", got, err)
	}
	for _, want := range []string{"Multiple nodegroups match pattern 'web'", "1) web-a", "Update all 2 matching nodegroups?"} {
		if !strings.Contains(prompt.String(), want) {
			t.Errorf("prompt stream missing %q; got %q", want, prompt.String())
		}
	}
}

func TestConfirmNodegroupSelection_ReadErrorCancels(t *testing.T) {
	origOut, origPrompt := nodegroupPromptOut, promptLine
	t.Cleanup(func() { nodegroupPromptOut, promptLine = origOut, origPrompt })
	nodegroupPromptOut = io.Discard
	promptLine = func(context.Context) (string, error) { return "", ui.ErrPromptCancelled }

	_, err := ConfirmNodegroupSelection(t.Context(), []string{"web-a", "web-b"}, "web")
	if err == nil || err.Error() != "operation cancelled" {
		t.Fatalf("err = %v, want \"operation cancelled\"", err)
	}
}
