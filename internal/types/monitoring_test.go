package types

import (
	"sync"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// ──────────────────────────────────────────────────────────────────────────────
// UpdateProgress
// ──────────────────────────────────────────────────────────────────────────────

func TestUpdateProgress_IsComplete_Successful(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusSuccessful}
	if !u.IsComplete() {
		t.Error("Successful status should be complete")
	}
}

func TestUpdateProgress_IsComplete_Failed(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusFailed}
	if !u.IsComplete() {
		t.Error("Failed status should be complete")
	}
}

func TestUpdateProgress_IsComplete_Cancelled(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusCancelled}
	if !u.IsComplete() {
		t.Error("Cancelled status should be complete")
	}
}

func TestUpdateProgress_IsComplete_InProgress(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusInProgress}
	if u.IsComplete() {
		t.Error("InProgress status should not be complete")
	}
}

func TestUpdateProgress_IsSuccessful_True(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusSuccessful}
	if !u.IsSuccessful() {
		t.Error("Successful status should be successful")
	}
}

func TestUpdateProgress_IsSuccessful_False(t *testing.T) {
	u := UpdateProgress{Status: ekstypes.UpdateStatusFailed}
	if u.IsSuccessful() {
		t.Error("Failed status should not be successful")
	}
}

func TestUpdateProgress_Duration_Complete(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	last := time.Now()
	u := UpdateProgress{
		Status:      ekstypes.UpdateStatusSuccessful,
		StartTime:   start,
		LastChecked: last,
	}
	got := u.Duration()
	want := last.Sub(start)
	if got != want {
		t.Errorf("completed: Duration = %v, want %v", got, want)
	}
}

func TestUpdateProgress_Duration_InProgress(t *testing.T) {
	u := UpdateProgress{
		Status:    ekstypes.UpdateStatusInProgress,
		StartTime: time.Now().Add(-2 * time.Second),
	}
	got := u.Duration()
	if got < time.Second {
		t.Errorf("in-progress: Duration = %v, expected at least 1s", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ProgressMonitor
// ──────────────────────────────────────────────────────────────────────────────

func TestNewProgressMonitor_Fields(t *testing.T) {
	pm := NewProgressMonitor(true, false, 10*time.Minute)
	if !pm.Quiet {
		t.Error("Quiet should be true")
	}
	if pm.NoWait {
		t.Error("NoWait should be false")
	}
	if pm.Timeout != 10*time.Minute {
		t.Errorf("Timeout = %v, want 10m", pm.Timeout)
	}
	if len(pm.Updates) != 0 {
		t.Error("Updates should be empty initially")
	}
}

func TestProgressMonitor_AddAndGetUpdates(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{NodegroupName: "workers", Status: ekstypes.UpdateStatusInProgress})
	pm.AddUpdate(UpdateProgress{NodegroupName: "gpu-nodes", Status: ekstypes.UpdateStatusInProgress})

	updates := pm.GetUpdates()
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
}

func TestProgressMonitor_GetUpdates_ReturnsCopy(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{NodegroupName: "workers"})

	updates := pm.GetUpdates()
	updates[0].NodegroupName = "mutated"

	original := pm.GetUpdates()
	if original[0].NodegroupName == "mutated" {
		t.Error("GetUpdates should return a copy, not a reference to the internal slice")
	}
}

func TestProgressMonitor_UpdateStatus(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{NodegroupName: "workers", Status: ekstypes.UpdateStatusInProgress})

	pm.UpdateStatus("workers", ekstypes.UpdateStatusSuccessful, "")

	updates := pm.GetUpdates()
	if updates[0].Status != ekstypes.UpdateStatusSuccessful {
		t.Errorf("status = %v, want Successful", updates[0].Status)
	}
}

func TestProgressMonitor_UpdateStatus_UnknownNodegroupIsNoOp(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{NodegroupName: "workers", Status: ekstypes.UpdateStatusInProgress})
	pm.UpdateStatus("ghost", ekstypes.UpdateStatusSuccessful, "")

	updates := pm.GetUpdates()
	if updates[0].Status != ekstypes.UpdateStatusInProgress {
		t.Error("update for unknown nodegroup should be a no-op")
	}
}

func TestProgressMonitor_AllComplete_Empty(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	if pm.AllComplete() {
		t.Error("empty monitor should not report all complete")
	}
}

func TestProgressMonitor_AllComplete_AllDone(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusSuccessful})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusFailed})
	if !pm.AllComplete() {
		t.Error("all terminal statuses should report complete")
	}
}

func TestProgressMonitor_AllComplete_OneInProgress(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusSuccessful})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusInProgress})
	if pm.AllComplete() {
		t.Error("should not be complete while one update is in progress")
	}
}

func TestProgressMonitor_SuccessCount(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusSuccessful})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusSuccessful})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusFailed})
	if got := pm.SuccessCount(); got != 2 {
		t.Errorf("SuccessCount = %d, want 2", got)
	}
}

func TestProgressMonitor_FailureCount(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusSuccessful})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusFailed})
	pm.AddUpdate(UpdateProgress{Status: ekstypes.UpdateStatusCancelled})
	if got := pm.FailureCount(); got != 2 {
		t.Errorf("FailureCount = %d, want 2", got)
	}
}

func TestProgressMonitor_ConcurrentAccess(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			pm.AddUpdate(UpdateProgress{
				NodegroupName: "ng",
				Status:        ekstypes.UpdateStatusInProgress,
			})
			_ = pm.GetUpdates()
			_ = pm.AllComplete()
			_ = pm.SuccessCount()
			_ = pm.FailureCount()
			_ = n
		}(i)
	}
	wg.Wait()
}

// ──────────────────────────────────────────────────────────────────────────────
// DefaultMonitorConfig
// ──────────────────────────────────────────────────────────────────────────────

func TestDefaultMonitorConfig_Sensible(t *testing.T) {
	cfg := DefaultMonitorConfig()
	if cfg.PollInterval <= 0 {
		t.Error("PollInterval should be positive")
	}
	if cfg.MaxRetries <= 0 {
		t.Error("MaxRetries should be positive")
	}
	if cfg.BackoffMultiple <= 1 {
		t.Error("BackoffMultiple should be > 1 for exponential backoff")
	}
	if cfg.Timeout <= 0 {
		t.Error("Timeout should be positive")
	}
}
