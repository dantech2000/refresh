package clusterview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

// upgradeVerdict maps the report's readiness (clustersvc.UpgradeReport.
// Readiness, which the command's exit code also follows) to one token:
// blocked is NOT READY, unreadable skew data is INCOMPLETE, warnings only
// is REVIEW, otherwise READY.
func upgradeVerdict(report *clustersvc.UpgradeReport) (render.Status, string) {
	switch level, _ := report.Readiness(); level {
	case clustersvc.ReadinessBlocked:
		return render.Fail, "NOT READY"
	case clustersvc.ReadinessIncomplete:
		return render.Unknown, "INCOMPLETE"
	case clustersvc.ReadinessReview:
		return render.Warn, "REVIEW"
	default:
		return render.Healthy, "READY"
	}
}

// healthStatusToken maps a health check status to a render status token.
func healthStatusToken(s health.HealthStatus) render.Status {
	switch s {
	case health.StatusPass:
		return render.Healthy
	case health.StatusWarn:
		return render.Warn
	case health.StatusFail:
		return render.Fail
	default:
		return render.Unknown
	}
}

func insightToken(th *render.Theme, status string) string {
	switch strings.ToUpper(status) {
	case clustersvc.InsightStatusError:
		return th.Token(render.Fail, status)
	case clustersvc.InsightStatusWarning:
		return th.Token(render.Warn, status)
	case clustersvc.InsightStatusPassing:
		return th.Token(render.Healthy, status)
	default:
		return th.Token(render.Unknown, status)
	}
}

// insightColumns is the upgrade-check insights column set, shared by the
// human table and the `-o plain` header.
func insightColumns() []ui.Column {
	return []ui.Column{
		{Title: "ID", Min: 8},
		{Title: "NAME", Min: 20, Max: 48},
		{Title: "CATEGORY", Min: 14},
		{Title: "STATUS", Min: 10},
		{Title: "K8S", Min: 6},
		{Title: "LAST REFRESH", Min: 14},
	}
}

// defaultInsightCategory is the --category that upgrade-check reads when none
// is given.
const defaultInsightCategory = "UPGRADE_READINESS"

// isDefaultCategory reports whether category is the upgrade-readiness one
// ("" counts as the default).
func isDefaultCategory(category string) bool {
	c := strings.ToUpper(strings.TrimSpace(category))
	return c == "" || c == defaultInsightCategory
}

// insightsTitle is the upgrade-check title for the insight category the
// report holds: "UPGRADE READINESS" for the default one, else the category
// ("MISCONFIGURATION INSIGHTS").
func insightsTitle(category string) string {
	if isDefaultCategory(category) {
		return "UPGRADE READINESS"
	}
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(category)), "_", " ") + " INSIGHTS"
}

// drillHint is the command that opens one insight of the report's category.
func drillHint(cluster, category string) string {
	hint := "refresh cluster upgrade-check -c " + cluster + " --id <id|name>"
	if !isDefaultCategory(category) {
		hint += " --category " + strings.ToUpper(strings.TrimSpace(category))
	}
	return hint
}

// blockingFindings is how many of the report's skew findings block the
// upgrade: skewFindings lists the nodegroups at the kubelet skew limit first.
func blockingFindings(skew clustersvc.SkewReport) int {
	n := 0
	for _, ng := range skew.Nodegroups {
		if ng.Blocking {
			n++
		}
	}
	return min(n, len(skew.Findings))
}

// noSkewText is the version-skew line when there is no finding. With read
// failures it covers only what was read.
func noSkewText(report *clustersvc.UpgradeReport) string {
	if len(report.Failures) > 0 {
		return "no skew in the nodegroups and addons that could be read"
	}
	return "nodegroups and addons are current"
}

