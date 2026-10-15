package status

import (
	"testing"

	"github.com/dantech2000/refresh/internal/fleet"
)

// The HEALTH column and the fleet view of "refresh ui" count the same
// issues: the count of the document is the count of the snapshot it
// comes from.
func TestHealthIssuesMatchTheSnapshot(t *testing.T) {
	issue := fleet.Issue{Code: "Code", Message: "message"}
	tests := []struct {
		name string
		edit func(s *fleet.Snapshot)
	}{
		{name: "healthy", edit: func(*fleet.Snapshot) {}},
		{name: "a control-plane issue", edit: func(s *fleet.Snapshot) { s.Cluster.HealthIssues = []fleet.Issue{issue} }},
		{name: "a nodegroup issue", edit: func(s *fleet.Snapshot) { s.Nodegroups[0].HealthIssues = []fleet.Issue{issue, issue} }},
		{name: "an add-on issue on ACTIVE", edit: func(s *fleet.Snapshot) { s.Addons[0].Issues = []fleet.Issue{issue} }},
		{name: "a DEGRADED add-on with no issue", edit: func(s *fleet.Snapshot) { s.Addons[0].Status = "DEGRADED" }},
		{name: "a DEGRADED add-on with issues", edit: func(s *fleet.Snapshot) {
			s.Addons[0].Status, s.Addons[0].Issues = "DEGRADED", []fleet.Issue{issue, issue}
		}},
		{name: "an add-on that was not read", edit: func(s *fleet.Snapshot) {
			s.Addons[0] = fleet.Addon{Name: "vpc-cni", State: fleet.Failed}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &fleet.Snapshot{
				Cluster:    fleet.Cluster{Name: "prod", Region: "us-east-1", Status: "ACTIVE"},
				Nodegroups: []fleet.Nodegroup{{Name: "web", State: fleet.Collected, Status: "ACTIVE"}},
				Addons:     []fleet.Addon{{Name: "vpc-cni", State: fleet.Collected, Status: "ACTIVE"}},
				Have:       fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected},
			}
			tt.edit(snap)
			if doc, want := healthIssues(snap.ClusterDoc()), snap.HealthIssues(); doc != want {
				t.Errorf("status counts %d, the snapshot %d", doc, want)
			}
		})
	}
}
