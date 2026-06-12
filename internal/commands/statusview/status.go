// Package statusview renders fleet patch-posture output for `refresh status`.
package statusview

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

const dateLayout = "2006-01-02"

// OutputFleetTable renders the fleet status as a table (or plain TSV when plain
// output is active) followed by a one-line summary footer.
func OutputFleetTable(statuses []statussvc.ClusterStatus, elapsed time.Duration) error {
	columns := []ui.Column{
		{Title: "CLUSTER", Min: 8},
		{Title: "REGION", Min: 9},
		{Title: "VERSION", Min: 7},
		{Title: "SUPPORT", Min: 24, Max: 40},
		{Title: "COMPUTE", Min: 10, Max: 28},
		{Title: "STALE AMI", Min: 9},
		{Title: "ADDONS BEHIND", Min: 13, Max: 30},
	}
	table := ui.NewPTable(columns, ui.CyanHeaders())
	for _, c := range statuses {
		table.AddRow(
			c.Name,
			c.Region,
			versionCell(c),
			supportCell(c.Support),
			computeCell(c),
			staleAMICell(c),
			addonsCell(c.AddonsBehind),
		)
	}
	table.Render()

	fmt.Println()
	fmt.Println(summaryFooter(statuses, elapsed))
	return nil
}

func versionCell(c statussvc.ClusterStatus) string {
	if c.Version == "" {
		return "unknown"
	}
	return c.Version
}

func supportCell(s statussvc.SupportPosture) string {
	star := ""
	if s.Fallback {
		star = "*"
	}
	switch s.Tier {
	case statussvc.SupportStandard:
		txt := "standard"
		if s.StandardUntil != nil {
			txt += " until " + s.StandardUntil.Format(dateLayout)
		}
		if s.DaysRemaining != nil {
			txt += fmt.Sprintf(" (%dd)", *s.DaysRemaining)
		}
		return txt + star
	case statussvc.SupportExtended:
		txt := "⚠ EXTENDED"
		if s.ExtendedUntil != nil {
			txt += " until " + s.ExtendedUntil.Format(dateLayout)
		}
		if s.ExtraCostUSDPerHour > 0 {
			txt += fmt.Sprintf(" (~$%.2f/hr)", s.ExtraCostUSDPerHour)
		}
		return color.YellowString(txt + star)
	case statussvc.SupportUnsupported:
		return color.RedString("✖ UNSUPPORTED" + star)
	default:
		return "unknown"
	}
}

func computeCell(c statussvc.ClusterStatus) string {
	switch c.Compute {
	case statussvc.ComputeManaged:
		return fmt.Sprintf("%d nodegroups", c.NodegroupCount)
	case statussvc.ComputeAutoMode:
		return color.CyanString("🤖 Auto Mode")
	case statussvc.ComputeKarpenter:
		return color.CyanString("Karpenter-managed")
	default:
		return "no managed nodegroups"
	}
}

func staleAMICell(c statussvc.ClusterStatus) string {
	// AMI staleness only applies to managed nodegroups; AWS owns AMIs for Auto
	// Mode and Karpenter manages them out-of-band.
	if c.Compute != statussvc.ComputeManaged {
		return "n/a"
	}
	if c.StaleAMI.Behind == 0 {
		return "0"
	}
	txt := fmt.Sprintf("%d/%d", c.StaleAMI.Behind, c.StaleAMI.Total)
	if c.StaleAMI.OldestDays != nil {
		txt += fmt.Sprintf(" (oldest %dd)", *c.StaleAMI.OldestDays)
	}
	return color.YellowString(txt)
}

func addonsCell(a statussvc.AddonsBehindSummary) string {
	if a.Behind == 0 {
		return "0"
	}
	names := a.Names
	const maxNames = 2
	suffix := ""
	if len(names) > maxNames {
		suffix = fmt.Sprintf(" +%d", len(names)-maxNames)
		names = names[:maxNames]
	}
	return color.YellowString(fmt.Sprintf("%d (%s%s)", a.Behind, strings.Join(names, ","), suffix))
}

func summaryFooter(statuses []statussvc.ClusterStatus, elapsed time.Duration) string {
	staleNodegroups, addonsBehind, supportRisk := 0, 0, 0
	for _, c := range statuses {
		staleNodegroups += c.StaleAMI.Behind
		addonsBehind += c.AddonsBehind.Behind
		if c.SupportRisk() {
			supportRisk++
		}
	}
	parts := []string{
		fmt.Sprintf("%d clusters", len(statuses)),
		fmt.Sprintf("%d stale nodegroups", staleNodegroups),
		fmt.Sprintf("%d addons behind", addonsBehind),
		fmt.Sprintf("%d extended/unsupported", supportRisk),
	}
	line := strings.Join(parts, " · ")
	if elapsed > 0 {
		line += fmt.Sprintf("  (%s)", elapsed.Round(time.Millisecond))
	}
	if supportRisk > 0 || staleNodegroups > 0 || addonsBehind > 0 {
		return line
	}
	return color.GreenString(line)
}
