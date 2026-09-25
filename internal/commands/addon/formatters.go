package addon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

// outputAddonsTable renders the add-on list. The human path uses the render
// design system (tokenized STATUS/HEALTH cells, and the INCOMPLETE DATA
// section for failures); `-o plain` writes pure TSV (header + one row per
// add-on) and sends the empty-list notice to stderr. The caller reports
// failures on stderr for -o plain.
func outputAddonsTable(cluster string, rows []addons.AddonSummary, failures []diag.Failure) error {
	if ui.PlainOutput() {
		if len(rows) == 0 {
			_, _ = fmt.Fprintf(ui.Stderr, "No add-ons found for cluster: %s\n", cluster)
		}
		addonListPlain(rows).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	if len(rows) == 0 {
		fmt.Println(th.Line(render.Neutral, "No add-ons found for cluster: %s", cluster))
	} else {
		for _, line := range addonListLines(th, cluster, rows) {
			fmt.Println(line)
		}
	}
	for _, line := range th.FailureSection(failures) {
		fmt.Println(line)
	}
	return nil
}

// columnTitles returns the header titles of cols.
func columnTitles(cols []ui.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Title
	}
	return out
}

// addonListPlain builds the `addon list -o plain` table, with the human
// table's headers and raw status/health values.
func addonListPlain(rows []addons.AddonSummary) *ui.PlainTable {
	t := ui.NewPlainTable(columnTitles(addonListColumns())...)
	for _, r := range rows {
		t.Row(r.Name, r.Version, r.Status, healthLabel(r.Health))
	}
	return t
}

// outputAddonDetailsTable renders one add-on. `-o plain` writes a FIELD/VALUE
// TSV (see addonDetailPlain).
func outputAddonDetailsTable(cluster string, d *addons.AddonDetails) error {
	if ui.PlainOutput() {
		addonDetailPlain(cluster, d).Render()
		return nil
	}
	for _, l := range addonDetailLines(render.Default(os.Stdout), cluster, d) {
		fmt.Println(l)
	}
	return nil
}

// addonDetailPlain builds the `addon describe -o plain` FIELD/VALUE table.
// Each issue is an "issue/<code>" row; the configuration is compact JSON on
// one line.
func addonDetailPlain(cluster string, d *addons.AddonDetails) *ui.PlainTable {
	t := ui.NewPlainKV()
	t.Add("name", d.Name).
		Add("cluster", cluster).
		Add("version", d.Version).
		Add("status", d.Status).
		Add("health", healthLabel(d.Health)).
		Add("arn", d.ARN).
		Add("service account role", d.ServiceAccountRole)
	if d.CreatedAt != nil {
		t.Add("created", d.CreatedAt.Format(time.RFC3339))
	}
	if d.ModifiedAt != nil {
		t.Add("modified", d.ModifiedAt.Format(time.RFC3339))
	}
	for _, issue := range d.Issues {
		v := issue.Message
		if len(issue.ResourceIDs) > 0 {
			v += " [" + strings.Join(issue.ResourceIDs, ", ") + "]"
		}
		t.Add("issue/"+issue.Code, v)
	}
	if len(d.Configuration) > 0 {
		cfg, err := json.Marshal(d.Configuration)
		if err != nil {
			cfg = []byte(fmt.Sprint(d.Configuration))
		}
		t.Add("configuration", string(cfg))
	}
	return t
}

// addonUpdatePlain builds the `addon update [--all] -o plain` table: one row
// per add-on update result. UPDATE ID is empty when no EKS update started
// (dry run, already current).
func addonUpdatePlain(results []addons.AddonUpdateResult) *ui.PlainTable {
	t := ui.NewPlainTable(columnTitles(updateResultColumns())...)
	for _, r := range results {
		t.Row(r.AddonName, r.PreviousVersion, r.NewVersion, string(r.Status), r.UpdateID)
	}
	return t
}

// writeHealthIssues writes the post-update health issues to w (stderr); the
// STATUS column only says CompletedWithIssues. Failures are reported
// separately (runner.ReportFailures).
func writeHealthIssues(w io.Writer, results []addons.AddonUpdateResult) {
	for _, r := range results {
		if r.HealthIssues != "" {
			_, _ = fmt.Fprintf(w, "%s: post-update health check found issues: %s\n", r.AddonName, r.HealthIssues)
		}
	}
}

func outputUpdateAllResults(cluster string, results []addons.AddonUpdateResult, dryRun bool) error {
	if ui.PlainOutput() {
		if len(results) == 0 {
			_, _ = fmt.Fprintf(ui.Stderr, "No add-ons to update for cluster: %s\n", cluster)
		}
		writeHealthIssues(ui.Stderr, results)
		addonUpdatePlain(results).Render()
		return nil
	}
	for _, l := range updateResultLines(render.Default(os.Stdout), cluster, results, dryRun) {
		fmt.Println(l)
	}
	writeHealthIssues(ui.Stderr, results)
	return nil
}

// orDash renders an empty value as "-".
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// orDashID renders an empty update ID (dry run, already current) as "-".
func orDashID(id string) string {
	if id == "" {
		return "-"
	}
	return id
}
