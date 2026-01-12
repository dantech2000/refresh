// Package types provides core data types used throughout the refresh CLI tool.
// It defines domain models with proper Go idioms and clean separation of concerns.
package types

import (
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
)

// NodegroupInfo contains essential information about an EKS nodegroup.
type NodegroupInfo struct {
	Name         string
	Status       string
	InstanceType string
	Desired      int32
	CurrentAmi   string
	AmiStatus    AMIStatus
}

// VersionInfo contains version information for the CLI tool.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

// String returns a formatted version string.
func (v VersionInfo) String() string {
	if v.Commit != "" {
		return v.Version + " (" + v.Commit + ")"
	}
	return v.Version
}

// UpdateProgress tracks the progress of a nodegroup update operation.
type UpdateProgress struct {
	NodegroupName string
	UpdateID      string
	ClusterName   string
	Status        types.UpdateStatus
	StartTime     time.Time
	LastChecked   time.Time
	ErrorMessage  string
}

// IsComplete returns true if the update has finished (success, failure, or cancelled).
func (u UpdateProgress) IsComplete() bool {
	return u.Status == types.UpdateStatusSuccessful ||
		u.Status == types.UpdateStatusFailed ||
		u.Status == types.UpdateStatusCancelled
}

// IsSuccessful returns true if the update completed successfully.
func (u UpdateProgress) IsSuccessful() bool {
	return u.Status == types.UpdateStatusSuccessful
}

// Duration returns the time elapsed since the update started.
func (u UpdateProgress) Duration() time.Duration {
	if u.IsComplete() {
		return u.LastChecked.Sub(u.StartTime)
	}
	return time.Since(u.StartTime)
}

// ProgressMonitor manages the monitoring of multiple concurrent nodegroup updates.
// It is thread-safe through the use of sync.RWMutex.
type ProgressMonitor struct {
	mu          sync.RWMutex
	Updates     []UpdateProgress
	StartTime   time.Time
	Quiet       bool
	NoWait      bool
	Timeout     time.Duration
	LastPrinted int
}

// NewProgressMonitor creates a new progress monitor with the specified configuration.
func NewProgressMonitor(quiet, noWait bool, timeout time.Duration) *ProgressMonitor {
	return &ProgressMonitor{
		Updates:   make([]UpdateProgress, 0),
		StartTime: time.Now(),
		Quiet:     quiet,
		NoWait:    noWait,
		Timeout:   timeout,
	}
}

// AddUpdate adds a new update to be monitored in a thread-safe manner.
func (pm *ProgressMonitor) AddUpdate(update UpdateProgress) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.Updates = append(pm.Updates, update)
}

// GetUpdates returns a copy of all updates being monitored.
func (pm *ProgressMonitor) GetUpdates() []UpdateProgress {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	updates := make([]UpdateProgress, len(pm.Updates))
	copy(updates, pm.Updates)
	return updates
}

// UpdateStatus updates the status of a specific nodegroup update.
func (pm *ProgressMonitor) UpdateStatus(nodegroupName string, status types.UpdateStatus, errorMsg string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.Updates {
		if pm.Updates[i].NodegroupName == nodegroupName {
			pm.Updates[i].Status = status
			pm.Updates[i].LastChecked = time.Now()
			pm.Updates[i].ErrorMessage = errorMsg
			return
		}
	}
}

// AllComplete returns true if all updates have finished.
func (pm *ProgressMonitor) AllComplete() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, update := range pm.Updates {
		if !update.IsComplete() {
			return false
		}
	}
	return len(pm.Updates) > 0
}

// SuccessCount returns the number of successful updates.
func (pm *ProgressMonitor) SuccessCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, update := range pm.Updates {
		if update.IsSuccessful() {
			count++
		}
	}
	return count
}

// FailureCount returns the number of failed updates.
func (pm *ProgressMonitor) FailureCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, update := range pm.Updates {
		if update.Status == types.UpdateStatusFailed || update.Status == types.UpdateStatusCancelled {
			count++
		}
	}
	return count
}

// MonitorConfig contains configuration for the update monitoring process.
type MonitorConfig struct {
	PollInterval    time.Duration
	MaxRetries      int
	BackoffMultiple float64
	Quiet           bool
	NoWait          bool
	Timeout         time.Duration
}

// DefaultMonitorConfig returns a MonitorConfig with sensible defaults.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		PollInterval:    15 * time.Second,
		MaxRetries:      3,
		BackoffMultiple: 2.0,
		Quiet:           false,
		NoWait:          false,
		Timeout:         40 * time.Minute,
	}
}

