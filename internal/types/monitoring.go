package types

import (
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

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
// It is thread-safe: the update list is unexported and all access goes through
// the locked accessors below.
type ProgressMonitor struct {
	mu          sync.RWMutex
	updates     []UpdateProgress
	StartTime   time.Time
	Quiet       bool
	NoWait      bool
	Timeout     time.Duration
	LastPrinted int
}

// NewProgressMonitor creates a new progress monitor with the specified configuration.
func NewProgressMonitor(quiet, noWait bool, timeout time.Duration) *ProgressMonitor {
	return &ProgressMonitor{
		updates:   make([]UpdateProgress, 0),
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
	pm.updates = append(pm.updates, update)
}

// GetUpdates returns a copy of all updates being monitored.
func (pm *ProgressMonitor) GetUpdates() []UpdateProgress {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	updates := make([]UpdateProgress, len(pm.updates))
	copy(updates, pm.updates)
	return updates
}

// Len returns the number of updates being monitored.
func (pm *ProgressMonitor) Len() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.updates)
}

// UpdateStatus updates the status of a specific nodegroup update.
func (pm *ProgressMonitor) UpdateStatus(nodegroupName string, status types.UpdateStatus, errorMsg string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.updates {
		if pm.updates[i].NodegroupName == nodegroupName {
			pm.updates[i].Status = status
			pm.updates[i].LastChecked = time.Now()
			pm.updates[i].ErrorMessage = errorMsg
			return
		}
	}
}

// SetResultByIndex records the result of a status check for the update at idx.
// Out-of-range indices are ignored.
func (pm *ProgressMonitor) SetResultByIndex(idx int, status types.UpdateStatus, errorMsg string, checkedAt time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if idx < 0 || idx >= len(pm.updates) {
		return
	}
	pm.updates[idx].Status = status
	pm.updates[idx].LastChecked = checkedAt
	pm.updates[idx].ErrorMessage = errorMsg
}

// SetErrorByIndex records a failed status check for the update at idx without
// touching its last known status. Out-of-range indices are ignored.
func (pm *ProgressMonitor) SetErrorByIndex(idx int, errorMsg string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if idx < 0 || idx >= len(pm.updates) {
		return
	}
	pm.updates[idx].ErrorMessage = errorMsg
}

// AllComplete returns true if all updates have finished.
func (pm *ProgressMonitor) AllComplete() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, update := range pm.updates {
		if !update.IsComplete() {
			return false
		}
	}
	return len(pm.updates) > 0
}

// SuccessCount returns the number of successful updates.
func (pm *ProgressMonitor) SuccessCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, update := range pm.updates {
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
	for _, update := range pm.updates {
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
