package status

import (
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

// The hint under the table names the most urgent cluster with the command
// that looks closer at it: an unsupported cluster before one that needs
// attention, else the first that needs attention in table order, and
// nothing when every cluster is current.
func TestHint(t *testing.T) {
	behind := true
	current := apidoc.Cluster{Name: "ok", Region: "us-east-1", Support: &apidoc.Support{Tier: apidoc.SupportStandard}}
	extended := apidoc.Cluster{Name: "old", Region: "eu-west-1", Support: &apidoc.Support{Tier: apidoc.SupportExtended}}
	lagging := apidoc.Cluster{Name: "lag", Region: "us-west-2", Support: &apidoc.Support{Tier: apidoc.SupportStandard},
		Nodegroups: []apidoc.Nodegroup{{Name: "web", VersionBehind: &behind}}}
	gone := apidoc.Cluster{Name: "gone", Region: "us-east-1", Support: &apidoc.Support{Tier: apidoc.SupportUnsupported}}
	tests := []struct {
		name     string
		clusters []apidoc.Cluster
		want     string
	}{
		{name: "every cluster current", clusters: []apidoc.Cluster{current}},
		{name: "the first that needs attention", clusters: []apidoc.Cluster{current, lagging, extended},
			want: "[!] lag has 1 nodegroup behind the control plane: refresh cluster upgrade-check lag -r us-west-2"},
		{name: "out of standard support", clusters: []apidoc.Cluster{extended},
			want: "[!] old is out of standard support: refresh cluster upgrade-check old -r eu-west-1"},
		{name: "an unsupported cluster comes first", clusters: []apidoc.Cluster{lagging, gone},
			want: "[X] gone is on a version that EKS no longer supports: refresh cluster upgrade-check gone -r us-east-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hint(view.Theme{}, tt.clusters); got != tt.want {
				t.Errorf("hint = %q, want %q", got, tt.want)
			}
		})
	}
}
