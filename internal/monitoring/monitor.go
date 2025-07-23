package monitoring

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"

	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// MonitorUpdates monitors the progress of multiple nodegroup updates
func MonitorUpdates(ctx context.Context, eksClient *eks.Client, monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	// Set up signal handling for graceful cancellation
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create a cancellable context with timeout
	monitorCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	if !config.Quiet {
		fmt.Printf("\nMonitoring %d nodegroup update(s)...\n", len(monitor.Updates))
		fmt.Printf("Timeout: %v | Poll interval: %v\n", config.Timeout, config.PollInterval)
		fmt.Printf("Press Ctrl+C to stop monitoring (updates will continue)\n\n")
	}

	ticker := time.NewTicker(config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			if !config.Quiet {
				color.Yellow("\nMonitoring cancelled by user. Updates are still running in AWS.")
				fmt.Printf("Use 'refresh list --cluster %s' to check status manually.\n", monitor.Updates[0].ClusterName)
			}
			return nil

		case <-monitorCtx.Done():
			if !config.Quiet {
				color.Red("\nMonitoring timeout reached after %v", config.Timeout)
				fmt.Printf("Updates may still be running. Use 'refresh list' to check status.\n")
			}
			return fmt.Errorf("monitoring timeout reached")

		case <-ticker.C:
			allComplete, err := checkAndUpdateProgress(monitorCtx, eksClient, monitor, config)
			if err != nil {
				if !config.Quiet {
					color.Red("Error checking update progress: %v", err)
				}
				// Continue monitoring unless it's a critical error
				continue
			}

			if allComplete {
				return DisplayCompletionSummary(monitor, config)
			}
		}
	}
}

// checkAndUpdateProgress checks the status of all updates and updates the display
func checkAndUpdateProgress(ctx context.Context, eksClient *eks.Client, monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) (bool, error) {
	allComplete := true
	now := time.Now()

	for i := range monitor.Updates {
		update := &monitor.Updates[i]

		// Skip if already completed or failed
		if update.Status == types.UpdateStatusSuccessful ||
			update.Status == types.UpdateStatusFailed ||
			update.Status == types.UpdateStatusCancelled {
			continue
		}

		// Check update status with retry logic
		updateStatus, err := checkUpdateWithRetry(ctx, eksClient, update, config)
		if err != nil {
			update.ErrorMessage = err.Error()
			continue
		}

		// Update progress
		update.Status = updateStatus.Update.Status
		update.LastChecked = now

		// Check for errors
		if len(updateStatus.Update.Errors) > 0 {
			var errorMessages []string
			for _, e := range updateStatus.Update.Errors {
				if e.ErrorMessage != nil {
					errorMessages = append(errorMessages, *e.ErrorMessage)
				}
			}
			update.ErrorMessage = strings.Join(errorMessages, "; ")
		}

		// Still in progress
		if update.Status == types.UpdateStatusInProgress {
			allComplete = false
		}
	}

	// Display current status
	if !config.Quiet {
		DisplayProgressUpdate(monitor)
	}

	return allComplete, nil
}

// checkUpdateWithRetry checks update status with exponential backoff retry
func checkUpdateWithRetry(ctx context.Context, eksClient *eks.Client, update *refreshTypes.UpdateProgress, config refreshTypes.MonitorConfig) (*eks.DescribeUpdateOutput, error) {
	var lastErr error
	backoff := time.Second

	for attempt := 0; attempt < config.MaxRetries; attempt++ {
		updateStatus, err := eksClient.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
			Name:          aws.String(update.ClusterName),
			NodegroupName: aws.String(update.NodegroupName),
			UpdateId:      aws.String(update.UpdateID),
		})

		if err == nil {
			return updateStatus, nil
		}

		lastErr = err

		// Don't retry on context cancellation or timeout
		if ctx.Err() != nil {
			break
		}

		// Exponential backoff
		if attempt < config.MaxRetries-1 {
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * config.BackoffMultiple)
		}
	}

	return nil, lastErr
}
