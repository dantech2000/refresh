package clusterview

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

func TestWrapText(t *testing.T) {
	if got := wrapText("a b c d e", 3); strings.Join(got, "|") != "a b|c d|e" {
		t.Errorf("wrapText width=3 = %v, want [a b|c d|e]", got)
	}
	// Collapses arbitrary whitespace/newlines into a single wrapped paragraph.
	if got := wrapText("  alpha\n\n  beta   gamma ", 80); strings.Join(got, "|") != "alpha beta gamma" {
		t.Errorf("wrapText collapse = %v", got)
	}
	if got := wrapText("   ", 80); got != nil {
		t.Errorf("blank input should yield nil, got %v", got)
	}
}

func TestInsightDetailLines(t *testing.T) {
	th := render.New(render.ColorNone, true)

	// AL2-style PASSING insight: rich recommendation + additionalInfo, no
	// resources, no deprecations (mirrors the real DescribeInsight output).
	d := &clustersvc.InsightDetail{
		InsightSummary: clustersvc.InsightSummary{
			ID: "bc8b2f86-6650-4ee3-a7b9-70dab041a350", Name: "Amazon Linux 2 compatibility",
			Category: "UPGRADE_READINESS", Status: clustersvc.InsightStatusPassing,
			StatusReason: "No Amazon Linux 2 nodes detected.", KubernetesVersion: "1.35",
		},
		Recommendation: "Migrate all EKS nodes using Amazon Linux 2 AMIs to Bottlerocket or Amazon Linux 2023 AMIs before the deadline.",
		AdditionalInfo: map[string]string{
			"Migrating to Amazon Linux 2023": "https://docs.aws.amazon.com/eks/latest/userguide/al2023.html",
			"Create nodes with Bottlerocket": "https://docs.aws.amazon.com/eks/latest/userguide/eks-optimized-ami-bottlerocket.html",
		},
	}
	joined := strings.Join(insightDetailLines(th, d), "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone detail contains ANSI:\n%s", joined)
	}
	for _, want := range []string{
		"Amazon Linux 2 compatibility   ● PASSING",
		"No Amazon Linux 2 nodes detected.",
		"▸ OVERVIEW", "category", "targets", "1.35",
		"▸ RECOMMENDATION", "Migrate all EKS nodes",
		"▸ MORE INFORMATION",
		"Migrating to Amazon Linux 2023",
		"https://docs.aws.amazon.com/eks/latest/userguide/al2023.html",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail view missing %q in:\n%s", want, joined)
		}
	}
	// additionalInfo is sorted (deterministic): "Create…" sorts before "Migrating…".
	if strings.Index(joined, "Create nodes with Bottlerocket") > strings.Index(joined, "Migrating to Amazon Linux 2023") {
		t.Error("additionalInfo links should be sorted alphabetically")
	}
	if strings.Contains(joined, "DEPRECATED APIs") {
		t.Errorf("no deprecations → no DEPRECATED APIs section:\n%s", joined)
	}
}

func TestInsightDetailLines_Deprecations(t *testing.T) {
	th := render.New(render.ColorNone, true)
	last := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	d := &clustersvc.InsightDetail{
		InsightSummary: clustersvc.InsightSummary{Name: "Deprecated APIs removed in 1.33", Status: clustersvc.InsightStatusError},
		Deprecations: []clustersvc.DeprecationDetail{{
			Usage: "policy/v1beta1 PodDisruptionBudget", ReplacedWith: "policy/v1 PodDisruptionBudget", StopServingVersion: "1.25",
			ClientStats: []clustersvc.ClientStat{{UserAgent: "newrelic-kube-state-metric/v2", LastRequestTime: &last, NumberOfRequestsLast30Days: 412}},
		}},
	}
	joined := strings.Join(insightDetailLines(th, d), "\n")
	for _, want := range []string{
		"▸ DEPRECATED APIs",
		"policy/v1beta1 PodDisruptionBudget → policy/v1 PodDisruptionBudget (removed in 1.25)",
		"newrelic-kube-state-metric/v2", "412 req/30d", "last seen 2026-06-14 09:30",
		"30-day window",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("deprecation detail missing %q in:\n%s", want, joined)
		}
	}
}

func TestUpgradeCheckLines_ControlPlaneGate(t *testing.T) {
	th := render.New(render.ColorNone, true)

	// A blocking control-plane failure (etcd near read-only) drives NOT READY
	// even with no insight errors, and renders the CONTROL PLANE section.
	rpt := &clustersvc.UpgradeReport{
		Cluster: "prod",
		ControlPlane: &health.HealthResult{
			Name:       "Control Plane",
			Status:     health.StatusFail,
			IsBlocking: true,
			Message:    "etcd database near the 8 GiB read-only limit (96.2%) — compact before upgrading",
			Details:    []string{"etcd database 96.2% of the 8 GiB limit (7.70 GiB in use)"},
		},
		Skew: clustersvc.SkewReport{ControlPlaneVersion: "1.32"},
	}
	joined := strings.Join(upgradeCheckLines(th, rpt), "\n")
	for _, want := range []string{
		"✗ NOT READY",
		"▸ CONTROL PLANE",
		"near the 8 GiB read-only limit",
		"7.70 GiB in use",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("control-plane gate missing %q in:\n%s", want, joined)
		}
	}

	// A skipped gate (pre-1.28) shows the unavailable note and does not fail the
	// verdict on its own.
	skipped := &clustersvc.UpgradeReport{
		Cluster:      "prod",
		ControlPlane: &health.HealthResult{Name: "Control Plane", Status: health.StatusPass, Skipped: true, Message: "control-plane metrics unavailable (requires EKS 1.28+ on a supported platform version)"},
		Skew:         clustersvc.SkewReport{ControlPlaneVersion: "1.27"},
	}
	got := strings.Join(upgradeCheckLines(th, skipped), "\n")
	if !strings.Contains(got, "● READY") || !strings.Contains(got, "metrics unavailable") {
		t.Errorf("skipped gate should be READY + unavailable note:\n%s", got)
	}
}

func TestDeprecationLines(t *testing.T) {
	// Empty input renders nothing.
	if got := deprecationLines(nil); got != nil {
		t.Errorf("nil deprecations should render nothing, got %v", got)
	}

	last := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	deps := []clustersvc.DeprecationDetail{{
		Usage:              "policy/v1beta1 PodDisruptionBudget",
		ReplacedWith:       "policy/v1 PodDisruptionBudget",
		StopServingVersion: "1.25",
		ClientStats: []clustersvc.ClientStat{
			{UserAgent: "newrelic-kube-state-metric/v2", LastRequestTime: &last, NumberOfRequestsLast30Days: 412},
		},
	}}
	joined := strings.Join(deprecationLines(deps), "\n")
	for _, want := range []string{
		"Deprecated APIs still in use:",
		"policy/v1beta1 PodDisruptionBudget → policy/v1 PodDisruptionBudget (removed in 1.25)",
		"newrelic-kube-state-metric/v2 — 412 req/30d, last seen 2026-06-14 09:30",
		"30-day window",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("deprecation render missing %q in:\n%s", want, joined)
		}
	}
}

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
