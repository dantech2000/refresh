package clusterview

import (
	"fmt"
	"io"
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
// chip line); `-o plain` writes pure TSV (header + one row per cluster) and
// sends the empty-list notice to stderr.
func OutputClustersTable(summaries []clustersvc.ClusterSummary, elapsed time.Duration, multiRegion bool, showHealth bool) error {
	if ui.PlainOutput() {
		if len(summaries) == 0 {
			_, _ = fmt.Fprintln(ui.Stderr, "No EKS clusters found")
		}
		clusterListPlain(summaries, multiRegion, showHealth).Render()
		return nil
	}
	if len(summaries) == 0 {
		color.Yellow("No EKS clusters found")
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range clusterListLines(th, summaries, multiRegion, showHealth) {
		fmt.Println(line)
	}
	return nil
}

// WriteClustersHint renders the cluster table to w (typically stderr) as a
// hint when a command could not resolve a cluster. Unlike OutputClustersTable
// it never touches stdout, so scripted consumers see no data on failure.
func WriteClustersHint(w io.Writer, summaries []clustersvc.ClusterSummary) {
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(w, "No EKS clusters found")
		return
	}
	th := render.Default(w)
	for _, line := range clusterListLines(th, summaries, false, false) {
		_, _ = fmt.Fprintln(w, line)
	}
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
