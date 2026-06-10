package monitoring

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// captureStdout redirects os.Stdout for fn and returns what was written.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func singleUpdate(status ekstypes.UpdateStatus) refreshTypes.UpdateProgress {
	return refreshTypes.UpdateProgress{
		NodegroupName: "ng-1",
		ClusterName:   "my-cluster",
		UpdateID:      "upd-abc",
		Status:        status,
		StartTime:     time.Now().Add(-10 * time.Second),
		LastChecked:   time.Now(),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printUpdateProgressTree
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintUpdateProgressTree_EmptyReturnsZero(t *testing.T) {
	var count int
	captureStdout(func() {
		count = printUpdateProgressTree(nil)
	})
	if count != 0 {
		t.Errorf("empty updates: lineCount = %d, want 0", count)
	}
}

func TestPrintUpdateProgressTree_SingleUpdate(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusInProgress)}
	var count int
	out := captureStdout(func() {
		count = printUpdateProgressTree(updates)
	})
	if count == 0 {
		t.Error("single update should produce lines")
	}
	if !strings.Contains(out, "ng-1") {
		t.Errorf("output should contain nodegroup name, got:\n%s", out)
	}
}

func TestPrintUpdateProgressTree_MultipleUpdates_LastHasCornerPrefix(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{
		singleUpdate(ekstypes.UpdateStatusInProgress),
		singleUpdate(ekstypes.UpdateStatusSuccessful),
	}
	updates[1].NodegroupName = "ng-2"
	out := captureStdout(func() {
		printUpdateProgressTree(updates)
	})
	if !strings.Contains(out, "└── ") {
		t.Errorf("last entry should use └── prefix, output:\n%s", out)
	}
	if !strings.Contains(out, "├── ") {
		t.Errorf("non-last entry should use ├── prefix, output:\n%s", out)
	}
}

func TestPrintUpdateProgressTree_ErrorMessageShown(t *testing.T) {
	update := singleUpdate(ekstypes.UpdateStatusFailed)
	update.ErrorMessage = "disk full"
	out := captureStdout(func() {
		printUpdateProgressTree([]refreshTypes.UpdateProgress{update})
	})
	if !strings.Contains(out, "disk full") {
		t.Errorf("error message should appear in output, got:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printCompletionSummaryTree
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintCompletionSummaryTree_Empty(t *testing.T) {
	// Should not panic on empty input
	captureStdout(func() {
		printCompletionSummaryTree(nil)
	})
}

func TestPrintCompletionSummaryTree_Successful(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusSuccessful)}
	out := captureStdout(func() {
		printCompletionSummaryTree(updates)
	})
	if !strings.Contains(out, "SUCCESSFUL") {
		t.Errorf("output should contain SUCCESSFUL, got:\n%s", out)
	}
}

func TestPrintCompletionSummaryTree_Failed(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusFailed)}
	out := captureStdout(func() {
		printCompletionSummaryTree(updates)
	})
	if !strings.Contains(out, "FAILED") {
		t.Errorf("output should contain FAILED, got:\n%s", out)
	}
}

func TestPrintCompletionSummaryTree_Cancelled(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusCancelled)}
	out := captureStdout(func() {
		printCompletionSummaryTree(updates)
	})
	if !strings.Contains(out, "CANCELLED") {
		t.Errorf("output should contain CANCELLED, got:\n%s", out)
	}
}

