package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/health"
)

func TestUpgradeReportReadiness(t *testing.T) {
	insight := func(status string) []InsightSummary { return []InsightSummary{{ID: "i", Status: status}} }
	for _, tc := range []struct {
		name   string
		report *UpgradeReport
		want   Readiness
		reason string
	}{
		{"nil", nil, ReadinessReady, ""},
		{"empty", &UpgradeReport{}, ReadinessReady, ""},
		{"passing", &UpgradeReport{Insights: insight("PASSING")}, ReadinessReady, ""},
		{"warning", &UpgradeReport{Insights: insight("WARNING")}, ReadinessReview, "1 WARNING insight(s)"},
		{"error", &UpgradeReport{Insights: insight("ERROR")}, ReadinessBlocked, "1 ERROR insight(s)"},
		{"unknown blocks", &UpgradeReport{Insights: insight("UNKNOWN")}, ReadinessBlocked, "1 UNKNOWN insight(s)"},
		{"nodegroup behind", &UpgradeReport{Skew: SkewReport{Nodegroups: []NodegroupSkew{{Name: "a", MinorsBehind: 1}}}}, ReadinessReview, "behind the control plane"},
		{"skew limit", &UpgradeReport{Skew: SkewReport{Nodegroups: []NodegroupSkew{{Name: "a", MinorsBehind: 3, Blocking: true}}}}, ReadinessBlocked, "kubelet skew limit"},
		{"addon behind", &UpgradeReport{Skew: SkewReport{Addons: []AddonSkew{{Name: "vpc-cni", Behind: true}}}}, ReadinessReview, "addon(s) behind latest"},
		{"control plane fail", &UpgradeReport{ControlPlane: &health.HealthResult{Status: health.StatusFail}}, ReadinessBlocked, "control-plane health check failed"},
		{"control plane warn", &UpgradeReport{ControlPlane: &health.HealthResult{Status: health.StatusWarn}}, ReadinessReview, "control-plane health warning"},
		{"control plane skipped", &UpgradeReport{ControlPlane: &health.HealthResult{Status: health.StatusWarn, Skipped: true}}, ReadinessReady, ""},
		{"incomplete", &UpgradeReport{Incomplete: []string{"nodegroup a: ThrottlingException"}}, ReadinessIncomplete, "could not be read"},
		{"incomplete beats warnings", &UpgradeReport{Incomplete: []string{"x"}, Insights: insight("WARNING")}, ReadinessIncomplete, "WARNING"},
		{"blocker beats incomplete", &UpgradeReport{Incomplete: []string{"x"}, Insights: insight("ERROR")}, ReadinessBlocked, "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := tc.report.Readiness()
			if got != tc.want {
				t.Errorf("readiness = %d, want %d (reasons %v)", got, tc.want, reasons)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(reasons, "; "), tc.reason) {
				t.Errorf("reasons = %v, want one containing %q", reasons, tc.reason)
			}
		})
	}
}
