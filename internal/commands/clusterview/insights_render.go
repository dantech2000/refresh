package clusterview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// upgradeVerdict collapses the insight/skew severity into one readiness token:
// any error → not ready (fail); any warning or version skew → review (warn);
// otherwise ready (healthy).
func upgradeVerdict(report *clustersvc.UpgradeReport) (render.Status, string) {
	errc, warnc := 0, 0
	for _, in := range report.Insights {
		switch strings.ToUpper(in.Status) {
		case clustersvc.InsightStatusError:
			errc++
		case clustersvc.InsightStatusWarning:
			warnc++
		}
	}
	// The control-plane gate participates in the verdict: a blocking failure
	// (e.g. etcd near the read-only limit) is NOT READY; a warning is REVIEW.
	cpFail, cpWarn := false, false
	if cp := report.ControlPlane; cp != nil && !cp.Skipped {
		switch cp.Status {
		case health.StatusFail:
			cpFail = true
		case health.StatusWarn:
			cpWarn = true
		}
	}
	switch {
	case errc > 0 || cpFail:
		return render.Fail, "NOT READY"
	case warnc > 0 || cpWarn || len(report.Skew.Findings) > 0:
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

// upgradeCheckLines builds the human `cluster upgrade-check` view (pure,
// golden-testable): a readiness verdict, the AWS Cluster Insights table, and the
// local version-skew section.
func upgradeCheckLines(th *render.Theme, report *clustersvc.UpgradeReport) []string {
	pal := th.Pal
	st, verdict := upgradeVerdict(report)
	out := []string{
		th.Bold(pal.White, "UPGRADE READINESS") + th.Paint(pal.Dim, "  "+report.Cluster) + "   " + th.Tokenf(st, verdict),
	}
	if report.Support != nil {
		out = append(out, th.Paint(pal.Dim, "support  ")+supportToken(th, report.Support))
	}
	out = append(out,
		"",
		th.Section("INSIGHTS")+th.Paint(pal.Dim, fmt.Sprintf("  %d", len(report.Insights))),
	)

	if len(report.Insights) == 0 {
		out = append(out, "  "+th.Token(render.Healthy, "no upgrade insights to address"))
	} else {
		tbl := th.NewTable(
			ui.Column{Title: "ID", Min: 8},
			ui.Column{Title: "NAME", Min: 20, Max: 48},
			ui.Column{Title: "CATEGORY", Min: 14},
			ui.Column{Title: "STATUS", Min: 10},
			ui.Column{Title: "K8S", Min: 6},
			ui.Column{Title: "LAST REFRESH", Min: 14},
		)
		var errc, warnc, passc int
		for _, in := range report.Insights {
			switch strings.ToUpper(in.Status) {
			case clustersvc.InsightStatusError:
				errc++
			case clustersvc.InsightStatusWarning:
				warnc++
			case clustersvc.InsightStatusPassing:
				passc++
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
		out = append(out, "  "+insightCountChips(th, errc, warnc, passc))
		// Tell the user how to drill in — the detail view accepts the short ID
		// above or a name substring, so they never need to copy a raw UUID.
		out = append(out, "  "+th.Paint(pal.Dim, "drill into one: cluster upgrade-check -c "+report.Cluster+" --id <id|name>"))
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
	if len(report.Skew.Findings) == 0 {
		out = append(out, "  "+th.Token(render.Healthy, "nodegroups and addons are current"))
	} else {
		for _, f := range report.Skew.Findings {
			out = append(out, "  "+th.Token(render.Warn, f))
		}
	}
	return out
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

func insightCountChips(th *render.Theme, errc, warnc, passc int) string {
	var parts []string
	if errc > 0 {
		parts = append(parts, th.Token(render.Fail, fmt.Sprintf("%d error", errc)))
	}
	if warnc > 0 {
		parts = append(parts, th.Token(render.Warn, fmt.Sprintf("%d warning", warnc)))
	}
	parts = append(parts, th.Token(render.Healthy, fmt.Sprintf("%d passing", passc)))
	return joinSpaced(parts)
}
