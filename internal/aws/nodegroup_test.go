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

// An exact name is returned without a prompt.
func TestConfirmNodegroupSelection_ExactMatchReturnsIt(t *testing.T) {
	origPrompt := promptLine
	t.Cleanup(func() { promptLine = origPrompt })
	promptLine = func(context.Context) (string, error) {
		t.Fatal("an exact name must not prompt")
		return "", nil
	}
	got, err := ConfirmNodegroupSelection(t.Context(), []string{"workers"}, "workers")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "workers" {
		t.Errorf("expected [workers], got %v", got)
	}
}

// A single non-exact (substring) match is confirmed on the prompt stream.
// Only an explicit yes selects it; Enter or anything else cancels.
func TestConfirmNodegroupSelection_SingleNonExactMatchPrompts(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"y", true}, {"YES", true}, {"", false}, {"n", false}} {
		t.Run("answer="+tc.answer, func(t *testing.T) {
			var prompt bytes.Buffer
			origOut, origPrompt := nodegroupPromptOut, promptLine
			t.Cleanup(func() { nodegroupPromptOut, promptLine = origOut, origPrompt })
			nodegroupPromptOut = &prompt
			promptLine = func(context.Context) (string, error) { return tc.answer, nil }

			got, err := ConfirmNodegroupSelection(t.Context(), []string{"payments-web"}, "web")
			if !strings.Contains(prompt.String(), `No nodegroup named "web". Update "payments-web"? [y/N]`) {
				t.Errorf("prompt stream = %q, want the single-match question", prompt.String())
			}
			if tc.want {
				if err != nil || len(got) != 1 || got[0] != "payments-web" {
					t.Errorf("got %v, %v; want [payments-web]", got, err)
				}
				return
			}
			if err == nil || got != nil {
				t.Errorf("got %v, %v; want a cancellation", got, err)
			}
		})
	}
}

func TestNodegroupPatternNeedsConfirmation(t *testing.T) {
	for _, tc := range []struct {
		matches []string
		pattern string
		want    bool
	}{
		{[]string{"web"}, "web", false},
		{[]string{"payments-web"}, "web", true},
		{[]string{"web-a", "web-b"}, "web", true},
		{[]string{"a", "b"}, "", false},
		{nil, "web", false},
	} {
		if got := NodegroupPatternNeedsConfirmation(tc.matches, tc.pattern); got != tc.want {
			t.Errorf("NodegroupPatternNeedsConfirmation(%v, %q) = %v, want %v", tc.matches, tc.pattern, got, tc.want)
		}
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
