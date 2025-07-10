package main

import (
	"testing"
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
			result := matchingNodegroups(nodegroups, tt.pattern)
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
			result := matchingClusters(clusters, tt.pattern)
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