// upgradeCheckLines builds the human `cluster upgrade-check` view (pure,
// golden-testable): a readiness verdict, the AWS Cluster Insights table for
// category (--category), the local version-skew section, and the INCOMPLETE
// DATA section for failures.
func upgradeCheckLines(th *render.Theme, report *clustersvc.UpgradeReport, category string) []string {
	pal := th.Pal
	st, verdict := upgradeVerdict(report)
	out := []string{
		th.Bold(pal.White, insightsTitle(category)) + th.Paint(pal.Dim, "  "+report.Cluster) + "   " + th.Tokenf(st, verdict),
	}
	if report.Support != nil {
		out = append(out, th.Paint(pal.Dim, "support  ")+supportToken(th, report.Support))
	}
	if report.Rollback != nil {
		out = append(out, th.Paint(pal.Dim, "rollback ")+th.Token(render.Neutral, rollbackText(report.Rollback))+
			th.Paint(pal.Dim, "  refresh cluster rollback "+report.Cluster))
	}
	out = append(out,
		"",
		th.Section("INSIGHTS")+th.Paint(pal.Dim, fmt.Sprintf("  %d", len(report.Insights))),
	)

	if len(report.Insights) == 0 && report.InsightsNotEvaluated {
		out = append(out, "  "+th.Token(render.Warn, "EKS has not evaluated upgrade insights for this cluster yet"),
			"    "+th.Paint(pal.Dim, "a new cluster waits up to a day; cluster upgrade asks EKS to evaluate them first"))
	} else if len(report.Insights) == 0 {
		kind := "upgrade"
		if !isDefaultCategory(category) {
			kind = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(category), "_", " "))
		}
		out = append(out, "  "+th.Token(render.Healthy, "no "+kind+" insights to address"))
	} else {
		tbl := th.NewTable(insightColumns()...)
		var errc, warnc, passc, unknownc int
		for _, in := range report.Insights {
			switch strings.ToUpper(in.Status) {
			case clustersvc.InsightStatusError:
				errc++
			case clustersvc.InsightStatusWarning:
				warnc++
			case clustersvc.InsightStatusPassing:
				passc++
			default:
				unknownc++
			}
			refresh := "-"
			if in.LastRefreshTime != nil {
				refresh = in.LastRefreshTime.Format(insightTimeLayout)
			}
			tbl.Row(
				th.Paint(pal.Dim, shortID(in.ID)),
				th.Paint(pal.White, in.Name),
				th.Paint(pal.Text, in.Category),
				insightToken(th, in.Status),
				th.Paint(pal.Text, valueOrDash(in.KubernetesVersion)),
				th.Paint(pal.Dim, refresh),
			)
		}
		for _, l := range tbl.Render() {
			out = append(out, "  "+l)
		}
		out = append(out, "  "+insightCountChips(th, errc, warnc, passc, unknownc))
		// Tell the user how to drill in — the detail view accepts the short ID
		// above or a name substring, so they never need to copy a raw UUID.
		out = append(out, "  "+th.Paint(pal.Dim, "drill into one: "+drillHint(report.Cluster, category)))
	}

	if cp := report.ControlPlane; cp != nil {
		out = append(out, "", th.Section("CONTROL PLANE"))
		if cp.Skipped {
			out = append(out, "  "+th.Paint(pal.Dim, cp.Message))
		} else {
			out = append(out, "  "+th.Tokenf(healthStatusToken(cp.Status), cp.Message))
			for _, d := range cp.Details {
				out = append(out, "    "+th.Paint(pal.Dim, d))
			}
		}
	}

	out = append(out, "", th.Section("VERSION SKEW")+th.Paint(pal.Dim, "  control plane "+valueOrDash(report.Skew.ControlPlaneVersion)))
	switch {
	case len(report.Skew.Findings) > 0:
		// A nodegroup at the kubelet skew limit blocks the upgrade, as in
		// the verdict and in `cluster upgrade`; the rest are warnings.
		blocking := blockingFindings(report.Skew)
		for i, f := range report.Skew.Findings {
			st := render.Warn
			if i < blocking {
				st = render.Fail
			}
			out = append(out, "  "+th.Token(st, f))
		}
	case len(report.Failures) > 0:
		out = append(out, "  "+th.Token(render.Unknown, noSkewText(report)))
	default:
		out = append(out, "  "+th.Token(render.Healthy, noSkewText(report)))
	}
	return append(out, th.FailureSection(report.Failures)...)
}

// shortID trims an insight UUID to a copy-pasteable prefix for the table; the
// detail view accepts this prefix (or a name) so the full UUID never has to be
// typed.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "-"
	}
	return id
}

// wrapText collapses whitespace in a (possibly Markdown) blob and word-wraps it
// to width, so long descriptions/recommendations read as a clean paragraph
// instead of one runaway line. Deterministic — golden-testable.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	lines := make([]string, 0)
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	return append(lines, cur)
}

