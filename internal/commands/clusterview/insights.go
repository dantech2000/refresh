package clusterview

import (
	"fmt"
	"strings"

	"github.com/fatih/color"

	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

const insightTimeLayout = "2006-01-02 15:04"

// OutputUpgradeCheck renders the upgrade-check report: AWS Cluster Insights
// followed by the local control-plane/nodegroup/addon version-skew section.
func OutputUpgradeCheck(report *clustersvc.UpgradeReport) error {
	ui.Outf("Upgrade readiness for %s\n", report.Cluster)

	outputInsights(report.Insights)
	fmt.Println()
	outputSkew(report.Skew)
	return nil
}

func outputInsights(insights []clustersvc.InsightSummary) {
	ui.Outf("EKS Cluster Insights\n")
	if len(insights) == 0 {
		color.Green("  ✓ No upgrade insights to address")
		return
	}

	tbl := ui.NewPTable([]ui.Column{
		{Title: "NAME", Min: 24, Max: 48, Align: ui.AlignLeft},
		{Title: "CATEGORY", Min: 16, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 8, Align: ui.AlignLeft},
		{Title: "K8S VERSION", Min: 11, Align: ui.AlignLeft},
		{Title: "LAST REFRESH", Min: 16, Align: ui.AlignLeft},
	}, ui.CyanHeaders())

	var errc, warnc, passc, otherc int
	for _, in := range insights {
		switch strings.ToUpper(in.Status) {
		case clustersvc.InsightStatusError:
			errc++
		case clustersvc.InsightStatusWarning:
			warnc++
		case clustersvc.InsightStatusPassing:
			passc++
		default:
			otherc++
		}
		refresh := "-"
		if in.LastRefreshTime != nil {
			refresh = in.LastRefreshTime.Format(insightTimeLayout)
		}
		tbl.AddRow(in.Name, in.Category, formatInsightStatus(in.Status), valueOrDash(in.KubernetesVersion), refresh)
	}
	tbl.Render()

	ui.Outf("  %s · %s · %s",
		color.RedString("%d error", errc),
		color.YellowString("%d warning", warnc),
		color.GreenString("%d passing", passc),
	)
	if otherc > 0 {
		ui.Outf(" · %d unknown", otherc)
	}
	fmt.Println()
}

func outputSkew(skew clustersvc.SkewReport) {
	ui.Outf("Version skew (control plane %s)\n", valueOrDash(skew.ControlPlaneVersion))
	if len(skew.Findings) == 0 {
		color.Green("  ✓ No version skew detected — nodegroups and addons are current")
		return
	}
	for _, f := range skew.Findings {
		fmt.Printf("  %s %s\n", color.YellowString("•"), f)
	}
}

// OutputInsightDetail renders a single insight's recommendation and affected
// resources (the DescribeInsight detail view).
func OutputInsightDetail(detail *clustersvc.InsightDetail) error {
	ui.Outf("Insight: %s\n", detail.Name)
	fmt.Printf("  Status:    %s\n", formatInsightStatus(detail.Status))
	if detail.StatusReason != "" {
		fmt.Printf("  Reason:    %s\n", detail.StatusReason)
	}
	fmt.Printf("  Category:  %s\n", detail.Category)
	if detail.KubernetesVersion != "" {
		fmt.Printf("  K8s:       %s\n", detail.KubernetesVersion)
	}
	if detail.Description != "" {
		fmt.Printf("\n  Description:\n    %s\n", oneLine(detail.Description))
	}
	if detail.Recommendation != "" {
		fmt.Printf("\n  Recommendation:\n    %s\n", oneLine(detail.Recommendation))
	}
	if len(detail.Resources) > 0 {
		fmt.Printf("\n  Affected resources:\n")
		for _, r := range detail.Resources {
			fmt.Printf("    - %s\n", r)
		}
	}
	return nil
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// oneLine collapses Markdown body text to a single line for terminal display.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
