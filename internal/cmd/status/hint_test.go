package status

import (
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

// The hint under the table names the most urgent cluster with the command
// that looks closer at it: an unsupported cluster before one that needs
// attention, else the first that needs attention in table order, and
// nothing when every cluster is current.
func TestHint(t *testing.T) {
	standard := fleet.Findings{Support: apidoc.SupportStandard}
	current := row{Cluster: apidoc.Cluster{Name: "ok", Region: "us-east-1"}, findings: standard}
	extended := row{Cluster: apidoc.Cluster{Name: "old", Region: "eu-west-1"}, findings: fleet.Findings{Support: apidoc.SupportExtended}}
	lagging := row{Cluster: apidoc.Cluster{Name: "lag", Region: "us-west-2"}, findings: fleet.Findings{Support: apidoc.SupportStandard, BehindNodegroups: 1}}
	gone := row{Cluster: apidoc.Cluster{Name: "gone", Region: "us-east-1"}, findings: fleet.Findings{Support: apidoc.SupportUnsupported}}
	tests := []struct {
		name     string
		clusters []row
		want     string
	}{
		{name: "every cluster current", clusters: []row{current}},
		{name: "the first that needs attention", clusters: []row{current, lagging, extended},
			want: "[!] lag has 1 nodegroup behind the control plane: refresh cluster upgrade-check lag -r us-west-2"},
		{name: "out of standard support", clusters: []row{extended},
			want: "[!] old is out of standard support: refresh cluster upgrade-check old -r eu-west-1"},
		{name: "an unsupported cluster comes first", clusters: []row{lagging, gone},
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
