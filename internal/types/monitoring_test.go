package types

import (
	"sync"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

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

func TestProgressMonitor_AddUpdate(t *testing.T) {
	pm := NewProgressMonitor(false, false, 0)
	pm.AddUpdate(UpdateProgress{NodegroupName: "workers", Status: ekstypes.UpdateStatusInProgress})
	pm.AddUpdate(UpdateProgress{NodegroupName: "gpu-nodes", Status: ekstypes.UpdateStatusInProgress})

	if len(pm.Updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(pm.Updates))
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
			_ = n
		}(i)
	}
	wg.Wait()
}
