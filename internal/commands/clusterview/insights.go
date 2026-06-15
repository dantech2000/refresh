package clusterview

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

const insightTimeLayout = "2006-01-02 15:04"

// OutputUpgradeCheck renders the upgrade-check report. The human path uses the
// render design system (a readiness verdict + tokenized insights + skew); `-o
// plain` keeps the uncolored tables.
func OutputUpgradeCheck(report *clustersvc.UpgradeReport) error {
	if !ui.PlainOutput() {
		th := render.Default(os.Stdout)
		for _, line := range upgradeCheckLines(th, report) {
			fmt.Println(line)
		}
		return nil
	}
	ui.Outf("Upgrade readiness for %s\n", report.Cluster)
	if report.Support != nil {
		ui.Outf("Support: %s\n", supportPlain(report.Support))
	}
	outputInsights(report.Insights)
	fmt.Println()
	outputControlPlane(report.ControlPlane)
	outputSkew(report.Skew)
	return nil
}

// outputControlPlane renders the control-plane gate for `-o plain`.
func outputControlPlane(cp *health.HealthResult) {
	if cp == nil {
		return
	}
	if cp.Skipped {
		ui.Outf("Control plane: %s\n", cp.Message)
	} else {
		ui.Outf("Control plane (%s): %s\n", cp.Status, cp.Message)
		for _, d := range cp.Details {
			fmt.Printf("  %s\n", d)
		}
	}
	fmt.Println()
}

func outputInsights(insights []clustersvc.InsightSummary) {
	ui.Outf("EKS Cluster Insights\n")
	if len(insights) == 0 {
		color.Green("  ✓ No upgrade insights to address")
		return
	}

	tbl := ui.NewPTable([]ui.Column{
		{Title: "ID", Min: 8, Align: ui.AlignLeft},
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
		tbl.AddRow(shortID(in.ID), in.Name, in.Category, formatInsightStatus(in.Status), valueOrDash(in.KubernetesVersion), refresh)
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

// OutputInsightDetail renders a single insight (the DescribeInsight detail
// view). The human path uses the render design system (header + sections);
// `-o plain` keeps an uncolored label/value layout for grep.
func OutputInsightDetail(detail *clustersvc.InsightDetail) error {
	if !ui.PlainOutput() {
		th := render.Default(os.Stdout)
		for _, line := range insightDetailLines(th, detail) {
			fmt.Println(line)
		}
		return nil
	}

	ui.Outf("Insight: %s\n", detail.Name)
	fmt.Printf("  Status:    %s\n", formatInsightStatus(detail.Status))
	if detail.StatusReason != "" {
		fmt.Printf("  Reason:    %s\n", detail.StatusReason)
	}
	fmt.Printf("  Category:  %s\n", detail.Category)
	if detail.KubernetesVersion != "" {
		fmt.Printf("  K8s:       %s\n", detail.KubernetesVersion)
	}
	if detail.ID != "" {
		fmt.Printf("  ID:        %s\n", detail.ID)
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
	if len(detail.AdditionalInfo) > 0 {
		fmt.Printf("\n  More information:\n")
		keys := make([]string, 0, len(detail.AdditionalInfo))
		for k := range detail.AdditionalInfo {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %s: %s\n", k, detail.AdditionalInfo[k])
		}
	}
	for _, line := range deprecationLines(detail.Deprecations) {
		fmt.Println(line)
	}
	return nil
}

// deprecationLines renders the deprecated-API breakdown: for each deprecated
// resource, the replacement and the clients still calling it (most-active
// first), so you know exactly which workload to fix before the upgrade.
func deprecationLines(deps []clustersvc.DeprecationDetail) []string {
	if len(deps) == 0 {
		return nil
	}
	out := []string{"", "  Deprecated APIs still in use:"}
	for _, d := range deps {
		head := "    - " + valueOrDash(d.Usage)
		if d.ReplacedWith != "" {
			head += " → " + d.ReplacedWith
		}
		if d.StopServingVersion != "" {
			head += fmt.Sprintf(" (removed in %s)", d.StopServingVersion)
		}
		out = append(out, head)
		if len(d.ClientStats) == 0 {
			continue
		}
		for _, c := range d.ClientStats {
			last := "-"
			if c.LastRequestTime != nil {
				last = c.LastRequestTime.Format(insightTimeLayout)
			}
			out = append(out, fmt.Sprintf("        %s — %d req/30d, last seen %s",
				valueOrDash(c.UserAgent), c.NumberOfRequestsLast30Days, last))
		}
	}
	out = append(out, "    note: EKS reads audit logs on a 30-day window — a check stays ERROR until the last call ages out.")
	return out
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
