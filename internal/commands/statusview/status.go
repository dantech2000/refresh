// Package statusview renders fleet patch-posture output for `refresh status`.
package statusview

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/render"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

const dateLayout = "2006-01-02"

// OutputFleetTable renders the fleet status. The human path uses the render
// design system (status tokens, summary chips, a next-step hint); `-o plain`
// writes pure TSV for grep/awk: a header and one row per cluster, with no
// footer, glyphs, or color.
func OutputFleetTable(statuses []statussvc.ClusterStatus, elapsed time.Duration) error {
	if ui.PlainOutput() {
		fleetPlain(statuses).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range fleetLines(th, statuses, elapsed) {
		fmt.Println(line)
	}
	return nil
}

// plainErrorsColumn is the one `-o plain` column the human table lacks: the
// human view lists incomplete rows in a separate INCOMPLETE DATA section.
const plainErrorsColumn = "ERRORS"

// fleetPlain builds the `status -o plain` table: the human table's named
// columns (without the leading glyph column) plus ERRORS. Values use the human
// vocabulary, never truncated.
func fleetPlain(statuses []statussvc.ClusterStatus) *ui.PlainTable {
	cols := fleetDataColumns()
	headers := make([]string, 0, len(cols)+1)
	for _, c := range cols {
		headers = append(headers, c.Title)
	}
	t := ui.NewPlainTable(append(headers, plainErrorsColumn)...)
	for _, c := range statuses {
		t.Row(
			nameOr(c),
			c.Region,
			versionCell(c),
			supportCell(c.Support),
			computeCell(c),
			staleAMICell(c),
			addonsCell(c.AddonsBehind),
			healthCell(c.HealthIssues),
			errorsCell(c),
		)
	}
	return t
}

func versionCell(c statussvc.ClusterStatus) string {
	if c.Version == "" {
		return "unknown"
	}
	return c.Version
}

// autoUpgradeNote replaces the extended-support premium for a cluster whose
// upgrade policy is STANDARD: it never pays extended support.
const autoUpgradeNote = "auto-upgrades at end of standard support"

// supportCell is the `-o plain` SUPPORT cell: the human tier word, plus the
// end date and extended-support premium (or the STANDARD-policy auto-upgrade
// note) the human table leaves out. A trailing
// "*" marks a posture from the compiled-in calendar (Fallback).
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
		if s.AutoUpgradeAtStandardEnd {
			txt += " " + autoUpgradeNote
		}
		return txt + star
	case statussvc.SupportExtended:
		txt := "extended"
		if s.ExtendedUntil != nil {
			txt += " until " + s.ExtendedUntil.Format(dateLayout)
		}
		if s.DaysRemaining != nil {
			txt += fmt.Sprintf(" (%dd)", *s.DaysRemaining)
		}
		switch {
		case s.AutoUpgradeAtStandardEnd:
			txt += " " + autoUpgradeNote
		case s.ExtraCostUSDPerHour > 0:
			txt += fmt.Sprintf(" +$%.2f/hr", s.ExtraCostUSDPerHour)
		}
		return txt + star
	case statussvc.SupportUnsupported:
		return "unsupported" + star
	default:
		return "unknown"
	}
}

// computeCell is the `-o plain` COMPUTE cell, in the human table's words.
func computeCell(c statussvc.ClusterStatus) string {
	switch c.Compute {
	case statussvc.ComputeManaged:
		return fmt.Sprintf("%d nodegroups", c.NodegroupCount)
	case statussvc.ComputeAutoMode:
		return "Auto Mode"
	case statussvc.ComputeKarpenter:
		return "Karpenter"
	default:
		return "none"
	}
}

// staleAMICell is the `-o plain` STALE AMI cell, in the human table's words.
func staleAMICell(c statussvc.ClusterStatus) string {
	// AMI staleness only applies to managed nodegroups; AWS owns AMIs for Auto
	// Mode and Karpenter manages them out-of-band.
	if c.Compute != statussvc.ComputeManaged {
		return "n/a"
	}
	return staleAMIText(c)
}

// addonsCell is the `-o plain` ADDONS cell: the count plus every name behind
// (the human table shows two names and "+N").
func addonsCell(a statussvc.AddonsBehindSummary) string {
	if a.Behind == 0 {
		return "0"
	}
	return fmt.Sprintf("%d (%s)", a.Behind, strings.Join(a.Names, ","))
}

// healthCell is the uncolored HEALTH cell: "0", or the number of AWS-reported
// control-plane health issues.
func healthCell(issues int) string {
	if issues == 0 {
		return "0"
	}
	return fmt.Sprintf("%d issue(s)", issues)
}

// errorsCell marks a row whose data is incomplete; "-" keeps the TSV column
// non-empty for awk.
func errorsCell(c statussvc.ClusterStatus) string {
	if !c.Incomplete() {
		return "-"
	}
	return strings.Join(c.Errors, "; ")
}
