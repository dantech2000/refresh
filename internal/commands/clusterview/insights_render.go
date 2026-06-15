package clusterview

import (
	"fmt"
	"strings"

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
	switch {
	case errc > 0:
		return render.Fail, "NOT READY"
	case warnc > 0 || len(report.Skew.Findings) > 0:
		return render.Warn, "REVIEW"
	default:
		return render.Healthy, "READY"
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
