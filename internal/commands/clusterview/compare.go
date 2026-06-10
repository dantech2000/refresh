package clusterview

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"

	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputComparisonTable renders a side-by-side cluster comparison.
func OutputComparisonTable(comparison *clustersvc.ClusterComparison, elapsed time.Duration) error {
	names := make([]string, len(comparison.Clusters))
	for i, c := range comparison.Clusters {
		names[i] = c.Name
	}
	ui.Outf("Cluster Comparison: %s\n", color.CyanString(strings.Join(names, " vs ")))
	ui.Outf("Analyzed in %s\n\n", ui.ElapsedString(elapsed))

	s := comparison.Summary
	summaryTbl := ui.NewDynamicTable()
	summaryTbl.Add("Total Differences", fmt.Sprintf("%d", s.TotalDifferences)).
		Add("Critical Issues", formatDifferenceCount(s.CriticalDifferences, "critical")).
		Add("Warnings", formatDifferenceCount(s.WarningDifferences, "warning")).
		Add("Informational", formatDifferenceCount(s.InfoDifferences, "info")).
		AddBool("Equivalent", s.ClustersAreEquivalent)
	summaryTbl.RenderSection("Comparison Summary")
	ui.Outln()

	if len(comparison.Differences) == 0 {
		color.Green("PASS: Clusters are identical in all analyzed aspects")
		return nil
	}

	ui.Outf("Basic Information:\n")
	cols := []ui.Column{
		{Title: "CLUSTER", Min: 14, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 7, Align: ui.AlignLeft},
		{Title: "VERSION", Min: 7, Align: ui.AlignLeft},
		{Title: "HEALTH", Min: 15, Align: ui.AlignLeft},
	}
	tbl := ui.NewPTable(cols, ui.CyanHeaders())
	for _, cl := range comparison.Clusters {
		healthStatus := color.WhiteString("UNKNOWN")
		if cl.Health != nil {
			healthStatus = decisionColor(cl.Health.Decision)(healthLabel(cl.Health.Decision))
		}
		tbl.AddRow(truncate(cl.Name, 14), formatStatus(cl.Status), cl.Version, healthStatus)
	}
	tbl.Render()
	ui.Outln()

	ui.Outf("Configuration Differences:\n\n")
	for _, sev := range []string{"critical", "warning", "info"} {
		diffs := filterDifferencesBySeverity(comparison.Differences, sev)
		if len(diffs) == 0 {
			continue
		}
		ui.Outf("%s %s:\n", severityColor(sev)("["+strings.ToUpper(sev)+"]"), severityHeading(sev))
		printDifferences(diffs)
		ui.Outln()
	}

	switch {
	case s.CriticalDifferences > 0:
		color.Red("\n[CRITICAL] Action Required:")
		color.Red("Critical differences detected that may affect cluster security or functionality.")
		color.Red("Review and address these issues before proceeding with production workloads.")
	case s.WarningDifferences > 0:
		color.Yellow("\n[WARNING] Consider Review:")
		color.Yellow("Configuration differences detected that may affect consistency.")
		color.Yellow("Review these differences to ensure they are intentional.")
	default:
		color.Green("\n[PASS] Analysis Complete:")
		color.Green("Only informational differences found. Clusters are functionally equivalent.")
	}
	return nil
}

func printDifferences(differences []clustersvc.Difference) {
	for _, diff := range differences {
		tag := severityColor(diff.Severity)("[" + strings.ToUpper(diff.Severity) + "]")
		ui.Outf("  %s %s: %s\n", tag, color.YellowString(diff.Field), diff.Description)
		for _, vp := range diff.Values {
			ui.Outf("    • %s: %v\n", color.CyanString(vp.ClusterName), vp.Value)
		}
		ui.Outln()
	}
}

func filterDifferencesBySeverity(differences []clustersvc.Difference, severity string) []clustersvc.Difference {
	var out []clustersvc.Difference
	for _, d := range differences {
		if d.Severity == severity {
			out = append(out, d)
		}
	}
	return out
}
