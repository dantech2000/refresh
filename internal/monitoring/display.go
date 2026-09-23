package monitoring

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/mattn/go-isatty"

	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// displayIsTerminal reports whether stdout is an interactive terminal.
// Overridable in tests.
var displayIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// DisplayProgressUpdate shows current progress in a live updating format with
// tree structure. On an interactive terminal the previous frame is cleared
// with cursor-control codes; when output is piped each poll appends a new
// block instead (control codes would garble the log).
func DisplayProgressUpdate(monitor *refreshTypes.ProgressMonitor) {
	interactive := displayIsTerminal()

	// Clear previous output if we have printed before
	if interactive && monitor.LastPrinted > 0 {
		fmt.Printf("\033[%dA", monitor.LastPrinted)
		fmt.Print("\033[J")
	}

	// Count lines as we print them
	lineCount := 0

	elapsed := time.Since(monitor.StartTime)
	fmt.Printf("Elapsed: %v\n", elapsed.Round(time.Second))
	lineCount++

	// Print cluster name as root (get from first update)
	if len(monitor.Updates) > 0 {
		fmt.Printf("%s\n", color.CyanString(monitor.Updates[0].ClusterName))
		lineCount++

		additionalLines := printUpdateProgressTree(monitor.Updates)
		lineCount += additionalLines
	}

	fmt.Println() // Extra line for readability
	lineCount++

	// Store the number of lines we printed for next iteration. When output is
	// not a terminal nothing is ever cleared, so keep it at zero.
	if interactive {
		monitor.LastPrinted = lineCount
	} else {
		monitor.LastPrinted = 0
	}
}

// printUpdateProgressTree displays update progress in tree format similar to list command
func printUpdateProgressTree(updates []refreshTypes.UpdateProgress) int {
	lineCount := 0

	for i, update := range updates {
		isLast := i == len(updates)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name with status
		fmt.Printf("%s%s %s\n", prefix, statusPrefixFor(update), color.YellowString(update.NodegroupName))
		lineCount++

		// Print update details
		duration := time.Since(update.StartTime).Round(time.Second)
		statusColor := ui.GetStatusColor(update.Status)

		statusText := statusColor(string(update.Status))
		switch {
		case update.MonitorErr != nil:
			statusText = color.YellowString("%s: %s", monitoringFailedLabel, firstLine(update.MonitorErr.Error()))
		case update.Status == types.UpdateStatusFailed || update.Status == types.UpdateStatusCancelled:
			if update.ErrorMessage != "" {
				statusText = color.RedString("%s: %s", string(update.Status), update.ErrorMessage)
			}
		case update.ErrorMessage != "":
			// AWS reported errors but the update is not terminal yet.
			statusText = fmt.Sprintf("%s %s", statusText, color.YellowString("(errors: %s)", update.ErrorMessage))
		case update.LastCheckError != "":
			// The status poll failed; the update itself may still be running.
			statusText = fmt.Sprintf("%s %s", statusText, color.YellowString("(status check failing, retrying: %s)", update.LastCheckError))
		}

		fmt.Printf("%s├── Status: %s\n", itemPrefix, statusText)
		fmt.Printf("%s├── Duration: %s\n", itemPrefix, color.BlueString(duration.String()))
		fmt.Printf("%s├── Update ID: %s\n", itemPrefix, color.WhiteString(update.UpdateID))
		fmt.Printf("%s└── Last Checked: %s\n", itemPrefix, color.GreenString(update.LastChecked.Format("15:04:05")))
		lineCount += 4

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
			lineCount++
		}
	}

	return lineCount
}

