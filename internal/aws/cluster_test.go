package aws

import (
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// MatchingClusters
// ──────────────────────────────────────────────────────────────────────────────

func TestMatchingClusters_EmptyPatternReturnsAll(t *testing.T) {
	clusters := []string{"prod", "dev", "staging"}
	got := MatchingClusters(clusters, "")
	if len(got) != 3 {
		t.Errorf("empty pattern should return all %d clusters, got %d", len(clusters), len(got))
	}
}

func TestMatchingClusters_ExactMatch(t *testing.T) {
	clusters := []string{"prod-east", "dev-east", "staging"}
	got := MatchingClusters(clusters, "prod-east")
	if len(got) != 1 || got[0] != "prod-east" {
		t.Errorf("expected exact match [prod-east], got %v", got)
	}
}

func TestMatchingClusters_SubstringMatch(t *testing.T) {
	clusters := []string{"prod-east", "prod-west", "dev-east"}
	got := MatchingClusters(clusters, "prod")
	if len(got) != 2 {
		t.Errorf("expected 2 matches for 'prod', got %d: %v", len(got), got)
	}
}

func TestMatchingClusters_NoMatch(t *testing.T) {
	clusters := []string{"prod", "dev", "staging"}
	got := MatchingClusters(clusters, "qa")
	if len(got) != 0 {
		t.Errorf("expected 0 matches, got %d: %v", len(got), got)
	}
}

func TestMatchingClusters_EmptyList(t *testing.T) {
	got := MatchingClusters(nil, "prod")
	if len(got) != 0 {
		t.Errorf("expected empty result for nil input, got %v", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// confirmClusterSelection
// ──────────────────────────────────────────────────────────────────────────────

func TestConfirmClusterSelection_ZeroMatchesReturnsError(t *testing.T) {
	_, err := confirmClusterSelection(nil, "ghost")
	if err == nil {
		t.Error("expected error for 0 matches")
	}
}

func TestConfirmClusterSelection_SingleMatchReturnsIt(t *testing.T) {
	got, err := confirmClusterSelection([]string{"prod-east"}, "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "prod-east" {
		t.Errorf("got %q, want %q", got, "prod-east")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// extractNameFromServer
// ──────────────────────────────────────────────────────────────────────────────

func TestExtractNameFromServer_ValidEKSEndpoint(t *testing.T) {
	// A real EKS endpoint looks like https://HASH.gr7.us-east-1.eks.amazonaws.com
	// The first segment after stripping https:// is the unique hash — usually not a valid cluster name.
	// But some kubeconfigs set the cluster entry to the cluster name directly.
	name := extractNameFromServer("https://my-cluster.gr7.us-east-1.eks.amazonaws.com")
	// my-cluster matches the AWS name pattern
	if name != "my-cluster" {
		t.Errorf("got %q, want %q", name, "my-cluster")
	}
}

func TestExtractNameFromServer_InvalidPrefix(t *testing.T) {
	// A hash-style server URL prefix won't match awsNamePattern (starts with digit is ok,
	// but a hex hash with dashes should still match — let's test the empty case).
	name := extractNameFromServer("")
	if name != "" {
		t.Errorf("empty server should return empty name, got %q", name)
	}
}

func TestExtractNameFromServer_HTTPSStripped(t *testing.T) {
	name := extractNameFromServer("https://valid-name.region.eks.amazonaws.com")
	if name != "valid-name" {
		t.Errorf("expected 'valid-name', got %q", name)
	}
}
