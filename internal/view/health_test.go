package view_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

func TestHealthLines(t *testing.T) {
	results := []apidoc.HealthResult{
		{Name: "Nodes", Outcome: apidoc.HealthPass, Message: "6/6 nodes ready"},
		{Name: "DrainBlockers", Outcome: apidoc.HealthFail, Message: "PDB default/web allows 0 disruptions"},
		{Name: "PDBCoverage", Outcome: apidoc.HealthWarn, Message: "1 deployment without a PDB"},
		{Name: "Utilization", Outcome: apidoc.HealthSkipped, Message: "metrics-server is not installed"},
		{Name: "Quotas", Outcome: apidoc.HealthUnreadable, Message: "could not read the vCPU quota"},
	}
	want := []string{
		"  [OK] Nodes          6/6 nodes ready",
		"  [X]  DrainBlockers  PDB default/web allows 0 disruptions",
		"  [!]  PDBCoverage    1 deployment without a PDB",
		"       Utilization    metrics-server is not installed",
		"  [?]  Quotas         could not read the vCPU quota",
	}
	got := view.HealthLines(view.Theme{}, results)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HealthLines (-want +got):\n%s", diff)
	}
	if view.HealthStatus(apidoc.HealthOutcome("later")) != view.NoStatus {
		t.Error("an outcome this build does not know should draw nothing")
	}
}

// One status for each verdict, insight, and standing, for every front end.
func TestStatusMappings(t *testing.T) {
	tests := []struct {
		name      string
		got, want view.Status
	}{
		{"Ready", view.ReadinessStatus(apidoc.ReadinessReady), view.Healthy},
		{"Review", view.ReadinessStatus(apidoc.ReadinessReview), view.Warn},
		{"Blocked", view.ReadinessStatus(apidoc.ReadinessBlocked), view.Fail},
		{"Incomplete", view.ReadinessStatus(apidoc.ReadinessIncomplete), view.Unknown},
		{"PASSING", view.InsightStatus("PASSING"), view.Healthy},
		{"WARNING", view.InsightStatus("WARNING"), view.Warn},
		{"ERROR", view.InsightStatus("ERROR"), view.Fail},
		{"UNKNOWN", view.InsightStatus("UNKNOWN"), view.Unknown},
		{"Current", view.StandingStatus(fleet.StandingCurrent), view.Healthy},
		{"Incomplete standing", view.StandingStatus(fleet.StandingIncomplete), view.Unknown},
		{"NeedsAttention", view.StandingStatus(fleet.StandingNeedsAttention), view.Warn},
		{"Unsupported", view.StandingStatus(fleet.StandingUnsupported), view.Fail},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: status %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func BenchmarkHealthLines(b *testing.B) {
	results := make([]apidoc.HealthResult, 10)
	for i := range results {
		results[i] = apidoc.HealthResult{Name: "Check", Outcome: apidoc.HealthPass, Message: "fine"}
	}
	th := view.Theme{Depth: view.TrueColor, Unicode: true}
	b.ReportAllocs()
	for b.Loop() {
		view.HealthLines(th, results)
	}
}
