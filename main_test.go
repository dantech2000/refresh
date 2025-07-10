package main

import (
	"testing"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/types"
)

func TestMatchingNodegroups(t *testing.T) {
	nodegroups := []string{"web-prod", "web-staging", "api-prod", "api-staging"}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "empty pattern returns all",
			pattern:  "",
			expected: []string{"web-prod", "web-staging", "api-prod", "api-staging"},
		},
		{
			name:     "pattern matches multiple",
			pattern:  "web",
			expected: []string{"web-prod", "web-staging"},
		},
		{
			name:     "pattern matches single",
			pattern:  "web-prod",
			expected: []string{"web-prod"},
		},
		{
			name:     "pattern matches none",
			pattern:  "database",
			expected: []string{},
		},
		{
			name:     "pattern matches prod",
			pattern:  "prod",
			expected: []string{"web-prod", "api-prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := awsClient.MatchingNodegroups(nodegroups, tt.pattern)
			if len(result) != len(tt.expected) {
				t.Errorf("matchingNodegroups() = %v, want %v", result, tt.expected)
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("matchingNodegroups() = %v, want %v", result, tt.expected)
					return
				}
			}
		})
	}
}

func TestMatchingClusters(t *testing.T) {
	clusters := []string{"prod-cluster", "staging-cluster", "dev-cluster"}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "empty pattern returns all",
			pattern:  "",
			expected: []string{"prod-cluster", "staging-cluster", "dev-cluster"},
		},
		{
			name:     "pattern matches multiple",
			pattern:  "cluster",
			expected: []string{"prod-cluster", "staging-cluster", "dev-cluster"},
		},
		{
			name:     "pattern matches single",
			pattern:  "prod",
			expected: []string{"prod-cluster"},
		},
		{
			name:     "pattern matches none",
			pattern:  "test",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := awsClient.MatchingClusters(clusters, tt.pattern)
			if len(result) != len(tt.expected) {
				t.Errorf("matchingClusters() = %v, want %v", result, tt.expected)
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("matchingClusters() = %v, want %v", result, tt.expected)
					return
				}
			}
		})
	}
}

func TestAMIStatusString(t *testing.T) {
	tests := []struct {
		name     string
		status   types.AMIStatus
		expected string
	}{
		{
			name:     "AMILatest shows Latest",
			status:   types.AMILatest,
			expected: "Latest",
		},
		{
			name:     "AMIOutdated shows Outdated",
			status:   types.AMIOutdated,
			expected: "Outdated",
		},
		{
			name:     "AMIUpdating shows Updating",
			status:   types.AMIUpdating,
			expected: "Updating",
		},
		{
			name:     "AMIUnknown shows Unknown",
			status:   types.AMIUnknown,
			expected: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.status.String()
			// Check if the expected text is contained in the colored output
			if !containsText(result, tt.expected) {
				t.Errorf("AMIStatus.String() = %v, want to contain %v", result, tt.expected)
			}
		})
	}
}

func TestDryRunActionString(t *testing.T) {
	tests := []struct {
		name     string
		action   types.DryRunAction
		expected string
	}{
		{
			name:     "ActionUpdate shows UPDATE",
			action:   types.ActionUpdate,
			expected: "UPDATE",
		},
		{
			name:     "ActionSkipUpdating shows SKIP",
			action:   types.ActionSkipUpdating,
			expected: "SKIP",
		},
		{
			name:     "ActionSkipLatest shows SKIP",
			action:   types.ActionSkipLatest,
			expected: "SKIP",
		},
		{
			name:     "ActionForceUpdate shows FORCE UPDATE",
			action:   types.ActionForceUpdate,
			expected: "FORCE UPDATE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.action.String()
			// Check if the expected text is contained in the colored output
			if !containsText(result, tt.expected) {
				t.Errorf("DryRunAction.String() = %v, want to contain %v", result, tt.expected)
			}
		})
	}
}

// Helper function to check if colored text contains expected string
func containsText(coloredText, expectedText string) bool {
	// Remove ANSI color codes for comparison
	// This is a simple check - in real scenarios you might want a more robust ANSI stripper
	cleanText := coloredText
	for i := 0; i < len(cleanText); i++ {
		if cleanText[i] == '\033' {
			// Find the end of the ANSI sequence
			for j := i; j < len(cleanText); j++ {
				if cleanText[j] == 'm' {
					cleanText = cleanText[:i] + cleanText[j+1:]
					break
				}
			}
		}
	}
	return cleanText == expectedText
}
