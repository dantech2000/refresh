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
	// ErrorMessage holds errors reported by the AWS update itself.
	ErrorMessage string
	// LastCheckError holds a transient status-polling failure (throttle,
	// network blip). It is display-only: the update may well still be running
	// in AWS, so it must not be rendered as a FAILED update.
	LastCheckError string
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
