package clusterview

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func distinctRegionsSummary(summaries []clustersvc.ClusterSummary) int {
	seen := make(map[string]struct{}, len(summaries))
	for _, s := range summaries {
		seen[s.Region] = struct{}{}
	}
	return len(seen)
}

// clusterListLines builds the human `cluster list` table (pure, golden-testable)
// with tokenized status/health cells and an optional health summary chip line.
func clusterListLines(th *render.Theme, summaries []clustersvc.ClusterSummary, multiRegion, showHealth bool) []string {
	pal := th.Pal
	head := th.Bold(pal.Mauve, "CLUSTERS") + "  " + th.Paint(pal.White, fmt.Sprintf("%d", len(summaries)))
	if multiRegion {
		head += th.Paint(pal.Dim, fmt.Sprintf(" · %d regions", distinctRegionsSummary(summaries)))
	}
	out := []string{head, ""}

	cols := []ui.Column{{Title: "CLUSTER", Min: 14}}
	if multiRegion {
		cols = append(cols, ui.Column{Title: "REGION", Min: 10})
	}
	cols = append(cols, ui.Column{Title: "STATUS", Min: 9}, ui.Column{Title: "VERSION", Min: 7})
	if showHealth {
		cols = append(cols, ui.Column{Title: "HEALTH", Min: 8})
	}
	// NODES is desired capacity here: a fleet-wide `cluster list` can't reach
	// every cluster's Kubernetes API, so it reports the desired count rather
	// than a fabricated ready/desired fraction. (REF-130)
	cols = append(cols, ui.Column{Title: "NODES", Min: 7, Align: ui.AlignRight})

	tbl := th.NewTable(cols...)
	for _, s := range summaries {
		row := []string{th.Paint(pal.White, s.Name)}
		if multiRegion {
			row = append(row, th.Paint(pal.Dim, s.Region))
		}
		row = append(row, statusToken(th, s.Status), th.Paint(pal.White, s.Version))
		if showHealth {
			row = append(row, decisionToken(th, s.Health))
		}
		row = append(row, th.Paint(pal.Text, nodeCountInfoText(s.NodeCount)))
		tbl.Row(row...)
	}
	out = append(out, tbl.Render()...)
	if showHealth {
		if chips := clusterHealthChips(th, summaries); chips != "" {
			out = append(out, "", chips)
		}
	}
	return out
}

// decisionToken renders a cluster's health decision as a colored token, or a
// dim dash when health wasn't gathered.
func decisionToken(th *render.Theme, h *health.HealthSummary) string {
	if h == nil {
		return th.Paint(th.Pal.Dim, "—")
	}
	st, _ := decisionStatusColor(th, h.Decision)
	return th.Tokenf(st, string(h.Decision))
}

func clusterHealthChips(th *render.Theme, summaries []clustersvc.ClusterSummary) string {
	var healthy, warning, critical int
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
	var parts []string
	if healthy > 0 {
		parts = append(parts, th.Token(render.Healthy, fmt.Sprintf("%d healthy", healthy)))
	}
	if warning > 0 {
		parts = append(parts, th.Token(render.Warn, fmt.Sprintf("%d warning", warning)))
	}
	if critical > 0 {
		parts = append(parts, th.Token(render.Fail, fmt.Sprintf("%d critical", critical)))
	}
	if len(parts) == 0 {
		return ""
	}
	return joinSpaced(parts)
}

func joinSpaced(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "   "
		}
		out += p
	}
	return out
}
