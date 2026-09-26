// Package healthview renders pre-flight health results for humans with the
// render design system: one status vocabulary (glyph + label + color) for the
// `nodegroup update` pre-flight report and the `cluster describe` HEALTH card.
// Machine formats never come through here; the health.HealthSummary document
// is encoded by runner.EncodeStdout.
package healthview

import (
	"fmt"
	"io"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
)

// barWidth is the cell width of a per-check score bar.
const barWidth = 20

// CheckStatus maps a check status to its render status.
func CheckStatus(s health.HealthStatus) render.Status {
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

// Decision maps a verdict to its render status and the color of its score
// bar.
func Decision(th *render.Theme, d health.Decision) (render.Status, render.Color) {
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

// statusColor is the palette color of a render status, for score bars.
func statusColor(th *render.Theme, s render.Status) render.Color {
	switch s {
	case render.Healthy:
		return th.Pal.Green
	case render.Warn:
		return th.Pal.Yellow
	case render.Fail:
		return th.Pal.Red
	default:
		return th.Pal.Dim
	}
}

// statusLabel is the upper-case word shown for a check status.
func statusLabel(s health.HealthStatus) string {
	if s == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(string(s))
}

// verdictText is the pre-flight report's wording for a decision.
func verdictText(d health.Decision) string {
	switch d {
	case health.DecisionProceed:
		return "READY FOR UPDATE"
	case health.DecisionWarn:
		return "READY WITH WARNINGS"
	case health.DecisionBlock:
		return "CRITICAL ISSUES FOUND"
	default:
		return "UNKNOWN"
	}
}

// nameWidth is the width of the longest check name.
func nameWidth(results []health.HealthResult) int {
	w := 0
	for _, r := range results {
		w = max(w, len(r.Name))
	}
	return w
}

// CheckRows itemizes each check as glyph + name + message, indented four
// spaces. A skipped check (missing prerequisite) renders dimmed with the
// neutral glyph, so a gap reads as "not measured", not "passed".
func CheckRows(th *render.Theme, results []health.HealthResult) []string {
	if len(results) == 0 {
		return nil
	}
	w := nameWidth(results)
	out := make([]string, 0, len(results))
	for _, r := range results {
		name := r.Name + strings.Repeat(" ", w-len(r.Name))
		if r.Skipped {
			out = append(out, "    "+th.Glyph(render.Neutral)+" "+th.Paint(th.Pal.Dim, name+"  "+r.Message))
			continue
		}
		out = append(out, "    "+th.Glyph(CheckStatus(r.Status))+" "+
			th.Paint(th.Pal.White, name)+th.Paint(th.Pal.Dim, "  "+r.Message))
	}
	return out
}

// ReportLines builds the pre-flight health report of `nodegroup update`: one
// row per check (score bar, name, status token), the verdict, and the
// warnings and errors. It is pure, so tests can check it without a terminal.
func ReportLines(th *render.Theme, s health.HealthSummary) []string {
	out := []string{"", th.Section("CLUSTER HEALTH ASSESSMENT"), ""}
	w := max(20, nameWidth(s.Results))
	for _, r := range s.Results {
		name := r.Name + strings.Repeat(" ", w-len(r.Name))
		if r.Skipped {
			// Not measured: an empty bar and a neutral SKIP token, never PASS.
			out = append(out, "  "+th.Bar(0, 100, barWidth, th.Pal.Dim)+" "+
				th.Paint(th.Pal.Dim, name)+"  "+th.Token(render.Neutral, th.Paint(th.Pal.Dim, "SKIP")))
			continue
		}
		st := CheckStatus(r.Status)
		out = append(out, "  "+th.Bar(r.Score, 100, barWidth, statusColor(th, st))+" "+
			th.Paint(th.Pal.White, name)+"  "+th.Tokenf(st, statusLabel(r.Status)))
	}

	st, _ := Decision(th, s.Decision)
	verdict := "Status: " + th.Tokenf(st, verdictText(s.Decision))
	if n := len(s.Warnings) + len(s.Errors); n > 0 {
		verdict += th.Paint(th.Pal.Dim, " ("+render.Plural(n, "issue")+" found)")
	}
	out = append(out, "", verdict)

	if len(s.Warnings) > 0 {
		out = append(out, "", "Warnings:")
		for _, m := range s.Warnings {
			out = append(out, "  "+th.Token(render.Warn, m))
		}
	}
	if len(s.Errors) > 0 {
		out = append(out, "", "Errors:")
		for _, m := range s.Errors {
			out = append(out, "  "+th.Token(render.Fail, m))
		}
	}
	return out
}

// WriteReport writes ReportLines to w, themed for w: -o json/yaml runs pass
// ui.Stderr so the report never mixes with the document.
func WriteReport(w io.Writer, s health.HealthSummary) {
	writeLines(w, ReportLines(render.Default(w), s))
}

// StartLines is the header printed before the pre-flight checks run.
func StartLines(th *render.Theme, cluster string) []string {
	return []string{"", th.Section("PRE-FLIGHT HEALTH CHECKS") + th.Paint(th.Pal.Dim, "  "+cluster)}
}

// WriteStart writes StartLines to w.
func WriteStart(w io.Writer, cluster string) {
	writeLines(w, StartLines(render.Default(w), cluster))
}

// VerdictLines is the closing banner for a decision. healthOnly is set for
// `--health-only`, which checks and stops, so a pass does not claim the
// update is going ahead. A warning has no banner: the prompt, the
// --require-healthy error, or the --health-only exit code says what happens.
func VerdictLines(th *render.Theme, d health.Decision, healthOnly bool) []string {
	switch d {
	case health.DecisionProceed:
		msg := "All health checks passed. Proceeding with the update."
		if healthOnly {
			msg = "All health checks passed."
		}
		return []string{"", th.Tokenf(render.Healthy, msg)}
	case health.DecisionBlock:
		return []string{
			"",
			th.Tokenf(render.Fail, "Critical health issues detected. Resolve them before proceeding."),
			"",
			"Run with --skip-health-check to bypass pre-flight health validation (not recommended).",
			"Use 'refresh nodegroup list --cluster <cluster>' to monitor current status.",
		}
	default:
		return nil
	}
}

// WriteVerdict writes VerdictLines to w.
func WriteVerdict(w io.Writer, d health.Decision, healthOnly bool) {
	writeLines(w, VerdictLines(render.Default(w), d, healthOnly))
}

func writeLines(w io.Writer, lines []string) {
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}
