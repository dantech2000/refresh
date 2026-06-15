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

// MonitorConfig contains configuration for the update monitoring process.
type MonitorConfig struct {
	PollInterval    time.Duration
	MaxRetries      int
	BackoffMultiple float64
	Quiet           bool
	NoWait          bool
	Timeout         time.Duration
}
