package view_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
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
