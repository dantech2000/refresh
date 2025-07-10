package monitoring

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"

	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// DisplayProgressUpdate shows current progress in a live updating format with tree structure
func DisplayProgressUpdate(monitor *refreshTypes.ProgressMonitor) {
	// Clear previous output if we have printed before
	if monitor.LastPrinted > 0 {
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

	// Store the number of lines we printed for next iteration
	monitor.LastPrinted = lineCount
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
		statusPrefix := ui.GetStatusPrefix(update.Status)
		fmt.Printf("%s%s %s\n", prefix, statusPrefix, color.YellowString(update.NodegroupName))
		lineCount++

		// Print update details
		duration := time.Since(update.StartTime).Round(time.Second)
		statusColor := ui.GetStatusColor(update.Status)

		statusText := statusColor(string(update.Status))
		if update.ErrorMessage != "" {
			statusText = color.RedString("FAILED: %s", update.ErrorMessage)
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
		totalDuration := time.Since(monitor.StartTime)

		fmt.Printf("\nAll updates completed in %v\n\n", totalDuration.Round(time.Second))

		// Print cluster name as root
		if len(monitor.Updates) > 0 {
			fmt.Printf("%s\n", color.CyanString(monitor.Updates[0].ClusterName))
			printCompletionSummaryTree(monitor.Updates)
		}

		// Print results summary
		successful := 0
		failed := 0
		for _, update := range monitor.Updates {
			switch update.Status {
			case types.UpdateStatusSuccessful:
				successful++
			case types.UpdateStatusFailed, types.UpdateStatusCancelled:
				failed++
			}
		}

		fmt.Printf("\nResults: %s successful, %s failed\n",
			color.GreenString("%d", successful),
			color.RedString("%d", failed))
	}

	// Return error if any updates failed
	for _, update := range monitor.Updates {
		if update.Status == types.UpdateStatusFailed {
			return fmt.Errorf("one or more nodegroup updates failed")
		}
	}

	return nil
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
		statusPrefix := ui.GetStatusPrefix(update.Status)
		fmt.Printf("%s%s %s\n", prefix, statusPrefix, color.YellowString(update.NodegroupName))

		// Print completion details
		duration := time.Since(update.StartTime).Round(time.Second)

		var statusText string
		switch update.Status {
		case types.UpdateStatusSuccessful:
			statusText = color.GreenString("SUCCESSFUL")
		case types.UpdateStatusFailed:
			statusText = color.RedString("FAILED")
			if update.ErrorMessage != "" {
				statusText = color.RedString("FAILED: %s", update.ErrorMessage)
			}
		case types.UpdateStatusCancelled:
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
