package health

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The findings of a check are the names it was given, never a part cut
// out of its message: a name that holds the words of the message format
// stays whole.
func TestFindingsAreNamesNotParsedText(t *testing.T) {
	tests := []struct {
		name string
		got  Result
		want []string
	}{
		{
			name: "workloads",
			got:  evaluateWorkloads(2, 1, []finding{{"kube-system/b (x)", "kube-system/b (x) (Pending)"}}),
			want: []string{"kube-system/b (x)"},
		},
		{
			name: "drain blockers",
			got: drainBlockersResult(
				[]finding{{"default/web (a)", "default/web (a) (2/2 pods healthy, 0 disruptions allowed)"}},
				[]finding{{"default/pod is covered", "pod default/pod is covered is covered by 2 PDBs (a, b); the eviction API refuses such pods"}},
				nil, nil, false),
			want: []string{"default/pod is covered", "default/web (a)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, tt.got.Findings); diff != "" {
				t.Errorf("findings (-want +got):\n%s", diff)
			}
		})
	}
}
