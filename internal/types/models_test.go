package types

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// VersionInfo.String
// ──────────────────────────────────────────────────────────────────────────────

func TestVersionInfo_StringWithCommit(t *testing.T) {
	v := VersionInfo{Version: "v1.2.3", Commit: "abc1234"}
	got := v.String()
	want := "v1.2.3 (abc1234)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestVersionInfo_StringWithoutCommit(t *testing.T) {
	v := VersionInfo{Version: "v1.2.3"}
	got := v.String()
	if got != "v1.2.3" {
		t.Errorf("got %q, want %q", got, "v1.2.3")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BatchUpdateResult
// ──────────────────────────────────────────────────────────────────────────────

func TestNewBatchUpdateResult_EmptyInitially(t *testing.T) {
	br := NewBatchUpdateResult()
	total, success, failed := br.GetSummary()
	if total != 0 || success != 0 || failed != 0 {
		t.Errorf("new batch should be empty: got total=%d success=%d failed=%d", total, success, failed)
	}
}

func TestBatchUpdateResult_AddResult(t *testing.T) {
	br := NewBatchUpdateResult()
	br.AddResult(UpdateResult{NodegroupName: "workers", Success: true})
	total, success, failed := br.GetSummary()
	if total != 1 || success != 1 || failed != 0 {
		t.Errorf("got total=%d success=%d failed=%d, want 1/1/0", total, success, failed)
	}
}

func TestBatchUpdateResult_FailedResult(t *testing.T) {
	br := NewBatchUpdateResult()
	br.AddResult(UpdateResult{NodegroupName: "workers", Success: false, Error: errors.New("timeout")})
	total, success, failed := br.GetSummary()
	if total != 1 || success != 0 || failed != 1 {
		t.Errorf("got total=%d success=%d failed=%d, want 1/0/1", total, success, failed)
	}
}

func TestBatchUpdateResult_MixedResults(t *testing.T) {
	br := NewBatchUpdateResult()
	br.AddResult(UpdateResult{Success: true})
	br.AddResult(UpdateResult{Success: true})
	br.AddResult(UpdateResult{Success: false})
	total, success, failed := br.GetSummary()
	if total != 3 || success != 2 || failed != 1 {
		t.Errorf("got total=%d success=%d failed=%d, want 3/2/1", total, success, failed)
	}
}

func TestBatchUpdateResult_IncrementStarted(t *testing.T) {
	br := NewBatchUpdateResult()
	br.IncrementStarted()
	br.IncrementStarted()
	if br.Started != 2 {
		t.Errorf("Started = %d, want 2", br.Started)
	}
}

func TestBatchUpdateResult_FinishedTracked(t *testing.T) {
	br := NewBatchUpdateResult()
	br.AddResult(UpdateResult{})
	br.AddResult(UpdateResult{})
	if br.Finished != 2 {
		t.Errorf("Finished = %d, want 2", br.Finished)
	}
}

func TestBatchUpdateResult_ConcurrentWrites(t *testing.T) {
	br := NewBatchUpdateResult()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			br.AddResult(UpdateResult{Success: true})
		}()
	}
	wg.Wait()
	total, success, _ := br.GetSummary()
	if total != 50 || success != 50 {
		t.Errorf("concurrent writes: got total=%d success=%d, want 50/50", total, success)
	}
}

func TestUpdateResult_Duration(t *testing.T) {
	dur := 3 * time.Second
	r := UpdateResult{Duration: dur}
	if r.Duration != dur {
		t.Errorf("Duration = %v, want %v", r.Duration, dur)
	}
}
