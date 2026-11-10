package fleet

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// findingsSnapshot is a cluster on 1.33 in standard support, with one
// current nodegroup and one current add-on, all read.
func findingsSnapshot() *Snapshot {
	return &Snapshot{
		Cluster: Cluster{Name: "prod", Region: "us-east-1", Status: "ACTIVE", Version: Version{1, 33},
			Support: Support{Tier: apidoc.SupportStandard}},
		Nodegroups: []Nodegroup{{Name: "web", State: Collected, Status: "ACTIVE", Version: Version{1, 33},
			AMIType: "AL2023_x86_64_STANDARD", AMI: AMI{State: Collected, Current: "ami-1", Latest: "ami-1"}}},
		Addons: []Addon{{Name: "vpc-cni", State: Collected, Status: "ACTIVE",
			Version: "v1.20.0-eksbuild.1", Latest: "v1.20.0-eksbuild.1", LatestState: Collected}},
		Have: Have{Cluster: Collected, Nodegroups: Collected, Addons: Collected, Support: Collected},
	}
}

// Findings is the one judgement of a row of the fleet, for "refresh
// status" and for "refresh ui". Extended support and a health issue need
// attention; only an unsupported cluster is the worst standing.
func TestFindings(t *testing.T) {
	issue := Issue{Code: "Code", Message: "message"}
	old := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	older := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	readFailure := apidoc.Failure{Kind: apidoc.FailureNodegroup, Name: "web", Reason: apidoc.ReasonThrottled, Error: "throttled"}
	base := Findings{Support: apidoc.SupportStandard, AMIs: 1}
	tests := []struct {
		name     string
		edit     func(s *Snapshot)
		want     func(f *Findings)
		standing Standing
	}{
		{name: "current", edit: func(*Snapshot) {}, want: func(*Findings) {}, standing: StandingCurrent},
		{
			name: "extended support needs attention",
			edit: func(s *Snapshot) { s.Cluster.Support.Tier = apidoc.SupportExtended },
			want: func(f *Findings) { f.Support = apidoc.SupportExtended }, standing: StandingNeedsAttention,
		},
		{
			name: "unsupported",
			edit: func(s *Snapshot) { s.Cluster.Support.Tier = apidoc.SupportUnsupported },
			want: func(f *Findings) { f.Support = apidoc.SupportUnsupported }, standing: StandingUnsupported,
		},
		{
			name: "support not read",
			edit: func(s *Snapshot) { s.Have.Support = Failed },
			want: func(f *Findings) { f.Support = apidoc.SupportUnknown }, standing: StandingCurrent,
		},
		{
			name: "a control-plane issue needs attention",
			edit: func(s *Snapshot) { s.Cluster.HealthIssues = []Issue{issue} },
			want: func(f *Findings) { f.HealthIssues = 1 }, standing: StandingNeedsAttention,
		},
		{
			name: "nodegroup issues",
			edit: func(s *Snapshot) { s.Nodegroups[0].HealthIssues = []Issue{issue, issue} },
			want: func(f *Findings) { f.HealthIssues = 2 }, standing: StandingNeedsAttention,
		},
		{
			name: "an add-on issue on ACTIVE",
			edit: func(s *Snapshot) { s.Addons[0].Issues = []Issue{issue} },
			want: func(f *Findings) { f.HealthIssues = 1 }, standing: StandingNeedsAttention,
		},
		{
			name: "a DEGRADED add-on with no issue counts as one",
			edit: func(s *Snapshot) { s.Addons[0].Status = "DEGRADED" },
			want: func(f *Findings) { f.HealthIssues = 1 }, standing: StandingNeedsAttention,
		},
		{
			name: "a DEGRADED add-on with issues counts its issues",
			edit: func(s *Snapshot) { s.Addons[0].Status, s.Addons[0].Issues = "DEGRADED", []Issue{issue, issue} },
			want: func(f *Findings) { f.HealthIssues = 2 }, standing: StandingNeedsAttention,
		},
		{
			name: "stale AMIs, with the oldest creation time",
			edit: func(s *Snapshot) {
				second := s.Nodegroups[0]
				second.Name = "api"
				s.Nodegroups[0].AMI.Current, s.Nodegroups[0].AMI.CurrentCreatedAt = "ami-0", old
				second.AMI.Current, second.AMI.CurrentCreatedAt = "ami-0", older
				s.Nodegroups = append(s.Nodegroups, second)
			},
			want:     func(f *Findings) { f.AMIs, f.StaleAMIs, f.OldestStaleAMI = 2, 2, older },
			standing: StandingNeedsAttention,
		},
		{
			name: "a nodegroup behind the control plane",
			edit: func(s *Snapshot) { s.Nodegroups[0].Version = Version{1, 32} },
			want: func(f *Findings) { f.BehindNodegroups = 1 }, standing: StandingNeedsAttention,
		},
		{
			name: "an add-on behind",
			edit: func(s *Snapshot) { s.Addons[0].Version = "v1.19.0-eksbuild.1" },
			want: func(f *Findings) { f.BehindAddons = []string{"vpc-cni"} }, standing: StandingNeedsAttention,
		},
		{
			name: "an AMI with nothing to compare is not unread",
			edit: func(s *Snapshot) { s.Nodegroups[0].AMI.Latest = "" },
			want: func(f *Findings) { f.UncomparedAMIs = 1 }, standing: StandingCurrent,
		},
		{
			name: "an AMI that was not requested is neither read nor unread",
			edit: func(s *Snapshot) { s.Nodegroups[0].AMI = AMI{} },
			want: func(f *Findings) { f.AMIs = 0 }, standing: StandingCurrent,
		},
		{
			name: "an AMI whose read failed",
			edit: func(s *Snapshot) {
				s.Nodegroups[0].AMI = AMI{State: Failed}
				s.Failures = []apidoc.Failure{readFailure}
			},
			want:     func(f *Findings) { f.AMIs, f.UnreadAMIs, f.Incomplete = 0, 1, true },
			standing: StandingIncomplete,
		},
		{
			name: "a nodegroup that was not described",
			edit: func(s *Snapshot) {
				s.Nodegroups[0] = Nodegroup{Name: "web", State: Failed}
				s.Have.Nodegroups = Partial
				s.Failures = []apidoc.Failure{readFailure}
			},
			want:     func(f *Findings) { f.AMIs, f.UnreadAMIs, f.Incomplete = 0, 1, true },
			standing: StandingIncomplete,
		},
		{
			name: "an add-on that was not read has no issue to count",
			edit: func(s *Snapshot) {
				s.Addons[0] = Addon{Name: "vpc-cni", State: Failed}
				s.Failures = []apidoc.Failure{readFailure}
			},
			want:     func(f *Findings) { f.UnreadAddons, f.Incomplete = 1, true },
			standing: StandingIncomplete,
		},
		{
			name: "an add-on whose newest version was not read",
			edit: func(s *Snapshot) {
				s.Addons[0].LatestState = Failed
				s.Failures = []apidoc.Failure{readFailure}
			},
			want:     func(f *Findings) { f.UnreadAddons, f.Incomplete = 1, true },
			standing: StandingIncomplete,
		},
		{
			name: "an add-on with no compatible version published is not unread",
			edit: func(s *Snapshot) { s.Addons[0].Latest = "" },
			want: func(*Findings) {}, standing: StandingCurrent,
		},
		{
			name: "something to do wins over incomplete",
			edit: func(s *Snapshot) {
				s.Nodegroups[0].Version = Version{1, 32}
				s.Failures = []apidoc.Failure{readFailure}
			},
			want:     func(f *Findings) { f.BehindNodegroups, f.Incomplete = 1, true },
			standing: StandingNeedsAttention,
		},
		{
			name: "a cluster that was not described",
			edit: func(s *Snapshot) {
				*s = Snapshot{Cluster: Cluster{Name: "prod", Region: "us-east-1"}, Have: Have{Cluster: Failed}, Failures: []apidoc.Failure{readFailure}}
			},
			want:     func(f *Findings) { *f = Findings{Support: apidoc.SupportUnknown, Incomplete: true} },
			standing: StandingIncomplete,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := findingsSnapshot()
			tt.edit(snap)
			want := base
			tt.want(&want)
			got := snap.Findings()
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("findings (-want +got):\n%s", diff)
			}
			if s := got.Standing(); s != tt.standing {
				t.Errorf("standing = %s, want %s", s, tt.standing)
			}
		})
	}
}