// insightDetailLines builds the human DescribeInsight detail view in the design
// system (pure, golden-testable): header + status, overview, description,
// recommendation, affected resources, doc links (additionalInfo), and — for
// deprecated-API insights — the per-API caller breakdown.
func insightDetailLines(th *render.Theme, d *clustersvc.InsightDetail) []string {
	pal := th.Pal
	const wrap = 88

	header := th.Bold(pal.White, valueOrDash(d.Name)) + "   " + insightToken(th, d.Status)
	out := []string{header}
	if d.StatusReason != "" {
		out = append(out, "  "+th.Paint(pal.Dim, d.StatusReason))
	}

	kv := [][2]string{{"category", th.Paint(pal.Text, valueOrDash(d.Category))}}
	if d.KubernetesVersion != "" {
		kv = append(kv, [2]string{"targets", th.Paint(pal.White, d.KubernetesVersion)})
	}
	if d.ID != "" {
		kv = append(kv, [2]string{"id", th.Paint(pal.Dim, d.ID)})
	}
	if d.LastRefreshTime != nil {
		kv = append(kv, [2]string{"refreshed", th.Paint(pal.Dim, d.LastRefreshTime.Format(insightTimeLayout))})
	}
	out = append(out, "", th.Section("OVERVIEW"))
	for _, l := range th.KV(kv) {
		out = append(out, "  "+l)
	}

	if d.Description != "" {
		out = append(out, "", th.Section("DESCRIPTION"))
		for _, l := range wrapText(d.Description, wrap) {
			out = append(out, "  "+th.Paint(pal.Text, l))
		}
	}
	if d.Recommendation != "" {
		out = append(out, "", th.Section("RECOMMENDATION"))
		for _, l := range wrapText(d.Recommendation, wrap) {
			out = append(out, "  "+th.Paint(pal.White, l))
		}
	}
	if len(d.Resources) > 0 {
		out = append(out, "", th.Section("AFFECTED RESOURCES")+th.Paint(pal.Dim, fmt.Sprintf("  %d", len(d.Resources))))
		for _, r := range d.Resources {
			out = append(out, "  "+th.Paint(pal.Text, "- "+r))
		}
	}
	if len(d.AdditionalInfo) > 0 {
		out = append(out, "", th.Section("MORE INFORMATION"))
		keys := make([]string, 0, len(d.AdditionalInfo))
		for k := range d.AdditionalInfo {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order (map iteration is random)
		for _, k := range keys {
			out = append(out, "  "+th.Paint(pal.White, k), "    "+th.Paint(pal.Sky, d.AdditionalInfo[k]))
		}
	}
	return append(out, insightDeprecationLines(th, d.Deprecations)...)
}

// insightDeprecationLines renders the deprecated-API breakdown in the design
// system: each deprecated API → its replacement (and removal version), then the
// clients still calling it (most-active first), plus the 30-day audit caveat.
func insightDeprecationLines(th *render.Theme, deps []clustersvc.DeprecationDetail) []string {
	if len(deps) == 0 {
		return nil
	}
	pal := th.Pal
	out := []string{"", th.Section("DEPRECATED APIs") + th.Paint(pal.Dim, fmt.Sprintf("  %d", len(deps)))}
	for _, d := range deps {
		head := valueOrDash(d.Usage)
		if d.ReplacedWith != "" {
			head += " → " + d.ReplacedWith
		}
		if d.StopServingVersion != "" {
			head += fmt.Sprintf(" (removed in %s)", d.StopServingVersion)
		}
		out = append(out, "  "+th.Token(render.Fail, head))
		for _, c := range d.ClientStats {
			last := "-"
			if c.LastRequestTime != nil {
				last = c.LastRequestTime.Format(insightTimeLayout)
			}
			out = append(out, "      "+th.Paint(pal.White, valueOrDash(c.UserAgent))+
				th.Paint(pal.Dim, fmt.Sprintf("  ·  %d req/30d  ·  last seen %s", c.NumberOfRequestsLast30Days, last)))
		}
	}
	return append(out, "  "+th.Paint(pal.Dim, "note: EKS reads audit logs on a 30-day window — a check stays ERROR until the last call ages out."))
}

// insightCountChips is the count line under the insights table, one chip per
// status that is present. PASSING insights are hidden unless asked for, so
// a "0 passing" chip would read as "none passed".
func insightCountChips(th *render.Theme, errc, warnc, passc, unknownc int) string {
	var parts []string
	if errc > 0 {
		parts = append(parts, th.Token(render.Fail, fmt.Sprintf("%d error", errc)))
	}
	if unknownc > 0 {
		parts = append(parts, th.Token(render.Unknown, fmt.Sprintf("%d unknown", unknownc)))
	}
	if warnc > 0 {
		parts = append(parts, th.Token(render.Warn, fmt.Sprintf("%d warning", warnc)))
	}
	if passc > 0 {
		parts = append(parts, th.Token(render.Healthy, fmt.Sprintf("%d passing", passc)))
	}
	return joinSpaced(parts)
}

// rollbackText says when a rollback is available, with the rollback
// readiness insight counts when they were read.
func rollbackText(a *upgrade.RollbackAvailability) string {
	s := fmt.Sprintf("to %s available until about %s", a.PreviousVersion, a.AvailableUntil.UTC().Format("2006-01-02 15:04 MST"))
	if c := a.Insights; c != nil {
		var parts []string
		for _, p := range []struct {
			n    int
			name string
		}{{c.Error, "error"}, {c.Unknown, "unknown"}, {c.Warning, "warning"}, {c.Passing, "passing"}} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
			}
		}
		if len(parts) > 0 {
			s += " (rollback insights: " + strings.Join(parts, ", ") + ")"
		}
	}
	return s
}
