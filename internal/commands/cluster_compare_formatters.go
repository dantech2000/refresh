package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func outputComparisonJSON(comparison *cluster.ClusterComparison) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(comparison)
}

func outputComparisonYAML(comparison *cluster.ClusterComparison) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(comparison)
}

func outputComparisonTable(comparison *cluster.ClusterComparison, elapsed time.Duration) error {
	names := make([]string, len(comparison.Clusters))
	for i, c := range comparison.Clusters {
		names[i] = c.Name
	}
	ui.Outf("Cluster Comparison: %s\n", color.CyanString(strings.Join(names, " vs ")))
	ui.Outf("Analyzed in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

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
	tbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, cl := range comparison.Clusters {
		healthStatus := color.WhiteString("UNKNOWN")
		if cl.Health != nil {
			switch cl.Health.Decision {
			case health.DecisionProceed:
				healthStatus = color.GreenString("PASS")
			case health.DecisionWarn:
				healthStatus = color.YellowString("WARN")
			case health.DecisionBlock:
				healthStatus = color.RedString("FAIL")
			}
		}
		tbl.AddRow(truncateString(cl.Name, 14), formatStatus(cl.Status), cl.Version, healthStatus)
	}
	tbl.Render()
	ui.Outln()

	ui.Outf("Configuration Differences:\n\n")
	if diffs := filterDifferencesBySeverity(comparison.Differences, "critical"); len(diffs) > 0 {
		ui.Outf("%s Critical Issues:\n", color.RedString("[CRITICAL]"))
		printDifferences(diffs)
		ui.Outln()
	}
	if diffs := filterDifferencesBySeverity(comparison.Differences, "warning"); len(diffs) > 0 {
		ui.Outf("%s Warnings:\n", color.YellowString("[WARNING]"))
		printDifferences(diffs)
		ui.Outln()
	}
	if diffs := filterDifferencesBySeverity(comparison.Differences, "info"); len(diffs) > 0 {
		ui.Outf("%s Information:\n", color.BlueString("[INFO]"))
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

func printDifferences(differences []cluster.Difference) {
	for _, diff := range differences {
		severity := ""
		switch diff.Severity {
		case "critical":
			severity = color.RedString("[CRITICAL]")
		case "warning":
			severity = color.YellowString("[WARNING]")
		case "info":
			severity = color.BlueString("[INFO]")
		}
		ui.Outf("  %s %s: %s\n", severity, color.YellowString(diff.Field), diff.Description)
		for _, vp := range diff.Values {
			ui.Outf("    • %s: %v\n", color.CyanString(vp.ClusterName), vp.Value)
		}
		ui.Outln()
	}
}

func formatDifferenceCount(count int, severity string) string {
	if count == 0 {
		return "0"
	}
	switch severity {
	case "critical":
		return color.RedString("%d", count)
	case "warning":
		return color.YellowString("%d", count)
	case "info":
		return color.BlueString("%d", count)
	default:
		return fmt.Sprintf("%d", count)
	}
}

func filterDifferencesBySeverity(differences []cluster.Difference, severity string) []cluster.Difference {
	var out []cluster.Difference
	for _, d := range differences {
		if d.Severity == severity {
			out = append(out, d)
		}
	}
	return out
}
