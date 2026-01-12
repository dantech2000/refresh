// Package monitoring provides update progress tracking for EKS nodegroup operations.
// It implements concurrent monitoring with proper channel patterns and graceful shutdown.
package monitoring

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"

	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// UpdateMonitor handles concurrent monitoring of multiple nodegroup updates.
type UpdateMonitor struct {
	eksClient *eks.Client
	config    refreshTypes.MonitorConfig
}

// NewUpdateMonitor creates a new update monitor instance.
func NewUpdateMonitor(eksClient *eks.Client, config refreshTypes.MonitorConfig) *UpdateMonitor {
	return &UpdateMonitor{
		eksClient: eksClient,
		config:    config,
	}
}

// statusResult holds the result of a status check for a single update.
type statusResult struct {
	index  int
	status types.UpdateStatus
	errMsg string
	err    error
}

// MonitorUpdates monitors the progress of multiple nodegroup updates.
// It uses channels for concurrent status checks and proper signal handling.
func MonitorUpdates(ctx context.Context, eksClient *eks.Client, monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	// Set up signal handling for graceful cancellation
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Create a cancellable context with timeout
	monitorCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	if !config.Quiet {
		printMonitoringHeader(monitor, config)
	}

	ticker := time.NewTicker(config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			return handleUserCancellation(monitor, config)

		case <-monitorCtx.Done():
			return handleTimeout(config)

		case <-ticker.C:
			allComplete, err := checkAllUpdatesWithChannels(monitorCtx, eksClient, monitor, config)
			if err != nil {
				if !config.Quiet {
					color.Red("Error checking update progress: %v", err)
				}
				continue
			}

			if allComplete {
				return DisplayCompletionSummary(monitor, config)
			}
		}
	}
}

// printMonitoringHeader displays initial monitoring information.
func printMonitoringHeader(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) {
	fmt.Printf("\nMonitoring %d nodegroup update(s)...\n", len(monitor.Updates))
	fmt.Printf("Timeout: %v | Poll interval: %v\n", config.Timeout, config.PollInterval)
	fmt.Printf("Press Ctrl+C to stop monitoring (updates will continue)\n\n")
}

// handleUserCancellation handles graceful cancellation by user signal.
func handleUserCancellation(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	if !config.Quiet && len(monitor.Updates) > 0 {
		color.Yellow("\nMonitoring cancelled by user. Updates are still running in AWS.")
		fmt.Printf("Use 'refresh list --cluster %s' to check status manually.\n", monitor.Updates[0].ClusterName)
	}
	return nil
}

// handleTimeout handles monitoring timeout.
func handleTimeout(config refreshTypes.MonitorConfig) error {
	if !config.Quiet {
		color.Red("\nMonitoring timeout reached after %v", config.Timeout)
		fmt.Printf("Updates may still be running. Use 'refresh list' to check status.\n")
	}
	return fmt.Errorf("monitoring timeout reached")
}

// checkAllUpdatesWithChannels checks all update statuses concurrently using channels.
func checkAllUpdatesWithChannels(ctx context.Context, eksClient *eks.Client, monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) (bool, error) {
	// Create buffered channel for results
	resultsChan := make(chan statusResult, len(monitor.Updates))

	// Use wait group to track goroutines
	var wg sync.WaitGroup

	// Launch concurrent status checks
	for i := range monitor.Updates {
		update := &monitor.Updates[i]

		// Skip completed updates
		if isUpdateComplete(update.Status) {
			continue
		}

		wg.Add(1)
		go func(idx int, u *refreshTypes.UpdateProgress) {
			defer wg.Done()
			result := checkSingleUpdate(ctx, eksClient, u, config)
			result.index = idx
			resultsChan <- result
		}(i, update)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	now := time.Now()
	allComplete := true

	for result := range resultsChan {
		update := &monitor.Updates[result.index]

		if result.err != nil {
			update.ErrorMessage = result.err.Error()
			allComplete = false
			continue
		}

		update.Status = result.status
		update.LastChecked = now
		update.ErrorMessage = result.errMsg

		if !isUpdateComplete(update.Status) {
			allComplete = false
		}
	}

	// Also check updates that were skipped (already complete)
	for _, update := range monitor.Updates {
		if !isUpdateComplete(update.Status) {
			allComplete = false
			break
		}
	}

	// Display current status
	if !config.Quiet {
		DisplayProgressUpdate(monitor)
	}

	return allComplete && len(monitor.Updates) > 0, nil
}

// checkSingleUpdate checks the status of a single update with retry logic.
func checkSingleUpdate(ctx context.Context, eksClient *eks.Client, update *refreshTypes.UpdateProgress, config refreshTypes.MonitorConfig) statusResult {
	result := statusResult{}

	updateStatus, err := checkUpdateWithRetry(ctx, eksClient, update, config)
	if err != nil {
		result.err = err
		return result
	}

	result.status = updateStatus.Update.Status

	// Extract error messages if any
	if len(updateStatus.Update.Errors) > 0 {
		var errorMessages []string
		for _, e := range updateStatus.Update.Errors {
			if e.ErrorMessage != nil {
				errorMessages = append(errorMessages, *e.ErrorMessage)
			}
		}
		result.errMsg = strings.Join(errorMessages, "; ")
	}

	return result
}

// isUpdateComplete checks if an update has reached a terminal state.
func isUpdateComplete(status types.UpdateStatus) bool {
	return status == types.UpdateStatusSuccessful ||
		status == types.UpdateStatusFailed ||
		status == types.UpdateStatusCancelled
}

// checkUpdateWithRetry checks update status with exponential backoff retry.
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

		// Exponential backoff before retry
		if attempt < config.MaxRetries-1 {
			if !waitWithContext(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff = time.Duration(float64(backoff) * config.BackoffMultiple)
		}
	}

	return nil, lastErr
}

// waitWithContext waits for the specified duration or until context is cancelled.
// Returns true if the wait completed, false if context was cancelled.
func waitWithContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
