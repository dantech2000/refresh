package health_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// The table of the add-on check: an ACTIVE target passes, a broken one is
// a warning so that the update can repair it, and one that EKS is changing
// fails.
func TestAddons(t *testing.T) {
	addon := func(name, status string, issues ...fleet.Issue) fleet.Addon {
		return fleet.Addon{Name: name, State: fleet.Collected, Status: status, Issues: issues}
	}
	replicas := fleet.Issue{Code: "InsufficientNumberOfReplicas"}
	tests := []struct {
		name     string
		addons   []fleet.Addon
		targets  []string
		outcome  health.Outcome
		message  string
		findings []string
	}{
		{
			name: "active", addons: []fleet.Addon{addon("vpc-cni", "ACTIVE"), addon("coredns", "DEGRADED")},
			targets: []string{"vpc-cni"}, outcome: health.Pass, message: "1 add-on ACTIVE",
		},
		{
			name: "degraded", addons: []fleet.Addon{addon("vpc-cni", "DEGRADED", replicas)}, targets: []string{"vpc-cni"},
			outcome: health.Warn, findings: []string{"vpc-cni"},
			message: "vpc-cni is DEGRADED (InsufficientNumberOfReplicas); the update will try to repair it",
		},
		{
			// EKS reports an issue for an add-on whose status still says
			// ACTIVE. The add-on is not healthy, as every list says.
			name: "active with an issue", addons: []fleet.Addon{addon("coredns", "ACTIVE", replicas)}, targets: []string{"coredns"},
			outcome: health.Warn, findings: []string{"coredns"},
			message: "coredns is ACTIVE with health issues (InsufficientNumberOfReplicas); the update will try to repair it",
		},
		{
			name: "update failed", addons: []fleet.Addon{addon("vpc-cni", "UPDATE_FAILED")}, targets: []string{"vpc-cni"},
			outcome: health.Warn, findings: []string{"vpc-cni"}, message: "vpc-cni is UPDATE_FAILED; the update will try to repair it",
		},
		{
			name: "create failed", addons: []fleet.Addon{addon("vpc-cni", "CREATE_FAILED")}, targets: []string{"vpc-cni"},
			outcome: health.Warn, findings: []string{"vpc-cni"}, message: "vpc-cni is CREATE_FAILED; the update will try to repair it",
		},
		{
			name: "delete failed", addons: []fleet.Addon{addon("vpc-cni", "DELETE_FAILED")}, targets: []string{"vpc-cni"},
			outcome: health.Warn, findings: []string{"vpc-cni"}, message: "vpc-cni is DELETE_FAILED; the update will try to repair it",
		},
		{
			name: "deleting", addons: []fleet.Addon{addon("coredns", "DEGRADED"), addon("vpc-cni", "DELETING")}, targets: []string{"coredns", "vpc-cni"},
			outcome: health.Fail, findings: []string{"coredns", "vpc-cni"},
			message: "coredns is DEGRADED; the update will try to repair it; vpc-cni is DELETING and cannot be updated",
		},
		{
			name: "not read", addons: []fleet.Addon{{Name: "vpc-cni", State: fleet.Failed}}, targets: []string{"vpc-cni"},
			outcome: health.Unreadable, message: "could not read vpc-cni",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod"}, Addons: tt.addons}
			got := health.Run(t.Context(), []health.Check{health.Addons(tt.targets)}, &health.Input{Snapshot: snap})
			want := []health.Result{{
				Name: "Addons", Blocking: true, FailClosed: true,
				Outcome: tt.outcome, Message: tt.message, Findings: tt.findings,
			}}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("results (-want +got):\n%s", diff)
			}
		})
	}
}