// DisplayCompletionSummary shows the final summary when all updates are complete in tree format
func DisplayCompletionSummary(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	if !config.Quiet {
		// Clear previous progress output if any was printed
		if displayIsTerminal() && monitor.LastPrinted > 0 {
			fmt.Printf("\033[%dA", monitor.LastPrinted)
			fmt.Print("\033[J")
			monitor.LastPrinted = 0
		}

		totalDuration := time.Since(monitor.StartTime)

		unmonitored := 0
		for _, update := range monitor.Updates {
			if update.MonitorErr != nil {
				unmonitored++
			}
		}
		if unmonitored == 0 {
			fmt.Printf("\nAll updates completed in %v\n\n", totalDuration.Round(time.Second))
		} else {
			fmt.Printf("\nMonitoring finished in %v\n\n", totalDuration.Round(time.Second))
		}

		// Print cluster name as root
		if len(monitor.Updates) > 0 {
			fmt.Printf("%s\n", color.CyanString(monitor.Updates[0].ClusterName))
			printCompletionSummaryTree(monitor.Updates)
		}

		// Print results summary
		successful := 0
		failed := 0
		for _, update := range monitor.Updates {
			if update.MonitorErr != nil {
				continue
			}
			switch update.Status {
			case types.UpdateStatusSuccessful:
				successful++
			case types.UpdateStatusFailed, types.UpdateStatusCancelled:
				failed++
			}
		}

		fmt.Printf("\nResults: %s successful, %s failed",
			color.GreenString("%d", successful),
			color.RedString("%d", failed))
		if unmonitored > 0 {
			fmt.Printf(", %s not monitored", color.YellowString("%d", unmonitored))
		}
		fmt.Println()
	}

	// Return an error if any update did not succeed. Failures carry the AWS
	// error details so they surface even when the summary above was
	// suppressed. A Cancelled update is counted as failed above and must not
	// exit 0 (or trigger verification). An update that could not be
	// monitored has an unknown outcome and must not exit 0 either.
	var failures, unmonitored []string
	var monitorErrs []error
	cancelled := false
	for _, update := range monitor.Updates {
		if update.MonitorErr != nil {
			unmonitored = append(unmonitored, update.NodegroupName+" ("+update.UpdateID+")")
			monitorErrs = append(monitorErrs, update.MonitorErr)
			continue
		}
		switch update.Status {
		case types.UpdateStatusFailed:
			msg := update.NodegroupName
			if update.ErrorMessage != "" {
				msg += ": " + update.ErrorMessage
			}
			failures = append(failures, msg)
		case types.UpdateStatusCancelled:
			cancelled = true
		}
	}
	var errs []error
	switch {
	case len(failures) > 0:
		errs = append(errs, fmt.Errorf("one or more nodegroup updates failed: %s", strings.Join(failures, "; ")))
	case cancelled:
		errs = append(errs, fmt.Errorf("one or more nodegroup updates were cancelled"))
	}
	if len(unmonitored) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s; the EKS update(s) continue in the background: %w",
			ErrUnmonitored, strings.Join(unmonitored, ", "), errors.Join(monitorErrs...)))
	}
	return errors.Join(errs...)
}

// monitoringFailedLabel marks an update whose status could not be polled. It
// is distinct from the EKS FAILED status: the update's outcome is unknown.
const monitoringFailedLabel = "MONITORING FAILED"

// statusPrefixFor returns the tree prefix for an update, marking one that
// could not be monitored instead of showing its last-known EKS status.
func statusPrefixFor(update refreshTypes.UpdateProgress) string {
	if update.MonitorErr != nil {
		return "[" + monitoringFailedLabel + "]"
	}
	return ui.GetStatusPrefix(update.Status)
}

// printCompletionSummaryTree displays completion summary in tree format
func printCompletionSummaryTree(updates []refreshTypes.UpdateProgress) {
	for i, update := range updates {
		isLast := i == len(updates)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name with status
		fmt.Printf("%s%s %s\n", prefix, statusPrefixFor(update), color.YellowString(update.NodegroupName))

		// Print completion details
		duration := time.Since(update.StartTime).Round(time.Second)

		var statusText string
		switch {
		case update.MonitorErr != nil:
			statusText = color.YellowString("%s: %s", monitoringFailedLabel, firstLine(update.MonitorErr.Error()))
		case update.Status == types.UpdateStatusSuccessful:
			statusText = color.GreenString("SUCCESSFUL")
		case update.Status == types.UpdateStatusFailed:
			statusText = color.RedString("FAILED")
			if update.ErrorMessage != "" {
				statusText = color.RedString("FAILED: %s", update.ErrorMessage)
			}
		case update.Status == types.UpdateStatusCancelled:
			statusText = color.YellowString("CANCELLED")
		default:
			statusText = color.WhiteString(string(update.Status))
		}

		fmt.Printf("%s├── Status: %s\n", itemPrefix, statusText)
		fmt.Printf("%s├── Duration: %s\n", itemPrefix, color.BlueString(duration.String()))
		fmt.Printf("%s└── Update ID: %s\n", itemPrefix, color.WhiteString(update.UpdateID))

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
		}
	}
}
