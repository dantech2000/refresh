package commands

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
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func outputClustersJSON(summaries []cluster.ClusterSummary) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"clusters": summaries, "count": len(summaries)})
}

func outputClustersYAML(summaries []cluster.ClusterSummary) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(map[string]any{"clusters": summaries, "count": len(summaries)})
}

func outputClustersTable(summaries []cluster.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	if multiRegion {
		regions := make(map[string]bool)
		for _, s := range summaries {
			regions[s.Region] = true
		}
		regionCount := len(regions)
		ui.Outf("EKS Clusters (%d regions, %d clusters)\n", regionCount, len(summaries))
	} else {
		ui.Outf("EKS Clusters (%d clusters)\n", len(summaries))
	}
	ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	headerColor := func(s string) string { return color.CyanString(s) }
	if multiRegion {
		cols := []ui.Column{
			{Title: "CLUSTER", Min: 14, Align: ui.AlignLeft},
			{Title: "REGION", Min: 10, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 7, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 7, Align: ui.AlignLeft},
		}
		if showHealth {
			cols = append(cols, ui.Column{Title: "HEALTH", Min: 8, Align: ui.AlignLeft})
		}
		cols = append(cols, ui.Column{Title: "READY/DESIRED", Min: 15, Align: ui.AlignRight})
		tbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(headerColor))
		for _, s := range summaries {
			if showHealth {
				tbl.AddRow(s.Name, s.Region, formatStatus(s.Status), s.Version, formatClusterHealth(s.Health), formatNodeCount(s.NodeCount))
			} else {
				tbl.AddRow(s.Name, s.Region, formatStatus(s.Status), s.Version, formatNodeCount(s.NodeCount))
			}
		}
		tbl.Render()
	} else {
		cols := []ui.Column{
			{Title: "CLUSTER", Min: 14, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 7, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 7, Align: ui.AlignLeft},
		}
		if showHealth {
			cols = append(cols, ui.Column{Title: "HEALTH", Min: 8, Align: ui.AlignLeft})
		}
		cols = append(cols, ui.Column{Title: "READY/DESIRED", Min: 15, Align: ui.AlignRight})
		tbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(headerColor))
		for _, s := range summaries {
			if showHealth {
				tbl.AddRow(s.Name, formatStatus(s.Status), s.Version, formatClusterHealth(s.Health), formatNodeCount(s.NodeCount))
			} else {
				tbl.AddRow(s.Name, formatStatus(s.Status), s.Version, formatNodeCount(s.NodeCount))
			}
		}
		tbl.Render()
	}

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

func outputClustersTree(summaries []cluster.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}

	regionGroups := make(map[string][]cluster.ClusterSummary)
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

func sortClusterSummaries(items []cluster.ClusterSummary, key string, desc bool) []cluster.ClusterSummary {
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

func formatClusterHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	switch h.Decision {
	case health.DecisionProceed:
		return color.GreenString("PASS")
	case health.DecisionWarn:
		return color.YellowString("WARN")
	case health.DecisionBlock:
		return color.RedString("FAIL")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatNodeCount(n cluster.NodeCountInfo) string {
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
