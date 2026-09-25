package addon

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

// addonDetailLines builds the human `addon describe` view (pure, so tests
// can check it): a name/status header, OVERVIEW key/values with status and
// health tokens, then the ISSUES and CONFIGURATION sections when present.
func addonDetailLines(th *render.Theme, cluster string, d *addons.AddonDetails) []string {
	pal := th.Pal
	status := th.Token(render.StatusFromString(d.Status), orDash(d.Status))
	out := []string{
		th.Bold(pal.White, d.Name) + "   " + status + th.Paint(pal.Dim, "   "+cluster),
		"",
		th.Section("OVERVIEW"),
	}
	kv := [][2]string{
		{"version", th.Paint(pal.Text, orDash(d.Version))},
		{"status", status},
	}
	if d.Health != "" {
		kv = append(kv, [2]string{"health", addonHealthToken(th, d.Health)})
	}
	if d.ARN != "" {
		kv = append(kv, [2]string{"arn", th.Paint(pal.Text, d.ARN)})
	}
	if d.ServiceAccountRole != "" {
		kv = append(kv, [2]string{"service account role", th.Paint(pal.Text, d.ServiceAccountRole)})
	}
	if d.CreatedAt != nil {
		kv = append(kv, [2]string{"created", th.Paint(pal.Text, d.CreatedAt.Format(time.RFC3339))})
	}
	if d.ModifiedAt != nil {
		kv = append(kv, [2]string{"modified", th.Paint(pal.Text, d.ModifiedAt.Format(time.RFC3339))})
	}
	for _, l := range th.KV(kv) {
		out = append(out, "  "+l)
	}

	if len(d.Issues) > 0 {
		out = append(out, "", th.Section("ISSUES")+th.Paint(pal.Dim, fmt.Sprintf("  %d", len(d.Issues))))
		for _, issue := range d.Issues {
			out = append(out, "  "+th.Token(render.Fail, issue.Code+": "+issue.Message))
			if len(issue.ResourceIDs) > 0 {
				out = append(out, "    "+th.Paint(pal.Dim, strings.Join(issue.ResourceIDs, ", ")))
			}
		}
	}
	if len(d.Configuration) > 0 {
		out = append(out, "", th.Section("CONFIGURATION"))
		y, _ := yaml.Marshal(d.Configuration)
		for _, l := range strings.Split(strings.TrimRight(string(y), "\n"), "\n") {
			out = append(out, "  "+l)
		}
	}
	return out
}

// updateResultStatus is the render status of an add-on update result. A
// result with a failure is Fail whatever its status.
func updateResultStatus(r addons.AddonUpdateResult) render.Status {
	switch {
	case r.Failed():
		return render.Fail
	case r.Status == addons.StatusCompleted, r.Status == addons.StatusUpToDate:
		return render.Healthy
	case r.Status == addons.StatusCompletedWithIssues:
		return render.Warn
	case r.Status == addons.StatusStarted, r.Status == addons.StatusInProgress:
		return render.Progress
	default:
		return render.Neutral
	}
}

// updateResultLines builds the human `addon update --all` view: a header,
// one row per add-on with a status token, and the summary counts.
func updateResultLines(th *render.Theme, cluster string, results []addons.AddonUpdateResult, dryRun bool) []string {
	pal := th.Pal
	head := th.Section("ADD-ON UPDATES") + "  " + th.Paint(pal.White, cluster)
	if dryRun {
		head += "  " + th.Bold(pal.Peach, "DRY RUN")
	}
	out := []string{head, ""}
	if len(results) == 0 {
		return append(out, th.Line(render.Neutral, "No add-ons to update"))
	}

	tbl := th.NewTable(updateResultColumns()...)
	var success, failed, issues, upToDate int
	for _, r := range results {
		switch st := updateResultStatus(r); {
		case st == render.Fail:
			failed++
		case r.Status == addons.StatusCompletedWithIssues:
			issues++
		case r.Status == addons.StatusUpToDate:
			upToDate++
		case r.Status != addons.StatusDryRun:
			success++
		}
		newVersion := th.Paint(pal.Text, r.NewVersion)
		if r.NewVersion != r.PreviousVersion {
			newVersion = th.Paint(pal.Green, r.NewVersion)
		}
		tbl.Row(
			th.Paint(pal.White, r.AddonName),
			th.Paint(pal.Text, r.PreviousVersion),
			newVersion,
			th.Token(updateResultStatus(r), string(r.Status)),
			th.Paint(pal.Dim, orDashID(r.UpdateID)),
		)
	}
	for _, l := range tbl.Render() {
		out = append(out, "  "+l)
	}
	if dryRun {
		return out
	}
	summary := "Summary: " + th.Paint(pal.Green, fmt.Sprint(success)) + " successful"
	if issues > 0 {
		summary += ", " + th.Paint(pal.Yellow, fmt.Sprint(issues)) + " with issues"
	}
	if upToDate > 0 {
		summary += fmt.Sprintf(", %d up to date", upToDate)
	}
	summary += ", " + th.Paint(pal.Red, fmt.Sprint(failed)) + " failed"
	return append(out, "", summary)
}

// updateOutcomeLines is the human result of a single `addon update`: one
// status line (and a hint when the run did not wait).
func updateOutcomeLines(th *render.Theme, clusterName, addonName string, r *addons.AddonUpdateResult) []string {
	switch r.Status {
	case addons.StatusDryRun:
		return []string{th.DryRun("Would update add-on %s from %s to %s on cluster %s",
			addonName, r.PreviousVersion, r.NewVersion, clusterName)}
	case addons.StatusUpToDate:
		return []string{th.Line(render.Healthy, "Add-on %s is already at %s; nothing to update", addonName, r.PreviousVersion)}
	case addons.StatusInProgress:
		return []string{th.Line(render.Progress, "Add-on %s is already being updated to %s; no new update was submitted. Use --wait to wait for it.", addonName, r.NewVersion)}
	case addons.StatusCompleted:
		return []string{th.Line(render.Healthy, "Add-on %s updated to %s (was %s)", addonName, r.NewVersion, r.PreviousVersion)}
	case addons.StatusCompletedWithIssues:
		return []string{th.Line(render.Warn, "Add-on %s updated to %s, but the post-update health check found issues: %s",
			addonName, r.NewVersion, r.HealthIssues)}
	case addons.StatusUnverified:
		return []string{th.Line(render.Warn, "Add-on %s updated to %s, but the post-update health check could not read it", addonName, r.NewVersion)}
	case addons.StatusWaitFailed:
		return []string{th.Line(render.Fail, "Update %s for add-on %s did not complete", r.UpdateID, addonName)}
	default:
		return []string{
			th.Line(render.Progress, "Update started for add-on %s (ID: %s)", addonName, r.UpdateID),
			fmt.Sprintf("Use AWS Console or 'refresh addon describe %s --addon %s' to check status.", clusterName, addonName),
		}
	}
}

// updateResultColumns is the add-on update result column set, shared by the
// human table and the `-o plain` header.
func updateResultColumns() []ui.Column {
	return []ui.Column{
		{Title: "ADDON", Min: 20, Max: 30, Align: ui.AlignLeft},
		{Title: "PREVIOUS", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "NEW", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "UPDATE ID", Min: 9, Max: 0, Align: ui.AlignLeft},
	}
}
