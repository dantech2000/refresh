package cluster

import "testing"

// filterSummaries applies the status/version filters that need each cluster's
// fetched summary (REF-1).
func TestFilterSummaries(t *testing.T) {
	summaries := []ClusterSummary{
		{Name: "prod-a", Status: "ACTIVE", Version: "1.32"},
		{Name: "prod-b", Status: "CREATING", Version: "1.31"},
		{Name: "prod-c", Status: "ACTIVE", Version: "1.31"},
	}

	for _, tc := range []struct {
		name    string
		filters map[string]string
		want    []string
	}{
		{"no filters returns all", nil, []string{"prod-a", "prod-b", "prod-c"}},
		{"name-only is a no-op here", map[string]string{"name": "prod"}, []string{"prod-a", "prod-b", "prod-c"}},
		{"status filter", map[string]string{"status": "ACTIVE"}, []string{"prod-a", "prod-c"}},
		{"status case-insensitive", map[string]string{"status": "active"}, []string{"prod-a", "prod-c"}},
		{"version filter", map[string]string{"version": "1.31"}, []string{"prod-b", "prod-c"}},
		{"status AND version", map[string]string{"status": "ACTIVE", "version": "1.31"}, []string{"prod-c"}},
		{"no match", map[string]string{"status": "DELETING"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filterSummaries(summaries, tc.filters)
			if len(got) != len(tc.want) {
				t.Fatalf("filterSummaries returned %d (%v), want %d (%v)", len(got), names(got), len(tc.want), tc.want)
			}
			for i, n := range tc.want {
				if got[i].Name != n {
					t.Errorf("result[%d] = %q, want %q", i, got[i].Name, n)
				}
			}
		})
	}
}

func names(s []ClusterSummary) []string {
	out := make([]string, len(s))
	for i, c := range s {
		out[i] = c.Name
	}
	return out
}
