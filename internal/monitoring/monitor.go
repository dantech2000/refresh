// Package monitoring provides update progress tracking for EKS nodegroup operations.
// It polls update status concurrently and stops on completion, timeout, or
// context cancellation.
package monitoring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	"github.com/fatih/color"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/common"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// ErrCancelled is returned by MonitorUpdates when the user interrupts
// monitoring (Ctrl+C / SIGTERM). The EKS updates keep running in AWS; callers
// must not treat the roll as finished (e.g. skip post-roll verification).
var ErrCancelled = errors.New("interrupted; the EKS update continues in the background")

// UpdateDescriber is the EKS call the monitor polls. *eks.Client satisfies it.
type UpdateDescriber interface {
	DescribeUpdate(ctx context.Context, params *eks.DescribeUpdateInput, optFns ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error)
}

// statusResult holds the result of a status check for a single update.
type statusResult struct {
	index  int
	status types.UpdateStatus
	errMsg string
	err    error
}

// MonitorUpdates monitors the progress of multiple nodegroup updates, polling
// their status concurrently every config.PollInterval.
//
// Cancellation comes only from ctx: main cancels the root context on Ctrl+C /
// SIGTERM (and a second signal is fatal), so the monitor installs no signal
// handler of its own. A cancelled ctx returns ErrCancelled; the monitor
// timeout returns ErrMonitorTimeout.
func MonitorUpdates(ctx context.Context, eksClient UpdateDescriber, monitor *refreshTypes.ProgressMonitor, cfg refreshTypes.MonitorConfig) error {
	// Nothing to monitor: return immediately instead of polling an empty list
	// until the timeout fires.
	if len(monitor.Updates) == 0 {
		return nil
	}
	// time.NewTicker panics on a non-positive interval. The CLI rejects one,
	// but the roll has already started here, so fall back to the default.
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = appconfig.DefaultPollInterval
	}

	// A timeout <= 0 means "no monitor timeout": wait until the updates finish
	// or the user cancels (matching the live roll view's handling of 0).
	monitorCtx, cancel := monitorContext(ctx, cfg.Timeout)
	defer cancel()

	if !cfg.Quiet {
		printMonitoringHeader(monitor, cfg)
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-monitorCtx.Done():
			// Plain cancellation is the user stopping (main cancels the root
			// context on Ctrl+C / SIGTERM); only a deadline is a timeout.
			if errors.Is(monitorCtx.Err(), context.Canceled) {
				return handleUserCancellation(monitor, cfg)
			}
			return handleTimeout(monitor, cfg)

		case <-ticker.C:
			allComplete, err := checkAllUpdatesWithChannels(monitorCtx, eksClient, monitor, cfg)
			if err != nil {
				// A permanent status-check failure (e.g. AccessDenied on
				// DescribeUpdate) won't clear on the next poll. The caller
				// prints the returned error.
				return fmt.Errorf("stopped monitoring (the EKS update continues in the background): %w", err)
			}

			if allComplete {
				return DisplayCompletionSummary(monitor, cfg)
			}
		}
	}
}

// monitorContext bounds ctx by timeout, or only by cancellation when
// timeout <= 0 (no limit).
func monitorContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// formatMonitorTimeout renders the monitor timeout for display ("none" for
// no limit).
func formatMonitorTimeout(timeout time.Duration) string {
	if timeout <= 0 {
		return "none"
	}
	return timeout.String()
}

// printMonitoringHeader displays initial monitoring information.
func printMonitoringHeader(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) {
	fmt.Printf("\nMonitoring %d nodegroup update(s)...\n", len(monitor.Updates))
	fmt.Printf("Timeout: %s | Poll interval: %v\n", formatMonitorTimeout(config.Timeout), config.PollInterval)
	fmt.Printf("Press Ctrl+C to stop monitoring (updates will continue)\n\n")
}

// handleUserCancellation handles graceful cancellation by user signal. It
// returns ErrCancelled so callers stop instead of treating the roll as done.
// The "check with refresh nodegroup list" hint is left to the caller's error
// (see nodegroup updateExit) so it prints once, in quiet/JSON runs too.
func handleUserCancellation(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	if !config.Quiet && len(monitor.Updates) > 0 {
		color.Yellow("\nMonitoring cancelled by user. Updates are still running in AWS.")
	}
	return ErrCancelled
}

