package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputClustersTable renders a table of cluster summaries. Exported so that
// other command packages (nodegroup, addon) can display the cluster list when no
// cluster argument is provided.
func OutputClustersTable(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	if multiRegion {
		regions := make(map[string]bool)
		for _, s := range summaries {
			regions[s.Region] = true
		}
		ui.Outf("EKS Clusters (%d regions, %d clusters)\n", len(regions), len(summaries))
	} else {
		ui.Outf("EKS Clusters (%d clusters)\n", len(summaries))
	}
	ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	headerColor := func(s string) string { return color.CyanString(s) }
	cols := []ui.Column{{Title: "CLUSTER", Min: 14, Align: ui.AlignLeft}}
	if multiRegion {
		cols = append(cols, ui.Column{Title: "REGION", Min: 10, Align: ui.AlignLeft})
	}
	cols = append(cols,
		ui.Column{Title: "STATUS", Min: 7, Align: ui.AlignLeft},
		ui.Column{Title: "VERSION", Min: 7, Align: ui.AlignLeft},
	)
	if showHealth {
		cols = append(cols, ui.Column{Title: "HEALTH", Min: 8, Align: ui.AlignLeft})
	}
	cols = append(cols, ui.Column{Title: "READY/DESIRED", Min: 15, Align: ui.AlignRight})

	tbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(headerColor))
	for _, s := range summaries {
		row := []string{s.Name}
		if multiRegion {
			row = append(row, s.Region)
		}
		row = append(row, formatStatus(s.Status), s.Version)
		if showHealth {
			row = append(row, formatClusterHealth(s.Health))
		}
		row = append(row, formatNodeCount(s.NodeCount))
		tbl.AddRow(row...)
	}
	tbl.Render()

	if showHealth {
		healthy, warning, critical, updating := 0, 0, 0, 0
		for _, s := range summaries {
			if s.Health != nil {
				switch s.Health.Decision {
				case health.DecisionProceed:
					healthy++
				case health.DecisionWarn:
					warning++
				case health.DecisionBlock:
					critical++
				}
			}
			if strings.Contains(strings.ToUpper(s.Status), "UPDAT") {
				updating++
			}
		}
		ui.Outf("\nSummary: ")
		var parts []string
		if healthy > 0 {
			parts = append(parts, color.GreenString("%d", healthy)+" healthy")
		}
		if warning > 0 {
			parts = append(parts, color.YellowString("%d", warning)+" warning")
		}
		if critical > 0 {
			parts = append(parts, color.RedString("%d", critical)+" critical")
		}
		if updating > 0 {
			parts = append(parts, color.CyanString("%d", updating)+" updating")
		}
		ui.Outln(strings.Join(parts, ", "))
	}
	return nil
}

func outputClustersJSON(summaries []clustersvc.ClusterSummary) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"clusters": summaries, "count": len(summaries)})
}

func outputClustersYAML(summaries []clustersvc.ClusterSummary) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(map[string]any{"clusters": summaries, "count": len(summaries)})
}

func outputClustersTree(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	regionGroups := make(map[string][]clustersvc.ClusterSummary)
	for _, s := range summaries {
		r := s.Region
		if r == "" {
			r = "unknown-region"
		}
		regionGroups[r] = append(regionGroups[r], s)
	}

	regionTree := ui.NewRegionTreeBuilder()
	regions := make([]string, 0, len(regionGroups))
	for r := range regionGroups {
		regions = append(regions, r)
	}
	sort.Strings(regions)

	for _, r := range regions {
		clusters := regionGroups[r]
		regionTree.AddRegion(r, len(clusters))
		sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })
		for _, c := range clusters {
			status := c.Status
			if showHealth && c.Health != nil {
				switch string(c.Health.Decision) {
				case "PROCEED":
					status = "HEALTHY"
				case "WARN":
					status = "WARNING"
				case "BLOCK":
					status = "CRITICAL"
				}
			}
			regionTree.AddClusterToRegion(c.Name, status, c.NodeCount.Ready)
		}
		regionTree.FinishRegion()
	}

	title := fmt.Sprintf("EKS Clusters (%d clusters)", len(summaries))
	if multiRegion {
		title = fmt.Sprintf("EKS Clusters (%d regions, %d clusters)", len(regions), len(summaries))
	}
	if err := regionTree.RenderWithTitle(title); err != nil {
		return err
	}
	ui.Outf("\n%s\n", ui.FormatTreeSummary(len(summaries), "clusters", elapsed.Seconds()))

	if showHealth {
		healthy, warning, critical := 0, 0, 0
		for _, s := range summaries {
			if s.Health == nil {
				continue
			}
			switch string(s.Health.Decision) {
			case "PROCEED":
				healthy++
			case "WARN":
				warning++
			case "BLOCK":
				critical++
			}
		}
		ui.Outf("\nHealth Summary: %s healthy, %s warnings, %s critical\n",
			color.GreenString("%d", healthy), color.YellowString("%d", warning), color.RedString("%d", critical))
	}
	return nil
}

