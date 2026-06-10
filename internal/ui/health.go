package ui

import (
	"bufio"
	"os"
	"strings"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
)

// DisplayHealthResults displays the health check results in the specified format
func DisplayHealthResults(summary health.HealthSummary) {
	Outln("\nCluster Health Assessment:")
	Outln()

	// Size the name column to the longest check name so long names don't
	// push their status out of alignment.
	nameWidth := 20
	for _, result := range summary.Results {
		if len(result.Name) > nameWidth {
			nameWidth = len(result.Name)
		}
	}
	for _, result := range summary.Results {
		progressBar := RenderProgressBar(result.Score, result.Status)
		statusText := GetHealthStatusText(result.Status)
		Outf("%s %-*s %s\n", progressBar, nameWidth, result.Name, statusText)
	}

	Outln()

	statusColor := GetHealthDecisionColor(summary.Decision)
	issueCount := len(summary.Warnings) + len(summary.Errors)
	if issueCount > 0 {
		Outf("Status: %s (%d issues found)\n", statusColor("%s", GetDecisionText(summary.Decision)), issueCount)
	} else {
		Outf("Status: %s\n", statusColor("%s", GetDecisionText(summary.Decision)))
	}

	if len(summary.Warnings) > 0 {
		Outln("\nWarnings:")
		for _, warning := range summary.Warnings {
			Outf("- %s\n", color.YellowString(warning))
		}
	}

	if len(summary.Errors) > 0 {
		Outln("\nErrors:")
		for _, err := range summary.Errors {
			Outf("- %s\n", color.RedString(err))
		}
	}
}

// RenderProgressBar creates a progress bar string based on score and status
func RenderProgressBar(score int, status health.HealthStatus) string {
	const barLength = 20
	filledLength := (score * barLength) / 100

	var bar strings.Builder
	bar.WriteString("[")

	// Choose character based on status
	var fillChar, emptyChar string
	switch status {
	case health.StatusPass:
		fillChar = "█"
		emptyChar = " "
	case health.StatusWarn:
		fillChar = "█"
		emptyChar = "▒"
	case health.StatusFail:
		fillChar = "█"
		emptyChar = "▒"
	default:
		fillChar = "█"
		emptyChar = " "
	}

	// Fill the progress bar
	for i := 0; i < barLength; i++ {
		if i < filledLength {
			bar.WriteString(fillChar)
		} else {
			bar.WriteString(emptyChar)
		}
	}

	bar.WriteString("]")

	// Color the bar based on status
	switch status {
	case health.StatusPass:
		return color.GreenString(bar.String())
	case health.StatusWarn:
		return color.YellowString(bar.String())
	case health.StatusFail:
		return color.RedString(bar.String())
	default:
		return bar.String()
	}
}

// GetHealthStatusText returns the text representation of health status
func GetHealthStatusText(status health.HealthStatus) string {
	switch status {
	case health.StatusPass:
		return color.GreenString("PASS")
	case health.StatusWarn:
		return color.YellowString("WARN")
	case health.StatusFail:
		return color.RedString("FAIL")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

// GetDecisionText returns the text representation of decision
func GetDecisionText(decision health.Decision) string {
	switch decision {
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

// GetHealthDecisionColor returns the color function for decision
func GetHealthDecisionColor(decision health.Decision) func(format string, a ...any) string {
	switch decision {
	case health.DecisionProceed:
		return color.GreenString
	case health.DecisionWarn:
		return color.YellowString
	case health.DecisionBlock:
		return color.RedString
	default:
		return color.WhiteString
	}
}

// PromptContinueWithWarnings prompts the user to continue despite warnings.
// The default is No: this guards a rolling update on a cluster that just
// failed health warnings, so a bare Enter (or unreadable/closed stdin, as in
// CI) must not silently proceed.
func PromptContinueWithWarnings(warnings []string) bool {
	if len(warnings) > 0 {
		Outf("\n%d warning(s) reported above.", len(warnings))
	}
	Outf("\nProceed with update? (y/N): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil && response == "" {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// DisplayHealthCheckStart displays the start of health check process
func DisplayHealthCheckStart(clusterName string) {
	Outf("\nrefresh update-ami --cluster %s\n\n", clusterName)
}

// DisplayHealthCheckComplete displays completion message based on decision
func DisplayHealthCheckComplete(decision health.Decision) {
	Outln()

	switch decision {
	case health.DecisionProceed:
		color.Green("[PASS] All health checks passed. Proceeding with AMI update...")
	case health.DecisionWarn:
		// User decision handled by prompt
	case health.DecisionBlock:
		color.Red("[FAIL] Critical health issues detected. Please resolve before proceeding.")
		Outln("\nRun with --force to bypass health checks (not recommended).")
		Outln("Use 'refresh nodegroup list --cluster <cluster>' to monitor current status.")
	}
}
