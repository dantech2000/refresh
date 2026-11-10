package health_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// The exit code of "refresh status": a cluster out of standard support
// (3) wins over one that needs attention (2). Extended support exits 3,
// although its row only needs attention.
func TestFleetExit(t *testing.T) {
	standard := fleet.Findings{Support: apidoc.SupportStandard}
	behind := fleet.Findings{Support: apidoc.SupportStandard, BehindNodegroups: 1}
	issue := fleet.Findings{Support: apidoc.SupportStandard, HealthIssues: 1}
	extended := fleet.Findings{Support: apidoc.SupportExtended}
	gone := fleet.Findings{Support: apidoc.SupportUnsupported}
	unread := fleet.Findings{Support: apidoc.SupportUnknown, Incomplete: true}
	tests := []struct {
		name     string
		clusters []fleet.Findings
		want     int
	}{
		{name: "no cluster", want: exitcode.OK},
		{name: "every cluster current", clusters: []fleet.Findings{standard, standard}, want: exitcode.OK},
		{name: "an incomplete row alone", clusters: []fleet.Findings{unread}, want: exitcode.OK},
		{name: "something behind", clusters: []fleet.Findings{standard, behind}, want: exitcode.NeedsAttention},
		{name: "a health issue", clusters: []fleet.Findings{issue}, want: exitcode.NeedsAttention},
		{name: "extended support", clusters: []fleet.Findings{behind, extended}, want: exitcode.Blocked},
		{name: "unsupported", clusters: []fleet.Findings{gone, issue}, want: exitcode.Blocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := health.FleetExit(tt.clusters); got != tt.want {
				t.Errorf("FleetExit = %d, want %d", got, tt.want)
			}
		})
	}
}
