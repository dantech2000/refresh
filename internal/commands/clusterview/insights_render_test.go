package clusterview

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

func TestUpgradeCheckLines_Verdicts(t *testing.T) {
	th := render.New(render.ColorNone, true)

	// Error insight → NOT READY.
	rptErr := &clustersvc.UpgradeReport{
		Cluster: "data-eu",
		Insights: []clustersvc.InsightSummary{
			{Name: "Deprecated APIs removed in 1.30", Category: "UPGRADE_READINESS", Status: clustersvc.InsightStatusError, KubernetesVersion: "1.30"},
			{Name: "EKS add-on compatibility", Category: "ADDON", Status: clustersvc.InsightStatusPassing},
		},
		Skew: clustersvc.SkewReport{ControlPlaneVersion: "1.29", Findings: []string{"nodegroup ng-a is 2 minors behind"}},
	}
	joined := strings.Join(upgradeCheckLines(th, rptErr), "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone output contains ANSI:\n%s", joined)
	}
	for _, want := range []string{
		"UPGRADE READINESS  data-eu   ✗ NOT READY",
		"▸ INSIGHTS  2",
		"✗ ERROR",
		"● PASSING",
		"1 error", "1 passing",
		"▸ VERSION SKEW  control plane 1.29",
		"▲ nodegroup ng-a is 2 minors behind",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("upgrade-check missing %q in:\n%s", want, joined)
		}
	}

	// No insights, no skew → READY.
	rptReady := &clustersvc.UpgradeReport{Cluster: "prod", Skew: clustersvc.SkewReport{ControlPlaneVersion: "1.33"}}
	ready := strings.Join(upgradeCheckLines(th, rptReady), "\n")
	for _, want := range []string{
		"● READY",
		"● no upgrade insights to address",
		"● nodegroups and addons are current",
	} {
		if !strings.Contains(ready, want) {
			t.Errorf("ready upgrade-check missing %q in:\n%s", want, ready)
		}
	}

	// Warning only → REVIEW.
	rptWarn := &clustersvc.UpgradeReport{
		Cluster:  "stage",
		Insights: []clustersvc.InsightSummary{{Name: "x", Status: clustersvc.InsightStatusWarning}},
	}
	if got := strings.Join(upgradeCheckLines(th, rptWarn), "\n"); !strings.Contains(got, "▲ REVIEW") {
		t.Errorf("warning-only verdict should be REVIEW:\n%s", got)
	}
}