func TestPrintCompletionSummaryTree_FailedWithMessage(t *testing.T) {
	update := singleUpdate(ekstypes.UpdateStatusFailed)
	update.ErrorMessage = "timeout error"
	out := captureStdout(func() {
		printCompletionSummaryTree([]refreshTypes.UpdateProgress{update})
	})
	if !strings.Contains(out, "timeout error") {
		t.Errorf("error message should appear in completion tree, got:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayProgressUpdate
// ──────────────────────────────────────────────────────────────────────────────

func forceInteractiveDisplay(t *testing.T) {
	t.Helper()
	old := displayIsTerminal
	displayIsTerminal = func() bool { return true }
	t.Cleanup(func() { displayIsTerminal = old })
}

func TestDisplayProgressUpdate_SetsLastPrinted(t *testing.T) {
	forceInteractiveDisplay(t)
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	monitor.StartTime = time.Now()

	captureStdout(func() {
		DisplayProgressUpdate(monitor)
	})
	if monitor.LastPrinted == 0 {
		t.Error("DisplayProgressUpdate should set LastPrinted > 0")
	}
}

func TestDisplayProgressUpdate_EmptyMonitor(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.StartTime = time.Now()
	// Should not panic on empty updates
	captureStdout(func() {
		DisplayProgressUpdate(monitor)
	})
}

func TestDisplayProgressUpdate_WithPreviousOutput(t *testing.T) {
	forceInteractiveDisplay(t)
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	monitor.StartTime = time.Now()
	monitor.LastPrinted = 5 // simulate previous output

	out := captureStdout(func() {
		DisplayProgressUpdate(monitor)
	})
	// Should include ANSI escape for clearing previous lines
	if !strings.Contains(out, "\033[") {
		t.Errorf("with LastPrinted>0 should emit ANSI clear sequence, got:\n%s", fmt.Sprintf("%q", out))
	}
}

func TestDisplayProgressUpdate_NonInteractiveAppendsOnly(t *testing.T) {
	old := displayIsTerminal
	displayIsTerminal = func() bool { return false }
	t.Cleanup(func() { displayIsTerminal = old })

	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	monitor.StartTime = time.Now()
	monitor.LastPrinted = 5 // would trigger a clear when interactive

	out := captureStdout(func() {
		DisplayProgressUpdate(monitor)
	})
	if strings.Contains(out, "\033[") {
		t.Errorf("piped output must not contain cursor-control codes, got %q", out)
	}
	if monitor.LastPrinted != 0 {
		t.Errorf("LastPrinted should stay 0 when not interactive, got %d", monitor.LastPrinted)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayCompletionSummary — verbose path
// ──────────────────────────────────────────────────────────────────────────────

func TestDisplayCompletionSummary_VerboseOutputsResults(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusSuccessful))
	monitor.StartTime = time.Now().Add(-5 * time.Second)
	cfg := refreshTypes.MonitorConfig{Quiet: false}
	out := captureStdout(func() {
		_ = DisplayCompletionSummary(monitor, cfg)
	})
	if !strings.Contains(out, "successful") && !strings.Contains(out, "Results") {
		t.Errorf("verbose mode should show results summary, got:\n%s", out)
	}
}

func TestDisplayCompletionSummary_VerboseEmptyUpdates(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.StartTime = time.Now()
	cfg := refreshTypes.MonitorConfig{Quiet: false}
	// Should not panic
	captureStdout(func() {
		_ = DisplayCompletionSummary(monitor, cfg)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// printMonitoringHeader
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintMonitoringHeader_ContainsUpdateCount(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	cfg := refreshTypes.MonitorConfig{
		Quiet:        false,
		PollInterval: 5 * time.Second,
		Timeout:      10 * time.Minute,
	}
	out := captureStdout(func() {
		printMonitoringHeader(monitor, cfg)
	})
	if !strings.Contains(out, "2") {
		t.Errorf("header should mention update count, got:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// handleUserCancellation — verbose path
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleUserCancellation_VerboseWithUpdates(t *testing.T) {
	monitor := refreshTypes.NewProgressMonitor(false, false, 0)
	monitor.AddUpdate(singleUpdate(ekstypes.UpdateStatusInProgress))
	cfg := refreshTypes.MonitorConfig{Quiet: false}
	// Should return nil and not panic
	if err := handleUserCancellation(monitor, cfg); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// NewUpdateMonitor
// ──────────────────────────────────────────────────────────────────────────────

func TestNewUpdateMonitor_NotNil(t *testing.T) {
	cfg := refreshTypes.MonitorConfig{Quiet: true}
	m := NewUpdateMonitor(nil, cfg)
	if m == nil {
		t.Error("NewUpdateMonitor should return non-nil monitor")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// printCompletionSummaryTree — default/unknown status branch
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintCompletionSummaryTree_UnknownStatus(t *testing.T) {
	update := singleUpdate("SOME_UNKNOWN_STATUS")
	out := captureStdout(func() {
		printCompletionSummaryTree([]refreshTypes.UpdateProgress{update})
	})
	if !strings.Contains(out, "SOME_UNKNOWN_STATUS") {
		t.Errorf("unknown status should appear verbatim, got:\n%s", out)
	}
}
