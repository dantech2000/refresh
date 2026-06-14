package clusterview

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// clusterDetailLines builds the human `cluster describe` view as a slice of
// lines (pure, so it is golden-testable). Sections: a name/status header,
// OVERVIEW key/values, NODEGROUPS + ADD-ONS tables, and a HEALTH card.
func clusterDetailLines(th *render.Theme, d *clustersvc.ClusterDetails, elapsed time.Duration) []string {
	pal := th.Pal

	header := th.Bold(pal.White, d.Name) + "   " + statusToken(th, d.Status)
	if d.Region != "" {
		header += th.Paint(pal.Dim, "   "+d.Region)
	}
	out := []string{header, "", th.Section("OVERVIEW")}

	version := th.Paint(pal.White, d.Version)
	if d.PlatformVersion != "" {
		version += th.Paint(pal.Dim, " · "+d.PlatformVersion)
	}
	kv := [][2]string{
		{"version", version},
		{"status", statusToken(th, d.Status)},
	}
	if d.Endpoint != "" {
		kv = append(kv, [2]string{"endpoint", th.Paint(pal.Sky, truncateEndpoint(d.Endpoint))})
	}
	if d.Networking.VpcId != "" {
		vpc := d.Networking.VpcId
		if d.Networking.VpcCidr != "" {
			vpc += " (" + d.Networking.VpcCidr + ")"
		}
		kv = append(kv, [2]string{"vpc", th.Paint(pal.Text, vpc)})
	}
	logging := "disabled"
	if len(d.Security.LoggingEnabled) > 0 {
		logging = strings.Join(d.Security.LoggingEnabled, ",") + " enabled"
	}
	kv = append(kv, [2]string{"logging", th.Paint(pal.Text, logging)})
	if d.Security.EncryptionEnabled {
		kv = append(kv, [2]string{"encryption", th.Token(render.Healthy, "enabled (KMS)")})
	} else {
		kv = append(kv, [2]string{"encryption", th.Token(render.Fail, "disabled")})
	}
	if d.CreatedAt.IsZero() {
		kv = append(kv, [2]string{"created", th.Paint(pal.Dim, "unknown")})
	} else {
		kv = append(kv, [2]string{"created",
			th.Paint(pal.Text, d.CreatedAt.Format("2006-01-02")) +
				th.Paint(pal.Dim, "  ("+formatAge(time.Since(d.CreatedAt))+")")})
	}
	for _, line := range th.KV(kv) {
		out = append(out, "  "+line)
	}

	if len(d.Nodegroups) > 0 {
		// The header "N nodes" is desired capacity (always known); per-row NODES
		// shows measured ready/desired only when readiness was measured. (REF-130)
		active, nodes := 0, int32(0)
		for _, ng := range d.Nodegroups {
			nodes += ng.DesiredSize
			if ng.Status == "ACTIVE" {
				active++
			}
		}
		out = append(out, "", th.Section("NODEGROUPS")+th.Paint(pal.Dim, fmt.Sprintf("  %d active · %d nodes", active, nodes)))
		tbl := th.NewTable(
			ui.Column{Title: "NAME", Min: 8, Max: 28},
			ui.Column{Title: "INSTANCE", Min: 8},
			ui.Column{Title: "NODES", Min: 5},
			ui.Column{Title: "STATUS", Min: 8},
		)
		for _, ng := range d.Nodegroups {
			tbl.Row(
				th.Paint(pal.White, ng.Name),
				th.Paint(pal.Text, ng.InstanceType),
				th.Paint(pal.Text, nodeCountText(ng.ReadyKnown, ng.ReadyNodes, ng.DesiredSize)),
				th.Token(render.StatusFromString(ng.Status), ng.Status),
			)
		}
		for _, l := range tbl.Render() {
			out = append(out, "  "+l)
		}
	}

	if len(d.Addons) > 0 {
		out = append(out, "", th.Section("ADD-ONS")+th.Paint(pal.Dim, fmt.Sprintf("  %d installed", len(d.Addons))))
		tbl := th.NewTable(
			ui.Column{Title: "NAME", Min: 8, Max: 24},
			ui.Column{Title: "VERSION", Min: 8},
			ui.Column{Title: "HEALTH", Min: 8},
		)
		for _, a := range d.Addons {
			h := a.Health
			if h == "" {
				h = "Unknown"
			}
			tbl.Row(
				th.Paint(pal.White, a.Name),
				th.Paint(pal.Text, a.Version),
				th.Token(render.StatusFromString(h), h),
			)
		}
		for _, l := range tbl.Render() {
			out = append(out, "  "+l)
		}
	}

	if len(d.HealthIssues) > 0 {
		out = append(out, "")
		out = append(out, healthIssueLines(th, d.HealthIssues)...)
	}

	if d.Health != nil {
		out = append(out, "")
		out = append(out, healthCardLines(th, d.Health)...)
	}
	return out
}

// healthIssueLines renders the HEALTH ISSUES section: AWS-reported control-plane
// problems (DescribeCluster Health.Issues) as failing tokens with affected
// resource IDs. Shared shape with nodegroup/addon issues.
func healthIssueLines(th *render.Theme, issues []clustersvc.HealthIssue) []string {
	out := []string{th.Section("HEALTH ISSUES") + th.Paint(th.Pal.Dim, fmt.Sprintf("  %d", len(issues)))}
	for _, iss := range issues {
		label := iss.Code
		if iss.Message != "" {
			label += ": " + iss.Message
		}
		out = append(out, "  "+th.Token(render.Fail, label))
		if len(iss.ResourceIds) > 0 {
			out = append(out, "    "+th.Paint(th.Pal.Dim, strings.Join(iss.ResourceIds, ", ")))
		}
	}
	return out
}

// statusToken renders a free-form status as a colored glyph + the status text.
func statusToken(th *render.Theme, status string) string {
	if status == "" {
		return th.Token(render.Unknown, "unknown")
	}
	return th.Token(render.StatusFromString(status), status)
}

// healthCardLines renders the HEALTH section: a verdict + score bar + a
// one-line summary. Shared by `cluster describe` and `upgrade-check`.
func healthCardLines(th *render.Theme, h *health.HealthSummary) []string {
	st, col := decisionStatusColor(th, h.Decision)
	head := th.Section("HEALTH") +
		th.Paint(th.Pal.Dim, fmt.Sprintf("  %d/100 · ", h.OverallScore)) +
		th.Tokenf(st, string(h.Decision))
	bar := th.Bar(h.OverallScore, 100, 24, col)
	return []string{head, "  " + bar + "  " + th.Paint(th.Pal.Dim, healthSummaryMsg(h))}
}

func decisionStatusColor(th *render.Theme, d health.Decision) (render.Status, render.Color) {
	switch d {
	case health.DecisionProceed:
		return render.Healthy, th.Pal.Green
	case health.DecisionWarn:
		return render.Warn, th.Pal.Yellow
	case health.DecisionBlock:
		return render.Fail, th.Pal.Red
	default:
		return render.Unknown, th.Pal.Dim
	}
}

func healthSummaryMsg(h *health.HealthSummary) string {
	if len(h.Errors) > 0 {
		return h.Errors[0]
	}
	if len(h.Warnings) > 0 {
		return h.Warnings[0]
	}
	return "all checks passed"
}
