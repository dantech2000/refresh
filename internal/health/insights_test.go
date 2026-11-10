package health

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The table of the insights check, which cluster upgrade-check runs: ERROR
// and UNKNOWN block, WARNING warns, and no insight is a warning in
// UPGRADE_READINESS.
func TestInsights(t *testing.T) {
	insight := func(name, status, version string) apidoc.Insight {
		return apidoc.Insight{Name: name, Status: status, KubernetesVersion: version}
	}
	tests := []struct {
		name     string
		in       insightsInput
		outcome  Outcome
		message  string
		findings []string
	}{
		{
			name:    "every insight passes",
			in:      insightsInput{Insights: []apidoc.Insight{insight("a", "PASSING", "1.33"), insight("b", "PASSING", "1.33")}},
			outcome: Pass, message: "2 insights passing",
		},
		{
			name:    "a warning",
			in:      insightsInput{Insights: []apidoc.Insight{insight("deprecated APIs", "WARNING", "1.33"), insight("b", "PASSING", "1.33")}},
			outcome: Warn, message: "1 WARNING insight (deprecated APIs)", findings: []string{"deprecated APIs"},
		},
		{
			name:    "an error",
			in:      insightsInput{Insights: []apidoc.Insight{insight("kubelet skew", "ERROR", "1.33")}},
			outcome: Fail, message: "1 ERROR insight (kubelet skew)", findings: []string{"kubelet skew"},
		},
		{
			name:    "an insight that EKS could not evaluate",
			in:      insightsInput{Insights: []apidoc.Insight{insight("add-on versions", "UNKNOWN", "1.33")}},
			outcome: Fail, message: "1 UNKNOWN insight that EKS could not evaluate (add-on versions)", findings: []string{"add-on versions"},
		},
		{
			name: "errors, unknowns, and warnings",
			in: insightsInput{Insights: []apidoc.Insight{
				insight("z warns", "WARNING", "1.33"), insight("b fails", "ERROR", "1.33"), insight("a unknown", "UNKNOWN", "1.33"),
			}},
			outcome:  Fail,
			message:  "1 ERROR insight (b fails); 1 UNKNOWN insight that EKS could not evaluate (a unknown)",
			findings: []string{"a unknown", "b fails", "z warns"},
		},
		{
			name:    "no insight in UPGRADE_READINESS",
			in:      insightsInput{},
			outcome: Warn, message: "EKS has not evaluated the upgrade insights of the cluster yet",
		},
		{
			name:    "no insight in another category",
			in:      insightsInput{Category: "MISCONFIGURATION"},
			outcome: Pass, message: "0 insights passing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &Input{Snapshot: &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod"}}}
			got := insightsCheck(tt.in).Run(context.Background(), in)
			if got.Outcome != tt.outcome || got.Message != tt.message {
				t.Errorf("result = %s %q, want %s %q", got.Outcome, got.Message, tt.outcome, tt.message)
			}
			if diff := cmp.Diff(tt.findings, got.Findings); diff != "" {
				t.Errorf("findings (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("the list could not be read", func(t *testing.T) {
		in := &Input{Snapshot: &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod"}}}
		got := insightsCheck(insightsInput{Err: errors.New("connection reset")}).Run(context.Background(), in)
		if got.Outcome != Unreadable || len(got.Failures) != 1 || got.Failures[0].Name != "prod" {
			t.Errorf("result = %s %q with %+v; want Unreadable with the failure of prod", got.Outcome, got.Message, got.Failures)
		}
	})
}
