// Package types provides core data types used throughout the refresh CLI tool.
package types

import (
	"sync"
	"time"
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