func outputClusterDetailsJSON(details *clustersvc.ClusterDetails) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(details)
}

func outputClusterDetailsYAML(details *clustersvc.ClusterDetails) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(details)
}

func outputClusterDetailsTable(details *clustersvc.ClusterDetails, elapsed time.Duration) error {
	ui.Outf("Cluster Information: %s\n", color.CyanString(details.Name))
	ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	tbl := ui.NewDynamicTable()
	tbl.Add("Status", formatStatus(details.Status)).
		Add("Version", details.Version).
		Add("Platform", details.PlatformVersion).
		Add("Endpoint", truncateEndpoint(details.Endpoint))

	if details.Health != nil {
		tbl.Add("Health", formatHealth(details.Health))
	}

	if len(details.Nodegroups) > 0 {
		var totalNodes int32
		activeNGs := 0
		for _, ng := range details.Nodegroups {
			totalNodes += ng.ReadyNodes
			if ng.Status == "ACTIVE" {
				activeNGs++
			}
		}
		tbl.Add("Nodegroups", fmt.Sprintf("%d active (%d nodes total)", activeNGs, totalNodes))
	}

	if details.Networking.VpcId != "" {
		vpc := details.Networking.VpcId
		if details.Networking.VpcCidr != "" {
			vpc += fmt.Sprintf(" (%s)", details.Networking.VpcCidr)
		}
		tbl.Add("VPC", vpc)
		if len(details.Networking.SubnetIds) > 0 {
			tbl.Add("Subnets", fmt.Sprintf("%d subnets", len(details.Networking.SubnetIds)))
		}
		if len(details.Networking.SecurityGroupIds) > 0 {
			tbl.Add("Security Groups", fmt.Sprintf("%d groups", len(details.Networking.SecurityGroupIds)))
		}
	}

	loggingStatus := "Disabled"
	if len(details.Security.LoggingEnabled) > 0 {
		loggingStatus = strings.Join(details.Security.LoggingEnabled, ", ") + " enabled"
	}
	tbl.Add("Logging", loggingStatus)

	encStatus := color.RedString("DISABLED")
	if details.Security.EncryptionEnabled {
		encStatus = color.GreenString("ENABLED") + " (at rest via KMS)"
	}
	tbl.Add("Encryption", encStatus)
	tbl.AddBool("Deletion Protection", details.Security.DeletionProtection)
	tbl.Add("Created", details.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
	tbl.Add("Age", formatAge(time.Since(details.CreatedAt)))
	tbl.Render()

	if len(details.Addons) > 0 {
		ui.Outln("\nAdd-ons:")
		cols := []ui.Column{
			{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 8, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 10, Align: ui.AlignLeft},
			{Title: "HEALTH", Min: 8, Align: ui.AlignLeft},
		}
		addTbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
		for _, a := range details.Addons {
			h := a.Health
			if h == "" {
				h = "Unknown"
			}
			addTbl.AddRow(truncate(a.Name, 24), a.Version, a.Status, formatAddonHealth(h))
		}
		addTbl.Render()
	}
	return nil
}

func outputComparisonJSON(comparison *clustersvc.ClusterComparison) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(comparison)
}

func outputComparisonYAML(comparison *clustersvc.ClusterComparison) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(comparison)
}