// handleTimeout handles monitoring timeout.
func handleTimeout(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	if !config.Quiet {
		color.Red("\nMonitoring timeout reached after %v", config.Timeout)
		if len(monitor.Updates) > 0 {
			fmt.Printf("Updates may still be running. Use 'refresh nodegroup list %s' to check status.\n", monitor.Updates[0].ClusterName)
		} else {
			fmt.Printf("Updates may still be running. Use 'refresh nodegroup list' to check status.\n")
		}
	}
	return ErrMonitorTimeout
}

// ErrMonitorTimeout is returned by MonitorUpdates when --timeout elapses before
// every update reaches a terminal state.
var ErrMonitorTimeout = errors.New("monitoring timeout reached")

// DisplayStopped prints the banner for a monitor run that ended before every
// update was terminal: the timeout banner when err is ErrMonitorTimeout, the
// user-cancellation banner when err is ErrCancelled. It is for callers that ran
// the monitor quietly (e.g. under the live roll panel) and need the banner once
// the panel has stopped. It prints nothing when config.Quiet is set.
func DisplayStopped(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig, err error) {
	switch {
	case errors.Is(err, ErrMonitorTimeout):
		_ = handleTimeout(monitor, config)
	case errors.Is(err, ErrCancelled):
		_ = handleUserCancellation(monitor, config)
	}
}

// checkAllUpdatesWithChannels checks all update statuses concurrently. A
// transient check failure is recorded on the update and polled through; a
// permanent one (a typed AWS error that retrying can't fix, such as
// AccessDenied) is returned so the monitor stops instead of polling forever.
func checkAllUpdatesWithChannels(ctx context.Context, eksClient UpdateDescriber, monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) (bool, error) {
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
			result := checkSingleUpdate(ctx, eksClient, u)
			result.index = idx
			resultsChan <- result
		}(i, update)
	}

	// Close channel when all goroutines complete. Await the closer before
	// returning so callers never race on an open channel. (REF-80)
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	now := time.Now()
	allComplete := true
	var permanentErr error

	for result := range resultsChan {
		update := &monitor.Updates[result.index]

		if result.err != nil {
			if permanentErr == nil && ctx.Err() == nil && isPermanentCheckError(result.err) {
				permanentErr = awsinternal.FormatAWSError(result.err,
					fmt.Sprintf("checking the status of nodegroup %s update %s", update.NodegroupName, update.UpdateID))
			}
			// Transient polling failure: the update is likely still running
			// in AWS. Record it separately so the display doesn't render an
			// in-flight update as FAILED.
			update.LastCheckError = result.err.Error()
			allComplete = false
			continue
		}

		update.Status = result.status
		update.LastChecked = now
		update.ErrorMessage = result.errMsg
		update.LastCheckError = ""

		if !isUpdateComplete(update.Status) {
			allComplete = false
		}
	}
	if permanentErr != nil {
		return false, permanentErr
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

// isPermanentCheckError reports whether a failed status check will fail the
// same way on the next poll: a typed AWS API error that is not retryable
// (AccessDenied, ResourceNotFound, validation). Transport errors (DNS,
// connection refused) and throttling stay transient, so the monitor polls
// through them.
func isPermanentCheckError(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && !common.IsRetryable(err)
}

// checkSingleUpdate checks the status of a single update, retrying transient
// errors.
func checkSingleUpdate(ctx context.Context, eksClient UpdateDescriber, update *refreshTypes.UpdateProgress) statusResult {
	result := statusResult{}

	updateStatus, err := common.WithRetry(ctx, common.DefaultRetryConfig,
		func(rc context.Context) (*eks.DescribeUpdateOutput, error) {
			return eksClient.DescribeUpdate(rc, &eks.DescribeUpdateInput{
				Name:          aws.String(update.ClusterName),
				NodegroupName: aws.String(update.NodegroupName),
				UpdateId:      aws.String(update.UpdateID),
			})
		})
	if err != nil {
		result.err = err
		return result
	}
	if updateStatus.Update == nil {
		result.err = errors.New("DescribeUpdate returned no update")
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

// AllComplete reports whether every monitored update has reached a terminal
// state (successful, failed, or cancelled).
func AllComplete(monitor *refreshTypes.ProgressMonitor) bool {
	if len(monitor.Updates) == 0 {
		return false
	}
	for _, u := range monitor.Updates {
		if !isUpdateComplete(u.Status) {
			return false
		}
	}
	return true
}

// isUpdateComplete checks if an update has reached a terminal state.
func isUpdateComplete(status types.UpdateStatus) bool {
	return status == types.UpdateStatusSuccessful ||
		status == types.UpdateStatusFailed ||
		status == types.UpdateStatusCancelled
}
