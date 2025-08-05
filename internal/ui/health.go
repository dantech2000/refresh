package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/theckman/yacspin"

	"github.com/dantech2000/refresh/internal/health"
)

// NewHealthSpinner creates a new spinner for health checks
func NewHealthSpinner(message string) *yacspin.Spinner {
	cfg := yacspin.Config{
		Frequency:         100 * time.Millisecond,
		CharSet:           yacspin.CharSets[14], // ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏
		Message:           message,
		SuffixAutoColon:   false,
		StopCharacter:     "",
		StopColors:        []string{},
		StopMessage:       "",
		StopFailCharacter: "",
		StopFailColors:    []string{},
		StopFailMessage:   "",
	}

	spinner, _ := yacspin.New(cfg)
	return spinner
}

// DisplayHealthResults displays the health check results in the specified format
func DisplayHealthResults(summary health.HealthSummary) {
	fmt.Println("\nCluster Health Assessment:")
	fmt.Println()

	// Display progress bars for each check
	for _, result := range summary.Results {
		progressBar := RenderProgressBar(result.Score, result.Status)
		statusText := GetHealthStatusText(result.Status)

		fmt.Printf("%s %-20s %s\n", progressBar, result.Name, statusText)
	}

	fmt.Println()

	// Display overall status
	statusColor := GetHealthDecisionColor(summary.Decision)
	issueCount := len(summary.Warnings) + len(summary.Errors)

	if issueCount > 0 {
		fmt.Printf("Status: %s (%d issues found)\n",
			statusColor("%s", GetDecisionText(summary.Decision)), issueCount)
	} else {
		fmt.Printf("Status: %s\n", statusColor("%s", GetDecisionText(summary.Decision)))
	}

	// Display warnings and errors
	if len(summary.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, warning := range summary.Warnings {
			fmt.Printf("- %s\n", color.YellowString(warning))
		}
	}

	if len(summary.Errors) > 0 {
		fmt.Println("\nErrors:")
		for _, err := range summary.Errors {
			fmt.Printf("- %s\n", color.RedString(err))
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
func GetHealthDecisionColor(decision health.Decision) func(format string, a ...interface{}) string {
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

// PromptContinueWithWarnings prompts the user to continue despite warnings
func PromptContinueWithWarnings(warnings []string) bool {
	fmt.Printf("\nProceed with update? (Y/n): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))

	// Default to yes if just Enter is pressed, or if y/yes is entered
	return response == "" || response == "y" || response == "yes"
}

// DisplayHealthCheckStart displays the start of health check process
func DisplayHealthCheckStart(clusterName string) {
	fmt.Printf("\nrefresh update-ami --cluster %s\n\n", clusterName)
}

// DisplayHealthCheckComplete displays completion message based on decision
func DisplayHealthCheckComplete(decision health.Decision) {
	fmt.Println()

	switch decision {
	case health.DecisionProceed:
		color.Green("✓ All health checks passed. Proceeding with AMI update...")
	case health.DecisionWarn:
		// User decision handled by prompt
	case health.DecisionBlock:
		color.Red("✗ Critical health issues detected. Please resolve before proceeding.")
		fmt.Println("\nRun with --force to bypass health checks (not recommended).")
		fmt.Println("Use 'refresh list --cluster <cluster>' to monitor current status.")
	}
}
