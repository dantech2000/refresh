package addon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"

	"gopkg.in/yaml.v3"

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
func outputAddonsTable(cluster string, rows []addons.AddonSummary, failures []diag.Failure, elapsed time.Duration) error {
	if ui.PlainOutput() {
		if len(rows) == 0 {
			_, _ = fmt.Fprintf(ui.Stderr, "No add-ons found for cluster: %s\n", cluster)
		}
		addonListPlain(rows).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	if len(rows) == 0 {
		ui.Outf("Add-ons for cluster: %s\n", color.CyanString(cluster))
		ui.PrintElapsed(elapsed)
		color.Yellow("No add-ons found")
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
		t.Row(r.Name, r.Version, r.Status, r.Health)
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
	fmt.Printf("Add-on Details: %s (%s)\n", color.CyanString(d.Name), color.WhiteString(cluster))
	fmt.Printf("Version: %s\n", d.Version)
	fmt.Printf("Status: %s\n", ui.StatusColorString(d.Status))
	if d.Health != "" {
		fmt.Printf("Health: %s\n", healthBadge(d.Health))
	}
	if d.ARN != "" {
		fmt.Printf("ARN: %s\n", d.ARN)
	}
	if d.ServiceAccountRole != "" {
		fmt.Printf("Service Account Role: %s\n", d.ServiceAccountRole)
	}
	if d.CreatedAt != nil {
		fmt.Printf("Created: %s\n", d.CreatedAt.Format(time.RFC3339))
	}
	if d.ModifiedAt != nil {
		fmt.Printf("Modified: %s\n", d.ModifiedAt.Format(time.RFC3339))
	}
	if len(d.Issues) > 0 {
		fmt.Println("\nIssues:")
		for _, issue := range d.Issues {
			fmt.Printf("  - %s: %s\n", issue.Code, issue.Message)
		}
	}
	if len(d.Configuration) > 0 {
		fmt.Println("\nConfiguration:")
		y, _ := yaml.Marshal(d.Configuration)
		fmt.Println(string(y))
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
		Add("health", d.Health).
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

// addonUpdatePlain builds the `addon update [--all] -o plain` table: one row
// per add-on update result. UPDATE ID is empty when no EKS update started
// (dry run, already current).
func addonUpdatePlain(results []addons.AddonUpdateResult) *ui.PlainTable {
	t := ui.NewPlainTable(columnTitles(updateResultColumns())...)
	for _, r := range results {
		t.Row(r.AddonName, r.PreviousVersion, r.NewVersion, r.Status, r.UpdateID)
	}
	return t
}

// writeUpdateIssues writes post-update health issues and wait failures to w
// (stderr); the STATUS column only says COMPLETED_WITH_ISSUES or WAIT_FAILED.
// With failed set, it also names each add-on whose update failed or was not
// attempted, with its "FAILED: <reason>" status. The table and plain views
// show that status on stdout, so only -o json/yaml runs set it.
func writeUpdateIssues(w io.Writer, results []addons.AddonUpdateResult, failed bool) {
	for _, r := range results {
		if failed && strings.HasPrefix(r.Status, "FAILED") {
			_, _ = fmt.Fprintf(w, "%s: %s\n", r.AddonName, r.Status)
		}
		if r.HealthIssues != "" {
			_, _ = fmt.Fprintf(w, "%s: post-update health check found issues: %s\n", r.AddonName, r.HealthIssues)
		}
		if r.Error != "" {
			_, _ = fmt.Fprintf(w, "%s: update %s did not complete: %s\n", r.AddonName, r.UpdateID, r.Error)
		}
	}
}

func outputUpdateAllResults(cluster string, results []addons.AddonUpdateResult, dryRun bool) error {
	if ui.PlainOutput() {
		if len(results) == 0 {
			_, _ = fmt.Fprintf(ui.Stderr, "No addons to update for cluster: %s\n", cluster)
		}
		writeUpdateIssues(ui.Stderr, results, false)
		addonUpdatePlain(results).Render()
		return nil
	}
	mode := ""
	if dryRun {
		mode = " (DRY RUN)"
	}
	ui.Outf("Addon Updates for cluster: %s%s\n\n", color.CyanString(cluster), color.YellowString(mode))

	if len(results) == 0 {
		color.Yellow("No addons to update")
		return nil
	}

	table := ui.NewPTable(updateResultColumns(), ui.CyanHeaders())

	successCount := 0
	failCount := 0
	warnCount := 0
	upToDateCount := 0
	for _, r := range results {
		var status string
		switch {
		case strings.Contains(r.Status, "FAILED"):
			status = color.RedString(r.Status)
			failCount++
		case r.Status == addons.StatusDryRun:
			status = color.YellowString(r.Status)
		case r.Status == addons.StatusCompletedWithIssues:
			status = color.YellowString(r.Status)
			warnCount++
		case r.Status == addons.StatusUpToDate:
			status = r.Status
			upToDateCount++
		default:
			status = color.GreenString(r.Status)
			successCount++
		}

		newVersion := r.NewVersion
		if r.NewVersion != r.PreviousVersion {
			newVersion = color.GreenString(r.NewVersion)
		}

		table.AddRow(r.AddonName, r.PreviousVersion, newVersion, status, orDashID(r.UpdateID))
	}
	table.Render()

	ui.Outln()
	if !dryRun {
		summary := fmt.Sprintf("Summary: %s successful", color.GreenString("%d", successCount))
		if warnCount > 0 {
			summary += fmt.Sprintf(", %s with issues", color.YellowString("%d", warnCount))
		}
		if upToDateCount > 0 {
			summary += fmt.Sprintf(", %d up to date", upToDateCount)
		}
		summary += fmt.Sprintf(", %s failed", color.RedString("%d", failCount))
		ui.Outf("%s\n", summary)
	}
	writeUpdateIssues(ui.Stderr, results, false)

	return nil
}

// orDashID renders an empty update ID (dry run, already current) as "-".
func orDashID(id string) string {
	if id == "" {
		return "-"
	}
	return id
}
