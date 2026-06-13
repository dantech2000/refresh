package addon

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
	"gopkg.in/yaml.v3"
)

// outputAddonsTable renders the add-on list. The human path uses the render
// design system (tokenized STATUS/HEALTH cells); `-o plain` keeps the uncolored
// tab-separated table.
func outputAddonsTable(cluster string, rows []addons.AddonSummary, elapsed time.Duration) error {
	if len(rows) == 0 {
		ui.Outf("Add-ons for cluster: %s\n", color.CyanString(cluster))
		ui.PrintElapsed(elapsed)
		color.Yellow("No add-ons found")
		return nil
	}
	if !ui.PlainOutput() {
		th := render.Default(os.Stdout)
		for _, line := range addonListLines(th, cluster, rows) {
			fmt.Println(line)
		}
		return nil
	}
	return outputAddonsPlain(cluster, rows, elapsed)
}

func outputAddonsPlain(cluster string, rows []addons.AddonSummary, elapsed time.Duration) error {
	ui.Outf("Add-ons for cluster: %s\n", color.CyanString(cluster))
	ui.PrintElapsed(elapsed)

	columns := []ui.Column{
		{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
		{Title: "VERSION", Min: 8, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "HEALTH", Min: 8, Max: 0, Align: ui.AlignLeft},
	}
	table := ui.NewPTable(columns, ui.CyanHeaders())
	for _, r := range rows {
		table.AddRow(r.Name, r.Version, ui.StatusColorString(r.Status), healthBadge(r.Health))
	}
	table.Render()
	return nil
}

func outputAddonDetailsTable(cluster string, d *addons.AddonDetails) error {
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

func outputUpdateAllResults(cluster string, results []addons.AddonUpdateResult, dryRun bool) error {
	mode := ""
	if dryRun {
		mode = " (DRY RUN)"
	}
	ui.Outf("Addon Updates for cluster: %s%s\n\n", color.CyanString(cluster), color.YellowString(mode))

	if len(results) == 0 {
		color.Yellow("No addons to update")
		return nil
	}

	columns := []ui.Column{
		{Title: "ADDON", Min: 20, Max: 30, Align: ui.AlignLeft},
		{Title: "PREVIOUS", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "NEW", Min: 15, Max: 0, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
	}
	table := ui.NewPTable(columns, ui.CyanHeaders())

	successCount := 0
	failCount := 0
	warnCount := 0
	for _, r := range results {
		var status string
		if strings.Contains(r.Status, "FAILED") {
			status = color.RedString(r.Status)
			failCount++
		} else if r.Status == "DRY_RUN" {
			status = color.YellowString(r.Status)
		} else if r.Status == "COMPLETED_WITH_ISSUES" {
			status = color.YellowString(r.Status)
			warnCount++
		} else {
			status = color.GreenString(r.Status)
			successCount++
		}

		newVersion := r.NewVersion
		if r.NewVersion != r.PreviousVersion {
			newVersion = color.GreenString(r.NewVersion)
		}

		table.AddRow(r.AddonName, r.PreviousVersion, newVersion, status)
	}
	table.Render()

	ui.Outln()
	if !dryRun {
		summary := fmt.Sprintf("Summary: %s successful", color.GreenString("%d", successCount))
		if warnCount > 0 {
			summary += fmt.Sprintf(", %s with issues", color.YellowString("%d", warnCount))
		}
		summary += fmt.Sprintf(", %s failed", color.RedString("%d", failCount))
		ui.Outf("%s\n", summary)
	}

	return nil
}
