package clusterview

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputClustersTable renders a table of cluster summaries. The human path uses
// the render design system (tokenized status/health cells, a health summary
// chip line); `-o plain` keeps the uncolored tab-separated table.
func OutputClustersTable(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}
	if !ui.PlainOutput() {
		th := render.Default(os.Stdout)
		for _, line := range clusterListLines(th, summaries, multiRegion, showHealth) {
			fmt.Println(line)
		}
		return nil
	}
	return outputClustersPlain(summaries, elapsed, multiRegion, showHealth)
}

// outputClustersPlain renders the uncolored tab-separated cluster table for
// `-o plain`.
func outputClustersPlain(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if multiRegion {
		regions := make(map[string]bool)
		for _, s := range summaries {
			regions[s.Region] = true
		}
		ui.Outf("EKS Clusters (%d regions, %d clusters)\n", len(regions), len(summaries))
	} else {
		ui.Outf("EKS Clusters (%d clusters)\n", len(summaries))
	}
	ui.PrintElapsed(elapsed)

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

	tbl := ui.NewPTable(cols, ui.CyanHeaders())
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
		renderHealthSummary(summaries)
	}
	return nil
}

func renderHealthSummary(summaries []clustersvc.ClusterSummary) {
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

// OutputClustersTree renders cluster summaries grouped by region as a tree.
func OutputClustersTree(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
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
			if showHealth {
				status = treeStatusWithHealth(c.Status, c.Health)
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
			switch s.Health.Decision {
			case health.DecisionProceed:
				healthy++
			case health.DecisionWarn:
				warning++
			case health.DecisionBlock:
				critical++
			}
		}
		ui.Outf("\nHealth Summary: %s healthy, %s warnings, %s critical\n",
			color.GreenString("%d", healthy), color.YellowString("%d", warning), color.RedString("%d", critical))
	}
	return nil
}

// SortClusterSummaries sorts items in place by key and returns the slice.
func SortClusterSummaries(items []clustersvc.ClusterSummary, key string, desc bool) []clustersvc.ClusterSummary {
	var less func(i, j int) bool
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
			// Swap arguments rather than negating: !less(i,j) returns true
			// for equal elements, which violates the sort contract and
			// destroys SliceStable's stability.
			return less(j, i)
		}
		return less(i, j)
	})
	return items
}
