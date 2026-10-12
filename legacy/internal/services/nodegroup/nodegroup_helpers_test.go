package nodegroup

import "testing"

// ──────────────────────────────────────────────────────────────────────────────
// extractInstanceIDFromProviderID
// ──────────────────────────────────────────────────────────────────────────────

func TestExtractInstanceIDFromProviderID_Standard(t *testing.T) {
	// Typical AWS provider ID format: aws:///us-east-1a/i-0abc1234567890def
	got := extractInstanceIDFromProviderID("aws:///us-east-1a/i-0abc1234567890def")
	if got != "i-0abc1234567890def" {
		t.Errorf("got %q, want %q", got, "i-0abc1234567890def")
	}
}

func TestExtractInstanceIDFromProviderID_NoSlash(t *testing.T) {
	// No slash → no separator found → return empty
	got := extractInstanceIDFromProviderID("noslash")
	if got != "" {
		t.Errorf("got %q, want empty string for input without slash", got)
	}
}

func TestExtractInstanceIDFromProviderID_TrailingSlash(t *testing.T) {
	got := extractInstanceIDFromProviderID("prefix/")
	if got != "" {
		t.Errorf("trailing slash: got %q, want empty string", got)
	}
}

func TestExtractInstanceIDFromProviderID_Empty(t *testing.T) {
	got := extractInstanceIDFromProviderID("")
	if got != "" {
		t.Errorf("empty input: got %q, want empty string", got)
	}
}

func TestExtractInstanceIDFromProviderID_MultipleSlashes(t *testing.T) {
	got := extractInstanceIDFromProviderID("a/b/c/i-deadbeef")
	if got != "i-deadbeef" {
		t.Errorf("got %q, want %q", got, "i-deadbeef")
	}
}