func outputComparisonTable(comparison *clustersvc.ClusterComparison, elapsed time.Duration) error {
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
		tbl.AddRow(truncate(cl.Name, 14), formatStatus(cl.Status), cl.Version, healthStatus)
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

func printDifferences(differences []clustersvc.Difference) {
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

func removeDuplicates(slice []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortClusterSummaries(items []clustersvc.ClusterSummary, key string, desc bool) []clustersvc.ClusterSummary {
	less := func(i, j int) bool { return false }
	switch strings.ToLower(key) {
	case "status":
		less = func(i, j int) bool { return items[i].Status < items[j].Status }
	case "version":
		less = func(i, j int) bool { return items[i].Version < items[j].Version }
	case "region":
		less = func(i, j int) bool { return items[i].Region < items[j].Region }
	default:
		less = func(i, j int) bool { return items[i].Name < items[j].Name }
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return !less(i, j)
		}
		return less(i, j)
	})
	return items
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

// colorString is the signature of fatih/color's *String helpers.
type colorString = func(format string, a ...interface{}) string

// severityColor maps a severity tag to its color helper.
func severityColor(severity string) colorString {
	switch severity {
	case "critical":
		return color.RedString
	case "warning":
		return color.YellowString
	case "info":
		return color.BlueString
	default:
		return fmt.Sprintf
	}
}

// decisionColor maps a health decision to its color helper.
func decisionColor(d health.Decision) colorString {
	switch d {
	case health.DecisionProceed:
		return color.GreenString
	case health.DecisionWarn:
		return color.YellowString
	case health.DecisionBlock:
		return color.RedString
	default:
		return color.WhiteString
	}
}

func formatDifferenceCount(count int, severity string) string {
	if count == 0 {
		return "0"
	}
	return severityColor(severity)("%d", count)
}

var statusStyles = map[string]struct {
	label string
	c     colorString
}{
	"ACTIVE":   {"Active", color.GreenString},
	"CREATING": {"Creating", color.YellowString},
	"UPDATING": {"Updating", color.YellowString},
	"DELETING": {"Deleting", color.RedString},
	"FAILED":   {"Failed", color.RedString},
}

func formatStatus(status string) string {
	if s, ok := statusStyles[strings.ToUpper(status)]; ok {
		return s.c(s.label)
	}
	return status
}

// healthLabel returns the short PASS/WARN/FAIL/UNKNOWN label for a decision.
func healthLabel(d health.Decision) string {
	switch d {
	case health.DecisionProceed:
		return "PASS"
	case health.DecisionWarn:
		return "WARN"
	case health.DecisionBlock:
		return "FAIL"
	default:
		return "UNKNOWN"
	}
}

func formatHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	c := decisionColor(h.Decision)
	switch h.Decision {
	case health.DecisionProceed:
		passed := 0
		for _, r := range h.Results {
			if r.Status == health.StatusPass {
				passed++
			}
		}
		return c("PASS (%d/%d checks passed)", passed, len(h.Results))
	case health.DecisionWarn:
		return c("WARN (%d issues)", len(h.Warnings)+len(h.Errors))
	case health.DecisionBlock:
		return c("FAIL (%d issues)", len(h.Errors))
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatAddonHealth(h string) string {
	switch h {
	case "Healthy":
		return color.GreenString("PASS")
	case "Issues", "Failed":
		return color.RedString("FAIL")
	case "Updating":
		return color.CyanString("[IN PROGRESS]")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatClusterHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	return decisionColor(h.Decision)(healthLabel(h.Decision))
}

func formatNodeCount(n clustersvc.NodeCountInfo) string {
	switch {
	case n.Total == 0:
		return "0/0 ready"
	case n.Ready == n.Total:
		return color.GreenString("%d/%d ready", n.Ready, n.Total)
	case n.Ready == 0:
		return color.RedString("%d/%d ready", n.Ready, n.Total)
	default:
		return color.YellowString("%d/%d ready", n.Ready, n.Total)
	}
}

func truncateEndpoint(endpoint string) string {
	if len(endpoint) > 120 {
		return endpoint[:117] + "..."
	}
	return endpoint
}

func formatAge(d time.Duration) string {
	if days := int(d.Hours() / 24); days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	if hours := int(d.Hours()); hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