// AMIStatus represents the status of a nodegroup's AMI relative to the latest available.
type AMIStatus int

const (
	// AMILatest indicates the nodegroup is using the latest AMI.
	AMILatest AMIStatus = iota
	// AMIOutdated indicates the nodegroup is using an older AMI.
	AMIOutdated
	// AMIUpdating indicates the nodegroup is currently being updated.
	AMIUpdating
	// AMIUnknown indicates the AMI status could not be determined.
	AMIUnknown
)

// String returns a human-readable, color-coded string representation of the AMI status.
func (s AMIStatus) String() string {
	switch s {
	case AMILatest:
		return color.GreenString("Latest")
	case AMIOutdated:
		return color.RedString("Outdated")
	case AMIUpdating:
		return color.YellowString("Updating")
	case AMIUnknown:
		return color.WhiteString("Unknown")
	default:
		return color.WhiteString("Unknown")
	}
}

// PlainString returns a plain string representation without color codes.
func (s AMIStatus) PlainString() string {
	switch s {
	case AMILatest:
		return "Latest"
	case AMIOutdated:
		return "Outdated"
	case AMIUpdating:
		return "Updating"
	case AMIUnknown:
		return "Unknown"
	default:
		return "Unknown"
	}
}

// NeedsUpdate returns true if the nodegroup should be updated.
func (s AMIStatus) NeedsUpdate() bool {
	return s == AMIOutdated
}

// DryRunAction represents the action that would be taken in dry run mode.
type DryRunAction int

const (
	// ActionUpdate indicates the nodegroup will be updated.
	ActionUpdate DryRunAction = iota
	// ActionSkipUpdating indicates the nodegroup is already updating.
	ActionSkipUpdating
	// ActionSkipLatest indicates the nodegroup is already at the latest AMI.
	ActionSkipLatest
	// ActionForceUpdate indicates the nodegroup will be force-updated.
	ActionForceUpdate
)

// String returns a human-readable, color-coded string representation of the action.
func (a DryRunAction) String() string {
	switch a {
	case ActionUpdate:
		return color.GreenString("UPDATE")
	case ActionSkipUpdating:
		return color.YellowString("SKIP")
	case ActionSkipLatest:
		return color.GreenString("SKIP")
	case ActionForceUpdate:
		return color.CyanString("FORCE UPDATE")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

// PlainString returns a plain string representation without color codes.
func (a DryRunAction) PlainString() string {
	switch a {
	case ActionUpdate:
		return "UPDATE"
	case ActionSkipUpdating:
		return "SKIP (already updating)"
	case ActionSkipLatest:
		return "SKIP (already latest)"
	case ActionForceUpdate:
		return "FORCE UPDATE"
	default:
		return "UNKNOWN"
	}
}

// Reason returns a human-readable reason for the action.
func (a DryRunAction) Reason() string {
	switch a {
	case ActionUpdate:
		return "AMI is outdated"
	case ActionSkipUpdating:
		return "Update already in progress"
	case ActionSkipLatest:
		return "Already using latest AMI"
	case ActionForceUpdate:
		return "Force flag specified"
	default:
		return "Unknown reason"
	}
}

// UpdateResult represents the result of an update operation.
type UpdateResult struct {
	NodegroupName string
	Success       bool
	UpdateID      string
	Error         error
	Duration      time.Duration
}

// BatchUpdateResult aggregates results from multiple update operations.
type BatchUpdateResult struct {
	mu       sync.RWMutex
	Results  []UpdateResult
	Started  int
	Finished int
}

// NewBatchUpdateResult creates a new BatchUpdateResult.
func NewBatchUpdateResult() *BatchUpdateResult {
	return &BatchUpdateResult{
		Results: make([]UpdateResult, 0),
	}
}

// AddResult adds an update result in a thread-safe manner.
func (br *BatchUpdateResult) AddResult(result UpdateResult) {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.Results = append(br.Results, result)
	br.Finished++
}

// IncrementStarted increments the started counter in a thread-safe manner.
func (br *BatchUpdateResult) IncrementStarted() {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.Started++
}

// GetSummary returns a summary of the batch update results.
func (br *BatchUpdateResult) GetSummary() (total, success, failed int) {
	br.mu.RLock()
	defer br.mu.RUnlock()
	total = len(br.Results)
	for _, r := range br.Results {
		if r.Success {
			success++
		} else {
			failed++
		}
	}
	return
}
