package cluster

import (
	"fmt"
	"io"
	"os"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

// The human `cluster upgrade` view. The engine reports what happens (phase
// labels, progress text, the report); glyphs and colors are chosen here.

// stepToken is the status token of a plan step.
func stepToken(th *render.Theme, st upgrade.StepStatus) string {
	switch st {
	case upgrade.StatusCompleted:
		return th.Tokenf(render.Healthy, "done")
	case upgrade.StatusBlocked:
		return th.Tokenf(render.Fail, "BLOCKED")
	case upgrade.StatusManual:
		return th.Tokenf(render.Warn, "manual")
	default:
		return th.Tokenf(render.Neutral, "pending")
	}
}

// planLines builds the human plan (pure, so tests can check it): the path,
// the notices, and each hop's numbered steps with a status token.
func planLines(th *render.Theme, plan *upgrade.Plan) []string {
	path := plan.CurrentVersion
	for _, hop := range plan.Hops {
		path += " → " + hop.To
	}
	out := []string{
		"",
		"Upgrade plan: " + th.Bold(th.Pal.White, plan.ClusterName) + " " + path +
			th.Paint(th.Pal.Dim, " (EKS upgrades are sequential minors)"),
	}
	for _, n := range plan.Notices {
		out = append(out, "  "+th.Token(render.Warn, "notice: "+n))
	}

	// Pad the step tokens to one width so the descriptions line up.
	width := 0
	for _, st := range []upgrade.StepStatus{upgrade.StatusCompleted, upgrade.StatusBlocked, upgrade.StatusManual, upgrade.StatusPending} {
		width = max(width, ui.VisibleWidth(stepToken(th, st)))
	}
	for _, hop := range plan.Hops {
		out = append(out, "", th.Section(fmt.Sprintf("Hop %s → %s", hop.From, hop.To)))
		for i, step := range hop.Steps {
			line := fmt.Sprintf("  %2d. %s %s", i+1, ui.PadANSI(stepToken(th, step.Status), width, ui.AlignLeft), step.Description)
			if step.Reason != "" {
				line += th.Paint(th.Pal.Dim, " — "+step.Reason)
			}
			out = append(out, line)
		}
	}
	return out
}

// renderPlan prints the human-readable plan.
func renderPlan(plan *upgrade.Plan) {
	for _, l := range planLines(render.Default(os.Stdout), plan) {
		ui.Outln(l)
	}
}

// reportLines builds the completed / stopped-at / remaining summary.
func reportLines(th *render.Theme, report *upgrade.Report) []string {
	out := []string{""}
	for _, c := range report.Completed {
		out = append(out, th.Token(render.Healthy, "completed: "+c))
	}
	if report.StoppedAt != "" {
		out = append(out, th.Token(render.Fail, fmt.Sprintf("stopped at: %s (%s)", report.StoppedAt, report.Status)))
	}
	for _, r := range report.Remaining {
		out = append(out, th.Token(render.Neutral, "remaining: "+r))
	}
	return out
}

// renderReport writes the completed / stopped-at / remaining summary to w.
func renderReport(w io.Writer, report *upgrade.Report) {
	if report == nil {
		return
	}
	writeLines(w, reportLines(render.Default(w), report))
}

// outcomeLines builds the last lines of a run that did not fail: "Upgrade
// complete" (ran is true) or "Nothing to do". When the plan has manual steps
// (custom-AMI nodegroups, --skip, --skip-nodegroup), the upgrade is not
// complete: they are named instead.
func outcomeLines(th *render.Theme, clusterName string, plan *upgrade.Plan, ran bool) []string {
	manual := plan.ManualSteps()
	if len(manual) == 0 {
		if ran {
			return []string{"", th.Line(render.Healthy, "Upgrade complete: %s is at %s.", clusterName, plan.TargetVersion)}
		}
		return []string{"", th.Line(render.Healthy, "Nothing to do: %s already satisfies %s.", clusterName, plan.TargetVersion)}
	}
	head := fmt.Sprintf("Upgrade not complete: %d manual step(s) remain before %s is fully at %s.", len(manual), clusterName, plan.TargetVersion)
	if !ran {
		head = fmt.Sprintf("Nothing left for refresh to do, but %d manual step(s) remain before %s is fully at %s.", len(manual), clusterName, plan.TargetVersion)
	}
	out := []string{"", th.Line(render.Warn, "%s", head)}
	for _, m := range manual {
		out = append(out, "  "+th.Token(render.Warn, "manual: "+m))
	}
	return out
}

// writeUpgradeOutcome writes outcomeLines to out.
func writeUpgradeOutcome(out io.Writer, clusterName string, plan *upgrade.Plan, ran bool) {
	writeLines(out, outcomeLines(render.Default(out), clusterName, plan, ran))
}

// phaseStart returns the engine's PhaseStart hook: each mutating phase opens
// with a section line on w, unless quiet.
func phaseStart(w io.Writer, quiet bool) func(label string) {
	return func(label string) {
		if !quiet {
			_, _ = fmt.Fprintf(w, "  %s\n", render.Default(w).Section(label))
		}
	}
}

func writeLines(w io.Writer, lines []string) {
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}
